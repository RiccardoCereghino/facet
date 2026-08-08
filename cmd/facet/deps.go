package main

import (
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/RiccardoCereghino/facet/internal/ghx"
	"github.com/RiccardoCereghino/facet/internal/routing"
	"github.com/RiccardoCereghino/facet/internal/tree"
)

// depsGH is the narrow surface the dependency commands need.
type depsGH interface {
	tree.Source
	BlockedBy(repo string, number int) ([]ghx.IssueRef, error)
	Blocking(repo string, number int) ([]ghx.IssueRef, error)
}

// newDepsCmd groups the dependency graph, which is NOT the issue graph.
//
// `blocked_by` says what must land first; a parent says what a thing is part
// of. They have different lifetimes and different shapes -- a parent does not
// imply an ordering, and a blocker does not imply membership -- so expressing
// one with the other loses information in both directions.
func newDepsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deps",
		Short: "Read and check issue dependencies",
		Long: "GitHub's issue-dependency edges: what must land before a thing, and what\n" +
			"cannot start until it does.\n\n" +
			"This is a different graph from `facet tree`. A parent says what an issue\n" +
			"is part of; a blocker says what has to happen first. Neither substitutes\n" +
			"for the other.",
	}
	cmd.AddCommand(newDepsShowCmd(), newDepsCheckCmd(), newDepsReadyCmd())
	return cmd
}

func newDepsShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <owner/repo#n>",
		Short: "Both dependency directions for one issue",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := parseIssueRef(args[0])
			if err != nil {
				return err
			}
			return runDepsShow(cmd.OutOrStdout(), gh, ref)
		},
	}
}

func newDepsCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check <owner/repo#n>",
		Short: "Compare the declared blockers against the wired ones",
		Long: "The issue body's \"Blocked by / waiting on\" section is the INPUT: `facet\n" +
			"file` reads it once and creates the edges, and every failure there is a\n" +
			"warning rather than a refusal, since the issue is already filed. Nothing\n" +
			"checks afterwards -- which is what this does.\n\n" +
			"Only one direction is a defect. A blocker DECLARED and not wired means the\n" +
			"write failed silently and the dependency exists solely as prose nobody\n" +
			"schedules from. A blocker wired and not mentioned in the body is normal:\n" +
			"after filing, the edge is the truth and the body simply ages.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := parseIssueRef(args[0])
			if err != nil {
				return err
			}
			return runDepsCheck(cmd.OutOrStdout(), gh, ref)
		},
	}
}

func newDepsReadyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ready <owner/repo#n>",
		Short: "Which issues below this one could be started now",
		Long: "Walks the tree below an issue and reports the open ones whose blockers\n" +
			"have all closed -- what could be picked up, rather than what exists.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := parseIssueRef(args[0])
			if err != nil {
				return err
			}
			return runDepsReady(cmd.OutOrStdout(), gh, ref)
		},
	}
}

func runDepsShow(w io.Writer, gh depsGH, ref ghx.IssueRef) error {
	blockers, err := gh.BlockedBy(ref.OwnerRepo(), ref.Number)
	if err != nil {
		return err
	}
	blocked, err := gh.Blocking(ref.OwnerRepo(), ref.Number)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(w, "%s\n", ref)
	printEdges(w, gh, "  blocked by", blockers)
	printEdges(w, gh, "  blocking  ", blocked)
	if len(blockers) == 0 && len(blocked) == 0 {
		_, _ = fmt.Fprintf(w, "  no dependency edges either way\n")
	}
	return nil
}

func printEdges(w io.Writer, gh depsGH, label string, refs []ghx.IssueRef) {
	for _, r := range refs {
		state := ""
		if iss, err := gh.ViewIssue(r.OwnerRepo(), r.Number); err == nil && iss != nil {
			if strings.EqualFold(iss.State, "CLOSED") {
				state = " (closed)"
			}
			_, _ = fmt.Fprintf(w, "%s  %s%s  %s\n", label, r, state, truncate(iss.Title, 50))
			continue
		}
		_, _ = fmt.Fprintf(w, "%s  %s\n", label, r)
	}
}

func runDepsCheck(w io.Writer, gh depsGH, ref ghx.IssueRef) error {
	iss, err := gh.ViewIssue(ref.OwnerRepo(), ref.Number)
	if err != nil {
		return err
	}
	if iss == nil {
		return fmt.Errorf("%s: no such issue", ref)
	}
	wired, err := gh.BlockedBy(ref.OwnerRepo(), ref.Number)
	if err != nil {
		return err
	}

	// PARSE WITH THE SAME FUNCTION THAT FILES THE EDGES. A second parser is a
	// second answer: a hand-rolled pattern reported false blockers by reading
	// the number in a shorthand reference as a bare one, and a check that
	// disagrees with the filer reports drift that is its own.
	declared := routing.ParseBlockedBy(iss.Body)

	wiredSet := map[string]bool{}
	for _, r := range wired {
		wiredSet[r.String()] = true
	}

	var missing []string
	for _, d := range declared {
		key := d.OwnerRepo
		if key == "" {
			key = ref.OwnerRepo() // a bare #n is same-repo
		}
		full := fmt.Sprintf("%s#%d", key, d.Number)
		if !wiredSet[full] {
			missing = append(missing, full)
		}
	}

	declaredSet := map[string]bool{}
	for _, d := range declared {
		key := d.OwnerRepo
		if key == "" {
			key = ref.OwnerRepo()
		}
		declaredSet[fmt.Sprintf("%s#%d", key, d.Number)] = true
	}
	var undeclared []string
	for _, r := range wired {
		if !declaredSet[r.String()] {
			undeclared = append(undeclared, r.String())
		}
	}

	_, _ = fmt.Fprintf(w, "%s  %d declared in the body, %d wired\n", ref, len(declared), len(wired))
	if len(undeclared) > 0 {
		// Normal, not a defect: after filing, the edge is the truth.
		_, _ = fmt.Fprintf(w, "  wired but not in the body (normal, the body ages): %s\n",
			strings.Join(undeclared, ", "))
	}
	if len(missing) == 0 {
		// "Every declared blocker is wired" is vacuously true of an issue that
		// declares none, and reads as a check having passed. Say which it was.
		if len(declared) == 0 {
			_, _ = fmt.Fprintf(w, "  the body declares no blockers, so there was nothing to check\n")
		} else {
			_, _ = fmt.Fprintf(w, "  every declared blocker is wired\n")
		}
		return nil
	}
	_, _ = fmt.Fprintf(w, "  DECLARED BUT NOT WIRED: %s\n", strings.Join(missing, ", "))
	_, _ = fmt.Fprintf(w, "    why: the edge write failed silently at filing, so this dependency\n"+
		"         exists only as prose and nothing schedules from it\n")
	_, _ = fmt.Fprintf(w, "    fix: wire each one, or correct the reference if it names nothing\n")
	return fmt.Errorf("%d declared blocker(s) are not wired on %s", len(missing), ref)
}

