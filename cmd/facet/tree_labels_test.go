package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RiccardoCereghino/facet/internal/ghx"
	"github.com/RiccardoCereghino/facet/internal/routing"
)

// labelFake scripts what each repository DEFINES.
type labelFake struct {
	defined map[string][]ghx.RepoLabel
	errs    map[string]error
	created []string
	failOn  map[string]error // "repo: label" -> the create fails
}

func (f *labelFake) RepoLabels(repo string) ([]ghx.RepoLabel, error) {
	if err, ok := f.errs[repo]; ok {
		return nil, err
	}
	return f.defined[repo], nil
}

func (f *labelFake) CreateLabel(repo string, l ghx.RepoLabel) error {
	key := repo + ": " + l.Name
	if err, ok := f.failOn[key]; ok {
		return err
	}
	f.created = append(f.created, key+" #"+l.Color)
	f.defined[repo] = append(f.defined[repo], l)
	return nil
}

func defines(names ...string) []ghx.RepoLabel {
	out := make([]ghx.RepoLabel, 0, len(names))
	for _, n := range names {
		out = append(out, ghx.RepoLabel{Name: n, Color: "b60205"})
	}
	return out
}

// labelRouting is the structure with three declared labels, plus two other
// labels the repositories carry that are none of facet's business.
func labelRouting(t *testing.T) *routing.Routing {
	t.Helper()
	withRouting(t, labelledFourLevelStructure)
	route, err := loadRouting()
	if err != nil {
		t.Fatal(err)
	}
	return route
}

// The estate view, which is the whole point: eleven repositories with four
// different answers looked fine one at a time.
func TestTreeLabelsReportsEveryRepositoryIncludingTheGoodOnes(t *testing.T) {
	route := labelRouting(t)
	f := &labelFake{defined: map[string][]ghx.RepoLabel{
		"acme/lab":      defines("type/commission", "type/seat", "type/block", "type/work", "bug"),
		"acme/doctrine": defines("type/commission", "type/seat", "type/block", "type/work"),
		"acme/harness":  defines("type/commission", "type/work"), // the untouched one
	}}
	var out bytes.Buffer

	err := runTreeLabels(&out, f, route, []string{"acme/lab", "acme/doctrine", "acme/harness"}, false)
	if err == nil {
		t.Fatal("a repository missing two declared labels was reported as clean")
	}
	got := out.String()
	for _, want := range []string{"acme/lab", "acme/doctrine", "acme/harness", "MISSING", "type/seat", "type/block"} {
		if !strings.Contains(got, want) {
			t.Errorf("output is missing %q:\n%s", want, got)
		}
	}
	// A repository with nothing wrong must still have a line: the gap was
	// invisible because the tooling only spoke about the repo in front of it.
	if strings.Count(got, "ok") < 2 {
		t.Errorf("the clean repositories are not accounted for:\n%s", got)
	}
	if !strings.Contains(err.Error(), "fix: facet tree labels --create") {
		t.Errorf("the failure does not name the remedy:\n%s", err)
	}
}

