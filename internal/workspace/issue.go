package workspace

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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
	// remote has *and* that are not, by patch content, already on the remote's
	// default branch under a different sha. This deliberately covers branches
	// with no upstream at all, and commits made on a detached HEAD: those are
	// the ones most easily lost. The patch-content exception is what keeps a
	// squash-merged, auto-deleted branch from being counted here: squash
	// rewrites the sha, so a sha-only reachability test can never see it.
	Unpushed int
	// SquashLanded counts commits that failed the sha-based check above --
	// Unpushed would otherwise have included them -- but were confirmed, by
	// patch-id (`git cherry`), to already be on the remote's default branch
	// under a different sha. Purely informational: these are excluded from
	// Unpushed and never block reap, but staying silent about them is what
	// taught operators to reach for --force (facet#66).
	SquashLanded int
	// FetchStale is set when `git fetch --prune origin` failed, or did not
	// finish within fetchTimeout, before Unpushed was computed. The count above
	// is still computed and used against whatever refs are on disk -- refusing
	// reap on every offline run would make it useless without a network -- but
	// Blockers appends a disclaimer to the unpushed-commit line whenever this is
	// set and there are unpushed commits to disclaim.
	FetchStale bool
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
	// SessionLive means a multiplexer session of this name is running.
	SessionLive bool
	// LiveRoots names tmux panes or processes -- in any session, not only the
	// one above -- whose current working directory is rooted here. Non-empty
	// is itself a blocker: deleting a directory out from under a running
	// process is silent corruption, not a merely premature reap.
	LiveRoots []string
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
			msg := fmt.Sprintf("%s has %d unpushed commit(s) -- this workspace is their only copy", c.Dir, c.Unpushed)
			if c.FetchStale {
				msg += " (could not fetch origin first -- a branch merged and deleted upstream may be miscounted here)"
			}
			out = append(out, msg)
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
	if s.SessionLive {
		out = append(out, "a multiplexer session is attached")
	}
	for _, r := range s.LiveRoots {
		out = append(out, fmt.Sprintf("%s -- kill it before reaping", r))
	}
	return out
}

// Notes lists non-blocking information about a workspace's state -- things
// worth saying so an operator does not read silence as "nothing to report"
// (facet#66). Nothing here belongs in Blockers(): every entry is something
// that does *not* hold the workspace.
func (s *IssueState) Notes() []string {
	var out []string
	for _, c := range s.Clone {
		if c.SquashLanded > 0 {
			out = append(out, fmt.Sprintf(
				"%s: %d local commit(s) already landed upstream under a different sha (squash-merged) -- not held",
				c.Dir, c.SquashLanded))
		}
	}
	return out
}

// LiveChecker reports whether a multiplexer session is running. It is an
// interface so the reap logic is testable without a multiplexer.
type LiveChecker interface{ Live(session string) bool }

// LiveRootChecker reports tmux panes or processes, in any session, whose
// current working directory is rooted inside a workspace -- unlike
// LiveChecker, this is not limited to the one session named after the
// workspace, so a pane moved elsewhere (tmux's link-window does exactly
// this) is still caught. Implemented optionally by whatever LiveChecker is
// passed to InspectIssue; one that does not implement it is simply not
// asked, the same way a missing PaneReader is skipped in package mux.
type LiveRootChecker interface {
	LiveRoots(dir string) ([]string, error)
}

// PRLookup finds the pull request for a branch. Nil means do not look.
type PRLookup interface {
	ViewPR(repo, branch string) (*ghx.PR, error)
}

// timeoutRunner is implemented by gitx.Git for operations that must not hang
// the caller forever -- a fetch against a blackholed connection or captive
// portal never returns on its own. Optional, the same way LiveRootChecker is
// optional on LiveChecker above: a Runner fake that does not implement it
// simply runs the fetch unbounded, since fakes model failure, not a hang.
type timeoutRunner interface {
	RunTimeout(dir string, env []string, timeout time.Duration, args ...string) (string, error)
}

// fetchTimeout bounds the pre-unpushed-check fetch. Long enough for a real,
// slow-but-working origin; short enough that a hung one degrades reap/issues/
// attach to a disclaimer instead of blocking them indefinitely.
const fetchTimeout = 15 * time.Second

