package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/RiccardoCereghino/facet/internal/ghx"
)

// spanFake is a holder with three children whose work lands in different
// places, including the case the whole verb exists for: a child FILED in one
// repository whose work lands in another.
func spanFake() *treeFake {
	f := &treeFake{issues: map[string]*ghx.Issue{
		"acme/lab#46":      {Title: "the holder", State: "OPEN"},
		"acme/lab#144":     {Title: "block: the ledger", State: "OPEN"},
		"acme/harness#12":  {Title: "the work", State: "OPEN"},
		"acme/lab#161":     {Title: "block: the console", State: "OPEN"},
		"acme/doctrine#99": {Title: "the other work", State: "OPEN"},
	}}
	f.children = map[string][]ghx.IssueRef{
		"acme/lab#46":  {iref("acme", "lab", 144), iref("acme", "lab", 161)},
		"acme/lab#144": {iref("acme", "harness", 12)},
		"acme/lab#161": {iref("acme", "doctrine", 99)},
	}
	return f
}

// !! THE ROW SHAPE: ONE PER CHILD OVER A HOLDER. !! That is what a composer
// actually has in front of it -- the question is never "what is this one node's
// span" but "which of these may share a slot".
func TestTreeSpansReportsOneRowPerChild(t *testing.T) {
	withRouting(t, "")
	var out bytes.Buffer

	if err := runTreeSpans(&out, spanFake(), iref("acme", "lab", 46), true); err != nil {
		t.Fatalf("spans: %v", err)
	}
	var got spanReport
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("--json did not emit JSON: %v\n%s", err, out.String())
	}
	if len(got.Rows) != 2 {
		t.Fatalf("rows = %d, want one per child: %+v", len(got.Rows), got.Rows)
	}
	if got.Rows[0].Ref != "acme/lab#144" {
		t.Errorf("first row = %q", got.Rows[0].Ref)
	}
}

// !! THE POINT OF THE VERB. !! A span is where the work LANDS, not where the
// issues live. Both blocks are filed in acme/lab; their work is in acme/harness
// and acme/doctrine. A verb answering "where do the children live" would report
// both as acme/lab and call them NOT disjoint -- confidently, and wrongly.
func TestASpanIsWhereTheWorkLandsNotWhereTheIssuesLive(t *testing.T) {
	withRouting(t, "")
	var out bytes.Buffer

	if err := runTreeSpans(&out, spanFake(), iref("acme", "lab", 46), true); err != nil {
		t.Fatalf("spans: %v", err)
	}
	var got spanReport
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !contains(got.Rows[0].Span, "acme/harness") {
		t.Errorf("#144's span does not reach the repo its work is in: %v", got.Rows[0].Span)
	}
	if !contains(got.Rows[1].Span, "acme/doctrine") {
		t.Errorf("#161's span does not reach the repo its work is in: %v", got.Rows[1].Span)
	}
	// And they are genuinely different sets, which is the answer a composer
	// acts on.
	if contains(got.Rows[0].Span, "acme/doctrine") {
		t.Errorf("#144's span leaked its sibling's repo: %v", got.Rows[0].Span)
	}
}

// A leaf gets one row -- "over a single node, one row".
func TestTreeSpansOverALeafIsOneRow(t *testing.T) {
	withRouting(t, "")
	var out bytes.Buffer

	if err := runTreeSpans(&out, spanFake(), iref("acme", "harness", 12), true); err != nil {
		t.Fatalf("spans: %v", err)
	}
	var got spanReport
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Rows) != 1 || got.Rows[0].Ref != "acme/harness#12" {
		t.Fatalf("rows = %+v, want exactly the node itself", got.Rows)
	}
}

// !! THE THIRD VALUE, AND ON THIS VERB IT IS THE ONE PLACE IT CAN BE
// DANGEROUS. !!
//
// A span computed from a partial read is WORSE THAN NO SPAN: a MISSING repo
// reads as DISJOINT, and disjoint is precisely the answer that authorises
// putting two groupings in one slot. So a partial read is exit 2 and the row
// says so, rather than being handed over as a plausible list.
func TestAPartialReadIsCouldNotLookAndNeverAPlausibleSpan(t *testing.T) {
	withRouting(t, "")
	f := spanFake()
	f.childErrs = map[string]error{"acme/lab#144": errors.New("HTTP 502")}
	var out bytes.Buffer

	err := runTreeSpans(&out, f, iref("acme", "lab", 46), true)
	if err == nil {
		t.Fatal("a span built from a failed read was reported as a clean answer")
	}
	if got := exitCodeFor(err); got != exitCantLook {
		t.Errorf("exit code = %d, want %d (could not look)\n  error: %v", got, exitCantLook, err)
	}
	var got spanReport
	if jerr := json.Unmarshal(out.Bytes(), &got); jerr != nil {
		t.Fatalf("--json emitted nothing parseable: %v\n%s", jerr, out.String())
	}
	// The document still comes out -- the caller must not be handed nothing --
	// and it carries the confidence rather than only the exit code.
	if got.Rows[0].Confidence == SpanCurrent {
		t.Errorf("a degraded span is marked CURRENT: %+v", got.Rows[0])
	}
}

