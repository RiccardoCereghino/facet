package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/RiccardoCereghino/facet/internal/config"
	"github.com/RiccardoCereghino/facet/internal/ghx"
	"github.com/RiccardoCereghino/facet/internal/knowledge"
	"github.com/RiccardoCereghino/facet/internal/manifest"
	"github.com/RiccardoCereghino/facet/internal/mux"
	"github.com/RiccardoCereghino/facet/internal/render"
	"github.com/RiccardoCereghino/facet/internal/routing"
	"github.com/RiccardoCereghino/facet/internal/workspace"
	"github.com/spf13/cobra"
)

func newSpawnCmd() *cobra.Command {
	var (
		repo       string
		clones     []string
		addClones  []string
		rmClones   []string
		slug       string
		base       string
		yes        bool
		noBranch   bool
		dryRun     bool
		attach     bool
		noAttach   bool
		ownSession bool
		muxName    string
	)
	cmd := &cobra.Command{
		Use:   "spawn <issue-number>",
		Short: "Create an ephemeral workspace for one GitHub issue",
		Long: "Reads the issue, works out which repositories it needs, shows you why, and\n" +
			"waits. On confirmation it creates an issue-linked branch, clones each repo\n" +
			"from the local mirror, and writes a CLAUDE.md carrying the issue body and the\n" +
			"durable hazards recorded for its areas.\n\n" +
			"Labels alone cannot decide the repo set: the same topic label is used in\n" +
			"several repos, and a cross-repo dependency lives in the issue body. So the\n" +
			"inference is always shown and never silently trusted.\n\n" +
			"It sets the workspace up and stops there. Opening the multiplexer and starting\n" +
			"the agent are yours: spawn prints the exact command, and writes the zellij\n" +
			"layout for it to use. Pass --attach if you want facet to open it for you.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("issue number: %w", err)
			}
			return runSpawn(spawnOpts{
				Number: number, Repo: repo, Clones: clones, Add: addClones, Remove: rmClones,
				Slug: slug, Base: base, Yes: yes, NoBranch: noBranch, DryRun: dryRun,
				Attach: attach, NoAttach: noAttach, OwnSession: ownSession, Mux: muxName,
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&repo, "repo", "", "the issue's home repository, as owner/name (required)")
	f.StringSliceVar(&clones, "clone", nil, "replace the inferred repo set entirely")
	f.StringSliceVar(&addClones, "add", nil, "add repos to the inferred set")
	f.StringSliceVar(&rmClones, "rm", nil, "drop repos from the inferred set")
	f.StringVar(&slug, "slug", "", "override the slug derived from the issue title")
	f.StringVar(&base, "base", "main", "base branch for the issue branch")
	f.BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	f.BoolVar(&noBranch, "no-branch", false, "do not create or check out an issue branch")
	f.BoolVar(&dryRun, "dry-run", false, "show the inference and exit, creating nothing")
	f.BoolVar(&attach, "attach", false, "also open the workspace in the multiplexer (default: just print how to)")
	f.BoolVar(&noAttach, "no-attach", false, "no-op; not opening is now the default")
	f.BoolVar(&ownSession, "session", false, "with --attach, open in a session of its own rather than as tabs")
	f.StringVar(&muxName, "mux", "", "multiplexer to use: zellij, wt, or none")
	return cmd
}

// muxFor resolves a launcher by name, or picks the best available.
func muxFor(name string) mux.Launcher {
	if name != "" {
		return mux.ByName(name)
	}
	return mux.Pick()
}

type spawnOpts struct {
	Number                       int
	Repo                         string
	Clones, Add, Remove          []string
	Slug, Base                   string
	Yes, NoBranch, DryRun        bool
	Attach, NoAttach, OwnSession bool
	Mux                          string
}

func runSpawn(o spawnOpts) error {
	if o.Repo == "" {
		return fmt.Errorf("--repo is required (owner/name): more than one repo may host issues, and gh's notion of the current repo is not it")
	}
	route, err := routing.Load(roots.Routing)
	if err != nil {
		return err
	}
	homeKey := route.OwnerRepoToKey[o.Repo]
	if homeKey == "" {
		return fmt.Errorf("%s is not in %s's ownerRepoToKey", o.Repo, roots.Routing)
	}

	iss, err := gh.ViewIssue(o.Repo, o.Number)
	if err != nil {
		return err
	}
	if !iss.IsOpen() && !o.Yes {
		return fmt.Errorf("issue %s#%d is %s; pass --yes to spawn anyway", o.Repo, o.Number, iss.State)
	}

	sel, hints := route.Infer(o.Repo, iss)
	sel = applyOverrides(sel, route, homeKey, o)

	slug := o.Slug
	if slug == "" {
		slug = render.Slug(iss.Title, 40)
	}
	wsName := render.WorkspaceName(config.IssuePrefix, homeKey, o.Number, slug)
	ws := filepath.Join(roots.Workspaces, wsName)
	branch := fmt.Sprintf("%d-%s", o.Number, slug)
	if o.NoBranch {
		branch = ""
	}

	fragNames := route.Fragments(iss.LabelNames())
	frags, fragErrs := knowledge.LoadAll(roots.Knowledge, fragNames)

	printPlan(ws, o.Repo, iss, sel, hints, route, branch, frags, fragErrs)

	if o.DryRun {
		fmt.Println("\n--dry-run: nothing was created.")
		return nil
	}
	if _, err := os.Stat(ws); err == nil {
		return fmt.Errorf("%s already exists", ws)
	}
	if !o.Yes && !confirm(fmt.Sprintf("Spawn %s with %d repo(s)?", wsName, len(sel))) {
		fmt.Println("aborted.")
		return nil
	}

	// The branch is created before the mirror refresh, so the fetch that follows
	// already carries it.
	if branch != "" {
		created, err := gh.DevelopBranch(o.Repo, o.Number, o.Base, branch)
		if err != nil {
			return fmt.Errorf("create issue branch: %w", err)
		}
		branch = created
	}

	m := &manifest.Manifest{
		Name:        wsName,
		Description: fmt.Sprintf("%s#%d: %s", o.Repo, o.Number, iss.Title),
		Clones:      map[string]string{},
		Remotes:     map[string]map[string]string{},
		LFS:         map[string]bool{},
		Issue: &manifest.Issue{
			Repo: o.Repo, Number: o.Number, Branch: branch, Home: route.Repos[homeKey].Dir,
			URL: iss.URL, Created: time.Now().UTC().Format(time.RFC3339), Labels: iss.LabelNames(),
		},
	}
	for _, s := range sel {
		r := route.Repos[s.Key]
		m.Clones[r.Dir] = r.URL
		if len(r.Remotes) > 0 {
			m.Remotes[r.Dir] = r.Remotes
		}
		if r.LFS != nil {
			m.LFS[r.Dir] = *r.LFS
		}
	}
	if err := os.MkdirAll(ws, 0o777); err != nil {
		return err
	}
	if err := m.Write(ws); err != nil {
		return err
	}

	rep := workspace.Reporter{W: os.Stdout}
	if err := workspace.Sync(roots, ws, git, rep, workspace.SyncOptions{Source: sourceFor(true, rep)}); err != nil {
		return err
	}

	homeDir := route.Repos[homeKey].Dir
	if branch != "" {
		if err := checkoutIssueBranch(filepath.Join(ws, homeDir), branch); err != nil {
			return fmt.Errorf("check out %s: %w", branch, err)
		}
		rep.Created("%s: on branch %s", homeDir, branch)
	}

	data := render.BuildIssueData(wsName, o.Repo, branch, homeDir, iss, sel, hints, route, frags, fragErrs, time.Now())
	md, err := render.IssueClaudeMD(data)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(ws, "CLAUDE.md"), md, 0o666); err != nil {
		return err
	}

	// The board is moved only once the workspace is real. "In progress" should
	// mean there is somewhere to do the work, so this comes after the clones, the
	// branch and the CLAUDE.md -- and never before the confirmation prompt.
	//
	// It is never fatal. A renamed board or a `gh` without the `project` scope
	// must not strand a workspace that is otherwise complete, exactly as a failed
	// mirror fetch does not.
	if target, ok := route.Target(); ok {
		if err := gh.SetIssueStatus(target, iss.URL); err != nil {
			rep.Warn("project %s/%d: %v", target.Owner, target.Number, err)
		} else {
			rep.Created("project %s/%d: %s = %s", target.Owner, target.Number, target.Field, target.Option)
		}
	}

	fmt.Printf("\nWorkspace ready: %s\n", ws)

	// The multiplexer comes last and is never fatal: the clones, the branch and
	// the CLAUDE.md all exist by now. zellij on Windows is a community fork.
	if o.Mux == "none" {
		return nil
	}
	l := muxFor(o.Mux)

	// facet SETS UP the workspace. Opening it is yours.
	//
	// spawn used to drive zellij by itself whenever it noticed it was already
	// inside a session. The Windows fork does not behave predictably enough for
	// that, and both failure modes land in the middle of somebody's work:
	//
	//   - `action new-tab --layout` re-applies the layout in full every time, so a
	//     second spawn or attach DUPLICATES the workspace's tabs. Observed live:
	//     one session holding three "#67" tabs and two "workspace" tabs.
	//   - when the workspace already owned a session, spawn switched this client
	//     into it -- which reads, to the person typing, as their session being
	//     replaced.
	//
	// So nothing is opened unless you ask with --attach. The layout is still
	// written, so `zellij --layout` can use it.
	open := o.Attach && !o.NoAttach
	if open {
		// spawn opens beside whatever you are doing; it must not move you. A freshly
		// spawned workspace has no session of its own to switch to anyway.
		_, asTab := mux.AutoOpen(l, o.OwnSession)
		return openSession(ws, wsName, homeDir, o.Number, o.Mux, asTab, true, false)
	}
	fmt.Printf("\nwork in:    %s\n", filepath.Join(ws, homeDir))
	if l != nil && l.Name() == "zellij" {
		// Render the layout now: naming it is the difference between "open a
		// terminal" and "open the workspace". A failure is cosmetic.
		if layout, err := renderLayout(ws, wsName, homeDir, o.Number); err == nil {
			fmt.Printf("open it:    zellij --session %s --new-session-with-layout %s\n", wsName, layout)
		}
		fmt.Printf("rejoin it:  %s\n", l.AttachCommand(wsName))
		fmt.Printf("\nRun that from OUTSIDE zellij -- sessions do not nest.\n")
	} else if l != nil {
		fmt.Printf("open it:    facet attach --path %s\n", ws)
		fmt.Printf("rejoin it:  %s\n", l.AttachCommand(wsName))
	}
	return nil
}

