package tree

import (
	"strings"
	"testing"

	"github.com/RiccardoCereghino/facet/internal/routing"
)

// childlessStructure is the lab's own shape with the block rung asking to hold
// children AT ALL TIMES, and the holder rung asking only once closed -- the two
// values facet#146 introduces, side by side, so a test can tell them apart.
func childlessStructure() *routing.Structure {
	return &routing.Structure{Levels: []routing.Level{
		{Name: "commission", Label: "type/commission"},
		{Name: "holder", RequiresChildren: routing.ChildrenRequiredWhenClosed,
			Accepts: []routing.LevelMatch{{Label: "type/seat"}}},
		{Name: "block", Optional: true, Label: "type/block",
			RequiresChildren: routing.ChildrenRequiredAlways},
		{Name: "issue", Label: "type/work"},
	}}
}

func childlessNode(n int, level int, state string, labels ...string) *Node {
	return &Node{
		Ref:   ref("o", "lab-workspaces", n),
		Title: "a node", State: state,
		Level: level, Assigned: true, LevelKnown: true, HasParent: true,
		Labels: labels,
	}
}

// !! THE 25-INSTANCE CASE, AND THE ONE THAT COSTS WORK. !!
//
// An OPEN block with no children was never reported by anything. `facet tree
// wire` manufactures them by CORRECT behaviour -- the level is assigned by
// POSITION, so a leaf wired straight onto a holder is recorded as the rung
// between them, and nobody chooses that. Measured across all 227 nodes of one
// commission: 25 open blocks containing zero issues, and no run ever said so.
func TestAnOpenBlockWithNoChildrenIsReported(t *testing.T) {
	root := childlessNode(150, 2, "OPEN", "type/block")

	ds := Doctor(root, routeFor(childlessStructure())).Defects
	if len(ds) == 0 {
		t.Fatal("an open block holding nothing was reported as clean")
	}
	if !strings.Contains(ds[0].What, "open block with no children") {
		t.Errorf("the defect does not name the state and the level: %q", ds[0].What)
	}
}

// And the closed one, which is where facet#146 was found: `lab-workspaces#150`
// was a closed type/block with zero children, and `tree doctor` reported "no
// defects" for the tree holding it while correctly reporting three childless
// closed HOLDERS in the same run. The check worked; it was pointed one level
// too high.
func TestAClosedBlockWithNoChildrenIsReported(t *testing.T) {
	root := childlessNode(150, 2, "CLOSED", "type/block")

	ds := Doctor(root, routeFor(childlessStructure())).Defects
	if len(ds) == 0 {
		t.Fatal("a closed block holding nothing was reported as clean")
	}
	if !strings.Contains(ds[0].What, "closed block with no children") {
		t.Errorf("the defect does not name the state and the level: %q", ds[0].What)
	}
	// The two states get different reasons, because they are different losses.
	if !strings.Contains(ds[0].Why, "can no longer be attributed") {
		t.Errorf("the closed case borrowed the open case's reason: %q", ds[0].Why)
	}
}

// !! THE NOISE GUARD, AND IT IS WHY THE REQUIREMENT HAS TWO VALUES. !!
//
// A holder is created AT SEATING and legitimately holds nothing until its work
// is wired. Reporting that would fire on every holder the moment it is made --
// and a check that cries wolf on the ordinary path is a check somebody turns
// off.
func TestAnOpenHolderWithNoChildrenIsNotReported(t *testing.T) {
	root := childlessNode(441, 1, "OPEN", "type/seat")

	if ds := Doctor(root, routeFor(childlessStructure())).Defects; len(ds) != 0 {
		t.Fatalf("a freshly-created holder was reported as a defect: %+v", ds)
	}
}

// The half that must not be lost while adding the open case: a CLOSED holder
// holding nothing is still the original defect.
func TestAClosedHolderWithNoChildrenIsStillReported(t *testing.T) {
	root := childlessNode(441, 1, "CLOSED", "type/seat")

	ds := Doctor(root, routeFor(childlessStructure())).Defects
	if len(ds) == 0 {
		t.Fatal("the original closed-holder check was lost")
	}
	if !strings.Contains(ds[0].What, "closed holder with no children") {
		t.Errorf("what = %q", ds[0].What)
	}
}

// A rung that asks for nothing is still silent, whatever its state -- the
// feature is opt-in per level and adds no check nobody configured.
func TestALevelThatRequiresNothingIsSilent(t *testing.T) {
	for _, state := range []string{"OPEN", "CLOSED"} {
		root := childlessNode(1, 3, state, "type/work")
		if ds := Doctor(root, routeFor(childlessStructure())).Defects; len(ds) != 0 {
			t.Errorf("a %s issue-level node was reported: %+v", state, ds)
		}
	}
}

// A block that HOLDS something is not a defect in either state. Without this
// the tests above pass for a check that reports every block.
func TestABlockWithChildrenIsNotReported(t *testing.T) {
	for _, state := range []string{"OPEN", "CLOSED"} {
		root := childlessNode(150, 2, state, "type/block")
		child := childlessNode(151, 3, state, "type/work")
		root.Children = []*Node{child}

		if ds := Doctor(root, routeFor(childlessStructure())).Defects; len(ds) != 0 {
			t.Errorf("a %s block holding work was reported: %+v", state, ds)
		}
	}
}

// THE STRUCTURE THAT PRODUCED THE ISSUE, reproduced exactly: `block` declares
// no requirement at all, so an empty block -- open or closed -- is invisible.
// This is the state the report came from, and it is what `printCoverage` must
// say out loud rather than leaving to be inferred from silence.
func TestWithNoRequirementOnBlockTheEmptyBlockIsInvisible(t *testing.T) {
	s := childlessStructure()
	s.Levels[2].RequiresChildren = routing.ChildrenNotRequired

	for _, state := range []string{"OPEN", "CLOSED"} {
		root := childlessNode(150, 2, state, "type/block")
		if ds := Doctor(root, routeFor(s)).Defects; len(ds) != 0 {
			t.Errorf("a %s empty block was reported by a structure that never asked: %+v", state, ds)
		}
	}
}
