package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RiccardoCereghino/facet/internal/config"
	"github.com/RiccardoCereghino/facet/internal/ghx"
)

// withRouting installs a routing file, optionally carrying a structure block.
// The two cases are the whole point of this file: the same tree must be
// judged with levels and not judged at all without them.
func withRouting(t *testing.T, structure string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "routing.json")
	contents := `{
		"version": 1,
		"repos": {
			"lab": {"dir": "lab", "url": "https://example.invalid/lab.git"},
			"doctrine": {"dir": "doctrine", "url": "https://example.invalid/doctrine.git"},
			"harness": {"dir": "harness", "url": "https://example.invalid/harness.git"}
		},
		"ownerRepoToKey": {
			"acme/lab": "lab", "acme/doctrine": "doctrine", "acme/harness": "harness"
		},
		"aliases": {}, "areaMap": {}, "knowledgeByArea": {}, "pathHints": {}` + structure + `
	}`
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := roots
	roots = config.Roots{Routing: path}
	t.Cleanup(func() { roots = prev })
}

const fourLevelStructure = `,
	"structure": {"levels": [
		{"name": "commission"},
		{"name": "seat", "requiresChildren": true,
		 "accepts": [{"repo": "doctrine", "titlePattern": "^seat: "}]},
		{"name": "block", "optional": true},
		{"name": "issue"}
	]}`

func iref(owner, repo string, n int) ghx.IssueRef {
	return ghx.IssueRef{Owner: owner, Repo: repo, Number: n}
}

// treeFake scripts what runTreeWire needs. It embeds fakeGH so only the
// methods that matter here are spelled out.
type treeFake struct {
	fakeGH
	issues map[string]*ghx.Issue
}

func (f *treeFake) ViewIssue(repo string, number int) (*ghx.Issue, error) {
	return f.issues[repo+"#"+itoa(number)], nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func issueWith(title string, labels ...string) *ghx.Issue {
	ls := make([]ghx.Label, 0, len(labels))
	for _, l := range labels {
		ls = append(ls, ghx.Label{Name: l})
	}
	return &ghx.Issue{Title: title, State: "OPEN", Labels: ls}
}

// wireFake sets up a commission holding one seat record, ready to be wired to.
func wireFake() *treeFake {
	f := &treeFake{issues: map[string]*ghx.Issue{
		"acme/lab#46":       issueWith("commission 1", "complexity/3"),
		"acme/doctrine#282": issueWith("seat: c1-structure", "complexity/3"),
		"acme/lab#75":       issueWith("the commands and the skill", "complexity/3"),
		"acme/harness#121":  issueWith("the work", "complexity/1"),
	}}
	f.parents = map[string]ghx.IssueRef{
		"acme/doctrine#282": iref("acme", "lab", 46),
	}
	f.issueIDs = map[string]int64{
		"acme/lab#75":       5049244556,
		"acme/harness#121":  5049244557,
		"acme/doctrine#282": 5049244558,
	}
	return f
}

// !! THE CONSTRAINT. !! With no structure declared, facet must wire whatever
// it is told to. The shape is an adopter's contract and facet holds none of
// its own -- so the very edge that is a defect in one organisation is simply
// an edge in another.
func TestTreeWireImposesNoShapeWithoutAStructureBlock(t *testing.T) {
	withRouting(t, "")
	f := wireFake()
	var out bytes.Buffer

	// A bundle straight under the commission: the exact shape the four-level
	// structure forbids.
	if err := runTreeWire(&out, f, iref("acme", "lab", 75), iref("acme", "lab", 46)); err != nil {
		t.Fatalf("wire refused with no structure configured: %v", err)
	}
	if len(f.addSubIssueCalls) != 1 {
		t.Fatalf("edge not written: %v", f.addSubIssueCalls)
	}
}

// THE DEFECT. With the levels declared, the same edge is refused -- and the
// refusal names what a child of that parent must be, not merely that this one
// is wrong.
func TestTreeWireRefusesABlockDirectlyUnderTheCommission(t *testing.T) {
	withRouting(t, fourLevelStructure)
	f := wireFake()
	var out bytes.Buffer

	err := runTreeWire(&out, f, iref("acme", "lab", 75), iref("acme", "lab", 46))
	if err == nil {
		t.Fatal("wire accepted a block directly under the commission")
	}
	for _, want := range []string{"cannot sit under", "seat", "doctrine", "fix:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal is missing %q:\n%s", want, err)
		}
	}
	if len(f.addSubIssueCalls) != 0 {
		t.Error("the edge was written despite the refusal")
	}
}

func TestTreeWireAcceptsTheCorrectShape(t *testing.T) {
	withRouting(t, fourLevelStructure)
	f := wireFake()
	var out bytes.Buffer

	// A block under a seat record, which is where a block belongs.
	if err := runTreeWire(&out, f, iref("acme", "lab", 75), iref("acme", "doctrine", 282)); err != nil {
		t.Fatalf("wire refused a well-formed edge: %v", err)
	}
	want := "acme/doctrine#282<-5049244556"
	if len(f.addSubIssueCalls) != 1 || f.addSubIssueCalls[0] != want {
		t.Errorf("calls = %v, want [%s]", f.addSubIssueCalls, want)
	}
}

