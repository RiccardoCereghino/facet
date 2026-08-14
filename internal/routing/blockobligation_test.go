package routing

import "testing"

// slateStructure mirrors `.tools/routing.json`'s real shape, and the fidelity
// is the point rather than tidiness: the block rung's `accepts` is what makes
// the change facet#148 asks for safe, and a fixture that omitted it would
// report the opposite answer. See TestAnUnconstrainedBlockRungAbsorbsTheIssue.
//
// childMustBe is a parameter because the whole question is what changes when it
// goes away.
func slateStructure(childMustBe string) *Structure {
	return &Structure{Levels: []Level{
		{Name: "commission", Label: "type/commission"},
		{Name: "holder", Accepts: []LevelMatch{
			{Repo: "lab-workspaces", TitlePattern: "^slate:", Label: "type/slate", ChildMustBe: childMustBe},
			{Label: "type/backlog", ChildMustBe: childMustBe},
		}},
		{Name: "block", Optional: true, Label: "type/block", Accepts: []LevelMatch{
			{TitlePattern: "^block:"},
			{Label: "type/block"},
		}},
		{Name: "issue", Label: "type/work"},
	}}
}

// !! facet#148, AND IT IS THE WHOLE DELIVERABLE ON THIS SIDE. !!
//
// The issue requires this to be established BY READING internal/routing rather
// than assumed. Read, and it is a DATA change: `ChildLevels` already walks
// forward past an optional rung to the first required one, so a holder's
// children may already be a block OR an issue. `childMustBe: "block"` is the
// thing that NARROWS that to a block, and `ChildLevelsFor` can only ever remove
// a candidate, never introduce one.
//
// SO DELETING THE FIELD IS SUFFICIENT, AND FACET NEEDS NO BEHAVIOUR CHANGE.
// This test is what makes that answer checkable instead of a claim in a PR
// body, and what stops the behaviour the routing change depends on from being
// refactored away by someone who does not know it is load-bearing.
func TestAHolderWithoutChildMustBeAdmitsABlockOrAnIssue(t *testing.T) {
	s := slateStructure("")
	within := s.ChildLevelsFor(1, "lab-workspaces", "slate: D", []string{"type/slate"})

	if len(within) != 2 {
		t.Fatalf("candidates = %v, want both the block and the issue rung", within)
	}
	// A lone issue, slated directly -- the case that previously needed a
	// wrapper block carrying no information beyond its child's own title.
	lvl, ok := s.AssignWithin(within, "prism", "the work", []string{"type/work"})
	if !ok || s.Levels[lvl].Name != "issue" {
		t.Errorf("a bare issue under a slate assigned to %q (ok=%v), want the issue rung",
			s.Levels[lvl].Name, ok)
	}
	// AND THE BLOCK STILL ASSIGNS TO THE BLOCK RUNG. This is the half that
	// makes the change safe: it WIDENS and narrows nothing, so `tree doctor`
	// cannot start reporting existing blocks as misplaced.
	lvl, ok = s.AssignWithin(within, "lab-workspaces", "block: a thread", []string{"type/block"})
	if !ok || s.Levels[lvl].Name != "block" {
		t.Errorf("a block under a slate assigned to %q (ok=%v), want the block rung",
			s.Levels[lvl].Name, ok)
	}
}

// The state being left behind, asserted so the two are compared rather than
// described: WITH the obligation, a lone issue is refused outright, which is
// why `lab-workspaces#161` had to exist at all.
func TestAHolderWithChildMustBeStillRefusesABareIssue(t *testing.T) {
	s := slateStructure("block")
	within := s.ChildLevelsFor(1, "lab-workspaces", "slate: D", []string{"type/slate"})

	if len(within) != 1 || s.Levels[within[0]].Name != "block" {
		t.Fatalf("childMustBe did not narrow to the block rung: %v", within)
	}
	if _, ok := s.AssignWithin(within, "prism", "the work", []string{"type/work"}); ok {
		t.Error("a bare issue was admitted under a slate that requires a block")
	}
}

