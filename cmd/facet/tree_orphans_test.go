package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/RiccardoCereghino/facet/internal/ghx"
)

// orphanFake scripts one OpenIssueParents answer per repository.
type orphanFake struct {
	issues map[string][]ghx.Parentage
	errs   map[string]error
	calls  []string
}

func (f *orphanFake) OpenIssueParents(repo string) ([]ghx.Parentage, error) {
	f.calls = append(f.calls, repo)
	if err, ok := f.errs[repo]; ok {
		return nil, err
	}
	return f.issues[repo], nil
}

func parented(owner, repo string, n int, title string, parent ghx.IssueRef) ghx.Parentage {
	return ghx.Parentage{Ref: iref(owner, repo, n), Title: title, Parent: parent, HasParent: true}
}

func unparented(owner, repo string, n int, title string) ghx.Parentage {
	return ghx.Parentage{Ref: iref(owner, repo, n), Title: title}
}

func orphanFixture() *orphanFake {
	return &orphanFake{issues: map[string][]ghx.Parentage{
		"acme/harness": {
			unparented("acme", "harness", 12, "held in a session todo and nowhere else"),
			parented("acme", "harness", 13, "the work", iref("acme", "lab", 46)),
			unparented("acme", "harness", 14, "deliberately outside any commission"),
		},
		"acme/lab": {
			parented("acme", "lab", 75, "the commands and the skill", iref("acme", "doctrine", 282)),
		},
	}}
}

func TestTreeOrphansReportsOnlyTheUnparented(t *testing.T) {
	f := orphanFixture()
	var out bytes.Buffer

	if err := runTreeOrphans(&out, f, []string{"acme/harness", "acme/lab"}, false); err != nil {
		t.Fatalf("orphans: %v", err)
	}
	got := out.String()
	for _, want := range []string{"acme/harness#12", "acme/harness#14"} {
		if !strings.Contains(got, want) {
			t.Errorf("output is missing the orphan %s:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"acme/harness#13", "acme/lab#75"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("%s has a parent and was reported anyway:\n%s", unwanted, got)
		}
	}
}

// !! THE RULING THIS COMMAND IS BUILT AROUND. !! Not every unparented issue is
// a defect -- plenty are deliberately outside a commission. The output is a
// question, so finding some is exit 0. Exit 1 here would make a planner treat
// a legitimate out-of-tree issue as a failure.
func TestFindingOrphansIsNotAFailure(t *testing.T) {
	f := orphanFixture()
	var out bytes.Buffer

	if err := runTreeOrphans(&out, f, []string{"acme/harness"}, false); err != nil {
		t.Fatalf("finding orphans reported an error: %v", err)
	}
	if !strings.Contains(out.String(), "not a verdict") {
		t.Errorf("the output does not say it is a question rather than a verdict:\n%s", out.String())
	}
}

// The one thing that IS a failure. A repository nobody could list would
// otherwise read as "nothing unparented there" -- silence answering for the
// half it could see, which is the class of defect this command exists inside.
func TestAnUnreadableRepositoryFails(t *testing.T) {
	f := orphanFixture()
	f.errs = map[string]error{"acme/doctrine": errors.New("HTTP 404")}
	var out bytes.Buffer

	err := runTreeOrphans(&out, f, []string{"acme/harness", "acme/doctrine"}, false)
	if err == nil {
		t.Fatal("an unreadable repository was reported as clean")
	}
	if !strings.Contains(err.Error(), "acme/doctrine") {
		t.Errorf("the failure does not name the repository:\n%s", err)
	}
	// The readable half still answers: one bad repository must not blank out
	// the rest of the report.
	if !strings.Contains(out.String(), "acme/harness#12") {
		t.Errorf("the readable repository's orphans went missing:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "COULD NOT READ") {
		t.Errorf("the output does not mark the unreadable repository:\n%s", out.String())
	}
}

// !! facet#145: THE CODE, NOT JUST THE FAILURE. !! The test above proves an
// unreadable repository fails; it passed before this change and after it,
// because `err != nil` is true either way. THAT IS THE GAP THIS CLOSES -- the
// defect was never "orphans reports a bad repo as clean", it was that a caller
// could not tell that answer apart from a finding.
//
// Deliberately asserted through exitCodeFor rather than by reading the message:
// a classifier built on another tool's prose is one wording change away from
// answering the wrong thing, which is the whole argument of facet#138.
func TestAnUnreadableRepositoryIsCouldNotLookNotAFinding(t *testing.T) {
	f := orphanFixture()
	f.errs = map[string]error{"acme/doctrine": errors.New("HTTP 404")}

	err := runTreeOrphans(&bytes.Buffer{}, f, []string{"acme/harness", "acme/doctrine"}, false)
	if err == nil {
		t.Fatal("an unreadable repository was reported as clean")
	}
	if got := exitCodeFor(err); got != exitCantLook {
		t.Errorf("exit code = %d, want %d (could not look)\n  error: %v", got, exitCantLook, err)
	}
}

// THE OTHER HALF, AND IT IS THE ONE A CODE CHANGE QUIETLY BREAKS. Finding
// orphans stays exit 0 (facet#116): an unparented issue is a valid issue and
// this is a question, not a verdict. Tagging the failure path must not drag the
// success path with it.
func TestFindingOrphansIsStillExitZeroAfterTagging(t *testing.T) {
	f := orphanFixture()
	if err := runTreeOrphans(&bytes.Buffer{}, f, []string{"acme/harness"}, false); err != nil {
		t.Fatalf("finding orphans returned an error, so it can no longer be exit 0: %v", err)
	}
}

// Naming no repository read nothing, so it cannot be a finding either. This is
// the entrance tagCantLook covers, and it is a different code path from the
// read failure above.
func TestTreeOrphansWithNoRepoIsCouldNotLook(t *testing.T) {
	cmd := newTreeOrphansCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(nil)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("orphans ran with no --repo")
	}
	if got := exitCodeFor(err); got != exitCantLook {
		t.Errorf("exit code = %d, want %d\n  error: %v", got, exitCantLook, err)
	}
}

// A mistyped flag is cobra's error rather than this package's, and it defaults
// to 1 unless the command opts in. `tree doctor` spelled these out one at a
// time and `tree labels` shipped without them; this is the same omission a
// third verb over.
func TestTreeOrphansFlagErrorIsNotAFinding(t *testing.T) {
	cmd := newTreeOrphansCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--jsonn"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("a misspelled flag was accepted")
	}
	if got := exitCodeFor(err); got != exitCantLook {
		t.Errorf("exit code = %d, want %d\n  error: %v", got, exitCantLook, err)
	}
}