// Every wire states both tiers and which one governs. An edge that silently
// moved merge authority would look exactly like filing, so the rule is printed
// at the moment the edge is made rather than left to be re-derived.
func TestTreeWirePrintsBothTiersAndWhoGoverns(t *testing.T) {
	withRouting(t, fourLevelStructure)
	f := wireFake()
	var out bytes.Buffer

	if err := runTreeWire(&out, f, iref("acme", "harness", 121), iref("acme", "doctrine", 282)); err != nil {
		t.Fatalf("wire: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"child", "acme/harness#121", "c1", // the child's own tier
		"parent", "acme/doctrine#282", "c3", // the grouping's worst case
		"worst case",
		"merge authority is the CHILD's own tier",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output is missing %q:\n%s", want, got)
		}
	}
	// It must NOT claim the wiring changed anything about merging.
	if strings.Contains(got, "merges at c3") {
		t.Errorf("output implies the parent's tier governs the child:\n%s", got)
	}
}

// An issue has exactly one parent, so wiring one that has a parent MOVES it --
// and GitHub's response is identical either way. If the previous parent is not
// read before the write, nobody ever learns what the edge replaced.
func TestTreeWireReportsAMove(t *testing.T) {
	withRouting(t, "")
	f := wireFake()
	f.parents["acme/lab#75"] = iref("acme", "lab", 46)
	var out bytes.Buffer

	if err := runTreeWire(&out, f, iref("acme", "lab", 75), iref("acme", "doctrine", 282)); err != nil {
		t.Fatalf("wire: %v", err)
	}
	if !strings.Contains(out.String(), "MOVED") || !strings.Contains(out.String(), "acme/lab#46") {
		t.Errorf("a move was not reported:\n%s", out.String())
	}
}

func TestTreeWireIsIdempotent(t *testing.T) {
	withRouting(t, "")
	f := wireFake()
	f.parents["acme/lab#75"] = iref("acme", "doctrine", 282)
	var out bytes.Buffer

	if err := runTreeWire(&out, f, iref("acme", "lab", 75), iref("acme", "doctrine", 282)); err != nil {
		t.Fatalf("wire: %v", err)
	}
	if len(f.addSubIssueCalls) != 0 {
		t.Error("re-wiring an existing edge wrote it again")
	}
	if !strings.Contains(out.String(), "nothing to do") {
		t.Errorf("output = %q, want it to say the edge already exists", out.String())
	}
}

// Refuse when you cannot tell. An unreadable parent means the depth is
// unknown, and skipping the check there is exactly how a wrong edge gets
// written by the tool built to prevent it.
func TestTreeWireRefusesWhenTheParentDepthCannotBeRead(t *testing.T) {
	withRouting(t, fourLevelStructure)
	f := wireFake()
	f.parentErrs = map[string]error{"acme/doctrine#282": errUnreadable{}}
	var out bytes.Buffer

	err := runTreeWire(&out, f, iref("acme", "lab", 75), iref("acme", "doctrine", 282))
	if err == nil {
		t.Fatal("wire proceeded without being able to establish the parent's depth")
	}
	if !strings.Contains(err.Error(), "could not establish") {
		t.Errorf("refusal = %v, want it to name what could not be determined", err)
	}
	if len(f.addSubIssueCalls) != 0 {
		t.Error("the edge was written despite an unreadable parent")
	}
}

type errUnreadable struct{}

func (errUnreadable) Error() string { return "403" }

func TestTreeWireRefusesSelfParenting(t *testing.T) {
	withRouting(t, "")
	var out bytes.Buffer
	err := runTreeWire(&out, wireFake(), iref("acme", "lab", 75), iref("acme", "lab", 75))
	if err == nil || !strings.Contains(err.Error(), "its own parent") {
		t.Errorf("err = %v, want a refusal", err)
	}
}

// Silence about the shape must not read as a clean bill of health for it.
func TestTreeDoctorSaysWhatItDidNotCheck(t *testing.T) {
	withRouting(t, "")
	f := wireFake()
	f.children = map[string][]ghx.IssueRef{}
	var out bytes.Buffer

	if err := runTreeDoctor(&out, f, iref("acme", "lab", 46)); err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if !strings.Contains(out.String(), "shape was not checked") {
		t.Errorf("output = %q, want it to say the shape went unchecked", out.String())
	}
}

// Refusing beats degrading: an open/closed count looks like a full answer, and
// "0 in progress" would read as measured rather than as unavailable.
func TestTreeStatusRefusesWithoutABoard(t *testing.T) {
	withRouting(t, "")
	f := wireFake()
	f.children = map[string][]ghx.IssueRef{
		"acme/lab#46": {iref("acme", "doctrine", 282)},
	}
	var out bytes.Buffer

	err := runTreeStatus(&out, f, iref("acme", "lab", 46))
	if err == nil {
		t.Fatal("status answered with no board configured")
	}
	for _, want := range []string{"project", "statusField", "fix:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal is missing %q:\n%s", want, err)
		}
	}
}

func TestParseIssueRef(t *testing.T) {
	got, err := parseIssueRef("acme/lab#46")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != iref("acme", "lab", 46) {
		t.Errorf("ref = %v", got)
	}
	// The refusal must talk about an issue reference, not about a scope file,
	// which is what the reused validator's own wording would have said.
	for _, bad := range []string{"46", "lab#46", "acme/lab", "acme/lab#0"} {
		err := func() error { _, e := parseIssueRef(bad); return e }()
		if err == nil {
			t.Errorf("parseIssueRef(%q) was accepted", bad)
			continue
		}
		if strings.Contains(err.Error(), "scope") {
			t.Errorf("refusal for %q mentions a scope file: %v", bad, err)
		}
	}
}
