// The bodies of the `facet tree` commands. The cobra wiring is in tree.go;
// keeping the two apart means a change to what a command DOES is not buried in
// a diff about which flags it takes.
package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/RiccardoCereghino/facet/internal/ghx"
	"github.com/RiccardoCereghino/facet/internal/routing"
	"github.com/RiccardoCereghino/facet/internal/tree"
)

// treeGH is the GitHub surface these commands use. Narrower than ghx.Client so
// the tests script five methods rather than twenty.
type treeGH interface {
	tree.Source
	IssueID(repo string, number int) (int64, error)
	IssueParent(repo string, number int) (ghx.IssueRef, bool, error)
	AddSubIssue(repo string, number int, childID int64) error
	ProjectStatuses(owner string, projectNumber int, field string) (map[string]string, error)
}

func loadRouting() (*routing.Routing, error) { return routing.Load(roots.Routing) }

// runTreeWire attaches child to parent and reports what it did.
func runTreeWire(w io.Writer, gh treeGH, child, parent ghx.IssueRef) error {
	if child == parent {
		return fmt.Errorf("%s cannot be its own parent", child)
	}

	childIssue, err := gh.ViewIssue(child.OwnerRepo(), child.Number)
	if err != nil {
		return err
	}
	parentIssue, err := gh.ViewIssue(parent.OwnerRepo(), parent.Number)
	if err != nil {
		return err
	}
	if childIssue == nil || parentIssue == nil {
		return fmt.Errorf("both issues must exist before they can be wired")
	}

	// Read the existing parent BEFORE writing. An issue has exactly one
	// parent, so this call is a move whenever there was one, and GitHub's
	// response is identical either way -- so if it is not read now, nobody
	// will ever know what the edge replaced.
	previous, hadParent, err := gh.IssueParent(child.OwnerRepo(), child.Number)
	if err != nil {
		return fmt.Errorf("could not read %s's current parent, so cannot tell whether this would move it: %w", child, err)
	}
	if hadParent && previous == parent {
		_, _ = fmt.Fprintf(w, "  %s is already a child of %s; nothing to do\n", child, parent)
		return nil
	}

	route, err := loadRouting()
	if err != nil {
		return err
	}
	if err := refuseByStructure(gh, route, child, childIssue, parent); err != nil {
		return err
	}

	id, err := gh.IssueID(child.OwnerRepo(), child.Number)
	if err != nil {
		return err
	}
	if err := gh.AddSubIssue(parent.OwnerRepo(), parent.Number, id); err != nil {
		return err
	}

	printTiers(w, child, childIssue, parent, parentIssue)
	if hadParent {
		_, _ = fmt.Fprintf(w, "\n  MOVED: %s was a child of %s\n", child, previous)
	}
	_, _ = fmt.Fprintf(w, "\n  wired %s under %s\n", child, parent)
	return nil
}

// printTiers states both tiers and which of them governs.
//
// A parent's complexity is an at-a-glance worst case for a grouping. It is NOT
// inherited: each pull request is considered on its own issue's label, so
// wiring cannot change who may merge the child's work. Both are printed anyway
// -- the worst case is the useful half, and stating the rule at the moment of
// the edge is what stops it being re-derived as an obvious improvement.
func printTiers(w io.Writer, child ghx.IssueRef, ci *ghx.Issue, parent ghx.IssueRef, pi *ghx.Issue) {
	childTier := describeTier(&tree.Node{Labels: ci.LabelNames()})
	parentTier := describeTier(&tree.Node{Labels: pi.LabelNames()})
	_, _ = fmt.Fprintf(w, "  child   %-22s %-14s -> merges at %s\n", child, childTier, childTier)
	_, _ = fmt.Fprintf(w, "  parent  %-22s %-14s (worst case in this grouping)\n", parent, parentTier)
	_, _ = fmt.Fprintf(w, "\n  merge authority is the CHILD's own tier; a parent's is never inherited\n")
}

func describeTier(n *tree.Node) string {
	tier, found := n.Tier()
	switch {
	case tier != "":
		return tier
	case len(found) > 1:
		return "ambiguous(" + strings.Join(found, ",") + ")"
	default:
		return "no tier"
	}
}

