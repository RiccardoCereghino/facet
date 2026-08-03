package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/RiccardoCereghino/facet/internal/ghx"
)

func writeFile(path, contents string) error {
	return os.WriteFile(path, []byte(contents), 0o644)
}

func depsFake(body string) *treeFake {
	f := &treeFake{issues: map[string]*ghx.Issue{
		"acme/lab#75":      {Title: "the work", State: "OPEN", Body: body},
		"acme/lab#48":      {Title: "a blocker, still open", State: "OPEN"},
		"acme/harness#121": {Title: "a blocker that landed", State: "CLOSED"},
	}}
	f.blockedBy = map[string][]ghx.IssueRef{}
	f.blocking = map[string][]ghx.IssueRef{}
	return f
}

// THE DEFECT. `facet file` creates these edges once at filing and every
// failure there is a warning rather than a refusal -- so a blocker declared in
// the body and never wired exists only as prose, and nothing schedules from it.
func TestDepsCheckReportsADeclaredBlockerThatWasNeverWired(t *testing.T) {
	withRouting(t, "")
	f := depsFake("### Blocked by / waiting on\n\nacme/lab#48 — the scaffold\n")
	var out bytes.Buffer

	err := runDepsCheck(&out, f, iref("acme", "lab", 75))
	if err == nil {
		t.Fatal("a declared-but-unwired blocker was not reported")
	}
	got := out.String()
	if !strings.Contains(got, "DECLARED BUT NOT WIRED") || !strings.Contains(got, "acme/lab#48") {
		t.Errorf("output does not name the missing edge:\n%s", got)
	}
	if !strings.Contains(got, "fix:") {
		t.Errorf("no fix line:\n%s", got)
	}
}

// The other direction is NOT a defect. Measured across a real repository's open
// issues: several carried native edges their bodies never mentioned, and none
// declared a blocker that was unwired. After filing, the edge is the truth and
// the body simply ages -- "fixing" that by editing bodies would be churn.
func TestDepsCheckTreatsAWiredButUndeclaredBlockerAsNormal(t *testing.T) {
	withRouting(t, "")
	f := depsFake("no blocked-by section at all\n")
	f.blockedBy["acme/lab#75"] = []ghx.IssueRef{iref("acme", "harness", 121)}
	var out bytes.Buffer

	if err := runDepsCheck(&out, f, iref("acme", "lab", 75)); err != nil {
		t.Fatalf("a wired-but-undeclared blocker was reported as a defect: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "normal") {
		t.Errorf("output does not say this is expected:\n%s", got)
	}
	// "Every declared blocker is wired" is vacuously true here and would read
	// as a check having passed rather than as one having had nothing to do.
	if strings.Contains(got, "every declared blocker is wired") {
		t.Errorf("a vacuous pass was reported for an issue declaring none:\n%s", got)
	}
	if !strings.Contains(got, "nothing to check") {
		t.Errorf("output does not distinguish an empty check from a passing one:\n%s", got)
	}
}

func TestDepsCheckPassesWhenTheyAgree(t *testing.T) {
	withRouting(t, "")
	f := depsFake("### Blocked by / waiting on\n\nacme/lab#48\n")
	f.blockedBy["acme/lab#75"] = []ghx.IssueRef{iref("acme", "lab", 48)}
	var out bytes.Buffer

	if err := runDepsCheck(&out, f, iref("acme", "lab", 75)); err != nil {
		t.Fatalf("check: %v", err)
	}
	if !strings.Contains(out.String(), "every declared blocker is wired") {
		t.Errorf("output = %q", out.String())
	}
}

// !! The check must use the SAME parser that files the edges. !! A hand-rolled
// pattern reported false blockers by reading the number in a repo-shorthand
// reference as a bare one and defaulting it to the current repository. A check
// that disagrees with the filer reports drift that is its own.
//
// The consequence, pinned here because it is a live limitation rather than a
// preference: shorthand like "harness#121" is NOT read as a reference, so a
// dependency written that way is invisible to the wiring. Write blockers as
// owner/repo#n.
func TestDepsCheckUsesTheFilersParserAndSoIgnoresRepoShorthand(t *testing.T) {
	withRouting(t, "")
	f := depsFake("### Blocked by / waiting on\n\nharness#121 has landed already\n")
	var out bytes.Buffer

	if err := runDepsCheck(&out, f, iref("acme", "lab", 75)); err != nil {
		t.Fatalf("shorthand was read as a reference and reported missing: %v", err)
	}
	if !strings.Contains(out.String(), "0 declared") {
		t.Errorf("shorthand was counted as a declared blocker:\n%s", out.String())
	}
}

// A bare #n is same-repo, and must be resolved against the issue's own
// repository before being compared with a wired edge -- otherwise every
// same-repo declaration reads as missing.
func TestDepsCheckResolvesABareNumberAgainstTheIssuesOwnRepo(t *testing.T) {
	withRouting(t, "")
	f := depsFake("### Blocked by / waiting on\n\n#48\n")
	f.blockedBy["acme/lab#75"] = []ghx.IssueRef{iref("acme", "lab", 48)}
	var out bytes.Buffer

	if err := runDepsCheck(&out, f, iref("acme", "lab", 75)); err != nil {
		t.Fatalf("a bare #n was not matched against the same repo: %v", err)
	}
}

// ready answers what could be picked up, not what exists.
func TestDepsReadySeparatesReadyFromBlocked(t *testing.T) {
	withRouting(t, "")
	f := depsFake("")
	f.issues["acme/lab#46"] = &ghx.Issue{Title: "the root", State: "OPEN"}
	f.issues["acme/lab#90"] = &ghx.Issue{Title: "blocked work", State: "OPEN"}
	f.issues["acme/lab#91"] = &ghx.Issue{Title: "free work", State: "OPEN"}
	f.issues["acme/lab#92"] = &ghx.Issue{Title: "already done", State: "CLOSED"}
	f.children = map[string][]ghx.IssueRef{
		"acme/lab#46": {iref("acme", "lab", 90), iref("acme", "lab", 91), iref("acme", "lab", 92)},
	}
	// #90 waits on something still open; #91 waits on something closed.
	f.blockedBy["acme/lab#90"] = []ghx.IssueRef{iref("acme", "lab", 48)}
	f.blockedBy["acme/lab#91"] = []ghx.IssueRef{iref("acme", "harness", 121)}

	var out bytes.Buffer
	if err := runDepsReady(&out, f, iref("acme", "lab", 46)); err != nil {
		t.Fatalf("ready: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "acme/lab#91") {
		t.Errorf("the unblocked issue was not listed:\n%s", got)
	}
	if strings.Contains(got, "acme/lab#90\t") {
		t.Errorf("a blocked issue was listed as ready:\n%s", got)
	}
	// A closed issue is neither ready nor blocked; it is done.
	if strings.Contains(got, "acme/lab#92") {
		t.Errorf("a closed issue was listed:\n%s", got)
	}
	if !strings.Contains(got, "1 ready, 1 still blocked") {
		t.Errorf("counts = %q", got)
	}
}

func TestDepsShowSaysWhenThereAreNoEdges(t *testing.T) {
	withRouting(t, "")
	var out bytes.Buffer
	if err := runDepsShow(&out, depsFake(""), iref("acme", "lab", 75)); err != nil {
		t.Fatalf("show: %v", err)
	}
	if !strings.Contains(out.String(), "no dependency edges") {
		t.Errorf("output = %q", out.String())
	}
}
