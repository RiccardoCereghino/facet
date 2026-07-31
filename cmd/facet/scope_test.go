package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