// refuseByStructure rejects an edge the declared levels forbid. With no
// structure configured it permits everything, which is the point: the shape is
// an adopter's contract and facet imposes none of its own.
func refuseByStructure(gh treeGH, route *routing.Routing, child ghx.IssueRef, childIssue *ghx.Issue, parent ghx.IssueRef) error {
	s := route.Structure
	if s == nil {
		return nil
	}

	// The child's level can only be judged from the parent's, so establish
	// that first by climbing to the root. The climb uses the child->parent
	// direction, which is the immediately consistent one, and it is bounded by
	// the depth of the tree -- four calls, not a graph walk.
	depth, err := depthOf(gh, parent)
	if err != nil {
		// Refuse when you cannot tell. A probe that errors is not a pass, and
		// silently skipping the check is how a wrong edge gets written by the
		// tool built to prevent it.
		return fmt.Errorf("could not establish %s's depth, so cannot check where %s would sit: %w\n"+
			"fix: resolve the read above, or wire it by hand if the structure is known to be right",
			parent, child, err)
	}

	key := route.KeyForRepo(child.OwnerRepo())
	if _, ok := s.Assign(depth, key, childIssue.Title); ok {
		return nil
	}

	var want []string
	for _, i := range s.ChildLevels(depth) {
		want = append(want, s.Levels[i].Describe())
	}
	if len(want) == 0 {
		return fmt.Errorf("%s is at the deepest declared level, so nothing may hang below it\n"+
			"  levels are: %s\n"+
			"fix: attach %s further up the tree",
			parent, strings.Join(levelNames(route), " > "), child)
	}
	return fmt.Errorf("%s cannot sit under %s\n"+
		"  %s is %q in %s\n"+
		"  a child of %s must be %s\n"+
		"fix: attach it to a node one level up, or correct its title or repo",
		child, parent,
		child, childIssue.Title, child.OwnerRepo(),
		parent, strings.Join(want, ", or "))
}

// depthOf counts how far an issue sits below its root, by climbing parents.
//
// The climb is the child->parent direction on purpose: it is immediately
// consistent, so an edge written moments ago is already visible, whereas
// listing children is not. A cycle would otherwise spin forever, so the path
// is tracked.
func depthOf(gh treeGH, ref ghx.IssueRef) (int, error) {
	seen := map[string]bool{ref.String(): true}
	depth := 0
	at := ref
	for {
		parent, ok, err := gh.IssueParent(at.OwnerRepo(), at.Number)
		if err != nil {
			return 0, err
		}
		if !ok {
			return depth, nil
		}
		if seen[parent.String()] {
			return 0, fmt.Errorf("cycle above %s: %s is its own ancestor", ref, parent)
		}
		seen[parent.String()] = true
		at = parent
		depth++
	}
}

func walk(gh treeGH, ref ghx.IssueRef, depth int) (*tree.Node, *routing.Routing, error) {
	route, err := loadRouting()
	if err != nil {
		return nil, nil, err
	}
	root, err := tree.Walk(gh, ref, depth, route)
	if err != nil {
		return nil, nil, err
	}
	return root, route, nil
}

func runTreeShow(w io.Writer, gh treeGH, ref ghx.IssueRef, depth int) error {
	root, route, err := walk(gh, ref, depth)
	if err != nil {
		return err
	}
	renderNode(w, root, route, "")
	return nil
}

func renderNode(w io.Writer, n *tree.Node, route *routing.Routing, indent string) {
	_, _ = fmt.Fprintf(w, "%s%s  %s\n", indent, n.Ref, describeNode(n, route))
	for _, c := range n.Children {
		renderNode(w, c, route, indent+"  ")
	}
}

func describeNode(n *tree.Node, route *routing.Routing) string {
	if n.Err != nil {
		return "!! " + n.Err.Error()
	}
	var parts []string
	if lvl := levelName(n, route); lvl != "" {
		parts = append(parts, "["+lvl+"]")
	}
	if n.IsClosed() {
		parts = append(parts, "(closed)")
	}
	parts = append(parts, truncate(n.Title, 58))
	return strings.Join(parts, " ")
}

