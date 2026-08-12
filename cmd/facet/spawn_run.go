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
	"sort"
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
	// catalogFailed carries a non-fatal failure to the end of the function, so
	// the exit code can be non-zero without abandoning a workspace that is
	// otherwise complete.
	var catalogFailed string
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
	if err := requirePreflight(os.Stderr, "spawn", o.UnsoundCredential); err != nil {
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
	// Checked before the issue lookup, same as the seat and scope checks above:
	// a repo marked spawnable:false (the workspaces root itself, cloned into
	// itself, is the case this exists for) should never reach the forge on the
	// way to a refusal.
	if !route.Repos[homeKey].IsSpawnable() {
		return fmt.Errorf("%s (%s) is marked spawnable:false in %s\n"+
			"fix: work it directly -- it already has a standing checkout -- or add it to a workspace by hand with `facet new`/`facet add`, not `facet spawn`",
			o.Repo, homeKey, roots.Routing)
	}

	iss, err := gh.ViewIssue(o.Repo, o.Number)
	if err != nil {
		return err
	}
	if !iss.IsOpen() && !o.Yes {
		return fmt.Errorf("issue %s#%d is %s; pass --yes to spawn anyway", o.Repo, o.Number, iss.State)
	}

	sel, hints := route.Infer(o.Repo, iss)
	sel, err = applyOverrides(sel, route, homeKey, o)
	if err != nil {
		return err
	}

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
	// The command catalog, so every seat has the whole tool surface at spawn
	// for zero sessions and zero turns (facet#118). Written AFTER identity and
	// before the clones: it needs nothing from them, and a slow probe should
	// not sit between a workspace being created and it saying whose it is.
	catRes, err := seat.WriteCatalog(ws, arganoBin())
	if err != nil {
		return err
	}
	if catRes.OK {
		rep.Created("%s: the command catalog", seat.CatalogFile)
	} else {
		// Named, not silent, and the exit is non-zero at the end. A spawn that
		// half-worked must not read as one that worked.
		rep.Created("%s: GENERATION FAILED -- %s", seat.CatalogFile, catRes.Detail)
		catalogFailed = catRes.Detail
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
	if o.Writeback {
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

	// The workspace is complete and usable, and one thing in it is not what it
	// claims to be. Non-zero LAST rather than early, so the spawn is not
	// abandoned over a probe -- but non-zero, because a seat reading a catalog
	// that says "generation failed" should have been told at spawn time, not
	// discovered it later.
	if catalogFailed != "" {
		return fmt.Errorf("the workspace is ready, but %s says it could not be generated: %s\n"+
			"fix: install argano and regenerate it: argano catalog --json > %s",
			seat.CatalogFile, catalogFailed, filepath.Join(ws, seat.CatalogFile))
	}
	return nil
}

// arganoBin is where the catalog generator lives.
//
// $ARGANO_BIN first so a test, or a machine that installs elsewhere, can point
// at its own. facet does NOT refuse when argano is absent: the catalog is a
// convenience for the seat, and a spawn is the thing being asked for.
func arganoBin() string {
	if v := os.Getenv("ARGANO_BIN"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "go", "bin", "argano")
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

// overrideKey resolves one --clone/--add/--rm value to a routing key, and
// REFUSES anything that cannot be one.
//
// Two spellings are accepted, deliberately: a bare routing key, and
// `key=url`. The second is what `facet new`/`facet edit` have always meant by
// --clone (dir=giturl), and it is the only form the harness emits --
// stele-home's lib/identity.sh passes `--clone <short-name>=git@github.com:…`,
// one per repo the seat's scope names. Every routing key today IS the short
// repo name, so the two grammars pick out the same repo.
//
// THE URL IS CHECKED RATHER THAN IGNORED. It cannot be honoured as an override:
// a selection resolves its directory, extra remotes and LFS flag out of
// route.Repos[key], so an override URL paired with routing's directory would be
// a half-applied override. And it cannot be discarded, because a discarded
// override that looks like an applied one is this function's whole defect
// (facet#160). So a URL that disagrees with routing refuses and prints both.
func overrideKey(flag, value string, route *routing.Routing) (string, error) {
	key, url, hasURL := strings.Cut(value, "=")
	if key == "" {
		return "", fmt.Errorf("--%s %q names no repository\n"+
			"fix: pass a routing key, or key=url: --%s %s", flag, value, flag, someKey(route))
	}
	// Aliases resolve here for the same reason they resolve in Infer
	// (routing.go): routing says `doctrine` names `stele`, so a flag that
	// refused it would be refusing a value routing itself can resolve. Repos
	// first, then aliases -- Infer's order, so one spelling cannot mean two
	// repos depending on which code path read it.
	r, ok := route.Repos[key]
	if !ok {
		if alias, hit := route.Aliases[key]; hit {
			ar, defined := route.Repos[alias]
			if !defined {
				// A dangling alias is a defect in routing, and saying which
				// hop is missing is the difference between fixing it and
				// re-typing the flag.
				return "", fmt.Errorf("--%s %q resolves through alias %q to %q, which %s defines no repo for\n"+
					"fix: correct the alias in routing, or name a key from: %s",
					flag, value, key, alias, roots.Routing, strings.Join(routeKeys(route), " "))
			}
			key, r, ok = alias, ar, true
		}
	}
	if !ok {
		spelling := "is not a routing key or alias"
		if hasURL {
			spelling = fmt.Sprintf("parses as key %q plus a url, and %q is not a routing key or alias", key, key)
		}
		return "", fmt.Errorf("--%s %q %s\n"+
			"known keys: %s\n"+
			"fix: name one of those, as a bare key or key=url",
			flag, value, spelling, strings.Join(routeKeys(route), " "))
	}
	if hasURL && url != r.URL {
		return "", fmt.Errorf("--%s %q supplies a url that is not the one routing has for %q\n"+
			"  you passed: %s\n"+
			"  routing has: %s\n"+
			"fix: drop the url and pass the bare key (routing's is what would be cloned), "+
			"or correct one of the two so they agree", flag, value, key, url, r.URL)
	}
	return key, nil
}

// routeKeys lists every routing key, sorted, for a refusal to print. Sorted
// because a map's order is random and a refusal that reads differently on every
// run is one nobody can diff.
func routeKeys(route *routing.Routing) []string {
	keys := make([]string, 0, len(route.Repos))
	for k := range route.Repos {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// someKey is one real key to show in a usage line, or a placeholder when
// routing knows none.
func someKey(route *routing.Routing) string {
	if keys := routeKeys(route); len(keys) > 0 {
		return keys[0]
	}
	return "<routing-key>"
}

// applyOverrides lets the operator correct the inference. --clone replaces it
// wholesale; --add and --rm adjust it. The home repo can never be removed: it
// carries the branch.
//
// EVERY VALUE IS RESOLVED THROUGH overrideKey, AND AN UNRESOLVABLE ONE REFUSES.
// All three flags used to skip what they could not recognise -- --clone by
// entering its `if ok` branch only on a hit, --add and --rm by discarding a
// miss -- so `facet spawn --clone stele=git@…` produced a workspace holding the
// home repo alone, at exit 0, with nothing in the output distinguishing "I
// applied your override" from "I threw it away" (facet#160). It ran that way
// under every multi-repo seating the harness ever performed.
//
// This returns an error rather than warning, and it is called before the issue
// branch is created and before the workspace directory exists, so a refusal
// leaves nothing behind on disk or on the forge.
func applyOverrides(sel []routing.Selection, route *routing.Routing, homeKey string, o spawnOpts) ([]routing.Selection, error) {
	if len(o.Clones) > 0 {
		replaced := []routing.Selection{{Key: homeKey, Reasons: []string{"home"}, Home: true}}
		for _, v := range o.Clones {
			k, err := overrideKey("clone", v, route)
			if err != nil {
				return nil, err
			}
			// The home repo is accepted and skipped rather than refused: it is
			// already in the set, it carries the branch, and the harness passes
			// it alongside the rest.
			if k == homeKey {
				continue
			}
			// Deduplicated because two spellings can now resolve to one repo:
			// `--clone stele --clone doctrine` is one repository named twice,
			// and a set listing it twice would print it twice, count it twice
			// in the confirmation, and clone it twice.
			seen := false
			for _, s := range replaced {
				if s.Key == k {
					seen = true
				}
			}
			if seen {
				continue
			}
			replaced = append(replaced, routing.Selection{Key: k, Reasons: []string{"manual"}})
		}
		sel = replaced
	}
	for _, v := range o.Add {
		k, err := overrideKey("add", v, route)
		if err != nil {
			return nil, err
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
		for _, v := range o.Remove {
			k, err := overrideKey("rm", v, route)
			if err != nil {
				return nil, err
			}
			drop[k] = true
		}
		// A key that routing knows but the set does not hold stays a no-op: the
		// state the operator asked for -- that repo absent -- is the state they
		// get. What refuses above is a value that could never name a repo at
		// all, which is a different thing and is never satisfied by silence.
		var kept []routing.Selection
		for _, s := range sel {
			if drop[s.Key] && !s.Home {
				continue
			}
			kept = append(kept, s)
		}
		sel = kept
	}
	return sel, nil
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
