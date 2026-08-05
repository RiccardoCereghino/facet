// The bodies of the `facet tree` commands. The cobra wiring is in tree.go;
// keeping the two apart means a change to what a command DOES is not buried in
// a diff about which flags it takes.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
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
	AddLabel(repo string, number int, label string) error
	RemoveSubIssue(repo string, number int, childID int64) error
	ProjectStatuses(owner string, projectNumber int, field string) (map[string]string, error)
}

// loadRouting reads the routing file, treating its ABSENCE as an empty one.
//
// These commands read GitHub, not the lab: the routing file only ever tells
// them which levels to expect and which comment kinds exist, and having none
// of either is a legitimate state. Refusing to render a tree because there is
// no routing file would make `show` require a configuration it does not use --
// against the rule that these commands work on a tree facet never built.
//
// A routing file that EXISTS and is malformed still refuses. "There is no
// configuration" and "the configuration is broken" are different answers, and
// only the first is ordinary.
func loadRouting() (*routing.Routing, error) {
	route, err := routing.Load(roots.Routing)
	if errors.Is(err, os.ErrNotExist) {
		return &routing.Routing{}, nil
	}
	return route, err
}

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

	// There is no atomic move on this API: an issue with a parent refuses a
	// second POST outright (422), so re-parenting is DELETE the old edge,
	// then POST the new one. Print the detach before attaching -- if the POST
	// below fails, this line is already on stdout and the issue's true state
	// (unparented, not silently still-under-the-old-parent) is recoverable by
	// hand rather than only inferable from a stack trace.
	if hadParent {
		if err := gh.RemoveSubIssue(previous.OwnerRepo(), previous.Number, id); err != nil {
			return fmt.Errorf("could not detach %s from its current parent %s, so did not attempt to attach it to %s: %w", child, previous, parent, err)
		}
		_, _ = fmt.Fprintf(w, "  detached %s from %s\n", child, previous)
	}

	if err := gh.AddSubIssue(parent.OwnerRepo(), parent.Number, id); err != nil {
		return err
	}

	recordLevel(w, gh, route, child, childIssue)
	printTiers(w, child, childIssue, parent, parentIssue)
	if hadParent {
		_, _ = fmt.Fprintf(w, "\n  MOVED: %s was a child of %s\n", child, previous)
	}
	_, _ = fmt.Fprintf(w, "\n  wired %s under %s\n", child, parent)
	return nil
}