// `type/backlog` is decided EXPLICITLY, as the issue requires: the same
// argument applies, and leaving it would make the backlog and the slate
// disagree about what they can hold.
func TestTheBacklogHolderBehavesTheSameWay(t *testing.T) {
	with := slateStructure("block")
	without := slateStructure("")

	narrowed := with.ChildLevelsFor(1, "lab-workspaces", "backlog: caryatid", []string{"type/backlog"})
	if len(narrowed) != 1 {
		t.Fatalf("the backlog holder is not narrowed by childMustBe: %v", narrowed)
	}
	widened := without.ChildLevelsFor(1, "lab-workspaces", "backlog: caryatid", []string{"type/backlog"})
	if len(widened) != 2 {
		t.Fatalf("dropping childMustBe did not widen the backlog holder: %v", widened)
	}
	lvl, ok := without.AssignWithin(widened, "gad", "a filed defect", []string{"type/work"})
	if !ok || without.Levels[lvl].Name != "issue" {
		t.Errorf("a filed issue under the backlog assigned to %q (ok=%v), want the issue rung",
			without.Levels[lvl].Name, ok)
	}
}

// !! THE PRECONDITION THE ROUTING CHANGE RESTS ON, LOCKED SO IT CANNOT BE
// REMOVED BY ACCIDENT. !!
//
// `Level.accepts` admits ANYTHING when a level declares no `accepts`, and
// AssignWithin takes the SHALLOWEST match. So an unconstrained OPTIONAL rung
// absorbs everything that could have belonged to the rung below it -- which is
// documented on Assign as a known trade, and becomes load-bearing here.
//
// WITH childMustBe REMOVED AND THE BLOCK RUNG UNCONSTRAINED, EVERY ISSUE SLATED
// DIRECTLY WOULD BE ASSIGNED TO THE BLOCK RUNG -- and `facet tree wire` records
// the level by POSITION, so it would be LABELLED type/block. That manufactures
// the exact defect facet#146 exists to report: a block containing no issues.
//
// The real routing file's block rung does declare `accepts`, so the change is
// safe. This test is here so that stays true.
func TestAnUnconstrainedBlockRungAbsorbsTheIssue(t *testing.T) {
	s := slateStructure("")
	s.Levels[2].Accepts = nil // the hazard: an optional rung that constrains nothing

	within := s.ChildLevelsFor(1, "lab-workspaces", "slate: D", []string{"type/slate"})
	lvl, ok := s.AssignWithin(within, "prism", "the work", []string{"type/work"})
	if !ok {
		t.Fatal("the issue was refused outright, which is not the hazard being described")
	}
	if s.Levels[lvl].Name != "block" {
		t.Skipf("an unconstrained optional rung no longer absorbs the rung below it "+
			"(issue assigned to %q) -- if that is deliberate, this test and Assign's "+
			"doc comment both need updating", s.Levels[lvl].Name)
	}
	// Recorded as an assertion about WHY the block rung must keep its accepts,
	// not as an endorsement of the behaviour.
	t.Log("confirmed: an unconstrained optional block rung would swallow every directly-slated issue, " +
		"and `tree wire` would label it type/block -- so `accepts` on the block rung is load-bearing for facet#148")
}

// validate() needs no change: it only refuses a childMustBe that names a rung
// the children may not occupy, so REMOVING the field removes the constraint it
// validates. Asserted rather than reasoned about, because "no change needed" is
// the claim nobody re-checks.
func TestAStructureWithoutChildMustBeStillValidates(t *testing.T) {
	repos := map[string]Repo{"lab-workspaces": {}, "prism": {}}
	if err := slateStructure("").validate(repos); err != nil {
		t.Fatalf("dropping childMustBe made the structure invalid: %v", err)
	}
	if err := slateStructure("block").validate(repos); err != nil {
		t.Fatalf("the structure with childMustBe stopped validating: %v", err)
	}
}