// InspectIssue gathers the state of one issue workspace. Network and multiplexer
// lookups are optional: pass nil to skip them.
func InspectIssue(ws string, git gitx.Runner, pr PRLookup, mux LiveChecker) (*IssueState, error) {
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
	if mux != nil {
		st.SessionLive = mux.Live(m.Name)
	}
	if lr, ok := mux.(LiveRootChecker); ok {
		if roots, err := lr.LiveRoots(ws); err == nil {
			st.LiveRoots = roots
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

	// Refresh remote-tracking refs before judging unpushed-ness: a clone that
	// hasn't talked to origin since a PR branch was merged and deleted upstream
	// would otherwise judge those commits against a stale origin/* and count
	// them as this workspace's only copy. Bounded to fetchTimeout wherever the
	// Runner supports it: a fast failure (no network, unreachable origin) and a
	// hung one (blackholed connection, captive portal) are both not fatal --
	// the count below still runs against whatever refs are on disk -- but both
	// are recorded so Blockers can disclose them.
	if tr, ok := git.(timeoutRunner); ok {
		if _, err := tr.RunTimeout(p, nil, fetchTimeout, "fetch", "--prune", "origin"); err != nil {
			c.FetchStale = true
		}
	} else if _, err := git.Run(p, nil, "fetch", "--prune", "origin"); err != nil {
		c.FetchStale = true
	}

	// Commits reachable from HEAD or any local branch but from no remote-tracking
	// branch. HEAD is named explicitly so a commit made on a detached HEAD -- on
	// no branch at all -- is counted too; a branch that was never pushed has all
	// of its commits counted.
	if out, err := git.Run(p, nil, "rev-list", "--count", "HEAD", "--branches", "--not", "--remotes"); err != nil {
		c.Unverifiable = append(c.Unverifiable, fmt.Sprintf("git rev-list failed: %v", err))
	} else if n, err := strconv.Atoi(strings.TrimSpace(out)); err != nil {
		c.Unverifiable = append(c.Unverifiable, fmt.Sprintf("could not read the unpushed-commit count: %v", err))
	} else if n > 0 {
		// A sha not reachable from any remote is not the same question as
		// "is this content already upstream": squash-merge rewrites the sha,
		// and GitHub's auto-delete-on-merge then removes the only ref that
		// could have proven it by name. Check by patch content instead.
		notLanded := unpushedNotLanded(git, p, n)
		c.SquashLanded = n - notLanded
		c.Unpushed = notLanded
	}

	if out, err := git.Run(p, nil, "stash", "list"); err != nil {
		c.Unverifiable = append(c.Unverifiable, fmt.Sprintf("git stash list failed: %v", err))
	} else {
		c.Stashed = countLines(strings.TrimSpace(out))
	}

	return c
}

// unpushedNotLanded narrows a sha-based unpushed count down to commits that
// are not, by patch content, already present on the remote's default branch.
// unpushed is the count `rev-list --not --remotes` already found; it is
// returned unchanged whenever the check below cannot run, so a git failure
// here fails safe toward the original, more conservative count -- never
// toward silently clearing a blocker.
func unpushedNotLanded(git gitx.Runner, dir string, unpushed int) int {
	def := remoteDefaultBranch(git, dir)
	if def == "" {
		return unpushed
	}
	refsOut, err := git.Run(dir, nil, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return unpushed
	}
	refs := strings.Fields(refsOut)
	refs = append(refs, "HEAD")

	notLanded := map[string]bool{}
	for _, ref := range refs {
		// git cherry <upstream> <head> partitions commits reachable from head
		// but not from upstream by sha into "+" (no equivalent patch found
		// upstream) and "-" (an equivalent patch already exists upstream,
		// under some other sha) -- exactly the squash-merge shape.
		out, err := git.Run(dir, nil, "cherry", def, ref)
		if err != nil {
			// Can't verify this ref's commits patch-wise (e.g. no common
			// history with def). Trust the original, more conservative count
			// rather than guess.
			return unpushed
		}
		for _, line := range strings.Split(out, "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			if fields[0] == "+" {
				notLanded[fields[1]] = true
			}
		}
	}
	return len(notLanded)
}

// remoteDefaultBranch resolves origin's default branch as "origin/<name>", or
// "" if it cannot be determined -- in which case callers must not guess.
func remoteDefaultBranch(git gitx.Runner, dir string) string {
	if out, err := git.Run(dir, nil, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if s := strings.TrimSpace(out); s != "" {
			return s
		}
	}
	// refs/remotes/origin/HEAD is set by a normal clone, but is not
	// guaranteed (a hand-built remote, an old fetch config). "main" is the
	// fleet's actual default (the branch ruling, D29-family), not a bare
	// guess, so fall back to it if it exists.
	if _, err := git.Run(dir, nil, "rev-parse", "--verify", "refs/remotes/origin/main"); err == nil {
		return "origin/main"
	}
	return ""
}

// countLines counts newline-separated entries in already-trimmed output.
func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// ListIssues inspects every issue workspace under the workspaces root.
func ListIssues(roots config.Roots, git gitx.Runner, pr PRLookup, mux LiveChecker) ([]*IssueState, error) {
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
		st, err := InspectIssue(dir, git, pr, mux)
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
