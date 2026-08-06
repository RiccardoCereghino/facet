package main

import (
	"bytes"
	"fmt"
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

// IssueChildren attaches each scripted child's title/state/labels from
// f.issues, the same map ViewIssue reads -- mirroring the real IssueChildren,
// which now returns those fields in the same call rather than a second one
// (facet#105). A child missing from f.issues comes back with an empty State,
// which the walker reads as "could not be read".
func (f *treeFake) IssueChildren(repo string, number int) ([]ghx.SubIssue, error) {
	k := f.key(repo, number)
	if err, ok := f.childErrs[k]; ok {
		return nil, err
	}
	refs := f.children[k]
	out := make([]ghx.SubIssue, 0, len(refs))
	for _, r := range refs {
		iss, ok := f.issues[r.OwnerRepo()+"#"+itoa(r.Number)]
		if !ok {
			out = append(out, ghx.SubIssue{Ref: r})
			continue
		}
		out = append(out, ghx.SubIssue{Ref: r, Title: iss.Title, State: iss.State, Labels: iss.LabelNames()})
	}
	return out, nil
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

// The 422 this whole issue is about: GitHub refuses a second POST outright
// when the child already has a DIFFERENT parent. wire must detach the old
// edge before attaching the new one -- and in that order, so a fake that let
// the detach happen after (or not at all) would still pass a test that only
// checked the end state.
func TestTreeWireDetachesBeforeReattaching(t *testing.T) {
	withRouting(t, "")
	f := wireFake()
	f.parents["acme/lab#75"] = iref("acme", "lab", 46) // already parented, elsewhere
	var out bytes.Buffer

	if err := runTreeWire(&out, f, iref("acme", "lab", 75), iref("acme", "doctrine", 282)); err != nil {
		t.Fatalf("wire: %v", err)
	}

	wantDetach := "acme/lab#46<-5049244556"
	if len(f.removeSubIssueCalls) != 1 || f.removeSubIssueCalls[0] != wantDetach {
		t.Fatalf("removeSubIssueCalls = %v, want [%s]", f.removeSubIssueCalls, wantDetach)
	}
	wantAttach := "acme/doctrine#282<-5049244556"
	if len(f.addSubIssueCalls) != 1 || f.addSubIssueCalls[0] != wantAttach {
		t.Fatalf("addSubIssueCalls = %v, want [%s]", f.addSubIssueCalls, wantAttach)
	}

	got := out.String()
	if !strings.Contains(got, "detached") || !strings.Contains(got, "acme/lab#46") {
		t.Errorf("output does not report the detach:\n%s", got)
	}
	if !strings.Contains(got, "MOVED") || !strings.Contains(got, "acme/lab#46") {
		t.Errorf("output does not report the move:\n%s", got)
	}
	// The detach must be printed before the attach happens, per the issue's
	// own mitigation: a failure between the two steps must be recoverable
	// from what was already on stdout.
	if i, j := strings.Index(got, "detached"), strings.Index(got, "wired"); i < 0 || j < 0 || i > j {
		t.Errorf("detach was not reported before the attach:\n%s", got)
	}
}

// A failed detach must not be followed by an attach attempt -- that would
// leave the child parented to neither the old nor the new parent with no
// record of why, exactly the silent-orphan failure mode the issue warns
// against.
func TestTreeWireStopsIfTheDetachFails(t *testing.T) {
	withRouting(t, "")
	f := wireFake()
	f.parents["acme/lab#75"] = iref("acme", "lab", 46)
	f.removeSubIssueErr = fmt.Errorf("boom")
	var out bytes.Buffer

	err := runTreeWire(&out, f, iref("acme", "lab", 75), iref("acme", "doctrine", 282))
	if err == nil {
		t.Fatal("wire did not report the detach failure")
	}
	if !strings.Contains(err.Error(), "detach") {
		t.Errorf("error = %q, want it to name the detach", err)
	}
	if len(f.addSubIssueCalls) != 0 {
		t.Error("wire attached the new parent despite the failed detach")
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

	if err := runTreeDoctor(&out, f, iref("acme", "lab", 46), false); err != nil {
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

// A structure whose skippable rung is CONSTRAINED, so it can actually be
// skipped. Under the committed lab structure `block` has no `accepts` and
// therefore never skips, which is why the divergence below was latent rather
// than live -- and why constraining that rung is the natural later change that
// would have armed it.
const skippableStructure = `,
	"structure": {"levels": [
		{"name": "commission"},
		{"name": "seat", "accepts": [{"repo": "doctrine", "titlePattern": "^seat: "}]},
		{"name": "block", "optional": true, "accepts": [{"titlePattern": "^block: "}]},
		{"name": "issue"}
	]}`

// !! wire MUST judge an edge by the parent's assigned LEVEL, never by its
// ancestor count. !! The two are equal only while no optional rung is ever
// skipped above the edge. Skip one and a depth-based check lets `wire` write
// the exact edge `doctor` reports as a defect -- in the command whose stated
// purpose is making the wrong shape unrepresentable.
//
// Here the block rung is skipped, so the work sits at LEVEL 3 (issue) with a
// DEPTH of 2. Nothing may hang below the deepest level, so the edge must be
// refused; judging by depth would consult level 2 instead and permit it.
func TestTreeWireJudgesByLevelNotDepth(t *testing.T) {
	withRouting(t, skippableStructure)
	f := &treeFake{issues: map[string]*ghx.Issue{
		"acme/lab#46":       issueWith("commission 1"),
		"acme/doctrine#282": issueWith("seat: a record"),
		"acme/harness#121":  issueWith("the work, hung straight off the record"),
		"acme/harness#122":  issueWith("something below the work"),
	}}
	f.parents = map[string]ghx.IssueRef{
		"acme/doctrine#282": iref("acme", "lab", 46),
		"acme/harness#121":  iref("acme", "doctrine", 282),
	}
	f.issueIDs = map[string]int64{"acme/harness#122": 5049244999}

	var out bytes.Buffer
	err := runTreeWire(&out, f, iref("acme", "harness", 122), iref("acme", "harness", 121))
	if err == nil {
		t.Fatal("wire accepted an edge below the deepest level; it judged by depth, not by level")
	}
	if !strings.Contains(err.Error(), "deepest declared level") {
		t.Errorf("refusal = %v, want it to name the deepest level", err)
	}
	if len(f.addSubIssueCalls) != 0 {
		t.Errorf("the edge was written despite the refusal: %v", f.addSubIssueCalls)
	}
}

// An ancestor sitting at no declared level is not this edge's fault, and must
// not be reported as if it were -- nothing below a misplaced node can be judged.
func TestTreeWireRefusesWhenAnAncestorIsItselfMisplaced(t *testing.T) {
	withRouting(t, skippableStructure)
	f := &treeFake{issues: map[string]*ghx.Issue{
		"acme/lab#46":      issueWith("commission 1"),
		"acme/lab#72":      issueWith("not a seat record at all"),
		"acme/harness#121": issueWith("work under a misplaced node"),
	}}
	f.parents = map[string]ghx.IssueRef{"acme/lab#72": iref("acme", "lab", 46)}
	f.issueIDs = map[string]int64{"acme/harness#121": 5049244998}

	var out bytes.Buffer
	err := runTreeWire(&out, f, iref("acme", "harness", 121), iref("acme", "lab", 72))
	if err == nil {
		t.Fatal("wire judged an edge under an ancestor that sits at no declared level")
	}
	if !strings.Contains(err.Error(), "does not itself sit at any level") {
		t.Errorf("refusal = %v, want it to blame the ancestor rather than this edge", err)
	}
	if len(f.addSubIssueCalls) != 0 {
		t.Error("the edge was written despite the refusal")
	}
}

// These commands read GitHub, not the lab. A missing routing file means "no
// levels and no comment kinds declared", which is a legitimate state -- not a
// reason to refuse to render a tree that facet never built.
func TestTreeAndCommentWorkWithNoRoutingFileAtAll(t *testing.T) {
	prev := roots
	roots = config.Roots{Routing: filepath.Join(t.TempDir(), "absent.json")}
	t.Cleanup(func() { roots = prev })

	f := wireFake()
	f.children = map[string][]ghx.IssueRef{"acme/lab#46": {iref("acme", "doctrine", 282)}}
	f.comments = map[string][]ghx.Comment{"acme/lab#46": {{ID: 1, Body: "## Plan\nx"}}}

	var out bytes.Buffer
	if err := runTreeShow(&out, f, iref("acme", "lab", 46), -1); err != nil {
		t.Errorf("tree show refused without a routing file: %v", err)
	}
	if err := runTreeDoctor(&bytes.Buffer{}, f, iref("acme", "lab", 46), false); err != nil {
		t.Errorf("tree doctor refused without a routing file: %v", err)
	}
	// --grep needs no configuration, so it must work here too.
	if err := runCommentList(&bytes.Buffer{}, f, iref("acme", "lab", 46), "", "Plan", true); err != nil {
		t.Errorf("comment --grep refused without a routing file: %v", err)
	}
	// --kind still refuses, because there genuinely are no kinds declared.
	if err := runCommentList(&bytes.Buffer{}, f, iref("acme", "lab", 46), "plan", "", true); err == nil {
		t.Error("--kind was accepted with no kinds declared anywhere")
	}
}

// The refusal must not teach the pattern this branch measured as wrong: an
// unanchored form returns a heading that merely CONTAINS the word.
func TestCommentKindRefusalDoesNotRecommendAnUnanchoredPattern(t *testing.T) {
	withRouting(t, "")
	err := runCommentList(&bytes.Buffer{}, commentFake(), iref("acme", "lab", 75), "plan", "", true)
	if err == nil {
		t.Fatal("--kind was accepted with no commentKinds block")
	}
	msg := err.Error()
	// What is OFFERED after "e.g." must be the anchored form. The unanchored
	// one may still appear -- naming it as the thing to avoid is the useful
	// half -- so this asserts what is recommended, not what is mentioned.
	offered, _, _ := strings.Cut(strings.SplitN(msg, "e.g. ", 2)[1], "\n")
	if !strings.Contains(offered, "(?mi)") || !strings.Contains(offered, "#{1,6}") {
		t.Errorf("the recommended pattern is not anchored and case-insensitive: %q", offered)
	}
	if strings.Contains(offered, ".*") {
		t.Errorf("the recommended pattern is unanchored: %q", offered)
	}
	if !strings.Contains(msg, "anchor") {
		t.Errorf("the fix line does not warn about anchoring:\n%s", msg)
	}
}
