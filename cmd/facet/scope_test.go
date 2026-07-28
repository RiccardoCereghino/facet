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
	if strings.Count(out, "none recorded") != 2 {
		t.Errorf("want both the seat and the scope reported as none recorded, got:\n%s", out)
	}
}