// The human output says it FIRST and loudly. A degraded span that reads like a
// clean one is the entire hazard.
//
// THE TWO DEGRADED STATES ARE DISTINCT AND BOTH ARE ASSERTED. Nothing below the
// subject was reached at all -- COULD NOT LOOK -- is a different fact from some
// of it was -- PARTIAL. Collapsing them would be this block's own defect
// committed inside the verb built to remove it.
func TestADegradedSpanIsMarkedInTheHumanOutput(t *testing.T) {
	withRouting(t, "")

	t.Run("nothing below the subject was reached", func(t *testing.T) {
		f := spanFake()
		f.childErrs = map[string]error{"acme/lab#144": errors.New("HTTP 502")}
		var out bytes.Buffer

		_ = runTreeSpans(&out, f, iref("acme", "lab", 46), false)
		got := out.String()
		for _, want := range []string{string(SpanBlind), "LOWER BOUND", "disjoint"} {
			if !strings.Contains(got, want) {
				t.Errorf("the degraded row does not say %q:\n%s", want, got)
			}
		}
	})

	t.Run("some of it was", func(t *testing.T) {
		f := spanFake()
		// One rung deeper: #144 reads, the work under it does not.
		f.childErrs = map[string]error{"acme/harness#12": errors.New("HTTP 502")}
		var out bytes.Buffer

		_ = runTreeSpans(&out, f, iref("acme", "lab", 46), false)
		got := out.String()
		for _, want := range []string{string(SpanPartial), "LOWER BOUND", "disjoint"} {
			if !strings.Contains(got, want) {
				t.Errorf("the partially-read row does not say %q:\n%s", want, got)
			}
		}
		if strings.Contains(got, string(SpanBlind)) {
			t.Errorf("a partial read was reported as having seen nothing at all:\n%s", got)
		}
	})
}

// A clean run is exit 0. Finding a span is not a failure -- it is a report, and
// this is the half a third-value change quietly breaks.
func TestACompleteSpanIsExitZero(t *testing.T) {
	withRouting(t, "")
	if err := runTreeSpans(&bytes.Buffer{}, spanFake(), iref("acme", "lab", 46), false); err != nil {
		t.Fatalf("a complete span reported an error: %v", err)
	}
}

// A root that cannot be read at all is could-not-look, not an empty span. An
// empty list would read as "this node touches nothing".
func TestAnUnreadableRootIsCouldNotLook(t *testing.T) {
	withRouting(t, "")
	f := spanFake()
	f.viewErrs = map[string]error{"acme/lab#46": errors.New("HTTP 404")}

	err := runTreeSpans(&bytes.Buffer{}, f, iref("acme", "lab", 46), false)
	if err == nil {
		t.Fatal("an unreadable root produced a span")
	}
	if got := exitCodeFor(err); got != exitCantLook {
		t.Errorf("exit code = %d, want %d", got, exitCantLook)
	}
}

// !! THE FLAG facet#149's OWN COMMENT ASKS FOR BY NAME. !! A node whose own
// repository is NOT in its span -- filed in one place, working in another -- is
// where both hand-derivation errors came from, and it is invisible in every
// other view.
func TestANodeFiledOutsideItsOwnSpanIsFlagged(t *testing.T) {
	withRouting(t, "")
	f := &treeFake{issues: map[string]*ghx.Issue{
		"acme/lab#161":    {Title: "block: the console", State: "OPEN"},
		"acme/harness#12": {Title: "the work", State: "OPEN"},
	}}
	f.children = map[string][]ghx.IssueRef{
		"acme/lab#161": {iref("acme", "harness", 12)},
	}
	var out bytes.Buffer

	// Over the leaf's parent, the single row is the leaf: filed in harness,
	// working in harness -- not flagged.
	if err := runTreeSpans(&out, f, iref("acme", "lab", 161), true); err != nil {
		t.Fatalf("spans: %v", err)
	}
	var got spanReport
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Rows[0].Elsewhere {
		t.Errorf("a node working in its own repository was flagged: %+v", got.Rows[0])
	}

	// Over the leaf itself there is nothing below it, so its span is its own
	// repository and it is likewise not flagged. The flag is for a node whose
	// work is entirely elsewhere.
	out.Reset()
	if err := runTreeSpans(&out, f, iref("acme", "harness", 12), true); err != nil {
		t.Fatalf("spans: %v", err)
	}
	got = spanReport{}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Rows[0].Elsewhere {
		t.Errorf("a leaf was flagged as filed outside its own span: %+v", got.Rows[0])
	}
}

// It says what it is NOT, in the output a human reads: facet holds no doctrine
// and must not be read as answering whether two spans may share a slot.
func TestTheOutputSaysItIsNotASlotChecker(t *testing.T) {
	withRouting(t, "")
	var out bytes.Buffer
	if err := runTreeSpans(&out, spanFake(), iref("acme", "lab", 46), false); err != nil {
		t.Fatalf("spans: %v", err)
	}
	if !strings.Contains(out.String(), "facet holds none") {
		t.Errorf("the output does not disclaim the doctrine question:\n%s", out.String())
	}
}

// The --help names the codes, and names the absence of a 1 -- as `orphans` now
// does, and for the same reason.
func TestTreeSpansHelpStatesTheExitCodes(t *testing.T) {
	long := newTreeSpansCmd().Long
	for _, want := range []string{"EXIT CODES", "could NOT look", "0  looked", "2  could"} {
		if !strings.Contains(long, want) {
			t.Errorf("--help is missing %q:\n%s", want, long)
		}
	}
}

// A malformed reference read nothing, so it cannot be a finding either.
func TestTreeSpansRefusesABadRefWithoutClaimingAFinding(t *testing.T) {
	cmd := newTreeSpansCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"not-a-ref"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal(`"not-a-ref" was accepted as an issue reference`)
	}
	if got := exitCodeFor(err); got != exitCantLook {
		t.Errorf("exit code = %d, want %d", got, exitCantLook)
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
