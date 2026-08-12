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
	// RepoLabels and CreateLabel are how `wire` answers "does this label exist
	// at all?" without reading gh's prose -- see recordLevel.
	RepoLabels(repo string) ([]ghx.RepoLabel, error)
	CreateLabel(repo string, label ghx.RepoLabel) error
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
	if err := refuseByStructure(gh, route, child, childIssue, parent, parentIssue); err != nil {
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

	// The level may fail to record after the edge is written, and that is a
	// FAILURE rather than a warning -- see recordLevel. It is held rather than
	// returned here so the edge, the tiers and the move are all still printed:
	// what happened must be fully visible before the command says it did not
	// entirely work.
	levelErr := recordLevel(w, gh, route, child, childIssue)
	printTiers(w, child, childIssue, parent, parentIssue)
	if hadParent {
		_, _ = fmt.Fprintf(w, "\n  MOVED: %s was a child of %s\n", child, previous)
	}
	_, _ = fmt.Fprintf(w, "\n  wired %s under %s\n", child, parent)
	return levelErr
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
// A LABEL THAT DOES NOT EXIST IS CREATED AND THE WIRE CONTINUES. That is the
// commonest reason this fails, and the moment of use is exactly when somebody
// is present to notice. Only a label the structure DECLARES can be created, so
// a typo cannot bring one into existence.
//
// A LEVEL THAT STILL CANNOT BE RECORDED IS AN ERROR, not a warning. This
// printed a WARNING inside a successful command until facet#139, on the
// argument that "refusing here would leave a wired tree and a non-zero exit,
// which reads as nothing happened". The concern was real; the remedy was the
// wrong one. The answer is the WORDING -- every line about what did happen is
// printed first, and the error says in as many words that the edge is wired --
// because a command reporting success while the tree gains a node whose level
// is unknown is the defect underneath the defect. It was survived twice in one
// session, caught both times only because a human read a warning line inside a
// wall of successful output.
func recordLevel(w io.Writer, gh treeGH, route *routing.Routing, child ghx.IssueRef, ci *ghx.Issue) error {
	if route == nil || route.Structure == nil {
		return nil
	}
	level, ok, err := levelOf(gh, route, child, ci.LabelNames())
	if err != nil || !ok {
		_, _ = fmt.Fprintf(w, "  level not recorded: %s sits at no declared level\n", child)
		return nil
	}
	label, ok := route.Structure.LabelFor(level, route.KeyForRepo(child.OwnerRepo()), ci.Title, ci.LabelNames())
	if !ok {
		// A structure that declares no labels keeps working exactly as before.
		return nil
	}

	addErr := gh.AddLabel(child.OwnerRepo(), child.Number, label)
	if addErr == nil {
		_, _ = fmt.Fprintf(w, "  level recorded: %s\n", label)
		return nil
	}

	// ASK, DO NOT PARSE. gh says "'type/work' not found" when the label is
	// undefined, and classifying on that sentence would make this correct until
	// the day the sentence changes -- the same mistake as reading another
	// tool's prose to tell an error apart from a result. One extra read, only
	// on the failure path, answers it structurally instead.
	created, cerr := createDeclaredLabel(w, gh, route, child.OwnerRepo(), label)
	if cerr != nil {
		return fmt.Errorf("%s IS WIRED, and its level is NOT recorded: applying %s failed (%v), "+
			"and %s could not be created either: %w\n"+
			"fix: facet tree labels --repo %s --create",
			child, label, addErr, label, cerr, child.OwnerRepo())
	}
	if created {
		retry := gh.AddLabel(child.OwnerRepo(), child.Number, label)
		if retry == nil {
			_, _ = fmt.Fprintf(w, "  level recorded: %s\n", label)
			return nil
		}
		addErr = retry
	}

	_, _ = fmt.Fprintf(w, "  LEVEL NOT RECORDED: %s was not applied to %s: %v\n", label, child, addErr)
	return fmt.Errorf("%s IS WIRED, and its level is NOT recorded: %s was not applied: %w\n"+
		"  the tree now holds a node whose level nothing can read, and `tree doctor` can only check a level it can see\n"+
		"fix: gh issue edit %d --repo %s --add-label %s",
		child, label, addErr, child.Number, child.OwnerRepo(), label)
}

// createDeclaredLabel defines label in repo IF the repository genuinely does
// not have it. It reports whether it created one.
//
// The guard is doubled deliberately. The label is checked against the
// structure's own declared set before anything is written, so this can never
// create a name that came from anywhere but the routing file -- and the
// existence read means an AddLabel that failed for some OTHER reason (a
// permission, a rate limit) is reported as itself rather than misdiagnosed as
// a missing label.
func createDeclaredLabel(w io.Writer, gh treeGH, route *routing.Routing, repo, label string) (bool, error) {
	// LabelsFor, not Labels: a label declared on a repo-scoped shape can only
	// ever be applied in that repository, so creating it anywhere else would
	// define a label no wire there could reach.
	declared := false
	for _, l := range route.Structure.LabelsFor(route.KeyForRepo(repo)) {
		if l == label {
			declared = true
			break
		}
	}
	if !declared {
		return false, fmt.Errorf("%s is not a label this routing file's structure declares for %s", label, repo)
	}
	have, err := gh.RepoLabels(repo)
	if err != nil {
		return false, err
	}
	for _, l := range have {
		if l.Name == label {
			// It exists, so the failure was something else entirely and must
			// not be reported as a missing label.
			return false, nil
		}
	}
	if err := gh.CreateLabel(repo, ghx.RepoLabel{Name: label, Color: defaultLabelColour}); err != nil {
		return false, err
	}
	_, _ = fmt.Fprintf(w, "  created the label %s in %s -- it was not defined there\n", label, repo)
	return true, nil
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
func refuseByStructure(gh treeGH, route *routing.Routing, child ghx.IssueRef, childIssue *ghx.Issue, parent ghx.IssueRef, parentIssue *ghx.Issue) error {
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
	level, ok, err := levelOf(gh, route, parent, parentIssue.LabelNames())
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
	// What the PARENT is, not merely what rung it sits on, decides what may sit
	// below it -- two shapes can share a rung and permit different things.
	parentKey := route.KeyForRepo(parent.OwnerRepo())
	within := s.ChildLevelsFor(level, parentKey, parentIssue.Title, parentIssue.LabelNames())
	if _, ok := s.AssignWithin(within, key, childIssue.Title, childIssue.LabelNames()); ok {
		return nil
	}

	var want []string
	for _, i := range within {
		want = append(want, s.Levels[i].Describe())
	}
	if len(want) == 0 {
		// Two different reasons nothing may sit here, and they want different
		// fixes: the parent is as deep as the ladder goes, or its own shape
		// narrows its children to a rung it cannot actually hold. Saying the
		// first about the second sends someone to re-parent a correct node.
		if len(s.ChildLevels(level)) > 0 {
			return fmt.Errorf("%s permits nothing below it: its shape narrows its children to a level they may not occupy\n"+
				"  levels are: %s\n"+
				"fix: correct childMustBe on that shape in the routing file -- `facet` refuses this structure at load, so it was built by hand",
				parent, strings.Join(levelNames(route), " > "))
		}
		return fmt.Errorf("%s is at the deepest declared level, so nothing may hang below it\n"+
			"  levels are: %s\n"+
			"fix: attach %s further up the tree",
			parent, strings.Join(levelNames(route), " > "), child)
	}
	// The fix names LABELLING as well as retitling, because a level is recorded
	// by a label and a node whose title carries no convention -- most of them --
	// has no title to correct. A refusal whose only suggested remedy cannot be
	// performed teaches the reader to force it instead.
	return fmt.Errorf("%s cannot sit under %s\n"+
		"  %s is %q in %s\n"+
		"  a child of %s must be %s\n"+
		"fix: attach it to a node one level up, or give it the level's label, or correct its title or repo",
		child, parent,
		child, childIssue.Title, child.OwnerRepo(),
		parent, strings.Join(want, ", or "))
}

// levelOf delegates to the walk's own resolver, so the edge check and the
// walk cannot disagree about one tree. Kept as a named wrapper because the
// refusal below reads better against a local name than an import path.
func levelOf(gh treeGH, route *routing.Routing, ref ghx.IssueRef, refLabels []string) (int, bool, error) {
	return tree.LevelOf(gh, route, ref, refLabels)
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

// runTreeDoctor reports a tree's defects, and SAYS WHICH OF THE TWO NON-ZERO
// ANSWERS IT IS GIVING.
//
// Exit 1 means the walk happened and these are its findings. Exit 2 means it
// never happened -- the routing file, the reference, or GitHub stopped it
// before anything was read. Every error out of here is tagged one way or the
// other, so an error added later cannot silently arrive as a finding: the only
// untagged return is the clean one (facet#138).
func runTreeDoctor(w io.Writer, gh treeGH, ref ghx.IssueRef, fixLabels bool) error {
	root, route, err := walk(gh, ref, -1)
	if err != nil {
		return withCode(exitCantLook, err)
	}
	if fixLabels {
		if err := backfillLabels(w, gh, root, route); err != nil {
			// A write that failed is not a report about the tree either. The
			// backfill runs BEFORE the checks, so whatever this run would have
			// found was never computed.
			return withCode(exitCantLook, err)
		}
	}
	rep := tree.DoctorWithSource(root, route, gh)

	// THE DEFECTS ARE PRINTED FIRST AND ALWAYS, even when the exit code is
	// about to say "could not look". An honest exit code that swallows findings
	// costs the bracket the thing it exists for -- and `doctor` brackets every
	// write to the tree.
	for _, d := range rep.Defects {
		_, _ = fmt.Fprintln(w, d)
	}
	if len(rep.Unread) > 0 {
		_, _ = fmt.Fprintf(w,
			"\nCOULD NOT LOOK -- %d %s not read, so nothing below is reported OR ruled out:\n",
			len(rep.Unread), plural(len(rep.Unread), "node was", "nodes were"))
		for _, u := range rep.Unread {
			_, _ = fmt.Fprintln(w, u)
		}
	}
	// ACKNOWLEDGED IS COUNTED AND PRINTED SEPARATELY, NEVER HIDDEN (facet#159).
	// A suppression that lowers the headline defect count is how an instrument
	// starts lying politely -- so a run with acknowledged items still says
	// "N defect(s), M acknowledged" rather than a lowered N alone.
	if len(rep.Acknowledged) > 0 {
		_, _ = fmt.Fprintf(w,
			"\nACKNOWLEDGED -- %d closed holder(s) with no children, verified against a reason on the record:\n",
			len(rep.Acknowledged))
		for _, a := range rep.Acknowledged {
			_, _ = fmt.Fprintln(w, a)
		}
	}
	// Only when there is nothing to report AND nothing went unread. With an
	// unread node this run is not a clean bill of health, and saying "no
	// defects" beside a list of things it could not see would be the exact
	// conflation the section above exists to remove.
	if len(rep.Defects) == 0 && len(rep.Unread) == 0 {
		if len(rep.Acknowledged) > 0 {
			_, _ = fmt.Fprintf(w, "0 defect(s), %d acknowledged in %s\n", len(rep.Acknowledged), ref)
		} else {
			_, _ = fmt.Fprintf(w, "no defects in %s\n", ref)
		}
	}

	// ON EVERY PATH, and the clean one is why this exists. "no defects" is a
	// claim about WHAT WAS CHECKED and reads as a claim about the tree, so a
	// clean result was indistinguishable from an unexamined level -- which is
	// exactly how a closed block with no children sat in a tree that reported
	// itself clean, while the same run correctly reported three childless
	// holders (facet#146).
	//
	// It is printed on the could-not-look path too: which levels were EXAMINED
	// and which nodes were UNREAD are different halves of the same question,
	// and a reader needs both to know what the run actually covered.
	printCoverage(w, route)

	switch {
	case len(rep.Unread) > 0:
		// COULD-NOT-LOOK WINS OVER A FINDING HERE, and the reason is specific to
		// this verb rather than a house preference. `tree labels` answers 1 for
		// a finding alongside an unreadable repository, because its finding is
		// complete and independent of what it could not read. `doctor` walks ONE
		// tree: an unread node makes the report silent about everything beneath
		// it, so the findings that ARE present cannot be trusted as the whole
		// answer. The difference is whether the unread part could have changed
		// the reported part.
		return withCode(exitCantLook, fmt.Errorf(
			"%d node(s) in %s could not be read, so this is not a verdict on the tree (%d defect(s) found in what could be read)",
			len(rep.Unread), ref, len(rep.Defects)))
	case len(rep.Defects) > 0:
		if len(rep.Acknowledged) > 0 {
			return withCode(exitLooked, fmt.Errorf("%d defect(s), %d acknowledged in %s",
				len(rep.Defects), len(rep.Acknowledged), ref))
		}
		return withCode(exitLooked, fmt.Errorf("%d defect(s) in %s", len(rep.Defects), ref))
	}
	return nil
}

// printCoverage says what this run actually examined.
//
// A CLEAN RESULT AND AN UNEXAMINED LEVEL PRINTED IDENTICALLY, which is the
// third value applied to the ANSWER "nothing is wrong" rather than to a failure
// to look. `no defects` is a claim about what the checker checks, and every
// reader takes it as a claim about the tree.
//
// It lists the levels rather than the check names alone, because the levels are
// where the gap was: the childless check ran on one rung and not another, and
// nothing in the output said which rungs had been considered at all.
func printCoverage(w io.Writer, route *routing.Routing) {
	_, _ = fmt.Fprintf(w, "\nchecked every node for: an unreadable node, a parent cycle, "+
		"and a closed parent with open children\n")

	if route == nil || route.Structure == nil || len(route.Structure.Levels) == 0 {
		// Say what was NOT checked. Silence about the shape reads as a clean
		// bill of health for it.
		_, _ = fmt.Fprintf(w, "shape was not checked: no `structure` block in the routing file\n")
		return
	}

	var names, holders []string
	for _, l := range route.Structure.Levels {
		names = append(names, l.Name)
		if l.RequiresChildren != routing.ChildrenNotRequired {
			holders = append(holders, fmt.Sprintf("%s (%s)", l.Name, childRequirementPhrase(l.RequiresChildren)))
		}
	}
	_, _ = fmt.Fprintf(w, "checked the shape against: %s\n", strings.Join(names, " > "))
	_, _ = fmt.Fprintf(w, "  level recorded as a label, and a node at a rung its parent may not hold\n")
	if len(holders) == 0 {
		// The state facet#146 was reported from, named rather than left silent:
		// no rung asks to hold children, so no empty node can ever be reported.
		_, _ = fmt.Fprintf(w, "  NO level requires children, so an empty node at any rung is NOT reported\n")
		return
	}
	_, _ = fmt.Fprintf(w, "  requires children: %s\n", strings.Join(holders, ", "))
}

func childRequirementPhrase(c routing.ChildRequirement) string {
	if c == routing.ChildrenRequiredAlways {
		return "open or closed"
	}
	return "once closed"
}

// backfillLabels records the level on every node that is missing it.
//
// The decision of WHAT to apply lives in internal/tree (planBackfill), where
// it is tested without touching a live issue. This half is the reporting and
// the write, which is all a command should own.
func backfillLabels(w io.Writer, gh treeGH, root *tree.Node, route *routing.Routing) error {
	if route == nil || route.Structure == nil {
		_, _ = fmt.Fprintf(w, "no labels to backfill: the routing file declares no `structure`\n")
		return nil
	}
	if len(route.Structure.Labels()) == 0 {
		_, _ = fmt.Fprintf(w, "no labels to backfill: no level in `structure` declares a label\n")
		return nil
	}

	var failed error
	applied, skipped := tree.PlanBackfill(route, append([]*tree.Node{root}, root.Descendants()...),
		func(repo string, number int, label string) error {
			if err := gh.AddLabel(repo, number, label); err != nil {
				failed = fmt.Errorf("could not label %s#%d with %s: %w", repo, number, label, err)
				return failed
			}
			_, _ = fmt.Fprintf(w, "  labelled %s#%-22d %s\n", repo, number, label)
			return nil
		})
	if failed != nil {
		return failed
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
