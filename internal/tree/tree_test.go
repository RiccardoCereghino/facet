package tree

import (
	"errors"
	"strings"
	"testing"

	"github.com/RiccardoCereghino/facet/internal/ghx"
	"github.com/RiccardoCereghino/facet/internal/routing"
)

// fakeSource scripts the two reads a walk makes. Keys are "owner/repo#n".
type fakeSource struct {
	issues   map[string]*ghx.Issue
	children map[string][]ghx.IssueRef
	parents  map[string]ghx.IssueRef
	errs     map[string]error
	// parentErrs makes a FAILED parent read expressible. Its absence is why
	// four rounds of enumeration could not reach the states behind it: every
	// tree the double could build was one where the climb succeeds, so the
	// failure branch was unreachable by construction rather than untested by
	// oversight. cmd/facet's double has carried this since round 1.
	parentErrs map[string]error

	// viewIssueCalls counts every ViewIssue call. facet#105's whole point: a
	// walk must call it once, for the root, and never again per child -- every
	// child's title/state/labels comes from IssueChildren directly.
	viewIssueCalls int
}

func (f *fakeSource) ViewIssue(repo string, number int) (*ghx.Issue, error) {
	f.viewIssueCalls++
	k := key(repo, number)
	if err, ok := f.errs[k]; ok {
		return nil, err
	}
	iss, ok := f.issues[k]
	if !ok {
		return nil, errors.New("no such issue: " + k)
	}
	return iss, nil
}

// IssueChildren mirrors the real IssueChildren: each child comes back with
// its title/state/labels already attached, read from f.issues. A child whose
// key is in f.errs, or that has no entry in f.issues, comes back with an
// empty State -- the fake's way of expressing "this child's fields did not
// resolve", the same fact ViewIssue failing used to carry before facet#105.
func (f *fakeSource) IssueChildren(repo string, number int) ([]ghx.SubIssue, error) {
	refs := f.children[key(repo, number)]
	out := make([]ghx.SubIssue, 0, len(refs))
	for _, r := range refs {
		k := key(r.OwnerRepo(), r.Number)
		if _, bad := f.errs[k]; bad {
			out = append(out, ghx.SubIssue{Ref: r})
			continue
		}
		iss, ok := f.issues[k]
		if !ok {
			out = append(out, ghx.SubIssue{Ref: r})
			continue
		}
		out = append(out, ghx.SubIssue{Ref: r, Title: iss.Title, State: iss.State, Labels: iss.LabelNames()})
	}
	return out, nil
}

// A miss is "asked, and there is none" -- the ordinary case for a root, and
// the one that must not be an error.
func (f *fakeSource) IssueParent(repo string, number int) (ghx.IssueRef, bool, error) {
	if err, ok := f.parentErrs[key(repo, number)]; ok {
		return ghx.IssueRef{}, false, err
	}
	p, ok := f.parents[key(repo, number)]
	return p, ok, nil
}

func key(repo string, n int) string { return repo + "#" + itoa(n) }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func ref(owner, repo string, n int) ghx.IssueRef {
	return ghx.IssueRef{Owner: owner, Repo: repo, Number: n}
}

func issue(title, state string, labels ...string) *ghx.Issue {
	ls := make([]ghx.Label, 0, len(labels))
	for _, l := range labels {
		ls = append(ls, ghx.Label{Name: l})
	}
	return &ghx.Issue{Title: title, State: state, Labels: ls}
}

// routeWithStructure mirrors the four-rung shape, with the repo keys the
// routing table would resolve.
func routeWithStructure() *routing.Routing {
	return &routing.Routing{
		Repos: map[string]routing.Repo{"lab": {}, "doctrine": {}, "harness": {}},
		OwnerRepoToKey: map[string]string{
			"acme/lab": "lab", "acme/doctrine": "doctrine", "acme/harness": "harness",
		},
		Structure: &routing.Structure{Levels: []routing.Level{
			{Name: "commission"},
			{Name: "seat", RequiresChildren: true, Accepts: []routing.LevelMatch{
				{Repo: "doctrine", TitlePattern: "^seat: "},
			}},
			{Name: "block", Optional: true},
			{Name: "issue"},
		}},
	}
}

// wellFormed is the shape as it should be: a programme, a record of who worked
// it, a bundle, and the work.
func wellFormed() *fakeSource {
	return &fakeSource{
		issues: map[string]*ghx.Issue{
			"acme/lab#46":       issue("commission 1", "OPEN"),
			"acme/doctrine#282": issue("seat: c1-structure", "OPEN"),
			"acme/lab#75":       issue("the commands and the skill", "OPEN", "complexity/3"),
			"acme/harness#121":  issue("the work", "OPEN", "complexity/1"),
		},
		children: map[string][]ghx.IssueRef{
			"acme/lab#46":       {ref("acme", "doctrine", 282)},
			"acme/doctrine#282": {ref("acme", "lab", 75)},
			"acme/lab#75":       {ref("acme", "harness", 121)},
		},
	}
}

// facet#105: ViewIssue must be called exactly ONCE per walk, for the root.
// Before this, it was called once per node -- a second `gh` process on top
// of IssueChildren's -- because a child's title/state/labels came from a
// separate read instead of the same call that listed it.
func TestWalkCallsViewIssueOnlyOnceForTheRoot(t *testing.T) {
	src := wellFormed()
	route := routeWithStructure()
	if _, err := Walk(src, ref("acme", "lab", 46), -1, route); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if src.viewIssueCalls != 1 {
		t.Errorf("ViewIssue was called %d time(s), want exactly 1 (the root only) -- "+
			"a tree of N nodes must cost O(parents) requests, not O(2N)", src.viewIssueCalls)
	}
}

