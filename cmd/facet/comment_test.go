package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/RiccardoCereghino/facet/internal/ghx"
)

const commentKinds = `,
	"commentKinds": {
		"plan":    "(?m)^#+ .*[Pp]lan",
		"finding": "(?m)^\\*\\*FINDING"
	}`

func commentFake() *treeFake {
	f := &treeFake{issues: map[string]*ghx.Issue{}}
	f.comments = map[string][]ghx.Comment{
		"acme/lab#75": {
			{ID: 1, Body: "## Plan\nthe first attempt", CreatedAt: "2026-08-03T10:00:00Z", UpdatedAt: "2026-08-03T10:00:00Z"},
			{ID: 2, Body: "some discussion, not a plan", CreatedAt: "2026-08-03T10:30:00Z", UpdatedAt: "2026-08-03T10:30:00Z"},
			{ID: 3, Body: "## Plan — revision 2\nTHE ONE THAT COUNTS", CreatedAt: "2026-08-03T11:00:00Z", UpdatedAt: "2026-08-03T11:00:00Z"},
			{ID: 4, Body: "**FINDING** something else", CreatedAt: "2026-08-03T12:00:00Z", UpdatedAt: "2026-08-03T12:00:00Z"},
		},
	}
	return f
}

// The whole reason this command exists: where a decision is revised by posting
// it again, the newest one is the one that counts, and finding it by eye is how
// the wrong revision gets acted on.
func TestCommentLastReturnsTheNewestMatch(t *testing.T) {
	withRouting(t, commentKinds)
	var out bytes.Buffer
	if err := runCommentList(&out, commentFake(), iref("acme", "lab", 75), "plan", "", true); err != nil {
		t.Fatalf("last: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "THE ONE THAT COUNTS") {
		t.Errorf("did not return the newest plan:\n%s", got)
	}
	if strings.Contains(got, "the first attempt") {
		t.Errorf("returned an older revision as well:\n%s", got)
	}
	// A thread carrying one plan and a thread carrying five must not look the
	// same, so the count is reported.
	if !strings.Contains(got, "2 of 2 matching") {
		t.Errorf("the number of matches was not reported:\n%s", got)
	}
}

func TestCommentListFiltersByKind(t *testing.T) {
	withRouting(t, commentKinds)
	var out bytes.Buffer
	if err := runCommentList(&out, commentFake(), iref("acme", "lab", 75), "finding", "", false); err != nil {
		t.Fatalf("list: %v", err)
	}
	if n := strings.Count(strings.TrimSpace(out.String()), "\n") + 1; n != 1 {
		t.Errorf("got %d lines, want 1:\n%s", n, out.String())
	}
	if !strings.Contains(out.String(), "FINDING") {
		t.Errorf("wrong comment matched:\n%s", out.String())
	}
}

// facet does not know what a plan is. With no kinds declared the flag refuses
// and names the fix -- rather than matching nothing, which is indistinguishable
// from an issue that genuinely has no plan on it.
func TestCommentKindRefusesWithoutTheRoutingBlock(t *testing.T) {
	withRouting(t, "")
	var out bytes.Buffer
	err := runCommentList(&out, commentFake(), iref("acme", "lab", 75), "plan", "", true)
	if err == nil {
		t.Fatal("--kind was accepted with no commentKinds block")
	}
	for _, want := range []string{"no comment kinds", "fix:", "commentKinds"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal is missing %q:\n%s", want, err)
		}
	}
}

func TestCommentUnknownKindNamesTheOnesThatExist(t *testing.T) {
	withRouting(t, commentKinds)
	var out bytes.Buffer
	err := runCommentList(&out, commentFake(), iref("acme", "lab", 75), "exit", "", true)
	if err == nil {
		t.Fatal("an undeclared kind was accepted")
	}
	for _, want := range []string{"unknown comment kind", "finding", "plan"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal is missing %q:\n%s", want, err)
		}
	}
}

// --grep works with no routing block at all, so an adopter who has declared
// nothing can still find things.
func TestCommentGrepNeedsNoConfiguration(t *testing.T) {
	withRouting(t, "")
	var out bytes.Buffer
	if err := runCommentList(&out, commentFake(), iref("acme", "lab", 75), "", "revision 2", true); err != nil {
		t.Fatalf("grep: %v", err)
	}
	if !strings.Contains(out.String(), "THE ONE THAT COUNTS") {
		t.Errorf("grep did not find it:\n%s", out.String())
	}
}

// "No plan on this issue" and "nothing was searched for" must not print the
// same thing.
func TestCommentEmptyResultSaysWhatWasSearched(t *testing.T) {
	withRouting(t, commentKinds)
	f := commentFake()
	f.comments = map[string][]ghx.Comment{"acme/lab#75": {{ID: 1, Body: "just chatter"}}}
	var out bytes.Buffer
	if err := runCommentList(&out, f, iref("acme", "lab", 75), "plan", "", true); err != nil {
		t.Fatalf("last: %v", err)
	}
	got := out.String()
	for _, want := range []string{"no matching comments", "1 comments searched", "kind plan"} {
		if !strings.Contains(got, want) {
			t.Errorf("output is missing %q:\n%s", want, got)
		}
	}
}

// An edited plan is not the plan that was agreed to, so the edit is surfaced
// wherever a comment is read as a decision.
func TestCommentLastFlagsAnEditedComment(t *testing.T) {
	withRouting(t, commentKinds)
	f := commentFake()
	f.comments["acme/lab#75"] = []ghx.Comment{{
		ID: 9, Body: "## Plan\nquietly changed",
		CreatedAt: "2026-08-03T10:00:00Z", UpdatedAt: "2026-08-03T18:00:00Z",
	}}
	var out bytes.Buffer
	if err := runCommentList(&out, f, iref("acme", "lab", 75), "plan", "", true); err != nil {
		t.Fatalf("last: %v", err)
	}
	if !strings.Contains(out.String(), "edited") {
		t.Errorf("an edited comment was not marked:\n%s", out.String())
	}
}

func TestCommentPostRefusesAnEmptyBody(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/empty.md"
	if err := writeFile(path, "   \n"); err != nil {
		t.Fatal(err)
	}
	if _, err := readBodyFile(path, "comment"); err == nil {
		t.Error("an empty body was accepted: a comment with no body is a note to nobody")
	}
	if _, err := readBodyFile("", "comment"); err == nil {
		t.Error("a missing --body-file was accepted")
	}
}
