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
	lvl, ok := s.Assign(0, "doctrine", "seat: c1-structure — the commands", nil)
	if !ok || s.Levels[lvl].Name != "seat" {
		t.Fatalf("seat record assigned to %d (ok=%v), want the seat level", lvl, ok)
	}

	// The planning issue sits at the same rung, in a different repo.
	lvl, ok = s.Assign(0, "lab", "maquette: the strikers", nil)
	if !ok || s.Levels[lvl].Name != "seat" {
		t.Fatalf("maquette assigned to %d (ok=%v), want the seat level", lvl, ok)
	}

	// A block under a seat.
	lvl, ok = s.Assign(1, "lab", "commands and the skill that drives them", nil)
	if !ok || s.Levels[lvl].Name != "block" {
		t.Fatalf("block assigned to %d (ok=%v), want the block level", lvl, ok)
	}
}

// labelledLevels mirrors the real routing file's shape (routing.json): every
// level declares the label that records it, and the seat level's two shapes
// each override it per repo -- type/seat in doctrine, type/maquette in lab.
func labelledLevels() *Structure {
	return &Structure{Levels: []Level{
		{Name: "commission", Label: "type/commission"},
		{Name: "seat", Accepts: []LevelMatch{
			{Repo: "doctrine", TitlePattern: "^seat: ", Label: "type/seat"},
			{Repo: "lab", TitlePattern: "maquette", Label: "type/maquette"},
		}},
		{Name: "block", Optional: true, Label: "type/block"},
		{Name: "issue", Label: "type/work"},
	}}
}

// A label satisfies a candidate whose TITLE would not have -- proving label
// and title are alternative ways to satisfy the SAME candidate (checked
// together, shallowest-first), not two separate passes where a deeper
// candidate's label could win over a shallower candidate's title.
func TestAssignAcceptsALabelWhereTitleWouldNotHaveMatched(t *testing.T) {
	s := labelledLevels()
	lvl, ok := s.Assign(0, "doctrine", "a retitled seat record with no prefix at all", []string{"type/seat"})
	if !ok || s.Levels[lvl].Name != "seat" {
		t.Fatalf("label-only match assigned to %d (ok=%v), want the seat level", lvl, ok)
	}
}

func TestLevelForLabels(t *testing.T) {
	s := labelledLevels()

	t.Run("a single matching label is authoritative", func(t *testing.T) {
		lvl, ok, ambiguous := s.LevelForLabels("lab", []string{"fleet", "type/block", "complexity/3"})
		if ambiguous {
			t.Fatal("one level's label present, reported ambiguous")
		}
		if !ok || s.Levels[lvl].Name != "block" {
			t.Fatalf("LevelForLabels = %d, %v, want the block level", lvl, ok)
		}
	})

	t.Run("no declared label present falls through", func(t *testing.T) {
		_, ok, ambiguous := s.LevelForLabels("lab", []string{"fleet", "priority/now"})
		if ok || ambiguous {
			t.Fatalf("LevelForLabels ok=%v ambiguous=%v on unlabelled input, want both false", ok, ambiguous)
		}
	})

	t.Run("two different levels' labels at once is a conflict, not a pick", func(t *testing.T) {
		_, ok, ambiguous := s.LevelForLabels("lab", []string{"type/block", "type/work"})
		if ok {
			t.Fatal("LevelForLabels silently picked one of two conflicting level labels")
		}
		if !ambiguous {
			t.Fatal("two conflicting level labels were present but not reported ambiguous")
		}
	})

	t.Run("a seat's per-repo label is found via Accepts, not just the level's own", func(t *testing.T) {
		lvl, ok, ambiguous := s.LevelForLabels("lab", []string{"type/maquette"})
		if ambiguous || !ok || s.Levels[lvl].Name != "seat" {
			t.Fatalf("LevelForLabels(type/maquette) = %d, %v, %v, want the seat level", lvl, ok, ambiguous)
		}
	})

	// The defect an audit of this feature found: a label match must be scoped
	// exactly as tightly as the title pattern it stands in for. type/maquette's
	// Accepts entry names "lab" specifically; a node in a third repo carrying
	// that label must fall through, exactly as its title ("maquette: ...")
	// would have failed to match there too.
	t.Run("a label scoped to another repo does not match here", func(t *testing.T) {
		_, ok, ambiguous := s.LevelForLabels("harness", []string{"type/maquette"})
		if ok || ambiguous {
			t.Fatalf("LevelForLabels(harness, type/maquette) = ok=%v ambiguous=%v, want both false -- "+
				"type/maquette is lab-scoped and harness is a different repo", ok, ambiguous)
		}
	})

	t.Run("the same repo-scoped label matches its own repo", func(t *testing.T) {
		lvl, ok, ambiguous := s.LevelForLabels("doctrine", []string{"type/seat"})
		if ambiguous || !ok || s.Levels[lvl].Name != "seat" {
			t.Fatalf("LevelForLabels(doctrine, type/seat) = %d, %v, %v, want the seat level", lvl, ok, ambiguous)
		}
	})
}