func TestWalkAssignsTheLevels(t *testing.T) {
	route := routeWithStructure()
	root, err := Walk(wellFormed(), ref("acme", "lab", 46), -1, route)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}

	want := []struct{ ref, level string }{
		{"acme/doctrine#282", "seat"},
		{"acme/lab#75", "block"},
		{"acme/harness#121", "issue"},
	}
	got := root.Descendants()
	if len(got) != len(want) {
		t.Fatalf("got %d descendants, want %d", len(got), len(want))
	}
	for i, w := range want {
		n := got[i]
		if n.Ref.String() != w.ref {
			t.Errorf("descendant %d = %s, want %s", i, n.Ref, w.ref)
		}
		if !n.Assigned {
			t.Fatalf("%s was assigned no level", n.Ref)
		}
		if name := route.Structure.Levels[n.Level].Name; name != w.level {
			t.Errorf("%s is at level %q, want %q", n.Ref, name, w.level)
		}
	}
}

// A record carrying its work directly, with no bundle in between. The rung is
// skippable, so this is correct rather than merely tolerated.
func TestWalkSkipsAnOptionalLevel(t *testing.T) {
	src := &fakeSource{
		issues: map[string]*ghx.Issue{
			"acme/lab#46":       issue("commission 1", "OPEN"),
			"acme/doctrine#282": issue("seat: no bundle", "OPEN"),
			"acme/harness#121":  issue("the work", "OPEN"),
		},
		children: map[string][]ghx.IssueRef{
			"acme/lab#46":       {ref("acme", "doctrine", 282)},
			"acme/doctrine#282": {ref("acme", "harness", 121)},
		},
	}
	route := routeWithStructure()
	root, err := Walk(src, ref("acme", "lab", 46), -1, route)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	work := root.Children[0].Children[0]
	if !work.Assigned {
		t.Fatal("work hanging directly off a record was assigned no level")
	}
	// It lands on "block" rather than "issue", and that is correct behaviour
	// rather than a near miss: "block" is declared with no Accepts, so it
	// admits anything, and being Optional it is the shallower candidate. The
	// declared structure simply does not distinguish a bundle from the work,
	// and facet does not invent a distinction the configuration lacks. What
	// matters is that the rung was skippable and nothing is reported wrong.
	if len(Doctor(root, route)) != 0 {
		t.Errorf("a skipped optional rung was reported as a defect: %v", Doctor(root, route))
	}
}

// The disambiguation the note above points at: constrain the skippable rung
// and the rung below it is reachable again.
func TestAConstrainedOptionalLevelLetsTheRungBelowBeReached(t *testing.T) {
	src := &fakeSource{
		issues: map[string]*ghx.Issue{
			"acme/lab#46":       issue("commission 1", "OPEN"),
			"acme/doctrine#282": issue("seat: no bundle", "OPEN"),
			"acme/harness#121":  issue("the work", "OPEN"),
		},
		children: map[string][]ghx.IssueRef{
			"acme/lab#46":       {ref("acme", "doctrine", 282)},
			"acme/doctrine#282": {ref("acme", "harness", 121)},
		},
	}
	route := routeWithStructure()
	// Blocks live in one repo; the work does not.
	route.Structure.Levels[2].Accepts = []routing.LevelMatch{{Repo: "lab"}}

	root := mustWalk(t, src, ref("acme", "lab", 46), route)
	work := root.Children[0].Children[0]
	if name := route.Structure.Levels[work.Level].Name; name != "issue" {
		t.Errorf("level = %q, want issue once the skippable rung is constrained", name)
	}
	if got := Doctor(root, route); len(got) != 0 {
		t.Errorf("defects = %v, want none", got)
	}
}

// THE ORIGINAL DEFECT: bundles and loose work filed straight under the
// programme as siblings of the records, collapsing four rungs into two.
func TestDoctorCatchesTheFlatTree(t *testing.T) {
	src := &fakeSource{
		issues: map[string]*ghx.Issue{
			"acme/lab#46":       issue("commission 1", "OPEN"),
			"acme/doctrine#282": issue("seat: c1-structure", "OPEN"),
			"acme/lab#72":       issue("a bundle filed at the wrong level", "OPEN"),
			"acme/harness#121":  issue("loose work", "OPEN"),
		},
		children: map[string][]ghx.IssueRef{
			"acme/lab#46": {
				ref("acme", "doctrine", 282),
				ref("acme", "lab", 72),
				ref("acme", "harness", 121),
			},
			"acme/doctrine#282": {ref("acme", "lab", 75)},
		},
	}
	route := routeWithStructure()
	root, err := Walk(src, ref("acme", "lab", 46), -1, route)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	defects := Doctor(root, route)

	var flagged []string
	for _, d := range defects {
		if strings.Contains(d.What, "may only hold") {
			flagged = append(flagged, d.Ref.String())
		}
	}
	if len(flagged) != 2 {
		t.Fatalf("flagged %v, want exactly the two misplaced nodes", flagged)
	}
	// The report has to name what was expected, not only that this is wrong --
	// and it must name the level derived from the PARENT's level, since that
	// is what the assignment actually used.
	for _, d := range defects {
		if !strings.Contains(d.What, "may only hold") {
			continue
		}
		if !strings.Contains(d.What, "seat") || !strings.Contains(d.What, "commission") {
			t.Errorf("defect names neither the parent's level nor the expected one:\n%s", d)
		}
	}
}