func runDepsReady(w io.Writer, gh depsGH, ref ghx.IssueRef) error {
	root, route, err := walkDeps(gh, ref)
	if err != nil {
		return err
	}
	_ = route

	// Read every edge and every blocker's state FIRST, together. One at a time
	// this was the whole cost of the command once the walk itself stopped
	// being: ~34 seconds of waiting for reads that cost nothing and do not
	// depend on each other.
	var live []*tree.Node
	for _, n := range root.Descendants() {
		if n.Err != nil || n.IsClosed() {
			continue
		}
		live = append(live, n)
	}
	edges, err := blockerEdges(gh, live)
	if err != nil {
		return err
	}
	blockerState := blockerStates(gh, edges)

	var ready, blocked int
	for _, n := range live {
		open := openBlockers(edges[n.Ref.String()], blockerState)
		if len(open) > 0 {
			blocked++
			continue
		}
		ready++
		_, _ = fmt.Fprintf(w, "%s\t%s\n", n.Ref, truncate(n.Title, 66))
	}
	_, _ = fmt.Fprintf(w, "\n%d ready, %d still blocked\n", ready, blocked)
	return nil
}

// depsWidth is how many dependency reads run at once. They are conditional
// REST reads: an unchanged one costs nothing, so latency is the only cost left
// and running them together is what removes it.
const depsWidth = 24

// blockerEdges reads every node's blocked-by edges, together.
func blockerEdges(gh depsGH, nodes []*tree.Node) (map[string][]ghx.IssueRef, error) {
	out := make([]([]ghx.IssueRef), len(nodes))
	errs := make([]error, len(nodes))
	sem := make(chan struct{}, depsWidth)
	var wg sync.WaitGroup
	for i, n := range nodes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i], errs[i] = gh.BlockedBy(n.Ref.OwnerRepo(), n.Ref.Number)
		}()
	}
	wg.Wait()

	edges := make(map[string][]ghx.IssueRef, len(nodes))
	for i, n := range nodes {
		if errs[i] != nil {
			return nil, errs[i]
		}
		edges[n.Ref.String()] = out[i]
	}
	return edges, nil
}

// blockerStates resolves each DISTINCT blocker once.
//
// A blocker holding ten issues was read ten times: the edge and the state came
// from different calls and nothing remembered the second. An empty state means
// the blocker could not be read, which is deliberately not the same as closed.
func blockerStates(gh depsGH, edges map[string][]ghx.IssueRef) map[string]string {
	seen := map[string]ghx.IssueRef{}
	for _, refs := range edges {
		for _, r := range refs {
			seen[r.String()] = r
		}
	}
	distinct := make([]ghx.IssueRef, 0, len(seen))
	for _, r := range seen {
		distinct = append(distinct, r)
	}

	states := make([]string, len(distinct))
	sem := make(chan struct{}, depsWidth)
	var wg sync.WaitGroup
	for i, r := range distinct {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if iss, err := gh.ViewIssue(r.OwnerRepo(), r.Number); err == nil && iss != nil {
				states[i] = iss.State
			}
		}()
	}
	wg.Wait()

	out := make(map[string]string, len(distinct))
	for i, r := range distinct {
		out[r.String()] = states[i]
	}
	return out
}

// openBlockers names what is still in this node's way. Pure: everything it
// reads was gathered above.
//
// An unreadable blocker counts as OPEN. Calling something ready on a blocker
// nobody could read is the one wrong answer available here -- unknown is not
// absent.
func openBlockers(blockers []ghx.IssueRef, state map[string]string) []string {
	var open []string
	for _, b := range blockers {
		st := state[b.String()]
		if st == "" {
			open = append(open, b.String()+" (unreadable)")
			continue
		}
		if !strings.EqualFold(st, "CLOSED") {
			open = append(open, b.String())
		}
	}
	return open
}

func walkDeps(gh depsGH, ref ghx.IssueRef) (*tree.Node, *routing.Routing, error) {
	route, err := loadRouting()
	if err != nil {
		return nil, nil, err
	}
	root, err := tree.Walk(gh, ref, -1, route)
	if err != nil {
		return nil, nil, err
	}
	return root, route, nil
}