// checkoutIssueBranch fetches the branch explicitly, because the mirror may have
// been created before `gh issue develop` pushed it.
func checkoutIssueBranch(dir, branch string) error {
	if _, err := git.Run(dir, nil, "fetch", "origin", branch); err != nil {
		return err
	}
	_, err := git.Run(dir, nil, "checkout", "-B", branch, "--track", "origin/"+branch)
	return err
}

// applyOverrides lets the operator correct the inference. --clone replaces it
// wholesale; --add and --rm adjust it. The home repo can never be removed: it
// carries the branch.
func applyOverrides(sel []routing.Selection, route *routing.Routing, homeKey string, o spawnOpts) []routing.Selection {
	if len(o.Clones) > 0 {
		sel = []routing.Selection{{Key: homeKey, Reasons: []string{"home"}, Home: true}}
		for _, k := range o.Clones {
			if k == homeKey {
				continue
			}
			if _, ok := route.Repos[k]; ok {
				sel = append(sel, routing.Selection{Key: k, Reasons: []string{"manual"}})
			}
		}
	}
	for _, k := range o.Add {
		if _, ok := route.Repos[k]; !ok {
			continue
		}
		found := false
		for _, s := range sel {
			if s.Key == k {
				found = true
			}
		}
		if !found {
			sel = append(sel, routing.Selection{Key: k, Reasons: []string{"manual"}})
		}
	}
	if len(o.Remove) > 0 {
		drop := map[string]bool{}
		for _, k := range o.Remove {
			drop[k] = true
		}
		var kept []routing.Selection
		for _, s := range sel {
			if drop[s.Key] && !s.Home {
				continue
			}
			kept = append(kept, s)
		}
		sel = kept
	}
	return sel
}