// A message derived from depth instead of from the parent's level says
// "expects issue" about a node it has just rejected for not being an issue --
// which is what it did before this was pinned. The setup is a skipped rung, so
// depth and level genuinely disagree.
func TestDoctorDerivesTheExpectationFromTheParentsLevelNotItsDepth(t *testing.T) {
	src := &fakeSource{
		issues: map[string]*ghx.Issue{
			"acme/lab#46":       issue("commission 1", "OPEN"),
			"acme/doctrine#282": issue("seat: a record", "OPEN"),
			"acme/harness#121":  issue("work, hung straight off the record", "OPEN"),
			"acme/harness#122":  issue("something below the work", "OPEN"),
		},
		children: map[string][]ghx.IssueRef{
			"acme/lab#46":       {ref("acme", "doctrine", 282)},
			"acme/doctrine#282": {ref("acme", "harness", 121)},
			"acme/harness#121":  {ref("acme", "harness", 122)},
		},
	}
	route := routeWithStructure()
	// Constrain the skippable rung so the work lands on "issue", the deepest
	// level -- then anything below it has nowhere to go.
	route.Structure.Levels[2].Accepts = []routing.LevelMatch{{TitlePattern: "^block: "}}

	defects := Doctor(mustWalk(t, src, ref("acme", "lab", 46), route), route)
	if len(defects) != 1 {
		t.Fatalf("defects = %v, want exactly one", defects)
	}
	got := defects[0].What
	if !strings.Contains(got, "deepest declared level") {
		t.Errorf("What = %q, want it to say nothing may sit below the deepest level", got)
	}
	// The old, depth-derived phrasing claimed the structure expected an issue
	// -- of a node it rejected for not being one.
	if strings.Contains(got, "expects issue") {
		t.Errorf("the message names the level it just rejected the node for: %q", got)
	}
}

// !! THE CONSTRAINT. !! With no structure declared, a tree that violates every
// rule the lab happens to use must produce no structural complaint at all --
// and an issue with no parent must never be mentioned. Anyone adopting facet
// files issues without a hierarchy and must not be told they are wrong.
func TestDoctorWithNoStructureReportsNoShapeDefects(t *testing.T) {
	src := &fakeSource{
		issues: map[string]*ghx.Issue{
			"acme/lab#46":      issue("a root", "OPEN"),
			"acme/lab#72":      issue("a child at a depth some other org would forbid", "OPEN"),
			"acme/harness#121": issue("another", "OPEN"),
		},
		children: map[string][]ghx.IssueRef{
			"acme/lab#46": {ref("acme", "lab", 72), ref("acme", "harness", 121)},
		},
	}
	route := &routing.Routing{Repos: map[string]routing.Repo{"lab": {}}}

	if got := Doctor(mustWalk(t, src, ref("acme", "lab", 46), route), route); len(got) != 0 {
		t.Fatalf("structure checks ran without a structure block: %v", got)
	}

	// And a lone issue with no parent and no children is simply an issue.
	lone := &fakeSource{issues: map[string]*ghx.Issue{"acme/lab#9": issue("standalone", "OPEN")}}
	if got := Doctor(mustWalk(t, lone, ref("acme", "lab", 9), route), route); len(got) != 0 {
		t.Errorf("an unparented issue was reported as a defect: %v", got)
	}
	// Also with a structure configured: no parent is still not a defect.
	withStructure := routeWithStructure()
	if got := Doctor(mustWalk(t, lone, ref("acme", "lab", 9), withStructure), withStructure); len(got) != 0 {
		t.Errorf("an unparented issue was a defect even under a structure: %v", got)
	}
}

// Universal: true of any hierarchy, so it must fire with no structure at all.
func TestDoctorCatchesClosedParentWithOpenChildrenWithoutAStructure(t *testing.T) {
	src := &fakeSource{
		issues: map[string]*ghx.Issue{
			"acme/lab#46":      issue("a closed root", "CLOSED"),
			"acme/harness#121": issue("still open", "OPEN"),
		},
		children: map[string][]ghx.IssueRef{
			"acme/lab#46": {ref("acme", "harness", 121)},
		},
	}
	route := &routing.Routing{Repos: map[string]routing.Repo{"lab": {}}}
	defects := Doctor(mustWalk(t, src, ref("acme", "lab", 46), route), route)
	if len(defects) != 1 || !strings.Contains(defects[0].What, "is closed") {
		t.Fatalf("defects = %v, want one closed-parent report", defects)
	}
	if defects[0].Why == "" || defects[0].Fix == "" {
		t.Error("a defect with no why and no fix is one the next person deletes")
	}
}

// A record closed holding nothing: the work it accounted for is now
// unattributable. Structural, so it needs the level declared.
func TestDoctorCatchesAClosedLevelThatShouldHoldChildren(t *testing.T) {
	src := &fakeSource{
		issues: map[string]*ghx.Issue{
			"acme/lab#46":       issue("commission 1", "OPEN"),
			"acme/doctrine#262": issue("seat: c1-guard", "CLOSED"),
		},
		children: map[string][]ghx.IssueRef{
			"acme/lab#46": {ref("acme", "doctrine", 262)},
		},
	}
	route := routeWithStructure()
	defects := Doctor(mustWalk(t, src, ref("acme", "lab", 46), route), route)
	if len(defects) != 1 || !strings.Contains(defects[0].What, "no children") {
		t.Fatalf("defects = %v, want one childless-record report", defects)
	}
}

func TestWalkStopsAtACycle(t *testing.T) {
	src := &fakeSource{
		issues: map[string]*ghx.Issue{
			"acme/lab#1": issue("a", "OPEN"),
			"acme/lab#2": issue("b", "OPEN"),
		},
		children: map[string][]ghx.IssueRef{
			"acme/lab#1": {ref("acme", "lab", 2)},
			"acme/lab#2": {ref("acme", "lab", 1)},
		},
	}
	route := &routing.Routing{Repos: map[string]routing.Repo{"lab": {}}}
	root := mustWalk(t, src, ref("acme", "lab", 1), route)

	defects := Doctor(root, route)
	var found bool
	for _, d := range defects {
		if strings.Contains(d.What, "cycle") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no cycle reported: %v", defects)
	}
}

