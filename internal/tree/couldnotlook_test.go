package tree

import (
	"errors"
	"strings"
	"testing"

	"github.com/RiccardoCereghino/facet/internal/ghx"
)

// !! THE INCIDENT, REPRODUCED EXACTLY AND DETERMINISTICALLY. !!
//
// Two issues were reported as recording "type/block, type/work" while sitting
// at level "issue". The report was DOUBTED and then hunted as a phantom for an
// hour, because a `gh issue view` minutes later showed no type/block.
//
// It was true. The label event timelines show both issues carried BOTH labels
// for about fifty seconds while a repair was mid-flight -- type/work added
// first, type/block removed after -- and the run landed in that window.
//
// What made a correct finding look like an invented one was its own fix line:
//
//	--remove-label type/block --remove-label type/work --add-label type/work
//
// `has` held every structure label the node carried, INCLUDING the one about to
// be added, so the remedy removed and re-added the correct label. That
// incoherence was the only signal anything was odd, and it pointed away from
// the truth.
func TestTheFixLineNeverRemovesTheLabelItIsAboutToAdd(t *testing.T) {
	// The exact shape: the right label present alongside a wrong one, which is
	// what a half-finished repair looks like from the outside.
	root := labelNode("gad", 115, "some work", 3, "type/block", "type/work")

	ds := Doctor(root, routeFor(levelStructure())).Defects
	if len(ds) != 1 {
		t.Fatalf("got %d defects, want 1: %+v", len(ds), ds)
	}
	fix := ds[0].Fix

	if strings.Contains(fix, "--remove-label type/work") {
		t.Errorf("the fix removes the label it then adds -- a no-op remedy:\n%s", fix)
	}
	if !strings.Contains(fix, "--remove-label type/block") {
		t.Errorf("the fix does not remove the label that is actually wrong:\n%s", fix)
	}
	if !strings.Contains(fix, "--add-label type/work") {
		t.Errorf("the fix does not add the right label:\n%s", fix)
	}
}

// A node carrying ONLY a wrong label still has it removed -- the guard above
// must not have been implemented by dropping the removals altogether.
func TestTheFixLineStillRemovesAWrongLabel(t *testing.T) {
	root := labelNode("gad", 1, "some work", 3, "type/block")

	ds := Doctor(root, routeFor(levelStructure())).Defects
	if len(ds) != 1 {
		t.Fatalf("got %d defects, want 1: %+v", len(ds), ds)
	}
	if !strings.Contains(ds[0].Fix, "--remove-label type/block") {
		t.Errorf("a wrong label is no longer removed:\n%s", ds[0].Fix)
	}
}

// !! A DEFECT MUST NAME THE EVIDENCE IT READ. !!
//
// This is what would have settled the incident in seconds rather than an hour.
// "records type/block" is a conclusion, and checking it against a live read
// minutes later answers a different question. "read: labels type/block,
// type/work" is falsifiable at the point of reading.
func TestADefectNamesTheLabelsItActuallyRead(t *testing.T) {
	root := labelNode("gad", 115, "some work", 3, "type/block", "type/work")

	ds := Doctor(root, routeFor(levelStructure())).Defects
	if len(ds) != 1 {
		t.Fatalf("got %d defects, want 1: %+v", len(ds), ds)
	}
	text := ds[0].String()
	if !strings.Contains(text, "read:") {
		t.Fatalf("the defect states no evidence:\n%s", text)
	}
	for _, want := range []string{"type/block", "type/work", "issue"} {
		if !strings.Contains(ds[0].Read, want) {
			t.Errorf("the evidence line is missing %q:\n%s", want, ds[0].Read)
		}
	}
}

// The evidence line must distinguish "no labels came back" from "no labels",
// which print identically if an empty list is rendered as an empty string.
func TestTheEvidenceLineSaysNoneRatherThanNothing(t *testing.T) {
	root := labelNode("gad", 1, "some work", 3)

	ds := Doctor(root, routeFor(levelStructure())).Defects
	if len(ds) != 1 {
		t.Fatalf("got %d defects, want 1: %+v", len(ds), ds)
	}
	if !strings.Contains(ds[0].Read, "labels none") {
		t.Errorf("an empty label set does not read as an explicit 'none':\n%s", ds[0].Read)
	}
}

// blindSource fails one specific read, so a could-not-look is FORCED rather
// than waited for.
//
// !! THIS IS THE POINT OF THE FAKE. !! The behaviour was also observed live
// under a real GraphQL exhaustion, and that observation cannot be re-run: the
// next reader cannot summon a rate limit. A probe whose two outcomes are
// indistinguishable is not a probe, and "the degradation did not happen this
// time" and "the fix works" would be exactly that.
type blindSource struct {
	Source
	parentErrs map[string]error
}

func (b blindSource) IssueParent(repo string, number int) (ghx.IssueRef, bool, error) {
	if err, ok := b.parentErrs[refKey(repo, number)]; ok {
		return ghx.IssueRef{}, false, err
	}
	return b.Source.IssueParent(repo, number)
}

// A read that did not answer is NOT a finding, and this is criterion 1 of
// facet#147 forced deterministically.
func TestAFailedAncestryReadIsCouldNotLookAndNeverAFinding(t *testing.T) {
	src := blindSource{
		Source:     wellFormedWithParents(),
		parentErrs: map[string]error{"acme/lab#46": errors.New("API rate limit exceeded")},
	}
	route := routeWithStructure()

	root, err := Walk(src, ref("acme", "lab", 46), -1, route)
	if err != nil {
		t.Fatalf("a failed ancestry read blanked the whole report: %v", err)
	}
	rep := Doctor(root, route)

	if len(rep.Defects) != 0 {
		t.Errorf("a read that never happened was reported as a finding: %+v", rep.Defects)
	}
	if len(rep.Unread) == 0 {
		t.Fatal("the failed read is not reported at all -- silence is worse than either answer")
	}
	// It is still NAMED. Suppressing it would trade one wrong answer for
	// another: a report that omits what it could not see reads as complete.
	if !strings.Contains(rep.Unread[0].String(), "API rate limit exceeded") {
		t.Errorf("the could-not-look does not carry the reason:\n%s", rep.Unread[0])
	}
}

// A tree with BOTH a real defect and an unread node must report both, and must
// not let either erase the other.
func TestARealDefectAndAnUnreadNodeAreBothReported(t *testing.T) {
	src := wellFormed()
	src.errs = map[string]error{"acme/doctrine#282": errors.New("HTTP 404")}
	// A READABLE sibling, so the tree has something a finding can be made
	// about. Without it the only child is the unreadable one, and the test
	// would pass or fail for the wrong reason.
	src.children["acme/lab#46"] = append(src.children["acme/lab#46"], ref("acme", "harness", 121))
	route := routeWithStructure()

	root := mustWalk(t, src, ref("acme", "lab", 46), route)
	// Make the root a genuine finding on its own terms: closed, holding an
	// open child.
	root.State = "CLOSED"

	rep := Doctor(root, route)
	if len(rep.Defects) == 0 {
		t.Error("the real defect was swallowed by the unread node")
	}
	if len(rep.Unread) == 0 {
		t.Error("the unread node was swallowed by the real defect")
	}
}
