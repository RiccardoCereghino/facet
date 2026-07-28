// Package config resolves the two roots facet operates between: the workspaces
// directory, and the projects directory that junction-backed workspaces point
// into. Both are overridable by environment variable, which is what lets the
// tests run hermetically against a temp dir.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/RiccardoCereghino/facet/internal/manifest"
)

// Roots are the directories facet works within.
type Roots struct {
	// Workspaces holds one directory per workspace, each with a .workspace.json.
	Workspaces string
	// Projects holds the real repos that `links` junction into.
	Projects string
	// Mirrors holds bare mirrors that clones are hardlinked from.
	Mirrors string
	// Routing is the project-specific map from issue labels and bodies to repos.
	// facet ships no knowledge of any organisation; this file supplies it.
	Routing string
	// Knowledge holds the area-*.md hazard fragments inlined into spawned
	// workspaces.
	Knowledge string
}

// Load resolves the roots from the environment, falling back to ~/Workspaces
// and ~/Projects.
func Load() (Roots, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Roots{}, fmt.Errorf("resolve home directory: %w", err)
	}
	r := Roots{
		Workspaces: envOr("FACET_WORKSPACES", filepath.Join(home, "Workspaces")),
		Projects:   envOr("FACET_PROJECTS", filepath.Join(home, "Projects")),
	}
	r.Mirrors = envOr("FACET_MIRRORS", filepath.Join(r.Projects, ".mirrors"))
	r.Routing = envOr("FACET_ROUTING", filepath.Join(r.Workspaces, ".tools", "routing.json"))
	r.Knowledge = envOr("FACET_KNOWLEDGE", filepath.Join(r.Workspaces, ".knowledge"))
	return r, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// IssuePrefix marks an ephemeral, issue-scoped workspace. Such workspaces are
// gitignored and skipped by `facet restore`.
const IssuePrefix = "iss-"

// ResolveWorkspace turns a possibly-empty path into an absolute workspace
// directory, defaulting to the working directory.
func ResolveWorkspace(path string) (string, error) {
	if path == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		path = wd
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if fi, err := os.Stat(abs); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("workspace path not found: %s", abs)
		}
		// A path that exists but is unreadable (EACCES) is a different problem;
		// reporting it as "not found" sends the user to debug the wrong thing.
		return "", fmt.Errorf("workspace path %s: %w", abs, err)
	} else if !fi.IsDir() {
		return "", fmt.Errorf("not a directory: %s", abs)
	}
	return abs, nil
}

// FindWorkspace resolves the workspace *containing* a path, by walking up to the
// nearest directory holding a manifest.
//
// It is separate from ResolveWorkspace, which reads the given directory and no
// other, because the two answer different questions and the existing callers
// want the strict one: `facet sync` acting on a parent directory because the one
// you named has no manifest would be a surprise with consequences.
//
// Walking up is what the seat files need. Work happens inside a workspace's repo
// subdirectory, so a command run from there must still find the workspace, and
// the leaf of the working directory is not the answer -- it is the repository's
// name, and two workspaces can hold repositories of the same name.
func FindWorkspace(start string) (string, error) {
	from, err := ResolveWorkspace(start)
	if err != nil {
		return "", err
	}
	dir := from
	for {
		if _, err := os.Stat(filepath.Join(dir, manifest.FileName)); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no %s in %s or any parent directory, so there is no workspace here\n"+
				"fix: run this from inside a workspace, or pass --path", manifest.FileName, from)
		}
		dir = parent
	}
}