func TestWalkRespectsMaxDepth(t *testing.T) {
	root := mustWalkDepth(t, wellFormed(), ref("acme", "lab", 46), 1, routeWithStructure())
	if got := len(root.Descendants()); got != 1 {
		t.Errorf("depth 1 returned %d descendants, want 1", got)
	}
}

// One unreadable node must not blank out its siblings: a partial answer that
// says which part is missing beats no answer.
func TestWalkKeepsGoingPastAnUnreadableNode(t *testing.T) {
	src := wellFormed()
	src.errs = map[string]error{"acme/doctrine#282": errors.New("404")}
	src.children["acme/lab#46"] = append(src.children["acme/lab#46"], ref("acme", "harness", 121))

	route := routeWithStructure()
	root := mustWalk(t, src, ref("acme", "lab", 46), route)
	if len(root.Children) != 2 {
		t.Fatalf("got %d children, want 2 -- the readable sibling was dropped", len(root.Children))
	}
	defects := Doctor(root, route)
	var named bool
	for _, d := range defects {
		if d.Ref.String() == "acme/doctrine#282" && strings.Contains(d.What, "could not be read") {
			named = true
		}
	}
	if !named {
		t.Errorf("the unreadable node was not reported: %v", defects)
	}
}

func TestNodeTier(t *testing.T) {
	if tier, _ := (&Node{Labels: []string{"complexity/2", "fleet"}}).Tier(); tier != "c2" {
		t.Errorf("tier = %q, want c2", tier)
	}
	// Two tiers cannot both govern one merge, and none cannot be guessed.
	if tier, found := (&Node{Labels: []string{"complexity/1", "complexity/3"}}).Tier(); tier != "" || len(found) != 2 {
		t.Errorf("tier = %q with %v, want no tier and both labels reported", tier, found)
	}
	if tier, found := (&Node{Labels: []string{"fleet"}}).Tier(); tier != "" || len(found) != 0 {
		t.Errorf("tier = %q with %v, want neither", tier, found)
	}
}

func TestTallySeparatesUnknownFromNotStarted(t *testing.T) {
	nodes := []*Node{
		{Ref: ref("acme", "lab", 1), State: "CLOSED"},
		{Ref: ref("acme", "lab", 2), State: "OPEN"},
		{Ref: ref("acme", "lab", 3), State: "OPEN"},
	}
	statuses := map[string]string{"acme/lab#1": "Done", "acme/lab#2": "In progress"}
	c := Tally(nodes, statuses)

	if c.Total != 3 || c.Closed != 1 {
		t.Errorf("Total/Closed = %d/%d, want 3/1", c.Total, c.Closed)
	}
	// Not on the board is a different fact from not started; folding them
	// together would make an unconfigured tree look untouched.
	if c.Unknown != 1 {
		t.Errorf("Unknown = %d, want 1", c.Unknown)
	}
	if c.ByStatus["Done"] != 1 || c.ByStatus["In progress"] != 1 {
		t.Errorf("ByStatus = %v", c.ByStatus)
	}
	if names := c.StatusNames(); len(names) != 2 || names[0] != "Done" {
		t.Errorf("StatusNames = %v, want a stable sorted list", names)
	}
}

func mustWalk(t *testing.T, src Source, r ghx.IssueRef, route *routing.Routing) *Node {
	t.Helper()
	return mustWalkDepth(t, src, r, -1, route)
}

func mustWalkDepth(t *testing.T, src Source, r ghx.IssueRef, depth int, route *routing.Routing) *Node {
	t.Helper()
	n, err := Walk(src, r, depth, route)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	return n
}

// !! A WALK MUST ESTABLISH WHAT ITS STARTING NODE ACTUALLY IS. !! Assuming the
// argument is a root is wrong for every subtree, and wrong in the direction
// that does damage: a seat walked directly would be called a commission, and
// every correctly-placed node beneath it reported as misplaced with a fix line
// saying "re-parent it". A doctor's false positive that tells someone to break
// a correct tree is worse than a missed defect.
//
// Measured on the live tree before the fix: doctoring the commission reported
// no defects, and doctoring a seat inside that same clean tree reported five.
func TestWalkFromASubtreeResolvesItsRealLevel(t *testing.T) {
	src := wellFormed()
	src.parents = map[string]ghx.IssueRef{
		"acme/doctrine#282": ref("acme", "lab", 46),
		"acme/lab#75":       ref("acme", "doctrine", 282),
		"acme/harness#121":  ref("acme", "lab", 75),
	}
	route := routeWithStructure()

	// Start at the seat, not the commission.
	root := mustWalk(t, src, ref("acme", "doctrine", 282), route)

	if !root.Assigned {
		t.Fatal("the starting node was assigned no level")
	}
	if name := route.Structure.Levels[root.Level].Name; name != "seat" {
		t.Errorf("walking from a seat calls it %q, want seat", name)
	}
	// And the subtree below it must come out clean, exactly as it does when
	// the same nodes are reached from the commission.
	if got := Doctor(root, route); len(got) != 0 {
		t.Errorf("doctoring a correct subtree directly reported %d defect(s): %v", len(got), got)
	}
}

// labelledRouteWithStructure mirrors routeWithStructure, but with the type/*
// labels facet#129 backfilled onto the real corpus this bug was found in --
// the same four rungs, each carrying the label that records it.
func labelledRouteWithStructure() *routing.Routing {
	return &routing.Routing{
		Repos: map[string]routing.Repo{"lab": {}, "doctrine": {}, "harness": {}},
		OwnerRepoToKey: map[string]string{
			"acme/lab": "lab", "acme/doctrine": "doctrine", "acme/harness": "harness",
		},
		Structure: &routing.Structure{Levels: []routing.Level{
			{Name: "commission", Label: "type/commission"},
			{Name: "seat", RequiresChildren: true, Accepts: []routing.LevelMatch{
				{Repo: "doctrine", TitlePattern: "^seat: ", Label: "type/seat"},
			}},
			{Name: "block", Optional: true, Label: "type/block"},
			{Name: "issue", Label: "type/work"},
		}},
	}
}

