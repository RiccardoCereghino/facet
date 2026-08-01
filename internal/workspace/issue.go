package workspace

import (
	"errors"
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
	// remote has *and* whose content is not proven to have already landed on
	// the remote's default branch (see proofLanded). This deliberately covers
	// branches with no upstream at all, and commits made on a detached HEAD:
	// those are the ones most easily lost. The landing proof is what keeps a
	// squash-merged, auto-deleted branch from being counted here: squash
	// rewrites the sha, so a sha-only reachability test can never see it.
	Unpushed int
	// SquashLanded counts commits that failed the sha-based check above --
	// Unpushed would otherwise have included them -- but were proven, by
	// proofLanded, to already be on the remote's default branch under a
	// different sha. Purely informational: these are excluded from Unpushed
	// and never block reap, but staying silent about them is what taught
	// operators to reach for --force (facet#66).
	SquashLanded int
	// FetchStale is set when `git fetch --prune origin` failed, or did not
	// finish within fetchTimeout, before Unpushed was computed. The count above
	// is still computed and used against whatever refs are on disk -- refusing
	// reap on every offline run would make it useless without a network -- but
	// Blockers appends a disclaimer to the unpushed-commit line whenever this is
	// set and there are unpushed commits to disclaim.
	FetchStale bool
	// DefaultBranchUnresolved is set when origin's default branch could not be
	// determined -- no refs/remotes/origin/HEAD and no origin/main, which is a
	// hand-built remote, an old fetch config, or a repo whose default is not
	// main -- *and* there were unpushed candidates that needed it. Nothing can
	// be proven to have landed on a branch that cannot be named, so proofLanded
	// fails safe and every candidate is counted unpushed. Without this the
	// operator is told about unpushed commits and never about the real cause
	// (facet#80). Like FetchStale it is a disclaimer on that line and never a
	// blocker of its own: it is set only when the fail-safe actually decided
	// something, so a clean workspace on such a remote still reaps.
	DefaultBranchUnresolved bool
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
			if c.DefaultBranchUnresolved {
				msg += " (could not resolve origin's default branch -- work already landed there cannot be told from unpushed work here)"
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

// CommitPRLookup finds the merged pull request that had a specific commit sha,
// directly -- unlike PRLookup, which resolves by branch name and goes blind
// the moment GitHub deletes the branch on merge. Optional on whatever PRLookup
// is passed to InspectIssue: a lookup that does not implement it is simply not
// asked, and the squash-merge landing proof falls back to the ancestor check
// alone (see proofLanded).
type CommitPRLookup interface {
	MergedPRForSHA(repo, sha string) (*ghx.PRForCommit, error)
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
		st.Clone = append(st.Clone, inspectClone(git, dir, p, m.Issue.Repo, pr))
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
// not masquerade as a clean, empty tree. repo is the issue's home repo
// ("owner/name"), used for the squash-merge landing proof's PR lookup; pr may
// be nil to skip that lookup (the ancestor check alone still runs).
func inspectClone(git gitx.Runner, dir, p, repo string, pr PRLookup) CloneState {
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
	// of its commits counted. This is a cheap fast-path check: the common case
	// is 0, and only then is the more expensive per-ref landing proof run.
	if out, err := git.Run(p, nil, "rev-list", "--count", "HEAD", "--branches", "--not", "--remotes"); err != nil {
		c.Unverifiable = append(c.Unverifiable, fmt.Sprintf("git rev-list failed: %v", err))
	} else if _, err := strconv.Atoi(strings.TrimSpace(out)); err != nil {
		c.Unverifiable = append(c.Unverifiable, fmt.Sprintf("could not read the unpushed-commit count: %v", err))
	} else if strings.TrimSpace(out) != "0" {
		// A sha not reachable from any remote is not the same question as
		// "is this content already upstream": squash-merge rewrites the sha,
		// and GitHub's auto-delete-on-merge then removes the only ref that
		// could have proven it by name. Prove landing per ref instead.
		var commitPR CommitPRLookup
		if pr != nil {
			commitPR, _ = pr.(CommitPRLookup)
		}
		if unpushed, landed, defUnresolved, err := unpushedLanding(git, p, repo, commitPR); err != nil {
			c.Unverifiable = append(c.Unverifiable, fmt.Sprintf("could not verify unpushed commits: %v", err))
		} else {
			c.Unpushed = unpushed
			c.SquashLanded = landed
			c.DefaultBranchUnresolved = defUnresolved
		}
	}

	if out, err := git.Run(p, nil, "stash", "list"); err != nil {
		c.Unverifiable = append(c.Unverifiable, fmt.Sprintf("git stash list failed: %v", err))
	} else {
		c.Stashed = countLines(strings.TrimSpace(out))
	}

	return c
}

// unpushedLanding walks every local ref -- each branch tip, plus a detached
// HEAD -- and decides, per ref, whether its not-yet-remote commits are
// genuinely unpushed or have already landed on the remote's default branch
// under a different sha (the squash-merge, auto-delete-on-merge shape
// facet#66 is about). It replaces an earlier per-commit patch-identity design
// (`git cherry`): that design could never recognize a multi-commit branch
// squashed into a single commit, because no individual commit's patch matches
// the combined diff -- an audited defect that made the fix work only for
// one-commit branches, the minority of this fleet's own PRs.
//
// Ported from the fleet's own harness workaround, which had already reached
// this design under real, repeated failures:
// `~/.stele/harness/lib/reap.sh` ("AUDITIONING FOR: facet#66").
//
// repo is the issue's home repo, needed for the merged-PR lookup; commitPR may
// be nil, in which case only the ancestor check runs and a squash-merge is not
// detected (fails safe toward "unpushed", never toward silently clearing a
// blocker).
//
// The counts are over DISTINCT COMMITS, not a sum of per-ref answers. A commit
// reachable from more than one local branch -- `git branch backup` before a
// rebase is enough -- was counted once per branch, so a workspace holding the
// only copy of one commit was told three commits were at risk (facet#77). The
// classification is still per ref, because "landed" is a property of the ref
// that was proven, not of an individual sha; only the accounting is a set.
func unpushedLanding(git gitx.Runner, dir, repo string, commitPR CommitPRLookup) (unpushed, landed int, defUnresolved bool, err error) {
	def := remoteDefaultBranch(git, dir)

	current, detached, err := currentRef(git, dir)
	if err != nil {
		return 0, 0, false, err
	}
	refsOut, err := git.Run(dir, nil, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return 0, 0, false, err
	}
	refs := strings.Fields(refsOut)
	if detached {
		refs = append(refs, "HEAD")
	}

	unpushedShas := map[string]struct{}{}
	landedShas := map[string]struct{}{}
	for _, ref := range refs {
		out, err := git.Run(dir, nil, "rev-list", ref, "--not", "--remotes")
		if err != nil {
			return 0, 0, false, err
		}
		shas := strings.Fields(out)
		if len(shas) == 0 {
			// This ref's commits are all already reachable from some remote
			// (pushed to its own branch, say) -- nothing to prove for it, and
			// nothing to add.
			continue
		}
		// A candidate exists, so the landing proof genuinely needed def. An
		// unresolvable default branch is only worth disclosing once it has
		// actually decided something (facet#80).
		defUnresolved = def == ""
		shaOut, err := git.Run(dir, nil, "rev-parse", ref)
		if err != nil {
			return 0, 0, false, err
		}
		tip := strings.TrimSpace(shaOut)
		isHead := ref == current || (detached && ref == "HEAD")
		into := unpushedShas
		if proofLanded(git, dir, tip, repo, def, commitPR, isHead) {
			into = landedShas
		}
		for _, s := range shas {
			into[s] = struct{}{}
		}
	}

	// A commit reachable from both a ref proven landed and one that is not
	// counts as UNPUSHED. The landing proof ran against the other ref's tip and
	// says nothing about a ref carrying further work on top, so the overlap is
	// resolved in the direction that holds the workspace -- and it keeps
	// SquashLanded's contract true: those commits are excluded from Unpushed,
	// never reported under both.
	for s := range unpushedShas {
		delete(landedShas, s)
	}
	return len(unpushedShas), len(landedShas), defUnresolved, nil
}

// proofLanded answers whether sha's content has already landed on def (the
// remote's resolved default branch, "" if unresolvable). Three independent
// checks; isHead selects which apply.
//
// isHead is true for the checked-out ref (a branch, or a detached HEAD -- the
// shape every auditor workspace is in, since auditing checks out the audited
// sha rather than a branch tip) and requires all three checks. isHead is false
// for a leftover local branch the workspace happens to still be carrying
// (an auditor's own working branch, say); that only needs the first two,
// because its tree is allowed to differ from def -- a later round inside the
// same PR can supersede it, and "tree identical to def" would then wrongly
// refuse a correct deletion.
func proofLanded(git gitx.Runner, dir, sha, repo, def string, commitPR CommitPRLookup, isHead bool) bool {
	if def == "" {
		return false
	}

	// (a) sha is already an ancestor of def -- no squash happened, and there
	// is nothing left to prove.
	if _, err := git.Run(dir, nil, "merge-base", "--is-ancestor", sha, def); err == nil {
		return true
	}

	// (b) a MERGED pull request had sha as a commit. With no lookup available,
	// nothing beyond (a) can be proven, and that is the safe direction to
	// fail in: the commit stays counted as unpushed.
	if commitPR == nil {
		return false
	}
	merged, err := commitPR.MergedPRForSHA(repo, sha)
	if err != nil || merged == nil {
		return false
	}
	if !isHead {
		return true
	}

	// (c) the checked-out ref's tree must be IDENTICAL to def. A merged PR
	// whose file then diverged on def would still be a loss, and (b) alone
	// would have called it safe.
	diff, err := git.Run(dir, nil, "diff", "--stat", sha, def)
	if err != nil {
		return false
	}
	return strings.TrimSpace(diff) == ""
}

// currentRef reports the checked-out branch name, or detached=true if HEAD is
// not on any branch.
func currentRef(git gitx.Runner, dir string) (branch string, detached bool, err error) {
	out, err := git.Run(dir, nil, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		// Exit 1 here means "detached", not a failure -- HEAD simply is not a
		// symbolic ref. Anything else is a genuine problem reading the repo.
		var gitErr *gitx.Error
		if errors.As(err, &gitErr) && gitErr.ExitCode == 1 {
			return "", true, nil
		}
		return "", false, err
	}
	return strings.TrimSpace(out), false, nil
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
//
// REGULAR FILES ONLY, and that is the whole point of the condition. WalkDir
// stats with Lstat, so a symlink arrives with IsDir() == false -- and os.Chmod
// FOLLOWS symlinks, so `!d.IsDir()` applied the mode to the target, outside the
// tree being deleted. A seat workspace links .claude/skills/<skill> into the
// live harness, so every reap left that directory at 0666: no execute bit, so
// it could not be traversed, so neither a seat nor git could read the live tree
// afterwards (facet#87, stele-home#16).
//
// os.Lchmod does not exist in the standard library, and a symlink has no
// read-only attribute of its own that would block the removal -- so skipping is
// both the only correct handling and the one that costs nothing. Sockets, fifos
// and devices are skipped for the same reason: the attribute this clears is one
// git sets on regular object and pack files.
func removeAllForce(root string) error {
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // keep going; the remove below will report what matters
		}
		if d.Type().IsRegular() {
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
