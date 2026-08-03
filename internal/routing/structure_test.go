package routing

import (
	"strings"
	"testing"
)

// fourLevels is the shape the lab that prompted this feature uses: a
// programme, holding the records of who worked on it, holding bundles, holding
// the work. The second rung has two accepted shapes because a planning issue
// sits there too, and the third is skippable because a bundle of one is just
// the work.
func fourLevels() *Structure {
	return &Structure{Levels: []Level{
		{Name: "commission"},
		{Name: "seat", Accepts: []LevelMatch{
			{Repo: "doctrine", TitlePattern: "^seat: "},
			{Repo: "lab", TitlePattern: "maquette"},
		}},
		{Name: "block", Optional: true},
		{Name: "issue"},
	}}
}

func TestAssignAcceptsTheRealShape(t *testing.T) {
	s := fourLevels()

	// A seat record under the commission.
	lvl, ok := s.Assign(0, "doctrine", "seat: c1-structure — the commands")
	if !ok || s.Levels[lvl].Name != "seat" {
		t.Fatalf("seat record assigned to %d (ok=%v), want the seat level", lvl, ok)
	}

	// The planning issue sits at the same rung, in a different repo.
	lvl, ok = s.Assign(0, "lab", "maquette: the strikers")
	if !ok || s.Levels[lvl].Name != "seat" {
		t.Fatalf("maquette assigned to %d (ok=%v), want the seat level", lvl, ok)
	}

	// A block under a seat.
	lvl, ok = s.Assign(1, "lab", "commands and the skill that drives them")
	if !ok || s.Levels[lvl].Name != "block" {
		t.Fatalf("block assigned to %d (ok=%v), want the block level", lvl, ok)
	}
}

// THE DEFECT THIS EXISTS TO CATCH. A bundle or a loose work issue filed
// straight under the programme, with no record of who worked it in between --
// which is how four levels collapse into two, one edge at a time, while every
// individual edge looks reasonable.
func TestAssignRefusesABlockDirectlyUnderTheRoot(t *testing.T) {
	s := fourLevels()
	if _, ok := s.Assign(0, "lab", "await.sh: four defects, three issues"); ok {
		t.Fatal("a loose issue was accepted directly under the root; the seat rung is required and cannot be skipped")
	}
}

// The skippable rung, which is the half an implementer gets wrong: a seat with
// no bundle carries its work directly, and that is correct rather than tolerated.
func TestAssignSkipsAnOptionalLevel(t *testing.T) {
	s := fourLevels()
	levels := s.ChildLevels(1)
	if len(levels) != 2 {
		t.Fatalf("children of a seat may occupy %d levels, want 2 (block or issue)", len(levels))
	}
	if s.Levels[levels[0]].Name != "block" || s.Levels[levels[1]].Name != "issue" {
		t.Errorf("candidates = %v, want block then issue", levels)
	}
}

// A required rung stops the skipping. Without this the optional flag would
// leak downwards and every level would become reachable from every other.
func TestChildLevelsStopsAtARequiredLevel(t *testing.T) {
	s := fourLevels()
	levels := s.ChildLevels(-1) // what may be a root
	if len(levels) != 1 || s.Levels[levels[0]].Name != "commission" {
		t.Fatalf("roots may be %v, want just the commission", levels)
	}
	// From the seat rung down: block (optional) then issue (required, stop).
	if got := s.ChildLevels(2); len(got) != 1 || s.Levels[got[0]].Name != "issue" {
		t.Errorf("children of a block = %v, want just the issue level", got)
	}
	// Nothing may hang below the last rung.
	if got := s.ChildLevels(3); len(got) != 0 {
		t.Errorf("children of the last level = %v, want none", got)
	}
}

// THE CONSTRAINT. A nil Structure is the state of every routing file that has
// not opted in, and it must produce no checks whatsoever -- not lenient ones.
// Anyone adopting facet files issues with no parent and must never be told
// that is wrong.
func TestNilStructureChecksNothing(t *testing.T) {
	var s *Structure
	if got := s.ChildLevels(0); got != nil {
		t.Errorf("ChildLevels on a nil structure = %v, want nil", got)
	}
	if _, ok := s.Assign(0, "anything", "anything"); ok {
		t.Error("a nil structure assigned a level; it must have no opinion at all")
	}
	if err := s.validate(nil); err != nil {
		t.Errorf("validate on a nil structure = %v, want nil", err)
	}
}

func TestLevelWithNoAcceptsAdmitsAnything(t *testing.T) {
	l := Level{Name: "issue"}
	if !l.accepts("any-repo", "any title at all") {
		t.Error("an unconstrained level rejected something; it must admit anything")
	}
}

func TestStructureValidate(t *testing.T) {
	repos := map[string]Repo{"lab": {}, "doctrine": {}}

	t.Run("the real shape validates", func(t *testing.T) {
		if err := fourLevels().validate(repos); err != nil {
			t.Errorf("validate = %v, want nil", err)
		}
	})

	t.Run("reports every problem at once", func(t *testing.T) {
		s := &Structure{Levels: []Level{
			{Name: ""},
			{Name: "seat", Accepts: []LevelMatch{
				{Repo: "nonexistent"},
				{TitlePattern: "^(unclosed"},
			}},
		}}
		err := s.validate(repos)
		if err == nil {
			t.Fatal("validate accepted a structure with three problems")
		}
		// Someone editing a routing file by hand should see the whole list
		// rather than rediscover one rule per attempt.
		for _, want := range []string{"has no name", "not in repos", "titlePattern"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error is missing %q:\n%s", want, err)
			}
		}
	})

	t.Run("an empty levels list is a mistake, not a disabled check", func(t *testing.T) {
		err := (&Structure{}).validate(repos)
		if err == nil || !strings.Contains(err.Error(), "omit the block entirely") {
			t.Errorf("validate = %v, want a refusal naming how to disable checks properly", err)
		}
	})

	t.Run("a trailing optional level permits nothing", func(t *testing.T) {
		s := &Structure{Levels: []Level{{Name: "top"}, {Name: "bottom", Optional: true}}}
		if err := s.validate(repos); err == nil {
			t.Error("validate accepted an optional last level; there is no rung below it to skip to")
		}
	})
}

func TestLevelDescribeNamesTheFix(t *testing.T) {
	got := fourLevels().Levels[1].Describe()
	for _, want := range []string{"seat", "doctrine", "^seat: ", "lab", "maquette", " or "} {
		if !strings.Contains(got, want) {
			t.Errorf("Describe() = %q, missing %q -- a refusal must name what was expected", got, want)
		}
	}
}