// facet#133, GAP001: a parentless "future commission seed" labelled
// type/block was always reported [commission]. ChildLevels(-1) can only ever
// be the shallowest non-optional level (position 0, "commission"), because
// that is the sole rung reachable from a hypothetical parent at -1 -- so no
// title convention and no amount of position logic could ever let a root be
// "block". Only reading the label can, which is what this pins.
func TestLevelOfReadsALabelForAParentlessNode(t *testing.T) {
	src := &fakeSource{
		issues: map[string]*ghx.Issue{
			"acme/lab#118": issue(
				"WIP seed: the bottega's core function, and the outside concepts that sharpen it",
				"OPEN", "type/block"),
		},
	}
	route := labelledRouteWithStructure()
	root := ref("acme", "lab", 118)
	level, ok, err := LevelOf(src, route, root, src.issues["acme/lab#118"].LabelNames())
	if err != nil {
		t.Fatalf("LevelOf: %v", err)
	}
	if !ok || route.Structure.Levels[level].Name != "block" {
		t.Fatalf("LevelOf(#118) = %d (ok=%v), want the block level -- its type/block label was not consulted",
			level, ok)
	}
}

// The fallback this must not regress: a root with no matching type/* label
// resolves exactly as it always has, unconditionally.
func TestLevelOfFallsBackToPositionWhenNoLabelMatches(t *testing.T) {
	src := &fakeSource{
		issues: map[string]*ghx.Issue{
			"acme/lab#46": issue("commission 1", "OPEN"),
		},
	}
	route := labelledRouteWithStructure()
	root := ref("acme", "lab", 46)
	level, ok, err := LevelOf(src, route, root, src.issues["acme/lab#46"].LabelNames())
	if err != nil {
		t.Fatalf("LevelOf: %v", err)
	}
	if !ok || route.Structure.Levels[level].Name != "commission" {
		t.Fatalf("LevelOf(#46) = %d (ok=%v), want commission -- the unlabelled fallback regressed",
			level, ok)
	}
}

// A root carrying labels for two different declared levels at once is a real
// data conflict; LevelOf must refuse rather than silently pick one.
func TestLevelOfRefusesAmbiguousRootLabels(t *testing.T) {
	src := &fakeSource{
		issues: map[string]*ghx.Issue{
			"acme/lab#9": issue("conflicting", "OPEN", "type/block", "type/work"),
		},
	}
	route := labelledRouteWithStructure()
	root := ref("acme", "lab", 9)
	_, ok, err := LevelOf(src, route, root, src.issues["acme/lab#9"].LabelNames())
	if err == nil {
		t.Fatal("LevelOf accepted a root carrying two conflicting level labels")
	}
	if ok {
		t.Error("LevelOf reported ok=true alongside an error")
	}
}

// The full reproduction, end to end through Walk: lab-workspaces#118's exact
// shape (a parentless type/block seed) with lab-workspaces#119's exact shape
// (a type/work child, titled with no convention the structure recognises) --
// previously #119 could not be wired under #118 at all.
func TestWalkResolvesAnOrphanedBlockAndItsWorkChild(t *testing.T) {
	src := &fakeSource{
		issues: map[string]*ghx.Issue{
			"acme/lab#118": issue(
				"WIP seed: the bottega's core function, and the outside concepts that sharpen it",
				"OPEN", "type/block"),
			"acme/lab#119": issue(
				"WIP: a session that reasons about a refusal, on a deliberately narrow corpus",
				"OPEN", "type/work"),
		},
		children: map[string][]ghx.IssueRef{
			"acme/lab#118": {ref("acme", "lab", 119)},
		},
	}
	route := labelledRouteWithStructure()
	root := mustWalk(t, src, ref("acme", "lab", 118), route)

	if !root.Assigned || route.Structure.Levels[root.Level].Name != "block" {
		t.Fatalf("root: assigned=%v level=%v, want the block level", root.Assigned, root.Level)
	}
	if len(root.Children) != 1 {
		t.Fatalf("got %d children, want 1", len(root.Children))
	}
	child := root.Children[0]
	if !child.Assigned || route.Structure.Levels[child.Level].Name != "issue" {
		t.Errorf("child: assigned=%v level=%v, want the issue level", child.Assigned, child.Level)
	}
	if got := Doctor(root, route); len(got) != 0 {
		t.Errorf("the worked example was reported as a defect: %v", got)
	}
}

// A node that genuinely sits at no declared level is still reported when it is
// the one asked about -- but its children are NOT, because judging them
// against the wrong rung would turn one defect into a cascade that all point
// at the wrong issues.
func TestWalkFromAMisplacedNodeDoesNotCascade(t *testing.T) {
	src := &fakeSource{
		issues: map[string]*ghx.Issue{
			"acme/lab#46":      issue("commission 1", "OPEN"),
			"acme/lab#72":      issue("not a seat record at all", "OPEN"),
			"acme/harness#121": issue("work below it", "OPEN"),
		},
		children: map[string][]ghx.IssueRef{
			"acme/lab#72": {ref("acme", "harness", 121)},
		},
		parents: map[string]ghx.IssueRef{
			"acme/lab#72": ref("acme", "lab", 46),
		},
	}
	route := routeWithStructure()
	root := mustWalk(t, src, ref("acme", "lab", 72), route)

	if root.Assigned {
		t.Fatal("a node at no declared level was assigned one")
	}
	defects := Doctor(root, route)
	if len(defects) != 1 {
		t.Fatalf("got %d defects, want exactly 1 -- the misplaced node itself, not a cascade: %v",
			len(defects), defects)
	}
	if defects[0].Ref.String() != "acme/lab#72" {
		t.Errorf("the defect blames %s, want the misplaced node itself", defects[0].Ref)
	}
}