// The same repo-scoping gap, on the non-root path: Assign must not let a
// label from one repo's convention satisfy a candidate declared for another.
func TestAssignDoesNotHonourALabelFromTheWrongRepo(t *testing.T) {
	s := labelledLevels()
	if lvl, ok := s.Assign(0, "harness", "no seat convention in this title at all", []string{"type/maquette"}); ok {
		t.Fatalf("Assign(harness, ..., type/maquette) = %d, true -- "+
			"type/maquette is lab-scoped and must not satisfy a harness-repo node", lvl)
	}
}

// THE DEFECT THIS EXISTS TO CATCH. A bundle or a loose work issue filed
// straight under the programme, with no record of who worked it in between --
// which is how four levels collapse into two, one edge at a time, while every
// individual edge looks reasonable.
func TestAssignRefusesABlockDirectlyUnderTheRoot(t *testing.T) {
	s := fourLevels()
	if _, ok := s.Assign(0, "lab", "await.sh: four defects, three issues", nil); ok {
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
	if _, ok := s.Assign(0, "anything", "anything", nil); ok {
		t.Error("a nil structure assigned a level; it must have no opinion at all")
	}
	if _, ok, ambiguous := s.LevelForLabels("anything", []string{"type/block"}); ok || ambiguous {
		t.Error("a nil structure resolved a label to a level; it must have no opinion at all")
	}
	if err := s.validate(nil); err != nil {
		t.Errorf("validate on a nil structure = %v, want nil", err)
	}
}

func TestLevelWithNoAcceptsAdmitsAnything(t *testing.T) {
	l := Level{Name: "issue"}
	if !l.accepts("any-repo", "any title at all", nil) {
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

// A refusal must also name the label alternative, not only the title
// convention -- the exact gap facet#133's GAP002 found: the refusal message
// named a title prefix and said nothing about the label the wire check could
// equally have accepted.
func TestLevelDescribeNamesTheLabelAlternative(t *testing.T) {
	got := labelledLevels().Levels[1].Describe()
	for _, want := range []string{"type/seat", "type/maquette", "or labelled"} {
		if !strings.Contains(got, want) {
			t.Errorf("Describe() = %q, missing %q -- a refusal must also name the label alternative", got, want)
		}
	}
}

// backlogLevels adds the shape facet#133 exists for: a rung directly under a
// commission holding EITHER a live seat OR a grouping filed for later, told
// apart by label, and permitting different things below them. A seat may hold
// work directly -- a bundle of one is just the work -- while a grouping must
// have its work bundled first, because grouping is the whole reason that shape
// is filed at all.
//
// The block rung is CONSTRAINED here, unlike fourLevels(). An unconstrained
// optional rung admits anything and so absorbs everything that could have
// belonged below it, which Assign's own doc names and tells the reader to fix
// exactly this way: give the level an Accepts if the difference matters. Here
// it matters, because "the bundle" and "the work" both hang off this rung.
func backlogLevels() *Structure {
	return &Structure{Levels: []Level{
		{Name: "commission", Label: "type/commission"},
		{Name: "holder", Accepts: []LevelMatch{
			{Repo: "doctrine", TitlePattern: "^seat: ", Label: "type/seat"},
			{Repo: "lab", TitlePattern: "^maquette:", Label: "type/maquette"},
			{Label: "type/backlog", ChildMustBe: "block"},
		}},
		{Name: "block", Optional: true, Label: "type/block", Accepts: []LevelMatch{
			{TitlePattern: "^block:"},
			{Label: "type/block"},
		}},
		{Name: "issue", Label: "type/work"},
	}}
}

// The defect facet#133 reports: a grouping that is not being worked has no
// seat, and needed one to reach the tree at all.
func TestAGroupingWithNoSeatSitsUnderACommission(t *testing.T) {
	s := backlogLevels()

	lvl, ok := s.Assign(0, "lab", "WIP seed: the bottega's core function", []string{"type/backlog"})
	if !ok {
		t.Fatal("a type/backlog node was refused under a commission -- this is the whole of facet#133")
	}
	if got := s.Levels[lvl].Name; got != "holder" {
		t.Fatalf("assigned to %q, want the rung directly under a commission", got)
	}
}

// The property the seat record said must not be traded away: nothing here
// makes an unoccupied node read as a seat. A backlog is its own label and the
// level's own recorded label follows the shape that matched.
func TestABacklogIsNotRecordedAsASeat(t *testing.T) {
	s := backlogLevels()

	label, ok := s.LabelFor(1, "lab", "WIP seed: something nobody is working", []string{"type/backlog"})
	if ok && label == "type/seat" {
		t.Fatal("a node with no seat title recorded type/seat -- an open seat must keep meaning a live seat")
	}

	// And the reverse: a real seat record still records type/seat.
	label, ok = s.LabelFor(1, "doctrine", "seat: c1-tree — the derivation", []string{"type/seat"})
	if !ok || label != "type/seat" {
		t.Fatalf("a real seat record recorded %q (ok=%v), want type/seat", label, ok)
	}
}

func TestChildMustBeNarrowsOnlyTheShapeThatAsksForIt(t *testing.T) {
	s := backlogLevels()

	// A seat may hold work directly: the optional block rung is skippable.
	seat := s.ChildLevelsFor(1, "doctrine", "seat: c1-tree — the derivation", []string{"type/seat"})
	if len(seat) != 2 {
		t.Fatalf("a seat's children are %v, want both the block and the issue rungs", names(s, seat))
	}

	// A backlog may not: its work has to be bundled first.
	backlog := s.ChildLevelsFor(1, "lab", "WIP seed: the bottega's core function", []string{"type/backlog"})
	if len(backlog) != 1 || s.Levels[backlog[0]].Name != "block" {
		t.Fatalf("a backlog's children are %v, want only the block rung", names(s, backlog))
	}
}

func TestABareWorkIssueUnderABacklogIsRefused(t *testing.T) {
	s := backlogLevels()
	within := s.ChildLevelsFor(1, "lab", "WIP seed: the bottega's core function", []string{"type/backlog"})

	if _, ok := s.AssignWithin(within, "lab", "a lone filing nobody has grouped", []string{"type/work"}); ok {
		t.Fatal("a bare work issue was accepted under a backlog -- grouping is the reason the shape exists")
	}

	// The same issue under a BLOCK under that backlog is fine, which is the
	// point: the work is not forbidden, only the missing grouping.
	blocks := s.ChildLevelsFor(2, "lab", "block: the four filings", []string{"type/block"})
	lvl, ok := s.AssignWithin(blocks, "lab", "a lone filing nobody has grouped", []string{"type/work"})
	if !ok || s.Levels[lvl].Name != "issue" {
		t.Fatalf("work under a block assigned to %d (ok=%v), want the issue level", lvl, ok)
	}
}

// A seat is unnarrowed, so it keeps holding work directly -- the shape live
// trees already use, and the reason `block` could not simply be made required.
func TestASeatStillHoldsWorkDirectly(t *testing.T) {
	s := backlogLevels()
	within := s.ChildLevelsFor(1, "doctrine", "seat: c1-tree — the derivation", []string{"type/seat"})

	lvl, ok := s.AssignWithin(within, "doctrine", "the crib forbids subagents that D9 allows", []string{"type/work"})
	if !ok || s.Levels[lvl].Name != "issue" {
		t.Fatalf("work directly under a seat assigned to %d (ok=%v), want the issue level", lvl, ok)
	}
}

// The absorption Assign's doc describes, and the fix it prescribes: an
// unconstrained optional rung swallows the rung below it. Constraining the
// block rung is what stops a work issue reading as a bundle.
func TestAConstrainedBlockRungNoLongerAbsorbsTheWork(t *testing.T) {
	absorbing := labelledLevels() // block is Optional with no Accepts
	lvl, ok := absorbing.Assign(1, "doctrine", "the crib forbids subagents that D9 allows", []string{"type/work"})
	if !ok || absorbing.Levels[lvl].Name != "block" {
		t.Fatalf("precondition: an unconstrained optional rung should absorb, got %d (ok=%v)", lvl, ok)
	}

	constrained := backlogLevels()
	lvl, ok = constrained.Assign(1, "doctrine", "the crib forbids subagents that D9 allows", []string{"type/work"})
	if !ok || constrained.Levels[lvl].Name != "issue" {
		t.Fatalf("work assigned to %d (ok=%v), want the issue level once the block rung is constrained",
			lvl, ok)
	}
}

func TestChildMustBeIsValidated(t *testing.T) {
	repos := map[string]Repo{"lab": {}}

	t.Run("a narrowing to a rung the children may not occupy is refused", func(t *testing.T) {
		s := &Structure{Levels: []Level{
			{Name: "commission", Accepts: []LevelMatch{{Repo: "lab", ChildMustBe: "issue"}}},
			{Name: "holder"},
			{Name: "issue"},
		}}
		err := s.validate(repos)
		if err == nil {
			t.Fatal("childMustBe naming a rung outside the children's candidates was accepted")
		}
		if !strings.Contains(err.Error(), "childMustBe") || !strings.Contains(err.Error(), "may not be") {
			t.Fatalf("error does not name the problem: %v", err)
		}
	})

	t.Run("a narrowing to a permitted rung passes", func(t *testing.T) {
		s := &Structure{Levels: []Level{
			{Name: "commission"},
			{Name: "holder", Accepts: []LevelMatch{{Repo: "lab", ChildMustBe: "block"}}},
			{Name: "block", Optional: true},
			{Name: "issue"},
		}}
		if err := s.validate(repos); err != nil {
			t.Fatalf("a well-formed childMustBe was refused: %v", err)
		}
	})
}

func names(s *Structure, idx []int) []string {
	var out []string
	for _, i := range idx {
		out = append(out, s.Levels[i].Name)
	}
	return out
}

// Finding 2 (L1), c1-audit-tree on facet!136: the doc promises a narrowing
// "can only ever REMOVE a candidate ChildLevels already returned", and the old
// fallback searched the WHOLE ladder by name -- so it could hand back a rung
// this parent never permitted, including one ABOVE it. Measured then:
// ChildLevels(1) = [block issue] while ChildLevelsFor(1, ...) = [commission].
//
// validate() refuses such a structure, so it was never reachable through Load.
// It is pinned anyway because that invariant is the argument the doc gives for
// the whole mechanism being safe: a reader asking "can this smuggle in a rung?"
// reads the comment, sees validate() cited, and stops reading.
func TestANarrowingCanOnlyEverRemoveACandidate(t *testing.T) {
	s := &Structure{Levels: []Level{
		{Name: "commission"},
		{Name: "holder", Accepts: []LevelMatch{
			{Label: "type/backlog", ChildMustBe: "commission"}, // a rung ABOVE
		}},
		{Name: "block", Optional: true},
		{Name: "issue"},
	}}

	within := s.ChildLevelsFor(1, "lab", "anything", []string{"type/backlog"})
	permitted := map[int]bool{}
	for _, i := range s.ChildLevels(1) {
		permitted[i] = true
	}
	for _, i := range within {
		if !permitted[i] {
			t.Fatalf("narrowing produced level %q, which a child of %q may not occupy -- "+
				"the result must always be a subset", s.Levels[i].Name, s.Levels[1].Name)
		}
	}

	// And nothing may be assigned through it, rather than something wrong being.
	if _, ok := s.AssignWithin(within, "lab", "some work", []string{"type/work"}); ok {
		t.Error("a structure whose narrowing names an impossible rung still placed a child")
	}
}

// A refusal must not describe a shape as accepting "anything" when the shape
// is satisfied ONLY by a label. Found by reading a live refusal produced while
// wiring the worked example: a grouping's children rendered as
//
//	must be block (a title matching ^block:, or anything (or labelled type/block))
//
// where the second shape accepts a labelled node and nothing else. A refusal
// that misdescribes its own rule teaches the reader to force it.
func TestDescribeDoesNotCallALabelOnlyShapeAnything(t *testing.T) {
	l := Level{Name: "block", Accepts: []LevelMatch{
		{TitlePattern: "^block:"},
		{Label: "type/block"},
	}}
	got := l.Describe()
	if strings.Contains(got, "anything") {
		t.Errorf("a label-only shape is described as accepting anything:\n  %s", got)
	}
	if !strings.Contains(got, "labelled type/block") {
		t.Errorf("the label the shape actually requires is not named:\n  %s", got)
	}

	// And where a title pattern IS present the two really are alternatives, so
	// "or labelled" stays right.
	both := Level{Name: "seat", Accepts: []LevelMatch{
		{Repo: "doctrine", TitlePattern: "^seat: ", Label: "type/seat"},
	}}
	if !strings.Contains(both.Describe(), "or labelled type/seat") {
		t.Errorf("an alternative label stopped being described as one:\n  %s", both.Describe())
	}
}

// Labels() answers "is this one of ours?" -- recognition, repo-independent.
// LabelsFor answers "could a wire HERE ever need this?" -- requirement, which
// is not. A label on a repo-scoped shape is reachable in that repository and
// nowhere else, because matchedShape skips a shape whose repo does not match.
//
// facet#139's first audit round: the parity check asked the recognition set
// the requirement question and faulted a repository for a label the same
// routing file forbids ever applying there.
func TestLabelsForIsScopedWhereLabelsIsNot(t *testing.T) {
	s := &Structure{Levels: []Level{
		{Name: "commission", Label: "type/commission"},
		{Name: "holder", Accepts: []LevelMatch{
			{Repo: "doctrine", TitlePattern: "^seat: ", Label: "type/seat"},
			{Repo: "lab", TitlePattern: "^maquette:", Label: "type/maquette"},
			{Label: "type/backlog", ChildMustBe: "block"},
		}},
		{Name: "block", Optional: true, Label: "type/block"},
		{Name: "issue", Label: "type/work"},
	}}

	all := s.Labels()
	if len(all) != 6 {
		t.Fatalf("Labels() = %v, want all six -- recognition is unscoped", all)
	}

	cases := []struct {
		repoKey string
		want    []string
	}{
		{"doctrine", []string{"type/commission", "type/seat", "type/backlog", "type/block", "type/work"}},
		{"lab", []string{"type/commission", "type/maquette", "type/backlog", "type/block", "type/work"}},
		// A repository named by no shape gets only the unscoped labels -- which
		// is exactly what a wire there could reach.
		{"cava", []string{"type/commission", "type/backlog", "type/block", "type/work"}},
		// A repository the routing table does not map at all resolves to "",
		// and must behave the same way rather than matching every scope.
		{"", []string{"type/commission", "type/backlog", "type/block", "type/work"}},
	}
	for _, tc := range cases {
		t.Run("repo="+tc.repoKey, func(t *testing.T) {
			got := s.LabelsFor(tc.repoKey)
			if strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Errorf("LabelsFor(%q) = %v, want %v", tc.repoKey, got, tc.want)
			}
		})
	}
}

func TestLabelsForOnANilStructure(t *testing.T) {
	var s *Structure
	if got := s.LabelsFor("anything"); got != nil {
		t.Errorf("LabelsFor on nil = %v, want nil", got)
	}
}
