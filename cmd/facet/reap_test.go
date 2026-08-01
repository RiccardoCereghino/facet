package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RiccardoCereghino/facet/internal/config"
)

// facet#83's invocation trap: `facet reap <name>` answered
//
//	facet: unknown command "iss-73" for "facet reap"
//
// because reap took --path only and cobra.NoArgs rejected the positional. The
// error names the wrong problem -- it reads as "reap is broken" rather than
// "reap wants a flag" -- and the trap was documented in prose instead of fixed,
// which is where a CLI puts what it has decided to live with.
func TestReapResolvesAPositionalWorkspaceName(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "iss-repo-1-x")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	saved := roots
	defer func() { roots = saved }()
	roots = config.Roots{Workspaces: root}

	got, err := reapTarget("", []string{"iss-repo-1-x"})
	if err != nil {
		t.Fatalf("a bare workspace name must resolve under the workspaces root: %v", err)
	}
	if got != ws {
		t.Errorf("reapTarget = %q, want %q", got, ws)
	}
}

// A positional that is a real path still means that path, wherever it is --
// the name fallback is a convenience, not a redirection.
func TestReapResolvesAPositionalPath(t *testing.T) {
	root := t.TempDir()
	elsewhere := filepath.Join(root, "elsewhere")
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	saved := roots
	defer func() { roots = saved }()
	roots = config.Roots{Workspaces: filepath.Join(root, "workspaces")}

	got, err := reapTarget("", []string{elsewhere})
	if err != nil {
		t.Fatalf("a positional path must resolve to itself: %v", err)
	}
	if got != elsewhere {
		t.Errorf("reapTarget = %q, want %q", got, elsewhere)
	}
}

// --path and a positional naming different directories must not have a silent
// winner. reap deletes what it resolves; a precedence rule between two arguments
// that disagree is how the wrong workspace goes.
func TestReapRefusesBothPathAndPositional(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"a", "b"} {
		if err := os.MkdirAll(filepath.Join(root, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	saved := roots
	defer func() { roots = saved }()
	roots = config.Roots{Workspaces: root}

	_, err := reapTarget(filepath.Join(root, "a"), []string{"b"})
	if err == nil {
		t.Fatal("--path and a positional naming different workspaces must refuse, not pick one")
	}
	if !strings.Contains(err.Error(), "name the workspace once") {
		t.Errorf("the refusal must say which two things disagree, got %v", err)
	}
}

// The unchanged default: no positional and no flag means the working directory,
// which is how reap is run from inside the workspace it deletes.
func TestReapWithNoArgumentsUsesTheWorkingDirectory(t *testing.T) {
	saved := roots
	defer func() { roots = saved }()
	roots = config.Roots{Workspaces: t.TempDir()}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	got, err := reapTarget("", nil)
	if err != nil {
		t.Fatalf("no arguments must resolve the working directory: %v", err)
	}
	if got != wd {
		t.Errorf("reapTarget = %q, want the working directory %q", got, wd)
	}
}