// !! READ THE SENTENCE, NOT ONLY THE COUNT. !! The sibling test above asserts
// how MANY defects a misplaced ancestor produces and which node they name, and
// both were right while the message itself said three false things: that the
// node sat below the commission (it sat below a stray the walk never visited),
// that "commission" was the deepest declared level (it is the shallowest), and
// nothing at all about the node actually at fault. A passing count is what
// made that look examined.
func TestDoctorExplainsAnUnplaceableStartNodeWithoutBlamingIt(t *testing.T) {
	src := &fakeSource{
		issues: map[string]*ghx.Issue{
			"acme/lab#46":      issue("commission 1", "OPEN"),
			"acme/lab#99":      issue("a stray that matches no seat shape", "OPEN"),
			"acme/harness#121": issue("the work", "OPEN"),
		},
		children: map[string][]ghx.IssueRef{
			"acme/lab#46": {ref("acme", "lab", 99)},
			"acme/lab#99": {ref("acme", "harness", 121)},
		},
		parents: map[string]ghx.IssueRef{
			"acme/lab#99":      ref("acme", "lab", 46),
			"acme/harness#121": ref("acme", "lab", 99),
		},
	}
	route := routeWithStructure()

	// Start at the work, whose ancestor #99 is the thing actually misplaced.
	defects := Doctor(mustWalk(t, src, ref("acme", "harness", 121), route), route)
	if len(defects) != 1 {
		t.Fatalf("got %d defects, want 1: %v", len(defects), defects)
	}
	got := defects[0].String()

	// It must not invert the structure by calling the shallowest rung the
	// deepest, and must not claim a parent relationship the walk never saw.
	if strings.Contains(got, "deepest declared level") {
		t.Errorf("the message calls a rung the deepest that is not:\n%s", got)
	}
	if strings.Contains(got, "sits below \"commission\"") {
		t.Errorf("the message asserts a parent the walk never visited:\n%s", got)
	}
	// And it must say where the real answer is rather than prescribing a fix
	// to this node, which may be perfectly well formed.
	for _, want := range []string{"could not be placed", "above it", "from the root"} {
		if !strings.Contains(got, want) {
			t.Errorf("the message is missing %q:\n%s", want, got)
		}
	}
}

// THE STATE ENUMERATION. Level resolution has more reachable states than it
// looks, and this branch has twice shipped a message written for a NEIGHBOURING
// state -- once deriving an expectation from depth when the assignment used a
// level, once giving a start node the wording meant for a child. Both produced
// sentences that were confidently false about a real tree.
//
// So: every state a node can be in, what it must say, and what it must NOT.
// The "must not" half is the load-bearing half. A message inherited from a
// neighbour is still fluent, still specific, and still passes any test that
// only counts defects -- which is exactly how the last one survived.
//
// Gated by five things: Err, LevelKnown, Assigned, HasParent, and whether the
// parent's level has any rung below it.
func TestEveryLevelResolutionStateHasItsOwnMessage(t *testing.T) {
	route := routeWithStructure()
	deepest := routeWithStructure()
	// Constrain the skippable rung so a node can actually land on the deepest
	// level and still have a child -- otherwise the unconstrained `block`
	// absorbs everything and that state is unreachable.
	deepest.Structure.Levels[2].Accepts = []routing.LevelMatch{{TitlePattern: "^block: "}}

	cases := []struct {
		state     string
		node      *Node
		structure *routing.Routing
		wantSays  []string
		wantNever []string
	}{{
		state: "S1 unreadable -- universal check owns it, structural must stay silent",
		node: &Node{Ref: ref("acme", "lab", 1), LevelKnown: true,
			Err: errors.New("404")},
		structure: route,
		wantSays:  []string{"could not be read"},
		// It must not also be judged for shape: nothing is known about a node
		// that could not be read, including where it belongs.
		wantNever: []string{"could not be placed", "sits below", "no children"},
	}, {
		state:     "S2 level unknown -- no structure, or a parent that was itself unplaceable",
		node:      &Node{Ref: ref("acme", "lab", 2), State: "OPEN"},
		structure: route,
		wantSays:  nil, // silence is the whole point
		wantNever: []string{"could not be placed", "sits below"},
	}, {
		state: "S3 start node, unplaceable -- the walk began here so the culprit may be unseen",
		node: &Node{Ref: ref("acme", "lab", 3), State: "OPEN",
			LevelKnown: true, Assigned: false, HasParent: false},
		structure: route,
		wantSays:  []string{"could not be placed", "above it", "from the root"},
		// It has no parent in this report, so it cannot claim one -- and it
		// must not invert the structure by calling the shallowest rung deepest.
		wantNever: []string{"sits below", "deepest declared level", "re-parent it"},
	}, {
		// Added after the enumeration was found to be closed over NODE states
		// given a walk that SUCCEEDED. These two are states of the resolution
		// itself, and no node existed in them until the walk stopped blanking
		// the report.
		state: "S8 the node read, its ancestry did not -- position unknown",
		node: &Node{Ref: ref("acme", "lab", 8), State: "OPEN",
			LevelErr: errors.New("HTTP 502")},
		structure: route,
		wantSays:  []string{"position in the tree could not be established", "HTTP 502", "another repo"},
		// The node itself read fine, so it is not unreadable; and nothing is
		// known about where it belongs, so no placement wording may appear.
		wantNever: []string{"could not be read", "sits below", "could not be placed", "cycle"},
	}, {
		state: "S9 a cycle in the ancestry -- known, nameable, and a record defect",
		node: &Node{Ref: ref("acme", "lab", 9), State: "OPEN",
			LevelErr: &ParentCycleError{
				At:    ref("acme", "lab", 9),
				Child: ref("acme", "lab", 10), Ancestor: ref("acme", "lab", 9)}},
		structure: route,
		wantSays:  []string{"parent cycle", "acme/lab#10", "re-parent"},
		// A cycle is fully known. Describing it as a failure to find something
		// out would be borrowing S8's sentence for a state that is its opposite.
		wantNever: []string{"could not be established", "retry", "could not be read"},
	}, {
		state: "S4 child at a rung its parent may not hold",
		node: &Node{Ref: ref("acme", "lab", 4), State: "OPEN",
			LevelKnown: true, Assigned: false, HasParent: true, ParentLevel: 0},
		structure: route,
		wantSays:  []string{"sits below", "commission", "may only hold", "seat"},
		// The parent is at the shallowest rung, so nothing here is "deepest".
		wantNever: []string{"deepest declared level", "could not be placed"},
	}, {
		state: "S5 child below the deepest rung -- nothing may hang there",
		node: &Node{Ref: ref("acme", "lab", 5), State: "OPEN",
			LevelKnown: true, Assigned: false, HasParent: true, ParentLevel: 3},
		structure: deepest,
		wantSays:  []string{"sits below", "issue", "deepest declared level"},
		wantNever: []string{"may only hold", "could not be placed"},
	}, {
		state: "S6 placed, and a rung that must hold others is closed holding none",
		node: &Node{Ref: ref("acme", "lab", 6), State: "CLOSED",
			LevelKnown: true, Assigned: true, Level: 1, HasParent: true},
		structure: route,
		wantSays:  []string{"closed seat", "no children"},
		wantNever: []string{"sits below", "could not be placed"},
	}, {
		state: "S7 placed, nothing to say",
		node: &Node{Ref: ref("acme", "lab", 7), State: "OPEN",
			LevelKnown: true, Assigned: true, Level: 3, HasParent: true},
		structure: route,
		wantSays:  nil,
		wantNever: []string{"sits below", "could not be placed", "no children"},
	}}

	for _, c := range cases {
		t.Run(c.state, func(t *testing.T) {
			got := Doctor(c.node, c.structure)
			var text string
			for _, d := range got {
				text += d.String() + "\n"
			}
			if len(c.wantSays) == 0 && len(got) != 0 {
				t.Fatalf("state should be silent, got:\n%s", text)
			}
			for _, w := range c.wantSays {
				if !strings.Contains(text, w) {
					t.Errorf("missing %q:\n%s", w, text)
				}
			}
			// The half that catches an inherited message. A sentence borrowed
			// from a neighbouring state reads perfectly and says something
			// false about this one.
			for _, w := range c.wantNever {
				if strings.Contains(text, w) {
					t.Errorf("says %q, which is not true in this state:\n%s", w, text)
				}
			}
		})
	}
}

