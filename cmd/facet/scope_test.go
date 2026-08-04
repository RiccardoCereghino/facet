package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RiccardoCereghino/facet/internal/ghx"
	"github.com/RiccardoCereghino/facet/internal/manifest"
	"github.com/RiccardoCereghino/facet/internal/seat"
)

// workspaceWithRepo builds a workspace holding one repository subdirectory, and
// returns both paths. It is the shape that matters here: the work is done in the
// subdirectory, so every seat-file command has to find the workspace from there.
func workspaceWithRepo(t *testing.T) (ws, repoDir string) {
	t.Helper()
	ws = t.TempDir()
	m := &manifest.Manifest{Name: "example", Clones: map[string]string{"repo": "git@example.com:owner/repo.git"}}
	if err := m.Write(ws); err != nil {
		t.Fatal(err)
	}
	repoDir = filepath.Join(ws, "repo")
	if err := os.MkdirAll(repoDir, 0o777); err != nil {
		t.Fatal(err)
	}
	return ws, repoDir
}

func TestScopeListReportsSeatAndScope(t *testing.T) {
	ws, _ := workspaceWithRepo(t)
	if err := seat.Write(ws, "w-example-12", []seat.Ref{{Repo: "owner/repo", Number: 12}, {Repo: "acme/tools", Number: 7}}); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := runScopeList(&buf, ws); err != nil {
		t.Fatalf("runScopeList: %v", err)
	}
	for _, want := range []string{"w-example-12", "owner/repo#12", "acme/tools#7"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("scope list output does not mention %q:\n%s", want, buf.String())
		}
	}
}

// TestScopeAddAcceptsALandingEntry is facet#97: a workspace whose issues are
// filed in one repo and whose PRs land in another can now record the landing
// repo honestly, without inventing an issue it does not cover.
func TestScopeAddAcceptsALandingEntry(t *testing.T) {
	ws, _ := workspaceWithRepo(t)
	if err := seat.Write(ws, "w-example-12", []seat.Ref{{Repo: "owner/repo", Number: 12}}); err != nil {
		t.Fatal(err)
	}

	refs, err := seat.ParseRefs([]string{"landing:owner/other"})
	if err != nil {
		t.Fatalf("ParseRefs: %v", err)
	}
	added, err := seat.AppendScope(ws, refs)
	if err != nil {
		t.Fatalf("AppendScope: %v", err)
	}
	if len(added) != 1 || added[0].String() != "landing:owner/other" {
		t.Fatalf("AppendScope reported %v as new, want [landing:owner/other]", added)
	}

	var buf bytes.Buffer
	if err := runScopeList(&buf, ws); err != nil {
		t.Fatalf("runScopeList: %v", err)
	}
	for _, want := range []string{"owner/repo#12", "landing:owner/other"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("scope list output does not mention %q:\n%s", want, buf.String())
		}
	}

	// Round-trip: read the file back and confirm the landing entry survives
	// exactly, alongside the issue entry it was added beside.
	got, err := seat.ReadScope(ws)
	if err != nil {
		t.Fatalf("ReadScope: %v", err)
	}
	if len(got) != 2 || got[0].String() != "owner/repo#12" || got[1].String() != "landing:owner/other" {
		t.Errorf("ReadScope = %v, want [owner/repo#12 landing:owner/other]", got)
	}
}

// scopeIssueFake scripts ViewIssue for reportResultingTier, keyed the same
// way treeFake keys its issues in tree_test.go.
type scopeIssueFake struct {
	fakeGH
	issues map[string]*ghx.Issue
}

func (f *scopeIssueFake) ViewIssue(repo string, number int) (*ghx.Issue, error) {
	iss, ok := f.issues[fmt.Sprintf("%s#%d", repo, number)]
	if !ok {
		return nil, fmt.Errorf("scopeIssueFake: no issue scripted for %s#%d", repo, number)
	}
	return iss, nil
}

