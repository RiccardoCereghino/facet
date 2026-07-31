// spawn_run.go holds the body of `facet spawn`, split out of spawn.go so that
// file holds only the cobra wiring -- the same thin newXCmd/runX shape the other
// commands use. The logic stays in cmd/facet rather than moving to internal/:
// it is orchestration glue over routing, render, workspace, mux and ghx, not a
// unit anything else would import.

package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/RiccardoCereghino/facet/internal/claudex"
	"github.com/RiccardoCereghino/facet/internal/config"
	"github.com/RiccardoCereghino/facet/internal/ghx"
	"github.com/RiccardoCereghino/facet/internal/knowledge"
	"github.com/RiccardoCereghino/facet/internal/manifest"
	"github.com/RiccardoCereghino/facet/internal/mux"
	"github.com/RiccardoCereghino/facet/internal/render"
	"github.com/RiccardoCereghino/facet/internal/routing"
	"github.com/RiccardoCereghino/facet/internal/seat"
	"github.com/RiccardoCereghino/facet/internal/workspace"
)

func runSpawn(o spawnOpts) error {
	if o.Repo == "" {
		return fmt.Errorf("--repo is required (owner/name): more than one repo may host issues, and gh's notion of the current repo is not it")
	}
	// The seat and its scope are checked before anything else -- before the
	// credential gate, before routing, before the issue is looked up. They cost
	// nothing to check and a mistyped seat name should not be discovered after a
	// branch has been created on the forge. There is deliberately no derived
	// default: a name nobody chose is a name nobody can be held to.
	if err := seat.ValidateName(o.Seat); err != nil {
		return fmt.Errorf("--seat: %w", err)
	}
	extraScope, err := seat.ParseRefs(o.Scope)
	if err != nil {
		return fmt.Errorf("--scope: %w", err)
	}
	// The issue being spawned for always leads, whether or not --scope repeats it.
	scopeRefs := seat.Dedupe(append([]seat.Ref{{Repo: o.Repo, Number: o.Number}}, extraScope...))

	// Parsed here, beside --scope, so a malformed ref refuses before anything is
	// created rather than after the clones are on disk. It is deliberately NOT
	// added to scopeRefs: the seat issue is a different thing from the work this
	// workspace covers, and folding it in would make `.scope` claim the seat
	// record as work — which is exactly the conflation the third file exists to
	// avoid.
	var seatIssueRef seat.Ref
	haveSeatIssue := o.SeatIssue != ""
	if haveSeatIssue {
		seatIssueRef, err = seat.ParseRef(o.SeatIssue)
		if err != nil {
			return fmt.Errorf("--seat-issue: %w", err)
		}
	}
	// Before routing, before the issue lookup, before anything is created on
	// disk or on the forge. A spawn that gets halfway on a bad credential
	// leaves a workspace whose branch was never linked, which is worse than a
	// refusal -- and a credential fault like this is worst found mid-operation,
	// by trying to use it, rather than checked first.
	if err := requirePreflight(os.Stderr, "spawn"); err != nil {
		return err
	}
	route, err := routing.Load(roots.Routing)
	if err != nil {
		return err
	}
	homeKey := route.KeyForRepo(o.Repo)
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
	} else if norm := render.Slug(slug, 0); norm != slug {
		// An operator-supplied --slug flows straight into a filesystem path via
		// render.WorkspaceName, which does not sanitise it. Reject anything that is
		// not already [a-z0-9-] (so "../evil" cannot escape the workspaces root)
		// rather than silently rewriting it.
		return fmt.Errorf("--slug must be lowercase [a-z0-9-]; %q is not (try %q)", slug, norm)
	}
	wsName := render.WorkspaceName(config.IssuePrefix, homeKey, o.Number, slug)
	ws := filepath.Join(roots.Workspaces, wsName)
	branch := issueBranchName(o.Number, slug)
	if o.NoBranch {
		branch = ""
	}

	fragNames := route.Fragments(iss.LabelNames())
	frags, fragErrs := knowledge.LoadAll(roots.Knowledge, fragNames)

	printPlan(ws, o.Repo, iss, sel, hints, route, branch, o.Seat, scopeRefs, o.SeatIssue, frags, fragErrs)

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

	// Identity is written as soon as the directory exists and before anything is
	// cloned into it, so a spawn that fails partway still leaves a workspace that
	// says whose it is. Unlike the board move and the issue write-back further
	// down, a failure here is fatal: those depend on a network and a token, this
	// is a handful of bytes to a directory just created, and a workspace with no
	// recorded owner is the state this exists to prevent.
	if err := seat.Write(ws, o.Seat, scopeRefs); err != nil {
		return err
	}
	rep.Created("%s: %s", seat.NameFile, o.Seat)
	rep.Created("%s: %s", seat.ScopeFile, seat.Join(scopeRefs))
	// Reported, never silent. facet#65 was exactly this defect for the first two
	// files — the one action with no line in the output — and it is closed; a
	// third silent write would reopen it under a new name.
	if haveSeatIssue {
		if err := seat.WriteSeatIssue(ws, seatIssueRef); err != nil {
			return err
		}
		rep.Created("%s: %s", seat.SeatIssueFile, seatIssueRef)
	}
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

	// Record the repo set the human just confirmed, so the next spawn reads it
	// rather than inferring it again -- and so an issue never filed through the
	// form still declares its scope. Same placement and the same rule as the
	// board: after the workspace is real, and never fatal.
	if !o.NoWriteback {
		if err := writeBackScope(gh, o.Repo, iss.Number, routing.Keys(sel), rep); err != nil {
			rep.Warn("issue body: %v", err)
		}
	}

	fmt.Printf("\nWorkspace ready: %s\n", ws)

	// The multiplexer comes last and is never fatal: the clones, the branch
	// and the CLAUDE.md all exist by now. By DEFAULT nothing is opened here
	// and no shell is spawned -- facet sets the workspace up and prints where
	// to work. Opening -- and the shell it runs -- is opt-in via --attach.
	//
	// When we do open a pane, the pane owns the agent: launching a second,
	// blocking claude in this terminal underneath it would fight for stdin.
	if o.Attach && !o.NoAttach && o.Mux != "none" {
		l := muxFor(o.Mux)
		if l == nil {
			fmt.Printf("\nNo multiplexer available; work in %s\n", filepath.Join(ws, homeDir))
			return nil
		}
		// spawn opens beside whatever you are doing; it must not move you. A
		// freshly spawned workspace has no session of its own to switch to
		// anyway.
		_, asTab := mux.AutoOpen(l, o.OwnSession)
		agent := o.Agent.agentOpts
		agent.SessionNamePrefix = route.SpawnSessionPrefix()
		return openSession(ws, wsName, homeDir, o.Number, openOpts{
			Mux: o.Mux, AsTab: asTab, Focus: true, Agent: agent,
		})
	}

	fmt.Printf("\nwork in:    %s\n", filepath.Join(ws, homeDir))
	if o.Mux != "none" {
		fmt.Printf("open it:    facet attach --path %s\n", ws)
	}

	// With no pane to put it in, launching an agent means taking over THIS
	// terminal, which no default may decide -- so unlike the pane, this stays
	// off unless routing or a flag turns it on. It runs last, once the clones,
	// branch and CLAUDE.md all exist, and is never fatal, exactly like the board
	// move and the scope write-back above.
	if resolveLaunch(o, route) {
		workDir := filepath.Join(ws, homeDir)
		opts := claudex.Options{
			RemoteControl:     o.Agent.Remote,
			SessionName:       wsName,
			SessionNamePrefix: route.SpawnSessionPrefix(),
		}
		fmt.Printf("\nlaunching %s in %s\n", claudex.ShellCommand(opts), workDir)
		if err := claudex.Launch(workDir, opts); err != nil {
			rep.Warn("%s: %v (workspace is ready; open it yourself in %s)",
				claudex.Exe, err, workDir)
		}
	}
	return nil
}

