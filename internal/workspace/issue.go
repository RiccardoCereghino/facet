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
	// Unpushed counts commits reachable from HEAD or any local branch that no
	// remote has. This deliberately covers branches with no upstream at all, and
	// commits made on a detached HEAD: those are the ones most easily lost.
	Unpushed int
	// Stashed counts `git stash` entries, which no push would ever carry.
	Stashed int
	// Unverifiable holds the reasons this clone's state could not be confirmed --
	// a git command that failed, or a directory that is not a readable repo. A
	// non-empty slice is itself a blocker: a clone we could not inspect must not
	// be assumed empty, or a git hiccup becomes silent data loss.
	Unverifiable []string
}

// IssueState is the full picture of one ephemeral workspace.
type IssueState struct {
	Dir   string
	Name  string
	Issue *manifest.Issue
	Clone []CloneState
	// PR is the pull request for the issue branch, or nil.
	PR *ghx.PR
	// PRUnknown is set when a pull-request lookup was attempted but failed, so an
	// open PR cannot be ruled out. Distinct from PR == nil, which means the lookup
	// ran and found none.
	PRUnknown bool
	SizeBytes int64
}

// Blockers lists the reasons this workspace must not be deleted. An empty slice
// means it is safe to reap.
//
// The ordering matters: work that cannot be recovered or even confirmed comes
// first, an open PR is merely premature. Every check fails safe -- a state we
// could not read is reported as a blocker, never assumed clean.
func (s *IssueState) Blockers() []string {
	var out []string
	for _, c := range s.Clone {
		for _, u := range c.Unverifiable {
			out = append(out, fmt.Sprintf("%s: %s -- cannot confirm it is safe to delete", c.Dir, u))
		}
	}
	for _, c := range s.Clone {
		if c.Unpushed > 0 {
			out = append(out, fmt.Sprintf("%s has %d unpushed commit(s) -- this workspace is their only copy", c.Dir, c.Unpushed))
		}
	}
	for _, c := range s.Clone {
		if c.Stashed > 0 {
			out = append(out, fmt.Sprintf("%s has %d stash entry(ies) -- no push would carry them", c.Dir, c.Stashed))
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
	if s.PRUnknown {
		out = append(out, "could not determine the pull-request state -- an open PR cannot be ruled out")
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
			// A dir that never got cloned holds nothing to lose. But a dir that
			// exists and simply is not a valid repo -- an interrupted clone, a
			// damaged .git -- may still carry a working tree full of edits, so it
			// must block rather than vanish from the accounting.
			if _, err := os.Stat(p); err == nil {
				st.Clone = append(st.Clone, CloneState{
					Dir: dir, Unverifiable: []string{"present but not a git repository"},
				})
			}
			continue
		}
		st.Clone = append(st.Clone, inspectClone(git, dir, p))
	}

	if pr != nil && m.Issue.Branch != "" {
		// A lookup that errors is not "no PR": auth expiry, a network blip, or a
		// rate limit must not silently drop the open-PR guard. Record that the
		// state is unknown so Blockers refuses.
		if found, err := pr.ViewPR(m.Issue.Repo, m.Issue.Branch); err == nil {
			st.PR = found
		} else {
			st.PRUnknown = true
		}
	}
	st.SizeBytes = dirSize(ws)
	return st, nil
}

// inspectClone reads one clone's state. Every git probe that fails is recorded
// as an Unverifiable reason rather than dropped: a git error must block the reap,
// not masquerade as a clean, empty tree.
func inspectClone(git gitx.Runner, dir, p string) CloneState {
	c := CloneState{Dir: dir}

	if out, err := git.Run(p, nil, "status", "--porcelain"); err != nil {
		c.Unverifiable = append(c.Unverifiable, fmt.Sprintf("git status failed: %v", err))
	} else {
		c.Dirty = strings.TrimSpace(out) != ""
	}

	// The branch name is informational only, so a failure here is not a safety
	// signal and is not recorded as a blocker.
	if out, err := git.Run(p, nil, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		c.Branch = out
	}

	// Commits reachable from HEAD or any local branch but from no remote-tracking
	// branch. HEAD is named explicitly so a commit made on a detached HEAD -- on
	// no branch at all -- is counted too; a branch that was never pushed has all
	// of its commits counted.
	if out, err := git.Run(p, nil, "rev-list", "--count", "HEAD", "--branches", "--not", "--remotes"); err != nil {
		c.Unverifiable = append(c.Unverifiable, fmt.Sprintf("git rev-list failed: %v", err))
	} else if n, err := strconv.Atoi(strings.TrimSpace(out)); err != nil {
		c.Unverifiable = append(c.Unverifiable, fmt.Sprintf("could not read the unpushed-commit count: %v", err))
	} else {
		c.Unpushed = n
	}

	if out, err := git.Run(p, nil, "stash", "list"); err != nil {
		c.Unverifiable = append(c.Unverifiable, fmt.Sprintf("git stash list failed: %v", err))
	} else {
		c.Stashed = countLines(strings.TrimSpace(out))
	}

	return c
}

// countLines counts newline-separated entries in already-trimmed output.
func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
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