// facet#112 asked that a scope edit state the worst-case tier the way `tree
// wire` states merge authority on every edge -- a scope edit that silently
// changes what a re-seed would derive is the same defect class.
func TestReportResultingTierPrintsTheWorstCase(t *testing.T) {
	f := &scopeIssueFake{issues: map[string]*ghx.Issue{
		"acme/a#1": issueWith("small", "complexity/1"),
		"acme/b#2": issueWith("bigger", "complexity/3"),
	}}
	refs, err := seat.ParseRefs([]string{"acme/a#1", "acme/b#2"})
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	reportResultingTier(&buf, f, refs)
	out := buf.String()
	if !strings.Contains(out, "c3") {
		t.Errorf("tier report does not name the worst case c3:\n%s", out)
	}
	if strings.Contains(out, "c1 (") {
		t.Errorf("tier report named the WRONG entry's tier as the worst case:\n%s", out)
	}
}

// An issue with no complexity label, or an unreadable one, must be named
// rather than silently excluded from the worst case -- silence here would
// make a scope edit's own report understate what it actually covers.
func TestReportResultingTierNamesWhatItCouldNotJudge(t *testing.T) {
	f := &scopeIssueFake{issues: map[string]*ghx.Issue{
		"acme/a#1": issueWith("no label at all"),
	}}
	refs, err := seat.ParseRefs([]string{"acme/a#1", "acme/missing#9"})
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	reportResultingTier(&buf, f, refs)
	out := buf.String()
	if !strings.Contains(out, "acme/a#1") || !strings.Contains(out, "no complexity label") {
		t.Errorf("did not name the unlabelled issue:\n%s", out)
	}
	if !strings.Contains(out, "acme/missing#9") {
		t.Errorf("did not name the issue that could not be read:\n%s", out)
	}
}

// A landing-only scope has no issue to judge a tier from at all -- it must
// not print a misleading "tier: c0" or crash looking one up.
func TestReportResultingTierSkipsLandingEntries(t *testing.T) {
	f := &scopeIssueFake{issues: map[string]*ghx.Issue{}}
	refs, err := seat.ParseRefs([]string{"landing:acme/a"})
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	reportResultingTier(&buf, f, refs)
	if buf.Len() != 0 {
		t.Errorf("a landing-only scope printed a tier report:\n%s", buf.String())
	}
}

// TestScopeRemoveThenListReflectsIt is the cobra-adjacent round trip: add two,
// remove one via seat.RemoveScope (what newScopeRemoveCmd's RunE calls), and
// confirm scope list -- the documented way to check a workspace's scope --
// shows exactly what remains.
func TestScopeRemoveThenListReflectsIt(t *testing.T) {
	ws, _ := workspaceWithRepo(t)
	if err := seat.Write(ws, "w-example-12", []seat.Ref{
		{Repo: "owner/repo", Number: 12}, {Repo: "acme/tools", Number: 7},
	}); err != nil {
		t.Fatal(err)
	}

	removed, _, err := seat.RemoveScope(ws, []seat.Ref{{Repo: "acme/tools", Number: 7}})
	if err != nil {
		t.Fatalf("RemoveScope: %v", err)
	}
	if len(removed) != 1 {
		t.Fatalf("removed = %v, want 1 entry", removed)
	}

	var buf bytes.Buffer
	if err := runScopeList(&buf, ws); err != nil {
		t.Fatalf("runScopeList: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "owner/repo#12") {
		t.Errorf("surviving entry missing from scope list:\n%s", out)
	}
	if strings.Contains(out, "acme/tools#7") {
		t.Errorf("removed entry still shows in scope list:\n%s", out)
	}
}

// A workspace with no scope recorded reads as "none recorded", not as a blank
// field and not as an error. Absent means there is nothing to check, and that is
// a state a real workspace is in -- one that covers no single issue.
func TestScopeListSaysSoWhenNothingIsRecorded(t *testing.T) {
	ws, _ := workspaceWithRepo(t)

	var buf bytes.Buffer
	if err := runScopeList(&buf, ws); err != nil {
		t.Fatalf("runScopeList: %v", err)
	}
	out := buf.String()
	// Named rather than counted. A bare count told you the number was wrong and
	// not which field had gone quiet, and it had to be edited anyway the moment
	// .seat-issue joined the family — so it may as well say what it wants.
	for _, field := range []string{seat.NameFile, seat.SeatIssueFile, seat.ScopeFile} {
		if !strings.Contains(out, "none recorded in "+field) {
			t.Errorf("%s is not reported as none recorded; a field that vanishes when unset "+
				"cannot be told from one nobody thought to print. got:\n%s", field, out)
		}
	}
}