// resolveLaunch decides whether to start claude in this terminal. A named flag
// wins over the routing default; --claude=false and its --no-rc alias win over
// everything.
func resolveLaunch(o spawnOpts, route *routing.Routing) bool {
	if !o.Agent.Claude {
		return false
	}
	if o.Agent.Explicit {
		return true
	}
	return route.SpawnRC()
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
	route *routing.Routing, branch, seatName string, scope []seat.Ref, seatIssue string,
	frags []knowledge.Fragment, fragErrs []error) {

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
	fmt.Printf("seat:      %s\n", seatName)
	fmt.Printf("scope:     %s\n", seat.Join(scope))
	// Shown in the plan as well as reported at write time, so --dry-run
	// confirms it before anything is created. A field that only appears when
	// set reads as one nobody thought to print.
	if seatIssue != "" {
		fmt.Printf("seat issue: %s\n", seatIssue)
	} else {
		fmt.Printf("seat issue: (none)\n")
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

// writeBackScope records the confirmed repo set in the issue's "Repos in scope"
// section, so the next spawn reads a decision instead of repeating a guess.
//
// The body is re-read first. Several agents work these issues at once, and the
// copy fetched at the top of spawn may be minutes old by the time the clones
// finish -- writing that stale copy back would silently revert whatever someone
// else wrote in between. A body that already names exactly these repos is left
// untouched, so spawning the same issue twice does not churn its history.
func writeBackScope(gh ghx.Client, repo string, number int, keys []string, rep workspace.Reporter) error {
	fresh, err := gh.ViewIssue(repo, number)
	if err != nil {
		return err
	}
	body, changed := routing.UpsertScope(fresh.Body, keys)
	if !changed {
		return nil
	}
	if err := gh.SetIssueBody(repo, number, body); err != nil {
		return err
	}
	rep.Created("issue body: Repos in scope = %s", strings.Join(keys, ", "))
	return nil
}
