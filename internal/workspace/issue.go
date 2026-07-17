package workspace

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/RiccardoCereghino/facet/internal/config"
	"github.com/RiccardoCereghino/facet/internal/ghx"
	"github.com/RiccardoCereghino/facet/internal/gitx"
	"github.com/RiccardoCereghino/facet/internal/manifest"
)

// CloneState is what a clone inside an issue workspace looks like right now.
type CloneState struct {
	Dir    string
	Branch string
	// Dirty means uncommitted changes or untracked files.
	Dirty bool
	// Unpushed counts commits on any local branch that no remote has. This
	// deliberately covers branches with no upstream at all: those are the ones
	// most easily lost.
	Unpushed int
}

// IssueState is the full picture of one ephemeral workspace.
type IssueState struct {
	Dir   string
	Name  string
	Issue *manifest.Issue
	Clone []CloneState
	// PR is the pull request for the issue branch, or nil.
	PR        *ghx.PR
	SizeBytes int64
}

// Blockers lists the reasons this workspace must not be deleted. An empty slice
// means it is safe to reap.
//
// The ordering matters: unpushed work is unrecoverable, an open PR is merely
// premature.
func (s *IssueState) Blockers() []string {
	var out []string
	for _, c := range s.Clone {
		if c.Unpushed > 0 {
			out = append(out, fmt.Sprintf("%s has %d unpushed commit(s) -- this workspace is their only copy", c.Dir, c.Unpushed))
		}
	}
	for _, c := range s.Clone {
		if c.Dirty {
			out = append(out, fmt.Sprintf("%s has uncommitted changes", c.Dir))
		}
	}
	if s.PR != nil && strings.EqualFold(s.PR.State, "OPEN") {
		out = append(out, fmt.Sprintf("pull request #%d is still open", s.PR.Number))
	}
	return out
}

// PRLookup finds the pull request for a branch. Nil means do not look.
type PRLookup interface {
	ViewPR(repo, branch string) (*ghx.PR, error)
}

// InspectIssue gathers the state of one issue workspace. The pull-request lookup
// is optional: pass nil to skip it.
func InspectIssue(ws string, git gitx.Runner, pr PRLookup) (*IssueState, error) {
	m, err := manifest.Read(ws)
	if err != nil {
		return nil, err
	}
	if !m.IsIssueWorkspace() {
		return nil, fmt.Errorf("%s is not an issue workspace", ws)
	}
	st := &IssueState{Dir: ws, Name: m.Name, Issue: m.Issue}

	for _, dir := range sortedKeys(m.Clones) {
		p := filepath.Join(ws, dir)
		if !gitx.IsRepo(p) {
			continue
		}
		c := CloneState{Dir: dir}
		if out, err := git.Run(p, nil, "status", "--porcelain"); err == nil {
			c.Dirty = strings.TrimSpace(out) != ""
		}
		if out, err := git.Run(p, nil, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
			c.Branch = out
		}
		// Commits reachable from any local branch but from no remote-tracking
		// branch. A branch that was never pushed has all of its commits counted.
		if out, err := git.Run(p, nil, "rev-list", "--count", "--branches", "--not", "--remotes"); err == nil {
			c.Unpushed, _ = strconv.Atoi(strings.TrimSpace(out))
		}
		st.Clone = append(st.Clone, c)
	}

	if pr != nil && m.Issue.Branch != "" {
		if found, err := pr.ViewPR(m.Issue.Repo, m.Issue.Branch); err == nil {
			st.PR = found
		}
	}
	st.SizeBytes = dirSize(ws)
	return st, nil
}

// ListIssues inspects every issue workspace under the workspaces root.
func ListIssues(roots config.Roots, git gitx.Runner, pr PRLookup) ([]*IssueState, error) {
	entries, err := os.ReadDir(roots.Workspaces)
	if err != nil {
		return nil, err
	}
	var out []*IssueState
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), config.IssuePrefix) {
			continue
		}
		dir := filepath.Join(roots.Workspaces, e.Name())
		if _, err := os.Stat(manifest.Path(dir)); err != nil {
			continue
		}
		st, err := InspectIssue(dir, git, pr)
		if err != nil {
			continue
		}
		out = append(out, st)
	}
	return out, nil
}

// Reap deletes an issue workspace. It never touches the shared mirror: removing a
// hardlinked object file only drops that name, and the mirror keeps its own.
//
// Callers must check Blockers() first; Reap does not, so that --force has
// somewhere to go.
func Reap(st *IssueState) error {
	// Windows will not remove a directory that is some process's working
	// directory -- and reap is meant to be run from inside the workspace it
	// deletes. Step out first, or the tree is half-removed and the error blames
	// "another process".
	if wd, err := os.Getwd(); err == nil && isUnder(st.Dir, wd) {
		if err := os.Chdir(filepath.Dir(st.Dir)); err != nil {
			return fmt.Errorf("step out of %s before deleting it: %w", st.Dir, err)
		}
	}
	return removeAllForce(st.Dir)
}

// isUnder reports whether path is root or lies beneath it.
func isUnder(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel))
}

// removeAllForce deletes a tree, clearing the read-only attribute git sets on
// objects and packs. Plain os.RemoveAll fails on a git repository on Windows.
func removeAllForce(root string) error {
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // keep going; the remove below will report what matters
		}
		if !d.IsDir() {
			_ = os.Chmod(path, 0o666)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return os.RemoveAll(root)
}

func dirSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			total += fi.Size()
		}
		return nil
	})
	return total
}
