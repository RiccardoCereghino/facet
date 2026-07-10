package workspace

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/RiccardoCereghino/facet/internal/config"
	"github.com/RiccardoCereghino/facet/internal/fslink"
	"github.com/RiccardoCereghino/facet/internal/gitx"
	"github.com/RiccardoCereghino/facet/internal/manifest"
)

// These exercise the real git binary against temp repos. They are the tests that
// protect the two properties whose failure destroys work.

func quiet() Reporter { return Reporter{W: io.Discard} }

// originRepo builds a throwaway repo with one commit and returns its path.
func originRepo(t *testing.T, dir string) string {
	t.Helper()
	g := gitx.Git{}
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	mustRun := func(args ...string) {
		t.Helper()
		if _, err := g.Run(dir, nil, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	mustRun("init", "-q", "-b", "main")
	mustRun("config", "user.email", "t@example.com")
	mustRun("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("hello\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	mustRun("add", "-A")
	mustRun("commit", "-qm", "init")
	return dir
}

func setup(t *testing.T) (config.Roots, string, string) {
	t.Helper()
	root := t.TempDir()
	roots := config.Roots{
		Workspaces: filepath.Join(root, "Workspaces"),
		Projects:   filepath.Join(root, "Projects"),
		Mirrors:    filepath.Join(root, "Projects", ".mirrors"),
	}
	ws := filepath.Join(roots.Workspaces, "w")
	if err := os.MkdirAll(ws, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(roots.Projects, 0o777); err != nil {
		t.Fatal(err)
	}
	origin := originRepo(t, filepath.Join(root, "origin", "repo"))
	return roots, ws, origin
}

func TestSyncCreatesMissingClone(t *testing.T) {
	roots, ws, origin := setup(t)
	m := &manifest.Manifest{Name: "w", Clones: map[string]string{"repo": origin}}
	if err := m.Write(ws); err != nil {
		t.Fatal(err)
	}
	if err := Sync(roots, ws, gitx.Git{}, quiet(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ws, "repo", "README")); err != nil {
		t.Fatalf("clone not checked out: %v", err)
	}
}

// The central guarantee: a clone that already exists is never pulled, reset or
// cleaned, because it may hold the only copy of unpushed work.
func TestSyncNeverTouchesExistingClone(t *testing.T) {
	roots, ws, origin := setup(t)
	m := &manifest.Manifest{Name: "w", Clones: map[string]string{"repo": origin}}
	if err := m.Write(ws); err != nil {
		t.Fatal(err)
	}
	if err := Sync(roots, ws, gitx.Git{}, quiet(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}

	clone := filepath.Join(ws, "repo")
	g := gitx.Git{}
	// Dirty the tree, add an untracked file, and move off the default branch.
	if err := os.WriteFile(filepath.Join(clone, "README"), []byte("LOCAL EDIT\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, "untracked"), []byte("x\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "checkout", "-qb", "wip"); err != nil {
		t.Fatal(err)
	}

	// A second sync must be a total no-op on the clone.
	if err := Sync(roots, ws, gitx.Git{}, quiet(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(clone, "README"))
	if err != nil || string(got) != "LOCAL EDIT\n" {
		t.Errorf("sync reverted a dirty file: %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(clone, "untracked")); err != nil {
		t.Error("sync cleaned an untracked file")
	}
	branch, err := g.Run(clone, nil, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil || branch != "wip" {
		t.Errorf("sync moved the branch: %q, %v", branch, err)
	}
}

func TestSyncAddsExtraRemoteButNeverRewritesIt(t *testing.T) {
	roots, ws, origin := setup(t)
	upstream := originRepo(t, filepath.Join(t.TempDir(), "upstream"))
	m := &manifest.Manifest{
		Name:    "w",
		Clones:  map[string]string{"repo": origin},
		Remotes: map[string]map[string]string{"repo": {"upstream": upstream}},
	}
	if err := m.Write(ws); err != nil {
		t.Fatal(err)
	}
	if err := Sync(roots, ws, gitx.Git{}, quiet(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	clone := filepath.Join(ws, "repo")
	g := gitx.Git{}
	got, ok := gitx.RemoteURL(g, clone, "upstream")
	if !ok || got != upstream {
		t.Fatalf("upstream = %q, %v; want %q", got, ok, upstream)
	}

	// A remote whose URL disagrees with the manifest is reported, never rewritten.
	if _, err := g.Run(clone, nil, "remote", "set-url", "upstream", "https://elsewhere.invalid/x.git"); err != nil {
		t.Fatal(err)
	}
	if err := Sync(roots, ws, g, quiet(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	got, _ = gitx.RemoteURL(g, clone, "upstream")
	if got != "https://elsewhere.invalid/x.git" {
		t.Errorf("sync silently rewrote a disagreeing remote to %q", got)
	}
}

func TestSyncLinkAndPrune(t *testing.T) {
	roots, ws, origin := setup(t)
	// A link points at a project under ProjectsRoot.
	project := filepath.Join(roots.Projects, "proj")
	originRepo(t, project)
	_ = origin

	m := &manifest.Manifest{Name: "w", Links: map[string]string{"proj": "proj"}}
	if err := m.Write(ws); err != nil {
		t.Fatal(err)
	}
	if err := Sync(roots, ws, gitx.Git{}, quiet(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	ok, err := fslink.IsLink(filepath.Join(ws, "proj"))
	if err != nil || !ok {
		t.Fatalf("link not created: %v", err)
	}
	// Sync captured the project's origin into the manifest.
	back, err := manifest.Read(ws)
	if err != nil {
		t.Fatal(err)
	}
	if back.Origins["proj"] == "" {
		t.Log("note: project has no origin remote, so none was captured (expected here)")
	}

	// Drop the link from the manifest, add a clone, then prune. The link goes;
	// the clone stays.
	stray := filepath.Join(ws, "proj")
	m2 := &manifest.Manifest{Name: "w", Clones: map[string]string{"repo": origin}}
	if err := m2.Write(ws); err != nil {
		t.Fatal(err)
	}
	if err := Sync(roots, ws, gitx.Git{}, quiet(), SyncOptions{Prune: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(stray); !os.IsNotExist(err) {
		t.Error("prune left an undeclared link behind")
	}
	if _, err := os.Stat(filepath.Join(ws, "repo", "README")); err != nil {
		t.Error("prune deleted a clone")
	}
	// The link target survives: only the reparse point/symlink was removed.
	if _, err := os.Stat(filepath.Join(project, "README")); err != nil {
		t.Fatal("!!! prune deleted the link's target contents")
	}
}

func TestDirsSkipsIssueWorkspaces(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"topical", "iss-repo-1-x", "no-manifest"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o777); err != nil {
			t.Fatal(err)
		}
		if name != "no-manifest" {
			m := &manifest.Manifest{Name: name}
			if err := m.Write(dir); err != nil {
				t.Fatal(err)
			}
		}
	}
	got, err := Dirs(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || filepath.Base(got[0]) != "topical" {
		t.Errorf("Dirs(includeIssue=false) = %v; want just [topical]", got)
	}
	got, err = Dirs(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("Dirs(includeIssue=true) = %v; want 2", got)
	}
}

// testRoots builds Roots under a temp directory.
func testRoots(root string) config.Roots {
	return config.Roots{
		Workspaces: filepath.Join(root, "Workspaces"),
		Projects:   filepath.Join(root, "Projects"),
		Mirrors:    filepath.Join(root, "Projects", ".mirrors"),
	}
}