// The codes are in --help, as `doctor`'s and `labels`' are. A caller must be
// able to learn that this verb has no 1 without reading the source -- an
// absence is exactly what nobody thinks to ask about.
func TestTreeOrphansHelpStatesTheExitCodes(t *testing.T) {
	long := newTreeOrphansCmd().Long
	for _, want := range []string{"EXIT CODES", "could NOT look", "0  looked", "2  could", "NO 1 HERE"} {
		if !strings.Contains(long, want) {
			t.Errorf("--help is missing %q:\n%s", want, long)
		}
	}
}

// A repository with no orphans must be visible in the output. Otherwise "no
// orphans in acme/lab" and "acme/lab was never scanned" print identically.
func TestARepositoryWithNoOrphansStillAppears(t *testing.T) {
	f := orphanFixture()
	var out bytes.Buffer

	if err := runTreeOrphans(&out, f, []string{"acme/lab"}, false); err != nil {
		t.Fatalf("orphans: %v", err)
	}
	if !strings.Contains(out.String(), "acme/lab") || !strings.Contains(out.String(), "0 of 1") {
		t.Errorf("a clean repository is not accounted for:\n%s", out.String())
	}
}

func TestTreeOrphansJSON(t *testing.T) {
	f := orphanFixture()
	var out bytes.Buffer

	if err := runTreeOrphans(&out, f, []string{"acme/harness", "acme/lab"}, true); err != nil {
		t.Fatalf("orphans: %v", err)
	}
	var got orphanReport
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("--json did not emit JSON: %v\n%s", err, out.String())
	}
	if len(got.Orphans) != 2 {
		t.Fatalf("orphans = %+v, want 2", got.Orphans)
	}
	if got.Orphans[0].Ref != "acme/harness#12" || got.Orphans[0].Number != 12 {
		t.Errorf("first orphan = %+v", got.Orphans[0])
	}
	if len(got.Repos) != 2 {
		t.Fatalf("repos = %+v, want 2", got.Repos)
	}
	// The counts are the reason the per-repo rows exist: 2 orphans out of 3
	// open issues is a different fact from 2 out of 300.
	if got.Repos[0].Open != 3 || got.Repos[0].Orphans != 2 {
		t.Errorf("acme/harness row = %+v, want open 3 orphans 2", got.Repos[0])
	}
}

// An empty orphan list must serialise as [] rather than null: a consumer
// iterating the field should not have to special-case the clean answer.
func TestTreeOrphansJSONIsEmptyListNotNull(t *testing.T) {
	f := orphanFixture()
	var out bytes.Buffer

	if err := runTreeOrphans(&out, f, []string{"acme/lab"}, true); err != nil {
		t.Fatalf("orphans: %v", err)
	}
	if !strings.Contains(out.String(), `"orphans": []`) {
		t.Errorf("want an empty array for orphans:\n%s", out.String())
	}
}

// A --json run that hit an unreadable repository still emits the document, and
// the error is in it. A caller parsing stdout must not be handed nothing.
func TestTreeOrphansJSONCarriesTheReadFailure(t *testing.T) {
	f := orphanFixture()
	f.errs = map[string]error{"acme/doctrine": errors.New("HTTP 404")}
	var out bytes.Buffer

	if err := runTreeOrphans(&out, f, []string{"acme/doctrine"}, true); err == nil {
		t.Fatal("an unreadable repository was reported as clean")
	}
	var got orphanReport
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("--json emitted nothing parseable: %v\n%s", err, out.String())
	}
	if len(got.Repos) != 1 || got.Repos[0].Error == "" {
		t.Errorf("the read failure is not in the document: %+v", got.Repos)
	}
}

// Naming no repository is a refusal with the invocation in it, not a scan of
// something facet picked.
func TestTreeOrphansNeedsARepo(t *testing.T) {
	cmd := newTreeOrphansCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(nil)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("orphans ran with no --repo")
	}
	if !strings.Contains(err.Error(), "fix: facet tree orphans --repo") {
		t.Errorf("the refusal does not name the invocation:\n%s", err)
	}
}

func TestTreeOrphansReadsEachRepoOnce(t *testing.T) {
	f := orphanFixture()
	if err := runTreeOrphans(&bytes.Buffer{}, f, []string{"acme/harness", "acme/lab"}, false); err != nil {
		t.Fatalf("orphans: %v", err)
	}
	if len(f.calls) != 2 {
		t.Errorf("calls = %v, want one per repository", f.calls)
	}
}