func TestTreeLabelsPassesWhenEveryRepositoryHasThem(t *testing.T) {
	route := labelRouting(t)
	all := defines("type/commission", "type/seat", "type/block", "type/work", "type/maquette")
	f := &labelFake{defined: map[string][]ghx.RepoLabel{"acme/lab": all, "acme/doctrine": all}}
	var out bytes.Buffer

	if err := runTreeLabels(&out, f, route, []string{"acme/lab", "acme/doctrine"}, false); err != nil {
		t.Fatalf("a repository set with full parity failed: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "every repository defines every label it can be asked for") {
		t.Errorf("output does not state the pass:\n%s", out.String())
	}
}

// --create closes the gap, and copies the definition from a repository that
// already has it. Parity means the same label, not merely the same name in a
// different colour everywhere.
func TestCreateCopiesTheDefinitionThatAlreadyExists(t *testing.T) {
	route := labelRouting(t)
	f := &labelFake{defined: map[string][]ghx.RepoLabel{
		"acme/lab": {
			{Name: "type/commission", Color: "0e8a16", Description: "a commission"},
			{Name: "type/seat", Color: "1d76db"},
			{Name: "type/block", Color: "5319e7"},
			{Name: "type/work", Color: "fbca04"},
		},
		"acme/harness": {{Name: "type/work", Color: "fbca04"}},
	}}
	var out bytes.Buffer

	if err := runTreeLabels(&out, f, route, []string{"acme/lab", "acme/harness"}, true); err != nil {
		t.Fatalf("--create left a gap: %v\n%s", err, out.String())
	}
	want := "acme/harness: type/commission #0e8a16"
	found := false
	for _, c := range f.created {
		if c == want {
			found = true
		}
	}
	if !found {
		t.Errorf("created = %v, want it to copy %q", f.created, want)
	}
}

// !! THE SAFETY REQUIREMENT. !! Only a label the routing file's structure
// declares can ever be created -- so a typo cannot bring a label into
// existence, which is the risk the issue named against create-on-demand.
func TestCreateOnlyEverMakesDeclaredLabels(t *testing.T) {
	route := labelRouting(t)
	f := &labelFake{defined: map[string][]ghx.RepoLabel{"acme/harness": nil}}
	var out bytes.Buffer

	if err := runTreeLabels(&out, f, route, []string{"acme/harness"}, true); err != nil {
		t.Fatalf("--create failed: %v\n%s", err, out.String())
	}
	// LabelsFor, not Labels: type/seat is declared under a doctrine-scoped
	// shape, so acme/harness must neither be asked for it nor given it.
	declared := map[string]bool{}
	for _, l := range route.Structure.LabelsFor(route.KeyForRepo("acme/harness")) {
		declared[l] = true
	}
	if declared["type/seat"] {
		t.Fatal("type/seat is doctrine-scoped; acme/harness must not require it")
	}
	for _, c := range f.created {
		name := strings.TrimSuffix(strings.SplitN(c, ": ", 2)[1], " #"+defaultLabelColour)
		if !declared[name] {
			t.Errorf("created %q, which the structure does not declare", name)
		}
	}
	if len(f.created) != len(declared) {
		t.Errorf("created %v, want one per declared label (%d)", f.created, len(declared))
	}
}

// A label no routed repository has yet gets the neutral default rather than a
// colour picked at random, so two runs cannot disagree.
func TestCreateFallsBackToADeterministicColour(t *testing.T) {
	route := labelRouting(t)
	f := &labelFake{defined: map[string][]ghx.RepoLabel{"acme/harness": nil}}

	if err := runTreeLabels(&bytes.Buffer{}, f, route, []string{"acme/harness"}, true); err != nil {
		t.Fatalf("--create failed: %v", err)
	}
	for _, c := range f.created {
		if !strings.HasSuffix(c, "#"+defaultLabelColour) {
			t.Errorf("created %q, want the default colour when nothing exists to copy", c)
		}
	}
}

// A create that fails leaves the gap reported rather than silently closed.
func TestACreateThatFailsStillReportsTheGap(t *testing.T) {
	route := labelRouting(t)
	f := &labelFake{
		defined: map[string][]ghx.RepoLabel{"acme/harness": nil},
		failOn:  map[string]error{"acme/harness: type/commission": errors.New("HTTP 403")},
	}
	var out bytes.Buffer

	err := runTreeLabels(&out, f, route, []string{"acme/harness"}, true)
	if err == nil {
		t.Fatal("a failed create was reported as success")
	}
	if !strings.Contains(out.String(), "COULD NOT CHECK") && !strings.Contains(err.Error(), "acme/harness") {
		t.Errorf("the failure does not name the repository:\nout: %s\nerr: %s", out.String(), err)
	}
}

// An unreadable repository is neither reported clean nor allowed to hide the
// others: same rule the whole of facet#139 turns on.
func TestAnUnreadableRepositoryIsNotClean(t *testing.T) {
	route := labelRouting(t)
	f := &labelFake{
		defined: map[string][]ghx.RepoLabel{"acme/lab": defines("type/commission", "type/seat", "type/block", "type/work", "type/maquette")},
		errs:    map[string]error{"acme/harness": errors.New("HTTP 404")},
	}
	var out bytes.Buffer

	err := runTreeLabels(&out, f, route, []string{"acme/lab", "acme/harness"}, false)
	if err == nil {
		t.Fatal("an unreadable repository was reported as full parity")
	}
	if !strings.Contains(out.String(), "COULD NOT CHECK") {
		t.Errorf("the report does not mark it:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "acme/lab") {
		t.Errorf("the readable repository went missing from the report:\n%s", out.String())
	}
}

// With no structure declared there is no required set, and inventing one would
// be facet holding an opinion about a hierarchy it deliberately has none of.
func TestTreeLabelsRefusesWithoutAStructure(t *testing.T) {
	withRouting(t, "")
	route, err := loadRouting()
	if err != nil {
		t.Fatal(err)
	}
	f := &labelFake{defined: map[string][]ghx.RepoLabel{}}

	err = runTreeLabels(&bytes.Buffer{}, f, route, []string{"acme/lab"}, false)
	if err == nil {
		t.Fatal("a required label set was asserted with no structure declared")
	}
	if !strings.Contains(err.Error(), "structure") {
		t.Errorf("the refusal does not say what is missing:\n%s", err)
	}
}

// The default sweep is every routed repository, because the ones that are
// short are the ones nobody looks at.
func TestTheDefaultSweepIsEveryRoutedRepository(t *testing.T) {
	route := labelRouting(t)
	got := routedRepos(route)
	want := []string{"acme/doctrine", "acme/harness", "acme/lab"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("routedRepos = %v, want %v (sorted, all of them)", got, want)
	}
}

// ---- the other half of facet#139: `wire` itself ----

// THE MEASURED FAILURE, verbatim from the issue:
//
//	WARNING: the edge is wired but type/work was not applied to …:
//	  'type/work' not found
//	wired … under …
//
// The edge lands, the level does not, and the command SUCCEEDS. Now it
// creates the label and carries on.
func TestWireCreatesADeclaredLabelTheRepositoryLacks(t *testing.T) {
	withRouting(t, labelledFourLevelStructure)
	f := wireFake()
	// The repository defines nothing, and AddLabel fails the way gh does.
	f.repoLabels = map[string][]ghx.RepoLabel{"acme/harness": nil}
	f.labelNotFound = true
	var out bytes.Buffer

	// lab#75 is a block under the seat, so harness#121 wired below it is work.
	// The edge THIS call writes is reflected by the fake, exactly as the live
	// parent read reflects it -- recordLevel runs after the write.
	f.parents["acme/lab#75"] = iref("acme", "doctrine", 282)
	err := runTreeWire(&out, f, iref("acme", "harness", 121), iref("acme", "lab", 75))
	if err != nil {
		t.Fatalf("wire failed after creating the label: %v\n%s", err, out.String())
	}
	if len(f.createdLabels) != 1 || !strings.Contains(f.createdLabels[0], "type/work") {
		t.Fatalf("createdLabels = %v, want type/work created in acme/harness", f.createdLabels)
	}
	if !strings.Contains(out.String(), "created the label type/work") {
		t.Errorf("the creation was not announced:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "level recorded: type/work") {
		t.Errorf("the level was not recorded after the retry:\n%s", out.String())
	}
}

// !! THE WARNING STOPS BEING SURVIVABLE. !! When the level still cannot be
// recorded, the command FAILS -- and the edge line still prints, and the error
// says the edge is wired, which is what the old code's objection was about.
func TestWireFailsWhenTheLevelCannotBeRecordedAtAll(t *testing.T) {
	withRouting(t, labelledFourLevelStructure)
	f := wireFake()
	f.repoLabels = map[string][]ghx.RepoLabel{"acme/harness": nil}
	f.labelNotFound = true
	f.createLabelErr = errors.New("HTTP 403: Resource not accessible by integration")
	var out bytes.Buffer

	// lab#75 is a block under the seat, so harness#121 wired below it is work.
	// The edge THIS call writes is reflected by the fake, exactly as the live
	// parent read reflects it -- recordLevel runs after the write.
	f.parents["acme/lab#75"] = iref("acme", "doctrine", 282)
	err := runTreeWire(&out, f, iref("acme", "harness", 121), iref("acme", "lab", 75))
	if err == nil {
		t.Fatal("the command succeeded with the tree holding a node whose level is unknown")
	}
	// The edge WAS written and the output must still say so -- that is the
	// whole answer to "a non-zero exit reads as nothing happened".
	if !strings.Contains(out.String(), "wired acme/harness#121 under acme/lab#75") {
		t.Errorf("the edge line is missing, so the failure reads as nothing happened:\n%s", out.String())
	}
	if !strings.Contains(err.Error(), "IS WIRED") {
		t.Errorf("the error does not say the edge landed:\n%s", err)
	}
	if !strings.Contains(err.Error(), "fix:") {
		t.Errorf("the error names no remedy:\n%s", err)
	}
}

// AN AddLabel FAILURE THAT IS NOT A MISSING LABEL MUST NOT BE MISDIAGNOSED.
// The existence read is what tells the two apart -- gh's sentence is not.
func TestWireDoesNotCreateALabelThatAlreadyExists(t *testing.T) {
	withRouting(t, labelledFourLevelStructure)
	f := wireFake()
	f.repoLabels = map[string][]ghx.RepoLabel{
		"acme/harness": {{Name: "type/work", Color: "fbca04"}},
	}
	f.addLabelErr = errors.New("HTTP 403: Resource not accessible by integration")
	var out bytes.Buffer

	// lab#75 is a block under the seat, so harness#121 wired below it is work.
	// The edge THIS call writes is reflected by the fake, exactly as the live
	// parent read reflects it -- recordLevel runs after the write.
	f.parents["acme/lab#75"] = iref("acme", "doctrine", 282)
	err := runTreeWire(&out, f, iref("acme", "harness", 121), iref("acme", "lab", 75))
	if err == nil {
		t.Fatal("the command succeeded with the level unrecorded")
	}
	if len(f.createdLabels) != 0 {
		t.Errorf("createdLabels = %v, want none: the label already exists", f.createdLabels)
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("the real failure was replaced by a missing-label diagnosis:\n%s", err)
	}
}

// A wire that records its level is unchanged: no extra reads, no writes.
func TestASuccessfulWireStillCostsNoLabelReads(t *testing.T) {
	withRouting(t, labelledFourLevelStructure)
	f := wireFake()
	var out bytes.Buffer

	f.parents["acme/lab#75"] = iref("acme", "doctrine", 282)
	if err := runTreeWire(&out, f, iref("acme", "harness", 121), iref("acme", "lab", 75)); err != nil {
		t.Fatalf("wire: %v\n%s", err, out.String())
	}
	if len(f.createdLabels) != 0 {
		t.Errorf("a working wire created labels: %v", f.createdLabels)
	}
	if !strings.Contains(out.String(), "level recorded: type/work") {
		t.Errorf("the level was not recorded:\n%s", out.String())
	}
}

// !! FINDING 1 OF THE FIRST AUDIT ROUND. !! A label declared on a REPO-SCOPED
// shape can only ever be applied in that repository -- `matchedShape` skips a
// shape whose repo does not match -- so demanding it everywhere reports a gap
// the structure itself says can never be used.
//
// Measured on the live routing file at the time: `cava` was reported MISSING
// type/maquette, which is declared `repo: lab-workspaces`. No wire into `cava`
// could ever want it. The report said 10 of 14; the defensible number was 9.
//
// The stakes are not cosmetic: --create would have DEFINED that label in every
// repository, and the report's own fix line invites exactly that.
func TestARepoScopedLabelIsRequiredOnlyInItsOwnRepo(t *testing.T) {
	route := labelRouting(t)
	// type/seat is declared under {repo: doctrine, …} in this structure.
	f := &labelFake{defined: map[string][]ghx.RepoLabel{
		"acme/doctrine": defines("type/commission", "type/seat", "type/block", "type/work"),
		"acme/harness":  defines("type/commission", "type/block", "type/work"),
	}}
	var out bytes.Buffer

	if err := runTreeLabels(&out, f, route, []string{"acme/doctrine", "acme/harness"}, false); err != nil {
		t.Fatalf("a repository was faulted for a label it can never be given: %v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "MISSING") {
		t.Errorf("nothing should be missing:\n%s", out.String())
	}
	// And the row says how many it was actually judged against, so "ok"
	// against a shorter list is visible rather than surprising.
	if !strings.Contains(out.String(), "acme/harness") || !strings.Contains(out.String(), "3 required") {
		t.Errorf("the per-repo required count is not shown:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "4 required") {
		t.Errorf("doctrine should be judged against four, including its scoped type/seat:\n%s", out.String())
	}
}

// The same rule on the write path: --create must not define a repo-scoped
// label in a repository the structure forbids applying it in.
func TestCreateNeverDefinesARepoScopedLabelElsewhere(t *testing.T) {
	route := labelRouting(t)
	f := &labelFake{defined: map[string][]ghx.RepoLabel{"acme/harness": nil}}

	if err := runTreeLabels(&bytes.Buffer{}, f, route, []string{"acme/harness"}, true); err != nil {
		t.Fatalf("--create failed: %v", err)
	}
	for _, c := range f.created {
		if strings.Contains(c, "type/seat") {
			t.Errorf("created %q: type/seat is doctrine-scoped and unreachable in acme/harness", c)
		}
	}
}

// An unreadable repository and NO findings is `tree doctor`'s exit 2 -- the
// same fact in the same tool, answered the same way. Two verbs disagreeing
// about "I could not look" is the inconsistency facet#138 exists to remove.
func TestCouldNotLookIsExitTwo(t *testing.T) {
	route := labelRouting(t)
	f := &labelFake{errs: map[string]error{"acme/harness": errors.New("HTTP 404")}}

	err := runTreeLabels(&bytes.Buffer{}, f, route, []string{"acme/harness"}, false)
	if err == nil {
		t.Fatal("an unreadable repository was reported as parity")
	}
	if got := exitCodeFor(err); got != exitCantLook {
		t.Errorf("exit code = %d, want %d (could not look)\n  %v", got, exitCantLook, err)
	}
}

// A gap FOUND is still exit 1, even alongside a repository that could not be
// read: there is a real finding, and the unchecked repositories are named in
// the message rather than folded into the code.
func TestAFindingIsExitOneEvenWithAnUnreadableRepo(t *testing.T) {
	route := labelRouting(t)
	f := &labelFake{
		defined: map[string][]ghx.RepoLabel{"acme/lab": defines("type/commission")},
		errs:    map[string]error{"acme/harness": errors.New("HTTP 404")},
	}

	err := runTreeLabels(&bytes.Buffer{}, f, route, []string{"acme/lab", "acme/harness"}, false)
	if err == nil {
		t.Fatal("a missing label was reported as parity")
	}
	if got := exitCodeFor(err); got != exitLooked {
		t.Errorf("exit code = %d, want %d (looked, and found something)\n  %v", got, exitLooked, err)
	}
	if !strings.Contains(err.Error(), "acme/harness") {
		t.Errorf("the unchecked repository is not named:\n%s", err)
	}
}

// !! FINDING 3 OF THE SECOND AUDIT ROUND. !! The `--help` text promised three
// exit codes and the command answered with two: only the two `withCode` calls
// INSIDE runTreeLabels were tagged, so every failure arriving through cobra --
// a mistyped flag, a flag with no value, an unexpected argument, an unreadable
// routing file -- still defaulted to 1 and claimed a parity gap.
//
// Measured on the shipped build of round 2:
//
//	facet tree labels --creat      -> exit 1, want 2
//	facet tree labels --repo       -> exit 1, want 2
//	facet tree labels extra-arg    -> exit 1, want 2
//	<malformed routing file>       -> exit 1, want 2   (tree doctor said 2)
//
// The consumers named in facet#138 read 1 from this verb as "there is a parity
// gap in the estate". A typo in a flag produced that sentence.
func TestTreeLabelsKeepsTheExitContractItsHelpStates(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"a mistyped flag", []string{"--creat"}},
		{"a flag with no value", []string{"--repo"}},
		{"an unexpected positional argument", []string{"extra-arg"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newTreeLabelsCmd()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs(tc.args)

			err := cmd.Execute()
			if err == nil {
				t.Fatalf("%v was accepted", tc.args)
			}
			if got := exitCodeFor(err); got != exitCantLook {
				t.Errorf("exit code = %d, want %d (could not look)\n  %v", got, exitCantLook, err)
			}
		})
	}
}

// An unreadable routing file is the purest could-not-look for this verb: the
// required set lives IN it, so nothing was even asked of GitHub. `tree doctor`
// answered 2 here while `tree labels` answered 1 -- the same input, the same
// binary, two verbs, two answers.
func TestAnUnreadableRoutingFileIsCouldNotLook(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routing.json")
	if err := os.WriteFile(path, []byte("{ this is not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Through the ENVIRONMENT, not by assigning `roots`. tagCantLook installs a
	// PersistentPreRunE that reloads the roots from the environment -- which is
	// the whole point of it, and which means a test that only set the package
	// variable would have its fixture replaced by the real routing file and go
	// to the network. That is not a wrinkle in the test: it is the code path
	// this case exists to exercise.
	t.Setenv("FACET_ROUTING", path)

	cmd := newTreeLabelsCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(nil)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("a malformed routing file was accepted")
	}
	if got := exitCodeFor(err); got != exitCantLook {
		t.Errorf("exit code = %d, want %d\n  %v", got, exitCantLook, err)
	}
}

// The contract is stated where a caller can read it without opening the source.
func TestTreeLabelsHelpStatesTheExitCodes(t *testing.T) {
	long := newTreeLabelsCmd().Long
	for _, want := range []string{"EXIT CODES", "could NOT look", "1  looked", "2  could"} {
		if !strings.Contains(long, want) {
			t.Errorf("--help is missing %q:\n%s", want, long)
		}
	}
}
