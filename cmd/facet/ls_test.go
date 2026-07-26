package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RiccardoCereghino/facet/internal/config"
	"github.com/RiccardoCereghino/facet/internal/gitx"
	"github.com/RiccardoCereghino/facet/internal/manifest"
)

// TestLsAtWorkspacesRootListsEveryWorkspace is the regression test for facet#31:
// `facet ls` run against a directory with no manifest of its own, but which
// contains manifest-bearing subdirectories, must list every one of them
// instead of erroring with "no .workspace.json in <dir>".
func TestLsAtWorkspacesRootListsEveryWorkspace(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"alpha", "beta"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		m := &manifest.Manifest{Name: name}
		if err := m.Write(dir); err != nil {
			t.Fatal(err)
		}
	}

	savedRoots, savedGit := roots, git
	defer func() { roots, git = savedRoots, savedGit }()
	roots = config.Roots{Workspaces: root}
	git = gitx.Git{}

	cmd := newLsCmd()
	cmd.SetArgs([]string{"--path", root})

	out := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("ls at workspaces root: %v", err)
		}
	})

	if !strings.Contains(out, "alpha") || !strings.Contains(out, "beta") {
		t.Errorf("expected ls to list both workspaces, got:\n%s", out)
	}
}

// TestLsWithNoManifestAnywhereStillErrors makes sure the workspaces-root
// fallback doesn't swallow the genuinely-wrong-directory case: a directory
// with no manifest and no manifest-bearing subdirectories must still get the
// original, helpful error.
func TestLsWithNoManifestAnywhereStillErrors(t *testing.T) {
	root := t.TempDir()

	savedRoots, savedGit := roots, git
	defer func() { roots, git = savedRoots, savedGit }()
	roots = config.Roots{Workspaces: root}
	git = gitx.Git{}

	cmd := newLsCmd()
	cmd.SetArgs([]string{"--path", root})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "facet new") {
		t.Errorf("want a helpful error pointing at `facet new`, got %v", err)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = saved }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}