// levelName reports the declared level, "?" for a node that matched none, and
// "" when no structure is configured -- three states, because "no levels
// declared" and "at the wrong level" must not print the same.
func levelName(n *tree.Node, route *routing.Routing) string {
	if route == nil || route.Structure == nil || !n.LevelKnown {
		return ""
	}
	if !n.Assigned {
		return "?"
	}
	return route.Structure.Levels[n.Level].Name
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func runTreeList(w io.Writer, gh treeGH, ref ghx.IssueRef, level string) error {
	root, route, err := walk(gh, ref, -1)
	if err != nil {
		return err
	}
	if level != "" && (route.Structure == nil) {
		return fmt.Errorf("--level %q needs a `structure` block in the routing file\n"+
			"fix: declare the levels, or drop --level", level)
	}
	if level != "" && !declaresLevel(route, level) {
		return fmt.Errorf("no level named %q; this routing file declares: %s",
			level, strings.Join(levelNames(route), ", "))
	}
	for _, n := range root.Descendants() {
		if level != "" && levelName(n, route) != level {
			continue
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\n", n.Ref, truncate(n.Title, 70))
	}
	return nil
}

func declaresLevel(route *routing.Routing, name string) bool {
	for _, n := range levelNames(route) {
		if n == name {
			return true
		}
	}
	return false
}

func levelNames(route *routing.Routing) []string {
	if route == nil || route.Structure == nil {
		return nil
	}
	out := make([]string, 0, len(route.Structure.Levels))
	for _, l := range route.Structure.Levels {
		out = append(out, l.Name)
	}
	return out
}

func runTreeStatus(w io.Writer, gh treeGH, ref ghx.IssueRef) error {
	root, route, err := walk(gh, ref, -1)
	if err != nil {
		return err
	}
	nodes := root.Descendants()

	p := route.Project
	if p == nil || p.StatusField == "" {
		// Refusing beats degrading. An open/closed count looks like a full
		// answer, and "0 in progress" would read as measured rather than as
		// unavailable.
		return fmt.Errorf("cannot derive status: the routing file has no `project` block with a statusField\n"+
			"  %s has %d %s below it, but open/closed cannot tell started from not\n"+
			"fix: add project.{owner,number,statusField} to the routing file",
			ref, len(nodes), plural(len(nodes), "issue", "issues"))
	}

	statuses, err := gh.ProjectStatuses(p.Owner, p.Number, p.StatusField)
	if err != nil {
		return err
	}
	c := tree.Tally(nodes, statuses)

	_, _ = fmt.Fprintf(w, "%s  %s\n", ref, truncate(root.Title, 58))
	_, _ = fmt.Fprintf(w, "  %d %s below it\n", c.Total, plural(c.Total, "issue", "issues"))
	for _, name := range c.StatusNames() {
		_, _ = fmt.Fprintf(w, "  %5d  %s\n", c.ByStatus[name], name)
	}
	if c.Unknown > 0 {
		// Not on the board is a different fact from not started.
		_, _ = fmt.Fprintf(w, "  %5d  not on the board\n", c.Unknown)
	}
	return nil
}

func runTreeDoctor(w io.Writer, gh treeGH, ref ghx.IssueRef) error {
	root, route, err := walk(gh, ref, -1)
	if err != nil {
		return err
	}
	defects := tree.Doctor(root, route)
	if len(defects) == 0 {
		if route.Structure == nil {
			_, _ = fmt.Fprintf(w, "no defects in %s\n", ref)
			// Say what was NOT checked. Silence about the shape reads as a
			// clean bill of health for it.
			_, _ = fmt.Fprintf(w, "  (shape was not checked: no `structure` block in the routing file)\n")
			return nil
		}
		_, _ = fmt.Fprintf(w, "no defects in %s\n", ref)
		return nil
	}
	for _, d := range defects {
		_, _ = fmt.Fprintln(w, d)
	}
	return fmt.Errorf("%d defect(s) in %s", len(defects), ref)
}