func printPlan(ws, repo string, iss *ghx.Issue, sel []routing.Selection, hints []routing.Hint,
	route *routing.Routing, branch string, frags []knowledge.Fragment, fragErrs []error) {

	fmt.Printf("%s#%d  %s\n", repo, iss.Number, iss.Title)
	fmt.Printf("  %s\n", iss.URL)
	if ls := iss.LabelNames(); len(ls) > 0 {
		fmt.Printf("  labels: %s\n", strings.Join(ls, ", "))
	}
	fmt.Printf("\nworkspace: %s\n", ws)
	if branch != "" {
		fmt.Printf("branch:    %s (linked to the issue)\n", branch)
	} else {
		fmt.Printf("branch:    none (--no-branch)\n")
	}
	if t, ok := route.Target(); ok {
		fmt.Printf("board:     %s/%d, %s = %s\n", t.Owner, t.Number, t.Field, t.Option)
	}

	fmt.Printf("\nrepos to clone, and why:\n")
	for _, s := range sel {
		tag := ""
		if s.Home {
			tag = "  [home, gets the branch]"
		}
		fmt.Printf("  %-16s %s%s\n", route.Repos[s.Key].Dir, strings.Join(s.Reasons, "; "), tag)
	}
	if len(hints) > 0 {
		fmt.Printf("\nmentioned but NOT cloned (add with --add):\n")
		for _, h := range hints {
			fmt.Printf("  %-16s %s\n", h.Key, h.Reason)
		}
	}
	if len(frags) > 0 {
		fmt.Printf("\nknowledge fragments to inline:\n")
		now := time.Now()
		for _, f := range frags {
			stale := ""
			if f.IsStale(now) {
				stale = "  (STALE -- reviewed " + f.Meta.LastReviewed + ")"
			}
			fmt.Printf("  %-16s %s%s\n", f.Name, f.Meta.SourceWorkspace, stale)
		}
	}
	for _, e := range fragErrs {
		fmt.Printf("  ! %v\n", e)
	}
}

func confirm(prompt string) bool {
	fmt.Printf("\n%s [y/N] ", prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false
	}
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}
