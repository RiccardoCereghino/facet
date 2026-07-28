package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RiccardoCereghino/facet/internal/manifest"
)

// workspaceWithRepo builds a workspace holding one repository subdirectory. That
// shape is the whole reason FindWorkspace exists: the work is done in the
// subdirectory, not at the workspace root.
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

// It walks UP. The leaf of the working directory is not the answer -- it is the
// repository's name, and two workspaces can hold repositories called the same
// thing -- and neither is the working directory itself, which is wherever the
// work happens to be.
func TestFindWorkspaceWalksUpFromTheRepoSubdirectory(t *testing.T) {
	ws, repoDir := workspaceWithRepo(t)
	deep := filepath.Join(repoDir, "internal", "somewhere")
	if err := os.MkdirAll(deep, 0o777); err != nil {
		t.Fatal(err)
	}

	// t.TempDir can hand back a path through a symlink, so compare against the
	// resolver's own answer for the root rather than against the raw string.
	want, err := FindWorkspace(ws)
	if err != nil {
		t.Fatal(err)
	}
	for _, start := range []string{ws, repoDir, deep} {
		got, err := FindWorkspace(start)
		if err != nil {
			t.Fatalf("FindWorkspace(%s): %v", start, err)
		}
		if got != want {
			t.Errorf("FindWorkspace(%s) = %s, want %s", start, got, want)
		}
	}
}

func TestFindWorkspaceRefusesOutsideAnyWorkspace(t *testing.T) {
	// A bare temp directory has no manifest above it, up to the filesystem root.
	_, err := FindWorkspace(t.TempDir())
	if err == nil {
		t.Fatal("FindWorkspace found a workspace where there is none")
	}
	if !strings.Contains(err.Error(), manifest.FileName) || !strings.Contains(err.Error(), "fix:") {
		t.Errorf("error %q does not name the file it looked for and the fix", err)
	}
}

// ResolveWorkspace is the strict one and must stay strict: `facet sync` acting
// on a parent directory because the one it was given has no manifest would be a
// surprise with consequences. This is the guard on the two not converging.
func TestResolveWorkspaceDoesNotWalkUp(t *testing.T) {
	_, repoDir := workspaceWithRepo(t)
	got, err := ResolveWorkspace(repoDir)
	if err != nil {
		t.Fatalf("ResolveWorkspace(%s): %v", repoDir, err)
	}
	if filepath.Base(got) != "repo" {
		t.Errorf("ResolveWorkspace(%s) = %s; it resolved somewhere other than the directory it was given", repoDir, got)
	}
}
