package tree

import (
	"errors"
	"strings"
	"testing"

	"github.com/RiccardoCereghino/facet/internal/ghx"
)

// sourceWithBody is the minimal Source double this file needs: it answers
// ViewIssue with a fixed body (or error) for one ref and refuses every other
// call, so a test cannot pass by accident through some OTHER read path.
type sourceWithBody struct {
	body string
	err  error
}

func (s sourceWithBody) ViewIssue(_ string, number int) (*ghx.Issue, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &ghx.Issue{Number: number, Body: s.body}, nil
}
func (sourceWithBody) IssueChildren(string, int) ([]ghx.SubIssue, error) {
	return nil, errors.New("not scripted: acknowledgedReason must not call IssueChildren")
}
func (sourceWithBody) IssueParent(string, int) (ghx.IssueRef, bool, error) {
	return ghx.IssueRef{}, false, errors.New("not scripted: acknowledgedReason must not call IssueParent")
}

// !! THE PROPERTY facet#159 EXISTS FOR !!
//
// A closed holder with no children can be the TRUTH -- and with a label AND a
// verified reason, it must move OUT of Defects and INTO Acknowledged, never
// vanish and never stay double-counted.
func TestAClosedHolderWithAVerifiedReasonIsAcknowledgedNotADefect(t *testing.T) {
	root := childlessNode(441, 1, "CLOSED", "type/seat", acknowledgedLabel)
	src := sourceWithBody{body: "## Acknowledged\n\nDeferred; live blocks re-homed to #128.\n"}

	rep := DoctorWithSource(root, routeFor(childlessStructure()), src)
	if len(rep.Defects) != 0 {
		t.Fatalf("Defects = %+v, want none -- an acknowledged holder must not also be a defect", rep.Defects)
	}
	if len(rep.Acknowledged) != 1 {
		t.Fatalf("Acknowledged = %+v, want exactly 1", rep.Acknowledged)
	}
	if !strings.Contains(rep.Acknowledged[0].Read, "Deferred") {
		t.Errorf("the reason is not carried in the evidence: %q", rep.Acknowledged[0].Read)
	}
}

// TestABareAcknowledgedLabelIsStillADefect is the acceptance line verbatim:
// "a bare `ignore` is a claim nobody re-checks". The label alone, with no
// reason anywhere in the body, must not suppress anything.
func TestABareAcknowledgedLabelIsStillADefect(t *testing.T) {
	root := childlessNode(441, 1, "CLOSED", "type/seat", acknowledgedLabel)
	src := sourceWithBody{body: "Just a description, no heading at all.\n"}

	rep := DoctorWithSource(root, routeFor(childlessStructure()), src)
	if len(rep.Acknowledged) != 0 {
		t.Fatalf("Acknowledged = %+v, want none -- a bare label must not suppress", rep.Acknowledged)
	}
	if len(rep.Defects) != 1 {
		t.Fatalf("Defects = %+v, want exactly 1", rep.Defects)
	}
	if !strings.Contains(rep.Defects[0].What, "NO reason recorded") {
		t.Errorf("the defect does not say the label was bare: %q", rep.Defects[0].What)
	}
}

// TestAnAcknowledgedHeadingWithNoContentIsStillBare: present but empty is
// the same failure as absent, mirroring checkSections' own rule in gad.
func TestAnAcknowledgedHeadingWithNoContentIsStillBare(t *testing.T) {
	root := childlessNode(441, 1, "CLOSED", "type/seat", acknowledgedLabel)
	src := sourceWithBody{body: "## Acknowledged\n\n## Next heading\nsomething else\n"}

	rep := DoctorWithSource(root, routeFor(childlessStructure()), src)
	if len(rep.Acknowledged) != 0 {
		t.Fatalf("Acknowledged = %+v, want none -- an empty section is not a reason", rep.Acknowledged)
	}
	if len(rep.Defects) != 1 {
		t.Fatalf("Defects = %+v, want exactly 1", rep.Defects)
	}
}

// TestAReadFailureDoesNotSuppressEitherWay: an unreadable body must not be
// silently read as either "acknowledged" or "ordinary childless defect" --
// it gets its OWN defect saying the check could not run, because a
// suppression that cannot be verified is not a suppression.
func TestAReadFailureDoesNotSuppressEitherWay(t *testing.T) {
	root := childlessNode(441, 1, "CLOSED", "type/seat", acknowledgedLabel)
	src := sourceWithBody{err: errors.New("GraphQL exhausted")}

	rep := DoctorWithSource(root, routeFor(childlessStructure()), src)
	if len(rep.Acknowledged) != 0 {
		t.Fatalf("Acknowledged = %+v, want none -- a read failure must not read as verified", rep.Acknowledged)
	}
	if len(rep.Defects) != 1 {
		t.Fatalf("Defects = %+v, want exactly 1", rep.Defects)
	}
	if !strings.Contains(rep.Defects[0].What, "could not be verified") {
		t.Errorf("the defect does not say the check itself failed: %q", rep.Defects[0].What)
	}
}

// TestDoctorWithNoSourceNeverAcknowledges is [Doctor]'s own contract: with no
// Source wired at all (every existing caller of Doctor, unchanged), the label
// is read but unverifiable, so the node reports exactly as it did before this
// issue -- an ordinary childless defect, never silently suppressed.
func TestDoctorWithNoSourceNeverAcknowledges(t *testing.T) {
	root := childlessNode(441, 1, "CLOSED", "type/seat", acknowledgedLabel)

	rep := Doctor(root, routeFor(childlessStructure()))
	if len(rep.Acknowledged) != 0 {
		t.Fatalf("Acknowledged = %+v, want none -- Doctor wires no Source", rep.Acknowledged)
	}
	if len(rep.Defects) != 1 {
		t.Fatalf("Defects = %+v, want exactly 1", rep.Defects)
	}
}

// TestAnOpenAcknowledgedHolderIsUnaffected: the label only ever matters on
// the CLOSED path -- an open holder with no children is not the childless
// defect at all (the noise guard childless_test.go already covers), and a
// stray acknowledged label on it must not change that either way.
func TestAnOpenAcknowledgedHolderIsUnaffected(t *testing.T) {
	root := childlessNode(441, 1, "OPEN", "type/seat", acknowledgedLabel)
	src := sourceWithBody{body: "## Acknowledged\n\nsome reason\n"}

	rep := DoctorWithSource(root, routeFor(childlessStructure()), src)
	if len(rep.Defects) != 0 || len(rep.Acknowledged) != 0 {
		t.Fatalf("an open holder was reported: defects=%+v acknowledged=%+v", rep.Defects, rep.Acknowledged)
	}
}

// TestSectionBodyStopsAtTheNextHeading pins the parsing rule directly: text
// under a LATER heading must never leak into an earlier section's reason.
func TestSectionBodyStopsAtTheNextHeading(t *testing.T) {
	got := sectionBody("## Acknowledged\n\nreal reason\n\n## Repos in scope\n\ngad\n", "acknowledged")
	if got != "real reason" {
		t.Errorf("sectionBody = %q, want %q", got, "real reason")
	}
}

// TestSectionBodyIsCaseAndLevelInsensitive, matching the house convention
// every other section parser in this bottega already follows.
func TestSectionBodyIsCaseAndLevelInsensitive(t *testing.T) {
	got := sectionBody("#### ACKNOWLEDGED\ncontent\n", "acknowledged")
	if got != "content" {
		t.Errorf("sectionBody = %q, want %q", got, "content")
	}
}