// recordLevel writes the level the wire just enforced onto the issue as a
// label.
//
// `wire` already DERIVES the level -- refuseByStructure refuses an edge whose
// child does not match the rung -- and then threw the answer away. Every other
// actor was left re-deriving it by matching a title prefix, which is invisible
// to `gh issue list --label`, silently wrong after a retitle, and absent
// entirely for a node whose title never had the prefix.
//
// A failure to label is REPORTED AND NOT FATAL. The edge is already written and
// is the thing being asked for; refusing here would leave a wired tree and a
// non-zero exit, which reads as "nothing happened".
func recordLevel(w io.Writer, gh treeGH, route *routing.Routing, child ghx.IssueRef, ci *ghx.Issue) {
	if route == nil || route.Structure == nil {
		return
	}
	level, ok, err := levelOf(gh, route, child)
	if err != nil || !ok {
		_, _ = fmt.Fprintf(w, "  level not recorded: %s sits at no declared level\n", child)
		return
	}
	label, ok := route.Structure.LabelFor(level, route.KeyForRepo(child.OwnerRepo()), ci.Title)
	if !ok {
		// A structure that declares no labels keeps working exactly as before.
		return
	}
	if err := gh.AddLabel(child.OwnerRepo(), child.Number, label); err != nil {
		_, _ = fmt.Fprintf(w, "  WARNING: the edge is wired but %s was not applied to %s: %v\n", label, child, err)
		_, _ = fmt.Fprintf(w, "  fix: gh issue edit %d --repo %s --add-label %s\n", child.Number, child.OwnerRepo(), label)
		return
	}
	_, _ = fmt.Fprintf(w, "  level recorded: %s\n", label)
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

	// The child's level can only be judged from the parent's, so establish the
	// PARENT'S LEVEL first -- not its depth. The two are equal only while no
	// optional rung is ever skipped above the edge, and they are exactly what
	// a skipped rung separates: skip one and the parent sits at a level deeper
	// than its ancestor count. Judging by depth there would let `wire` write
	// the very edge `doctor` reports as a defect, in the command whose whole
	// purpose is making the wrong shape unrepresentable.
	level, ok, err := levelOf(gh, route, parent)
	if err != nil {
		// Refuse when you cannot tell. A probe that errors is not a pass, and
		// silently skipping the check is how a wrong edge gets written by the
		// tool built to prevent it.
		return fmt.Errorf("could not establish %s's level, so cannot check where %s would sit: %w\n"+
			"fix: resolve the read above, or wire it by hand if the structure is known to be right",
			parent, child, err)
	}
	if !ok {
		return fmt.Errorf("%s does not itself sit at any level this structure declares, "+
			"so where %s belongs under it cannot be judged\n"+
			"fix: run `facet tree doctor` on the tree above %s and correct it first",
			parent, child, parent)
	}

	key := route.KeyForRepo(child.OwnerRepo())
	if _, ok := s.Assign(level, key, childIssue.Title); ok {
		return nil
	}

	var want []string
	for _, i := range s.ChildLevels(level) {
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

// levelOf delegates to the walk's own resolver, so the edge check and the
// walk cannot disagree about one tree. Kept as a named wrapper because the
// refusal below reads better against a local name than an import path.
func levelOf(gh treeGH, route *routing.Routing, ref ghx.IssueRef) (int, bool, error) {
	return tree.LevelOf(gh, route, ref)
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

func runTreeDoctor(w io.Writer, gh treeGH, ref ghx.IssueRef, fixLabels bool) error {
	root, route, err := walk(gh, ref, -1)
	if err != nil {
		return err
	}
	if fixLabels {
		if err := backfillLabels(w, gh, root, route); err != nil {
			return err
		}
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

// backfillLabels records the level on every node that is missing it.
//
// It applies ONLY the unambiguous case. A node whose label CONTRADICTS its
// position is left exactly as it is and reported by the doctor pass that
// follows: two sources of truth is the defect, and quietly picking one is how
// a backfill destroys the evidence of which was wrong. Same reason a node whose
// level could not be established is skipped rather than guessed at.
//
// The tree is re-walked by the caller's doctor pass afterwards, so what this
// printed and what the report then says cannot disagree.
func backfillLabels(w io.Writer, gh treeGH, root *tree.Node, route *routing.Routing) error {
	if route == nil || route.Structure == nil {
		_, _ = fmt.Fprintf(w, "no labels to backfill: the routing file declares no `structure`\n")
		return nil
	}
	if len(route.Structure.Labels()) == 0 {
		_, _ = fmt.Fprintf(w, "no labels to backfill: no level in `structure` declares a label\n")
		return nil
	}

	known := map[string]bool{}
	for _, l := range route.Structure.Labels() {
		known[l] = true
	}

	applied, skipped := 0, 0
	for _, n := range append([]*tree.Node{root}, root.Descendants()...) {
		if n.Err != nil || !n.Assigned {
			skipped++
			continue
		}
		want, ok := route.Structure.LabelFor(n.Level, route.KeyForRepo(n.Ref.OwnerRepo()), n.Title)
		if !ok {
			continue
		}
		has := false
		conflict := false
		for _, l := range n.Labels {
			switch {
			case l == want:
				has = true
			case known[l]:
				conflict = true
			}
		}
		if has || conflict {
			continue
		}
		if err := gh.AddLabel(n.Ref.OwnerRepo(), n.Ref.Number, want); err != nil {
			return fmt.Errorf("could not label %s with %s: %w", n.Ref, want, err)
		}
		_, _ = fmt.Fprintf(w, "  labelled %-28s %s\n", n.Ref, want)
		applied++
	}
	_, _ = fmt.Fprintf(w, "backfill: %d labelled", applied)
	if skipped > 0 {
		// Never silent about what was not reached: a count that only reports
		// successes reads as complete coverage.
		_, _ = fmt.Fprintf(w, ", %d not placeable and skipped", skipped)
	}
	_, _ = fmt.Fprintf(w, "\n\n")
	return nil
}