// The enumeration above is only worth having if those states are ALL of them.
// These are the invariants that make it exhaustive rather than illustrative,
// asserted over every node of several real-shaped trees.
//
// Without them the table is a list of cases someone thought of, which is the
// same standard that produced the two inherited messages it exists to prevent.
func TestTheStateSpaceIsClosed(t *testing.T) {
	route := routeWithStructure()
	constrained := routeWithStructure()
	constrained.Structure.Levels[2].Accepts = []routing.LevelMatch{{TitlePattern: "^block: "}}

	trees := []struct {
		name  string
		src   *fakeSource
		start ghx.IssueRef
		route *routing.Routing
	}{
		{"well formed, from the root", wellFormed(), ref("acme", "lab", 46), route},
		{"well formed, no structure at all", wellFormed(), ref("acme", "lab", 46),
			&routing.Routing{Repos: map[string]routing.Repo{"lab": {}}}},
		{"a misplaced node with work beneath it", misplacedAncestorTree(), ref("acme", "lab", 46), route},
		{"started inside a broken subtree", misplacedAncestorTree(), ref("acme", "harness", 121), route},
		{"a skippable rung that is constrained", wellFormedWithParents(), ref("acme", "lab", 46), constrained},
		// The climb FAILING is a resolution state, not a node state, and the
		// closure test could not reach it until the double could express it.
		{"the parent read fails", failingClimbTree(), ref("acme", "lab", 46), route},
	}

	for _, tc := range trees {
		t.Run(tc.name, func(t *testing.T) {
			root := mustWalk(t, tc.src, tc.start, tc.route)
			for _, n := range append([]*Node{root}, root.Descendants()...) {
				// 1. HasParent implies the level was resolved. assign() sets
				//    both together or neither, so a node claiming a parent
				//    level while its own level is unknown cannot exist -- and
				//    if it did, ParentLevel would be read as a real index.
				if n.HasParent && !n.LevelKnown {
					t.Errorf("%s: HasParent without LevelKnown", n.Ref)
				}
				// 2. ParentLevel is only ever read for a node whose parent was
				//    ASSIGNED, so it must always be a valid index. This is what
				//    makes levelNameAt's out-of-range fallback unreachable
				//    rather than load-bearing.
				if n.HasParent && tc.route.Structure != nil {
					if n.ParentLevel < 0 || n.ParentLevel >= len(tc.route.Structure.Levels) {
						t.Errorf("%s: ParentLevel %d is not a valid level index", n.Ref, n.ParentLevel)
					}
				}
				// 3. A node below an unplaceable one is never itself judged.
				//    This is the cascade guard, and it is what keeps one
				//    misplaced ancestor from producing a report full of
				//    innocent issue numbers.
				if n.LevelKnown && !n.Assigned {
					for _, c := range n.Children {
						if c.LevelKnown {
							t.Errorf("%s: judged beneath the unplaceable %s", c.Ref, n.Ref)
						}
					}
				}
				// 4b. A node whose position could not be established is never
				//     also judged for placement -- the two messages are
				//     mutually exclusive by construction.
				if n.LevelErr != nil && n.Assigned {
					t.Errorf("%s: assigned a level despite an unresolved position", n.Ref)
				}
				// 4. With no structure, nothing is ever judged.
				if tc.route.Structure == nil && n.LevelKnown {
					t.Errorf("%s: a level was known with no structure declared", n.Ref)
				}
			}
		})
	}
}

