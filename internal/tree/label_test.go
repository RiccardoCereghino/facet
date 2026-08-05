package tree

import (
	"strings"
	"testing"

	"github.com/RiccardoCereghino/facet/internal/ghx"
	"github.com/RiccardoCereghino/facet/internal/routing"
)

// levelStructure mirrors the lab's own shape, including the part that makes a
// derived-from-the-name label impossible: ONE rung spelled differently per
// repo, recorded under two different labels.
func levelStructure() *routing.Structure {
	return &routing.Structure{Levels: []routing.Level{
		{Name: "commission", Label: "type/commission"},
		{Name: "seat", Accepts: []routing.LevelMatch{
			{Repo: "stele", TitlePattern: "^seat: ", Label: "type/seat"},
			{Repo: "lab-workspaces", TitlePattern: "^maquette:", Label: "type/maquette"},
		}},
		{Name: "block", Label: "type/block", Optional: true},
		{Name: "issue", Label: "type/work"},
	}}
}

func routeFor(s *routing.Structure) *routing.Routing {
	return &routing.Routing{
		Structure:      s,
		OwnerRepoToKey: map[string]string{"o/stele": "stele", "o/lab-workspaces": "lab-workspaces", "o/gad": "gad"},
	}
}

func labelNode(ref string, n int, title string, level int, labels ...string) *Node {
	return &Node{
		Ref: ghx.IssueRef{Owner: "o", Repo: ref, Number: n}, Title: title,
		Level: level, Assigned: true, LevelKnown: true, Labels: labels, State: "OPEN",
	}
}

// The acceptance clause: doctor goes RED on a missing label. A check that
// cannot fail is not a check.
func TestDoctorReportsAMissingLevelLabel(t *testing.T) {
	root := labelNode("gad", 1, "some work", 3)
	ds := Doctor(root, routeFor(levelStructure()))
	if len(ds) == 0 {
		t.Fatal("doctor is silent about a node that records no level")
	}
	d := ds[0]
	if !strings.Contains(d.What, "type/work") {
		t.Fatalf("the defect does not name the label: %q", d.What)
	}
	if !strings.Contains(d.Fix, "--add-label type/work") {
		t.Fatalf("the fix is not a command: %q", d.Fix)
	}
}

// And red on a CONTRADICTION, which is the one that matters more: two sources
// of truth is the defect, not the fix, so it is never silently corrected.
func TestDoctorReportsALabelThatDisagreesWithTheTree(t *testing.T) {
	root := labelNode("gad", 1, "some work", 3, "type/block")
	ds := Doctor(root, routeFor(levelStructure()))
	if len(ds) == 0 {
		t.Fatal("doctor is silent about a label that contradicts the tree")
	}
	if !strings.Contains(ds[0].What, "type/block") || !strings.Contains(ds[0].What, "type/work") {
		t.Fatalf("the defect does not state both sides: %q", ds[0].What)
	}
	if !strings.Contains(ds[0].Why, "two sources of truth") {
		t.Fatalf("the defect does not say why it matters: %q", ds[0].Why)
	}
}

func TestDoctorIsSilentWhenTheLabelIsRight(t *testing.T) {
	root := labelNode("gad", 1, "some work", 3, "type/work", "complexity/1")
	if ds := Doctor(root, routeFor(levelStructure())); len(ds) != 0 {
		t.Fatalf("doctor complained about a correctly labelled node: %+v", ds)
	}
}

// One rung, two spellings: the MATCHED shape decides the label, which is why
// it cannot be derived from the level's name.
func TestLabelForUsesTheMatchedShape(t *testing.T) {
	s := levelStructure()
	tests := []struct{ repo, title, want string }{
		{"stele", "seat: c1-argano — the argano block", "type/seat"},
		{"lab-workspaces", "maquette: the argano", "type/maquette"},
	}
	for _, tt := range tests {
		got, ok := s.LabelFor(1, tt.repo, tt.title)
		if !ok || got != tt.want {
			t.Fatalf("LabelFor(seat, %s) = %q,%v want %q", tt.repo, got, ok, tt.want)
		}
	}
}

// A structure that declares no labels keeps working exactly as before -- the
// feature is additive and an adopter who wants none is not nagged.
func TestNoDeclaredLabelsIsSilent(t *testing.T) {
	s := &routing.Structure{Levels: []routing.Level{{Name: "commission"}, {Name: "issue"}}}
	if _, ok := s.LabelFor(1, "gad", "x"); ok {
		t.Fatal("a structure with no labels claimed one")
	}
	if ds := Doctor(labelNode("gad", 1, "work", 1), routeFor(s)); len(ds) != 0 {
		t.Fatalf("doctor complained with no labels declared: %+v", ds)
	}
}

func TestLabelsListsEveryDeclaredLabel(t *testing.T) {
	got := levelStructure().Labels()
	for _, want := range []string{"type/commission", "type/seat", "type/maquette", "type/block", "type/work"} {
		found := false
		for _, g := range got {
			if g == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("Labels() = %v, missing %q -- doctor cannot tell a level label from any other", got, want)
		}
	}
}