func failingClimbTree() *fakeSource {
	s := wellFormedWithParents()
	s.parentErrs = map[string]error{"acme/lab#46": errors.New("HTTP 502")}
	return s
}

func misplacedAncestorTree() *fakeSource {
	return &fakeSource{
		issues: map[string]*ghx.Issue{
			"acme/lab#46":      issue("commission 1", "OPEN"),
			"acme/lab#99":      issue("a stray that matches no seat shape", "OPEN"),
			"acme/harness#121": issue("the work", "OPEN"),
		},
		children: map[string][]ghx.IssueRef{
			"acme/lab#46": {ref("acme", "lab", 99)},
			"acme/lab#99": {ref("acme", "harness", 121)},
		},
		parents: map[string]ghx.IssueRef{
			"acme/lab#99":      ref("acme", "lab", 46),
			"acme/harness#121": ref("acme", "lab", 99),
		},
	}
}

func wellFormedWithParents() *fakeSource {
	s := wellFormed()
	s.parents = map[string]ghx.IssueRef{
		"acme/doctrine#282": ref("acme", "lab", 46),
		"acme/lab#75":       ref("acme", "doctrine", 282),
		"acme/harness#121":  ref("acme", "lab", 75),
	}
	return s
}

// !! A FAILED PARENT READ MUST NOT BLANK THE REPORT. !! The child direction has
// always held this -- "one inaccessible issue must not blank out the rest of a
// tree, and a partial answer that says which part is missing beats no answer" --
// and the parent climb entered the walk without it, so a single failed read
// returned no tree at all.
//
// Reachability is not exotic: a parent routinely lives in ANOTHER repository,
// so any transient failure, or a credential that can read the starting repo and
// not the parent's, hits this.
func TestWalkCarriesAFailedParentReadInsteadOfBlankingTheReport(t *testing.T) {
	src := wellFormedWithParents()
	src.parentErrs = map[string]error{"acme/lab#46": errors.New("HTTP 502")}
	route := routeWithStructure()

	root, err := Walk(src, ref("acme", "lab", 46), -1, route)
	if err != nil {
		t.Fatalf("a failed parent read blanked the whole report: %v", err)
	}
	if root == nil {
		t.Fatal("no tree returned")
	}
	// The tree below is still rendered -- that is the point of carrying it.
	if len(root.Descendants()) == 0 {
		t.Error("the tree below the failure was lost")
	}

	got := Doctor(root, route)
	if len(got) != 1 {
		t.Fatalf("got %d defects, want 1: %v", len(got), got)
	}
	text := got[0].String()
	for _, w := range []string{"position in the tree could not be established", "HTTP 502", "another repo"} {
		if !strings.Contains(text, w) {
			t.Errorf("missing %q:\n%s", w, text)
		}
	}
	// It must NOT claim the node was unreadable -- it read fine -- and must not
	// borrow the placement wording, which asserts things it cannot know.
	for _, w := range []string{"could not be read", "sits below", "could not be placed"} {
		if strings.Contains(text, w) {
			t.Errorf("says %q, which is not true in this state:\n%s", w, text)
		}
	}
}

// A cycle in the PARENT direction. Same asymmetry as the read failure, but a
// different answer: a cycle is a DEFECT IN THE RECORD, fully known and
// nameable, not a failure to find something out. The pre-existing cycle test
// covers the CHILD direction and runs with no structure at all, so it never
// called LevelOf -- its name made the climb look covered when it was not.
func TestWalkReportsAParentCycleAsADefectRatherThanFailing(t *testing.T) {
	src := &fakeSource{
		issues: map[string]*ghx.Issue{
			"acme/lab#1": issue("a", "OPEN"),
			"acme/lab#2": issue("b", "OPEN"),
		},
		children: map[string][]ghx.IssueRef{"acme/lab#1": {ref("acme", "lab", 2)}},
		parents: map[string]ghx.IssueRef{
			"acme/lab#1": ref("acme", "lab", 2),
			"acme/lab#2": ref("acme", "lab", 1),
		},
	}
	route := routeWithStructure()

	root, err := Walk(src, ref("acme", "lab", 1), -1, route)
	if err != nil {
		t.Fatalf("a parent cycle blanked the whole report: %v", err)
	}
	got := Doctor(root, route)
	var found string
	for _, d := range got {
		if strings.Contains(d.What, "parent cycle") {
			found = d.String()
		}
	}
	if found == "" {
		t.Fatalf("the parent cycle was not reported: %v", got)
	}
	// It must name the CLOSING EDGE -- both nodes -- so there is something to
	// go and break. "X is its own ancestor" is true, repeats the node the
	// caller already supplied, and names nothing actionable.
	for _, w := range []string{"acme/lab#1", "acme/lab#2", "re-parent"} {
		if !strings.Contains(found, w) {
			t.Errorf("the cycle report is missing %q:\n%s", w, found)
		}
	}
	// And it is not described as a read failure, which it is not.
	if strings.Contains(found, "could not be established") {
		t.Errorf("a known cycle is described as a failure to find out:\n%s", found)
	}
}
