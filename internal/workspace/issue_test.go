package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RiccardoCereghino/facet/internal/ghx"
	"github.com/RiccardoCereghino/facet/internal/gitx"
	"github.com/RiccardoCereghino/facet/internal/gitx/gitxtest"
	"github.com/RiccardoCereghino/facet/internal/manifest"
)

type fakePR struct{ pr *ghx.PR }

func (f fakePR) ViewPR(string, string) (*ghx.PR, error) { return f.pr, nil }

// fakePRErr models a lookup that could not run (auth expiry, network blip).
type fakePRErr struct{}

func (fakePRErr) ViewPR(string, string) (*ghx.PR, error) {
	return nil, errors.New("gh auth expired")
}

// fakeCommitPR answers the squash-merge landing proof's merged-PR-by-commit
// lookup (CommitPRLookup), reporting merged for exactly the shas in merged.
// It also implements ViewPR (returning nil, no open PR) so a single value can
// be passed as InspectIssue's pr argument and still be found by the
// CommitPRLookup type assertion inspectClone performs on it.
type fakeCommitPR struct{ merged map[string]bool }

func (fakeCommitPR) ViewPR(string, string) (*ghx.PR, error) { return nil, nil }

func (f fakeCommitPR) MergedPRForSHA(_, sha string) (*ghx.PRForCommit, error) {
	if f.merged[sha] {
		return &ghx.PRForCommit{Number: 1, MergedAt: "2026-01-01T00:00:00Z"}, nil
	}
	return nil, nil
}

// failGit fails every command, standing in for a repo whose state cannot be read
// (a held index.lock, a corrupt object store).
func failGit() *gitxtest.Runner {
	return &gitxtest.Runner{
		Fail: func([]string) bool { return true },
		Err:  errors.New("git unavailable"),
	}
}

// fetchFailGit delegates to the real git for everything except `fetch`, which
// always errors -- models an unreachable origin (offline clone, DNS failure,
// revoked credentials) without touching any other probe. Real is set so both
// Run and RunTimeout fall through to gitx.Git for every other command;
// inspectClone's type assertion to timeoutRunner finds gitxtest.Runner's own
// RunTimeout, which is what makes the `fetch` interception apply there too.
func fetchFailGit() *gitxtest.Runner {
	return &gitxtest.Runner{
		Real: gitx.Git{},
		Fail: func(args []string) bool { return len(args) > 0 && args[0] == "fetch" },
		Err:  errors.New("could not resolve host: origin"),
	}
}

// fakeLive stands in for a multiplexer that reports the queried session as live.
type fakeLive struct{ live bool }

func (f fakeLive) Live(string) bool { return f.live }

// fakeLiveRoots implements both LiveChecker and LiveRootChecker, the same
// combination mux.Tmux satisfies in production, so InspectIssue's type
// assertion to LiveRootChecker picks it up.
type fakeLiveRoots struct{ roots []string }

func (fakeLiveRoots) Live(string) bool { return false }

func (f fakeLiveRoots) LiveRoots(string) ([]string, error) { return f.roots, nil }

// issueWorkspace builds a real issue workspace: an origin repo, a clone of it,
// and a manifest carrying the issue block.
func issueWorkspace(t *testing.T) (ws string, clone string) {
	t.Helper()
	root := t.TempDir()
	origin := originRepo(t, filepath.Join(root, "origin"))
	ws = filepath.Join(root, "iss-repo-1-x")
	if err := os.MkdirAll(ws, 0o777); err != nil {
		t.Fatal(err)
	}
	m := &manifest.Manifest{
		Name:   "iss-repo-1-x",
		Clones: map[string]string{"repo": origin},
		Issue: &manifest.Issue{
			Repo: "o/repo", Number: 1, Branch: "1-x", Home: "repo",
		},
	}
	if err := m.Write(ws); err != nil {
		t.Fatal(err)
	}
	roots := testRoots(root)
	if err := Sync(roots, ws, gitx.Git{}, quiet(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	return ws, filepath.Join(ws, "repo")
}

// issueWorkspaceNoDefaultBranch builds an issue workspace whose origin has NO
// resolvable default branch: its own HEAD points at a branch that does not
// exist, and it has no `main`. That is the "hand-built remote, an old fetch
// config, a repo whose default is not main" shape remoteDefaultBranch's comment
// names, and it is the only one that survives inspectClone's fetch --
// `git remote set-head origin --delete` is undone by the next
// `git fetch --prune origin` on git 2.51.2, measured, so a clone doctored after
// the fact never reaches the branch under test (facet#80).
//
// The clone therefore has no checked-out branch either; callers make one.
func issueWorkspaceNoDefaultBranch(t *testing.T) (ws string, clone string) {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin")
	g := gitx.Git{}
	if err := os.MkdirAll(origin, 0o777); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "trunk"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
	} {
		if _, err := g.Run(origin, nil, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	if err := os.WriteFile(filepath.Join(origin, "README"), []byte("hello\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "-A"},
		{"commit", "-qm", "init"},
		{"symbolic-ref", "HEAD", "refs/heads/ghost"},
	} {
		if _, err := g.Run(origin, nil, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}

	ws = filepath.Join(root, "iss-repo-1-x")
	if err := os.MkdirAll(ws, 0o777); err != nil {
		t.Fatal(err)
	}
	m := &manifest.Manifest{
		Name:   "iss-repo-1-x",
		Clones: map[string]string{"repo": origin},
		Issue: &manifest.Issue{
			Repo: "o/repo", Number: 1, Branch: "1-x", Home: "repo",
		},
	}
	if err := m.Write(ws); err != nil {
		t.Fatal(err)
	}
	if err := Sync(testRoots(root), ws, g, quiet(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	return ws, filepath.Join(ws, "repo")
}

// facet#80: proofLanded's `def == ""` fail-safe had never executed, in four
// rounds of audit on facet#66 or anywhere since -- the only path in the landing
// proof that decides whether a workspace is reapable and had never run.
//
// This asserts the fixture actually reaches it. Without this, the end-to-end
// test below could pass for the wrong reason: a workspace refuses for any number
// of causes, and "the message changed" is not evidence the branch was entered.
func TestUnresolvableDefaultBranchIsTheFailSafesTrigger(t *testing.T) {
	_, clone := issueWorkspaceNoDefaultBranch(t)
	if def := remoteDefaultBranch(gitx.Git{}, clone); def != "" {
		t.Fatalf("remoteDefaultBranch = %q, want \"\" -- the fixture does not reach the fail-safe, so nothing below tests it", def)
	}
}

// And what reap prints when it fires is now a decision, not a side effect. The
// direction is unchanged -- an unresolvable default branch still holds the
// workspace, because nothing can be proven landed against a branch that cannot
// be named -- but an operator on such a remote was told only about unpushed
// commits, with no way to learn the real cause.
//
// The disclaimer rides the unpushed line, exactly as FetchStale's does, rather
// than becoming an Unverifiable entry: Unverifiable blocks even with zero
// unpushed commits, which would make every clean workspace on a repo like this
// unreapable. This surfaces only where the fail-safe actually cost something.
func TestUnresolvableDefaultBranchNamesTheCauseInTheRefusal(t *testing.T) {
	ws, clone := issueWorkspaceNoDefaultBranch(t)
	g := gitx.Git{}
	if _, err := g.Run(clone, nil, "checkout", "-qb", "1-x", "origin/trunk"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, "work.txt"), []byte("work\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "commit", "-qm", "work on an odd remote"); err != nil {
		t.Fatal(err)
	}

	st, err := InspectIssue(ws, g, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !hasBlocker(st, "unpushed") {
		t.Fatalf("nothing can be proven landed against an unresolvable default branch, so the work must still be held; blockers = %v", st.Blockers())
	}
	if !hasBlocker(st, "could not resolve origin's default branch") {
		t.Errorf("the refusal must name the actual cause, not just report unpushed commits; blockers = %v", st.Blockers())
	}
}

// The disclaimer must not appear where the fail-safe changed nothing. A clean
// workspace on the same odd remote has no unpushed candidates, so the default
// branch was never needed -- reporting it there would be noise on a reap that
// succeeds, and the FetchStale precedent it copies is silent for the same reason.
func TestUnresolvableDefaultBranchIsSilentOnACleanWorkspace(t *testing.T) {
	ws, clone := issueWorkspaceNoDefaultBranch(t)
	g := gitx.Git{}
	// A branch at origin/trunk with nothing on top. The checkout is needed
	// because this fixture's clone has no HEAD to speak of -- an unborn HEAD is
	// not a clean workspace, it is an unverifiable one, and reap rightly refuses
	// it for a different reason entirely.
	if _, err := g.Run(clone, nil, "checkout", "-qb", "1-x", "origin/trunk"); err != nil {
		t.Fatal(err)
	}
	st, err := InspectIssue(ws, g, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b := st.Blockers(); len(b) != 0 {
		t.Errorf("a clean workspace must still reap even when origin's default branch cannot be resolved, got %v", b)
	}
}

func TestInspectCleanWorkspaceIsReapable(t *testing.T) {
	ws, _ := issueWorkspace(t)
	st, err := InspectIssue(ws, gitx.Git{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b := st.Blockers(); len(b) != 0 {
		t.Errorf("a clean workspace should be reapable, got %v", b)
	}
	if st.SizeBytes == 0 {
		t.Error("size not measured")
	}
}

func TestUncommittedChangesBlockReap(t *testing.T) {
	ws, clone := issueWorkspace(t)
	if err := os.WriteFile(filepath.Join(clone, "README"), []byte("edited\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	st, err := InspectIssue(ws, gitx.Git{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !hasBlocker(st, "uncommitted") {
		t.Errorf("blockers = %v", st.Blockers())
	}
}

func TestUntrackedFileBlocksReap(t *testing.T) {
	ws, clone := issueWorkspace(t)
	if err := os.WriteFile(filepath.Join(clone, "scratch.txt"), []byte("x"), 0o666); err != nil {
		t.Fatal(err)
	}
	st, _ := InspectIssue(ws, gitx.Git{}, nil, nil)
	if !hasBlocker(st, "uncommitted") {
		t.Errorf("an untracked file must block: %v", st.Blockers())
	}
}

// The case that loses work: a branch that was never pushed, so it has no
// upstream at all. `git rev-list @{u}..HEAD` would error here; counting commits
// reachable from any branch but no remote catches it.
func TestUnpushedCommitsOnBranchWithNoUpstreamBlockReap(t *testing.T) {
	ws, clone := issueWorkspace(t)
	g := gitx.Git{}
	if _, err := g.Run(clone, nil, "checkout", "-qb", "never-pushed"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, "new.txt"), []byte("work\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "commit", "-qm", "precious"); err != nil {
		t.Fatal(err)
	}
	st, err := InspectIssue(ws, g, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !hasBlocker(st, "unpushed") {
		t.Fatalf("a never-pushed branch must block reap; blockers = %v", st.Blockers())
	}
	// And unpushed work must be reported before the merely-inconvenient reasons.
	if !strings.Contains(st.Blockers()[0], "unpushed") {
		t.Errorf("unpushed work should be the first blocker, got %q", st.Blockers()[0])
	}
}

// A clone that has never re-fetched since a PR branch was merged and deleted
// upstream must not judge that commit as unpushed just because its own
// origin/* refs are stale -- reap must fetch first. This reproduces a real
// teardown bug found in practice: the commit is landed on origin under a
// different branch name (never the checked-out default -- pushing onto that trips
// git's receive.denyCurrentBranch on the non-bare origin fixture) while the
// workspace clone's own remote-tracking refs are left exactly as they were
// at Sync time.
func TestStaleRemoteRefButCommitAlreadyUpstreamReapsClean(t *testing.T) {
	ws, clone := issueWorkspace(t)
	m, err := manifest.Read(ws)
	if err != nil {
		t.Fatal(err)
	}
	origin := m.Clones["repo"]

	g := gitx.Git{}
	if _, err := g.Run(clone, nil, "checkout", "-qb", "1-x"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, "landed.txt"), []byte("landed\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "commit", "-qm", "landed upstream"); err != nil {
		t.Fatal(err)
	}

	second := t.TempDir()
	if _, err := g.Run(second, nil, "clone", "-q", clone, second); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(second, nil, "remote", "set-url", "origin", origin); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(second, nil, "push", "-q", "origin", "HEAD:refs/heads/landed"); err != nil {
		t.Fatal(err)
	}

	// clone's own origin/* refs are still exactly what Sync left them at --
	// stale relative to what's now on origin -- until InspectIssue fetches.
	st, err := InspectIssue(ws, gitx.Git{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b := st.Blockers(); len(b) != 0 {
		t.Errorf("a commit already landed upstream must reap clean once refs are fetched fresh, got %v", b)
	}
}

// The bug facet#66 is about: GitHub squash-merges a PR, rewriting the sha, and
// auto-deletes the branch on merge. From the clone's perspective that is
// indistinguishable, by sha, from a commit that was never pushed at all -- no
// remote-tracking ref exists under any name that contains it. The landing
// proof (ancestor check, then merged-PR-by-commit, then tree-identical) is
// what tells the two apart, and this must reap clean without --force.
func TestSquashMergedBranchWithDeletedRemoteReapsClean(t *testing.T) {
	ws, clone := issueWorkspace(t)
	m, err := manifest.Read(ws)
	if err != nil {
		t.Fatal(err)
	}
	origin := m.Clones["repo"]
	g := gitx.Git{}

	if _, err := g.Run(clone, nil, "checkout", "-qb", "1-x"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, "landed.txt"), []byte("landed\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "commit", "-qm", "feature work"); err != nil {
		t.Fatal(err)
	}
	sha, err := g.Run(clone, nil, "rev-parse", "1-x")
	if err != nil {
		t.Fatal(err)
	}
	sha = strings.TrimSpace(sha)

	// Simulate the squash merge: the same content lands on origin's default
	// branch as a brand-new commit (different sha, different message), the
	// way "Squash and merge" on GitHub does it. The feature branch is never
	// pushed here at all -- after auto-delete-on-merge and a prune, a real
	// clone's view of the world is exactly this.
	patch, err := g.Run(clone, nil, "format-patch", "-1", "1-x", "--stdout")
	if err != nil {
		t.Fatal(err)
	}
	patchFile := filepath.Join(t.TempDir(), "squash.patch")
	if err := os.WriteFile(patchFile, []byte(patch+"\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(origin, nil, "am", patchFile); err != nil {
		t.Fatal(err)
	}
	// GitHub's squash-merge always synthesizes its own commit message (the PR
	// title plus its number), so amend it here too, the way real squash-merges
	// do -- the tree must match, the sha and message need not.
	if _, err := g.Run(origin, nil, "commit", "--amend", "-qm", "feature work (squash-merged, #1)"); err != nil {
		t.Fatal(err)
	}

	// The landing proof's second check needs a merged pull request whose head
	// was the local commit sha -- real GitHub records this regardless of how
	// the merge rewrote history on the base branch; the fake stands in for it.
	pr := fakeCommitPR{merged: map[string]bool{sha: true}}
	st, err := InspectIssue(ws, g, pr, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b := st.Blockers(); len(b) != 0 {
		t.Errorf("a squash-merged, content-landed branch must reap clean, got %v", b)
	}
	notes := st.Notes()
	if len(notes) == 0 || !strings.Contains(notes[0], "not held") {
		t.Errorf("a landed-but-rewritten commit must be reported, not silent: %v", notes)
	}
}

// Audit finding on facet!76, round 3: `git cherry` compares patch content per
// commit, so a multi-commit branch collapsed into ONE squash-merge commit has
// no individual commit whose patch matches the combined diff -- the fix that
// worked for a one-commit branch did nothing for the common case (the
// auditor's corpus: 11 of the last 20 merged facet PRs had more than one
// commit). The landing proof replaces per-commit comparison with an
// ancestor-or-merged-PR-or-identical-tree test on the branch tip, which does
// not care how many commits got there.
func TestMultiCommitSquashMergedBranchReapsClean(t *testing.T) {
	ws, clone := issueWorkspace(t)
	m, err := manifest.Read(ws)
	if err != nil {
		t.Fatal(err)
	}
	origin := m.Clones["repo"]
	g := gitx.Git{}

	if _, err := g.Run(clone, nil, "checkout", "-qb", "1-x"); err != nil {
		t.Fatal(err)
	}
	for i, line := range []string{"line 1", "line 2", "line 3"} {
		if err := os.WriteFile(filepath.Join(clone, "landed.txt"), []byte(strings.Join([]string{"line 1", "line 2", "line 3"}[:i+1], "\n")+"\n"), 0o666); err != nil {
			t.Fatal(err)
		}
		if _, err := g.Run(clone, nil, "add", "-A"); err != nil {
			t.Fatal(err)
		}
		if _, err := g.Run(clone, nil, "commit", "-qm", "feature work "+line); err != nil {
			t.Fatal(err)
		}
	}
	sha, err := g.Run(clone, nil, "rev-parse", "1-x")
	if err != nil {
		t.Fatal(err)
	}
	sha = strings.TrimSpace(sha)

	// Squash the branch's three commits into origin's default branch as ONE
	// commit -- the combined diff, one message, the way GitHub's "Squash and
	// merge" produces it.
	diff, err := g.Run(clone, nil, "diff", "main", "1-x")
	if err != nil {
		t.Fatal(err)
	}
	patchFile := filepath.Join(t.TempDir(), "squash.diff")
	if err := os.WriteFile(patchFile, []byte(diff+"\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(origin, nil, "apply", patchFile); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(origin, nil, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(origin, nil, "commit", "-qm", "feature work (squash-merged, #1)"); err != nil {
		t.Fatal(err)
	}

	pr := fakeCommitPR{merged: map[string]bool{sha: true}}
	st, err := InspectIssue(ws, g, pr, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b := st.Blockers(); len(b) != 0 {
		t.Errorf("a multi-commit branch squashed into one commit must reap clean, got %v", b)
	}
}

// Audit finding on facet!76, round 3: the harness's own workaround
// (`~/.stele/harness/lib/reap.sh`) exists because every auditor workspace is a
// DETACHED HEAD -- auditing checks out the audited sha, never a branch tip, so
// a squash-merged audit workspace is 100% of that traffic. The landing proof's
// third check (tree identical to def) is what a detached HEAD needs, same as
// a checked-out branch.
func TestDetachedHeadOnSquashMergedShaReapsClean(t *testing.T) {
	ws, clone := issueWorkspace(t)
	m, err := manifest.Read(ws)
	if err != nil {
		t.Fatal(err)
	}
	origin := m.Clones["repo"]
	g := gitx.Git{}

	if _, err := g.Run(clone, nil, "checkout", "-qb", "1-x"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, "landed.txt"), []byte("landed\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "commit", "-qm", "feature work"); err != nil {
		t.Fatal(err)
	}
	sha, err := g.Run(clone, nil, "rev-parse", "1-x")
	if err != nil {
		t.Fatal(err)
	}
	sha = strings.TrimSpace(sha)
	if _, err := g.Run(clone, nil, "checkout", "-q", "--detach", sha); err != nil {
		t.Fatal(err)
	}

	patch, err := g.Run(clone, nil, "format-patch", "-1", sha, "--stdout")
	if err != nil {
		t.Fatal(err)
	}
	patchFile := filepath.Join(t.TempDir(), "squash.patch")
	if err := os.WriteFile(patchFile, []byte(patch+"\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(origin, nil, "am", patchFile); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(origin, nil, "commit", "--amend", "-qm", "feature work (squash-merged, #1)"); err != nil {
		t.Fatal(err)
	}

	pr := fakeCommitPR{merged: map[string]bool{sha: true}}
	st, err := InspectIssue(ws, g, pr, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b := st.Blockers(); len(b) != 0 {
		t.Errorf("a detached HEAD at a squash-merged sha must reap clean, got %v", b)
	}
}

// Audit finding on facet!76: an auditor leaves working branches behind (e.g. a
// `pr183` branch pinned to a round-1 head that a later round superseded
// inside the same PR). That branch's tree is deliberately NOT identical to
// the default branch, so the head-shaped proof (which requires an identical
// tree) would wrongly refuse to clear it. A leftover, non-checked-out branch
// only needs to prove "this belonged to a merged PR" -- whatever superseded
// it merged too, so nothing is lost.
func TestLeftoverBranchBelongingToMergedPRIsNotCountedUnpushed(t *testing.T) {
	ws, clone := issueWorkspace(t)
	g := gitx.Git{}

	if _, err := g.Run(clone, nil, "checkout", "-qb", "pr183", "main"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, "round1.txt"), []byte("superseded\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "commit", "-qm", "round 1 (superseded)"); err != nil {
		t.Fatal(err)
	}
	sha, err := g.Run(clone, nil, "rev-parse", "pr183")
	if err != nil {
		t.Fatal(err)
	}
	sha = strings.TrimSpace(sha)

	// Back to main, checked out -- pr183 is a leftover, not the current ref.
	if _, err := g.Run(clone, nil, "checkout", "-q", "main"); err != nil {
		t.Fatal(err)
	}

	// This commit belonged to a merged PR, but origin/main never contains its
	// tree (round 2 replaced it inside the same PR) -- the point of the test.
	pr := fakeCommitPR{merged: map[string]bool{sha: true}}
	st, err := InspectIssue(ws, g, pr, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b := st.Blockers(); len(b) != 0 {
		t.Errorf("a leftover branch belonging to a merged PR must not block reap even though its tree differs from main, got %v", b)
	}
}

// Audit finding on facet!76: a sibling local branch that is fully pushed to
// its *own* remote branch (never merged to main -- an open PR on a second
// issue, say) has commits that are not on the default branch either. A naive
// check that runs the landing proof against every local branch unconditionally
// would treat those as unpushed the same as a genuinely unpushed commit; the
// per-ref rev-list --not --remotes pre-check (0 commits => skip) is what keeps
// this branch's already-pushed-elsewhere commits from being touched at all.
func TestSquashLandedBranchWithSiblingPushedElsewhereReapsClean(t *testing.T) {
	ws, clone := issueWorkspace(t)
	m, err := manifest.Read(ws)
	if err != nil {
		t.Fatal(err)
	}
	origin := m.Clones["repo"]
	g := gitx.Git{}

	if _, err := g.Run(clone, nil, "checkout", "-qb", "1-x"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, "landed.txt"), []byte("landed\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "commit", "-qm", "feature work"); err != nil {
		t.Fatal(err)
	}
	sha, err := g.Run(clone, nil, "rev-parse", "1-x")
	if err != nil {
		t.Fatal(err)
	}
	sha = strings.TrimSpace(sha)
	patch, err := g.Run(clone, nil, "format-patch", "-1", "1-x", "--stdout")
	if err != nil {
		t.Fatal(err)
	}
	patchFile := filepath.Join(t.TempDir(), "squash.patch")
	if err := os.WriteFile(patchFile, []byte(patch+"\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(origin, nil, "am", patchFile); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(origin, nil, "commit", "--amend", "-qm", "feature work (squash-merged, #1)"); err != nil {
		t.Fatal(err)
	}

	// The sibling: a second local branch, pushed to its own remote branch,
	// never merged into main.
	if _, err := g.Run(clone, nil, "checkout", "-qb", "other", "main"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, "other.txt"), []byte("other work\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "commit", "-qm", "unrelated open PR work"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "push", "-q", "origin", "other"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "checkout", "-q", "1-x"); err != nil {
		t.Fatal(err)
	}

	pr := fakeCommitPR{merged: map[string]bool{sha: true}}
	st, err := InspectIssue(ws, g, pr, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b := st.Blockers(); len(b) != 0 {
		t.Errorf("the squash-landed branch must still reap clean with an unrelated pushed sibling present, got %v", b)
	}
}

// The sibling of the test above: a branch whose remote is gone but whose
// content never landed anywhere must still refuse. Squash-merge detection
// must not become "an absent remote ref is safe" -- that is exactly the case
// the guard exists for (Deliberately not proposed, facet#66).
func TestBranchWithDeletedRemoteButNoLandedContentStillBlocks(t *testing.T) {
	ws, clone := issueWorkspace(t)
	g := gitx.Git{}
	if _, err := g.Run(clone, nil, "checkout", "-qb", "never-landed"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, "precious.txt"), []byte("work\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "commit", "-qm", "never merged anywhere"); err != nil {
		t.Fatal(err)
	}
	st, err := InspectIssue(ws, g, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !hasBlocker(st, "unpushed") {
		t.Fatalf("content that never landed upstream must still block reap; blockers = %v", st.Blockers())
	}
	if notes := st.Notes(); len(notes) != 0 {
		t.Errorf("nothing landed here; Notes() must stay empty, got %v", notes)
	}
}

// squashLand replays ref's tip commit onto origin's default branch as a brand-new
// commit -- different sha, GitHub's own synthesized message -- which is what
// "Squash and merge" leaves a clone looking at once the branch is auto-deleted.
// Returns the local sha, the one a merged-PR-by-commit lookup would report.
func squashLand(t *testing.T, g gitx.Git, clone, origin, ref string) string {
	t.Helper()
	sha, err := g.Run(clone, nil, "rev-parse", ref)
	if err != nil {
		t.Fatal(err)
	}
	sha = strings.TrimSpace(sha)
	patch, err := g.Run(clone, nil, "format-patch", "-1", ref, "--stdout")
	if err != nil {
		t.Fatal(err)
	}
	patchFile := filepath.Join(t.TempDir(), "squash.patch")
	if err := os.WriteFile(patchFile, []byte(patch+"\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(origin, nil, "am", patchFile); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(origin, nil, "commit", "--amend", "-qm", "feature work (squash-merged, #1)"); err != nil {
		t.Fatal(err)
	}
	return sha
}

// facet#77: the count is per-ref and summed, so one commit reachable from three
// local branches is reported three times. The shape is ordinary -- `git branch
// backup` before a rebase produces it -- and the number lands in the one message
// whose entire job is telling an operator how much work is at risk.
//
// Measured on facet!76: this fixture reports 3. `main`'s single
// `rev-list --count HEAD --branches --not --remotes` is a set and reports 1,
// which is the answer; the per-ref walk that replaced it lost that property.
func TestUnpushedCommitCountedOnceAcrossSeveralBranches(t *testing.T) {
	ws, clone := issueWorkspace(t)
	g := gitx.Git{}

	if _, err := g.Run(clone, nil, "checkout", "-qb", "never-pushed"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, "precious.txt"), []byte("work\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "commit", "-qm", "the only copy"); err != nil {
		t.Fatal(err)
	}
	// Two more refs at the very same tip: the backup-branch-before-a-rebase shape.
	for _, b := range []string{"copy1", "copy2"} {
		if _, err := g.Run(clone, nil, "branch", b, "never-pushed"); err != nil {
			t.Fatal(err)
		}
	}

	st, err := InspectIssue(ws, g, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !hasBlocker(st, "unpushed") {
		t.Fatalf("one genuinely unpushed commit must still block reap; blockers = %v", st.Blockers())
	}
	if got := st.Clone[0].Unpushed; got != 1 {
		t.Errorf("one commit reachable from three local branches is one commit at risk: Unpushed = %d, want 1", got)
	}
	if !hasBlocker(st, "1 unpushed commit(s)") {
		t.Errorf("the refusal must quote the true count; blockers = %v", st.Blockers())
	}
}

// The same overlap inflates SquashLanded, so the non-blocking note overstates
// too -- the note exists to stop an operator reading silence as "nothing to
// report", and a wrong number there teaches exactly the distrust it was added
// to remove.
func TestSquashLandedCommitCountedOnceAcrossSeveralBranches(t *testing.T) {
	ws, clone := issueWorkspace(t)
	m, err := manifest.Read(ws)
	if err != nil {
		t.Fatal(err)
	}
	origin := m.Clones["repo"]
	g := gitx.Git{}

	if _, err := g.Run(clone, nil, "checkout", "-qb", "1-x"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, "landed.txt"), []byte("landed\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "commit", "-qm", "feature work"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "branch", "backup", "1-x"); err != nil {
		t.Fatal(err)
	}
	sha := squashLand(t, g, clone, origin, "1-x")

	pr := fakeCommitPR{merged: map[string]bool{sha: true}}
	st, err := InspectIssue(ws, g, pr, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b := st.Blockers(); len(b) != 0 {
		t.Fatalf("a squash-landed branch with a backup at the same tip must still reap clean, got %v", b)
	}
	if got := st.Clone[0].SquashLanded; got != 1 {
		t.Errorf("one landed commit reachable from two local branches is one commit: SquashLanded = %d, want 1", got)
	}
	if notes := st.Notes(); len(notes) != 1 || !strings.Contains(notes[0], "1 local commit(s)") {
		t.Errorf("the note must quote the true count, got %v", notes)
	}
}

// The direction the deduplication must fail in. A commit reachable from BOTH a
// ref proven landed and a ref that is not must count as unpushed: the landed
// classification belongs to the ref, not to the commit, and the ref carrying
// more work has not proven anything about the commits it shares. Counting it
// landed would clear a blocker on a workspace that is genuinely the only copy
// of the commit stacked on top.
func TestCommitOnBothLandedAndUnpushedRefsCountsUnpushed(t *testing.T) {
	ws, clone := issueWorkspace(t)
	m, err := manifest.Read(ws)
	if err != nil {
		t.Fatal(err)
	}
	origin := m.Clones["repo"]
	g := gitx.Git{}

	if _, err := g.Run(clone, nil, "checkout", "-qb", "1-x"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, "landed.txt"), []byte("landed\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "commit", "-qm", "feature work"); err != nil {
		t.Fatal(err)
	}

	// A second ref carrying that same commit plus one more that landed nowhere.
	if _, err := g.Run(clone, nil, "checkout", "-qb", "stacked"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, "stacked.txt"), []byte("not landed\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "commit", "-qm", "stacked on top, landed nowhere"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "checkout", "-q", "1-x"); err != nil {
		t.Fatal(err)
	}
	sha := squashLand(t, g, clone, origin, "1-x")

	pr := fakeCommitPR{merged: map[string]bool{sha: true}}
	st, err := InspectIssue(ws, g, pr, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !hasBlocker(st, "unpushed") {
		t.Fatalf("the stacked commit is the only copy of itself; blockers = %v", st.Blockers())
	}
	if got := st.Clone[0].Unpushed; got != 2 {
		t.Errorf("both the shared commit and the one stacked on it are at risk: Unpushed = %d, want 2", got)
	}
	if got := st.Clone[0].SquashLanded; got != 0 {
		t.Errorf("a commit counted unpushed must not also be counted landed: SquashLanded = %d, want 0", got)
	}
	if notes := st.Notes(); len(notes) != 0 {
		t.Errorf("nothing is safely landed here; Notes() must stay empty, got %v", notes)
	}
}

// Audit finding on facet!76, round 2: `git cherry` silently omits merge
// commits from its output entirely -- neither "+" nor "-". A merge commit
// that is the sole unpushed candidate (both its parents already pushed to
// their own remote branches, never merged to main) is therefore never
// mentioned by any cherry invocation, and a design that starts from "not
// landed = whatever cherry marks +" treats an unmentioned candidate as
// landed by omission. That deleted the only copy of a hand-resolved merge
// conflict in the audit's reproduction. The fix flips the default: a
// candidate only counts as landed when cherry explicitly marks it "-";
// anything else, including silence, stays unpushed.
func TestUnpushedMergeCommitStillBlocksReap(t *testing.T) {
	ws, clone := issueWorkspace(t)
	g := gitx.Git{}

	if _, err := g.Run(clone, nil, "checkout", "-qb", "a", "main"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, "a.txt"), []byte("a\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "commit", "-qm", "a work"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "push", "-q", "origin", "a"); err != nil {
		t.Fatal(err)
	}

	if _, err := g.Run(clone, nil, "checkout", "-qb", "b", "main"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, "b.txt"), []byte("b\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "commit", "-qm", "b work"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "push", "-q", "origin", "b"); err != nil {
		t.Fatal(err)
	}

	if _, err := g.Run(clone, nil, "checkout", "-qb", "merged", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "merge", "--no-ff", "-q", "-m", "merge b into a", "b"); err != nil {
		t.Fatal(err)
	}

	st, err := InspectIssue(ws, g, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !hasBlocker(st, "unpushed") {
		t.Fatalf("the only copy of a merge commit must block reap even though both its parents are pushed elsewhere; blockers = %v", st.Blockers())
	}
	if notes := st.Notes(); len(notes) != 0 {
		t.Errorf("the merge commit never landed anywhere; Notes() must stay empty, got %v", notes)
	}
}

// A fetch failure alone -- no network, unreachable origin -- must not turn
// into a hard block on a clean clone; that would make reap useless offline.
func TestFetchFailureIsStalenessDisclaimerNotBlocker(t *testing.T) {
	ws, _ := issueWorkspace(t)
	st, err := InspectIssue(ws, fetchFailGit(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b := st.Blockers(); len(b) != 0 {
		t.Errorf("a failed fetch on an otherwise clean clone must not block, got %v", b)
	}
}

// Genuinely unpushed work must still refuse even when the fetch itself
// failed, and the blocker message must disclose that the count may be stale.
func TestFetchFailureDisclaimerOnGenuinelyUnpushedWork(t *testing.T) {
	ws, clone := issueWorkspace(t)
	g := gitx.Git{}
	if _, err := g.Run(clone, nil, "checkout", "-qb", "never-pushed"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, "new.txt"), []byte("work\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "commit", "-qm", "precious"); err != nil {
		t.Fatal(err)
	}
	st, err := InspectIssue(ws, fetchFailGit(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !hasBlocker(st, "unpushed") {
		t.Fatalf("genuinely unpushed work must still block reap when the fetch fails; blockers = %v", st.Blockers())
	}
	if !hasBlocker(st, "could not fetch") {
		t.Errorf("a failed fetch must be disclosed on the unpushed blocker; blockers = %v", st.Blockers())
	}
}

func TestOpenPRBlocksReapButMergedDoesNot(t *testing.T) {
	ws, _ := issueWorkspace(t)

	st, _ := InspectIssue(ws, gitx.Git{}, fakePR{&ghx.PR{Number: 9, State: "OPEN"}}, nil)
	if !hasBlocker(st, "still open") {
		t.Errorf("an open PR must block: %v", st.Blockers())
	}

	st, _ = InspectIssue(ws, gitx.Git{}, fakePR{&ghx.PR{Number: 9, State: "MERGED"}}, nil)
	if len(st.Blockers()) != 0 {
		t.Errorf("a merged PR must not block: %v", st.Blockers())
	}

	st, _ = InspectIssue(ws, gitx.Git{}, fakePR{nil}, nil)
	if len(st.Blockers()) != 0 {
		t.Errorf("no PR must not block: %v", st.Blockers())
	}
}

// A git command that fails during inspection must make the clone unverifiable
// and block the reap -- never be read as a clean, empty tree. This is the path
// that silently lost work before: a held index.lock makes `git status` exit
// non-zero, and the old code left Dirty=false.
func TestGitProbeFailureBlocksReap(t *testing.T) {
	ws, _ := issueWorkspace(t)
	st, err := InspectIssue(ws, failGit(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !hasBlocker(st, "cannot confirm") {
		t.Fatalf("a failed git probe must block reap; blockers = %v", st.Blockers())
	}
}

// A clone directory that exists but is not a valid repo may still hold edited
// files, so it must block reap rather than drop out of the accounting entirely.
func TestNonRepoCloneDirBlocksReap(t *testing.T) {
	ws, clone := issueWorkspace(t)
	if err := os.RemoveAll(filepath.Join(clone, ".git")); err != nil {
		t.Fatal(err) // leave the working tree, destroy the repo
	}
	st, err := InspectIssue(ws, gitx.Git{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !hasBlocker(st, "not a git repository") {
		t.Fatalf("a present non-repo clone must block reap; blockers = %v", st.Blockers())
	}
}

// Commits made on a detached HEAD belong to no branch; --branches alone would
// miss them. They are unpushed work all the same and must block reap.
func TestDetachedHeadCommitsBlockReap(t *testing.T) {
	ws, clone := issueWorkspace(t)
	g := gitx.Git{}
	if _, err := g.Run(clone, nil, "checkout", "-q", "--detach"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, "detached.txt"), []byte("work\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "commit", "-qm", "on a detached head"); err != nil {
		t.Fatal(err)
	}
	st, err := InspectIssue(ws, g, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !hasBlocker(st, "unpushed") {
		t.Fatalf("a detached-HEAD commit must block reap; blockers = %v", st.Blockers())
	}
}

// Stashed work is carried by no push and shown by no status; it must block reap.
func TestStashedWorkBlocksReap(t *testing.T) {
	ws, clone := issueWorkspace(t)
	g := gitx.Git{}
	if err := os.WriteFile(filepath.Join(clone, "README"), []byte("stash me\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Run(clone, nil, "stash"); err != nil {
		t.Fatal(err)
	}
	st, err := InspectIssue(ws, g, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !hasBlocker(st, "stash") {
		t.Fatalf("stashed work must block reap; blockers = %v", st.Blockers())
	}
}

// A pull-request lookup that errors is not proof there is no open PR; it must
// block reap, distinctly from a lookup that ran and found none (which does not).
func TestFailedPRLookupBlocksReap(t *testing.T) {
	ws, _ := issueWorkspace(t)
	st, err := InspectIssue(ws, gitx.Git{}, fakePRErr{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !hasBlocker(st, "pull-request state") {
		t.Fatalf("a failed PR lookup must block reap; blockers = %v", st.Blockers())
	}
}

// A live multiplexer session on this workspace's name is a soft blocker: the
// reap is "only rude" while attached, but must still refuse.
func TestLiveSessionBlocksReap(t *testing.T) {
	ws, _ := issueWorkspace(t)
	st, err := InspectIssue(ws, gitx.Git{}, nil, fakeLive{live: true})
	if err != nil {
		t.Fatal(err)
	}
	if !hasBlocker(st, "multiplexer session") {
		t.Fatalf("a live session must block reap; blockers = %v", st.Blockers())
	}
	// A dead session (or nil checker) must not.
	st, _ = InspectIssue(ws, gitx.Git{}, nil, fakeLive{live: false})
	if hasBlocker(st, "multiplexer session") {
		t.Errorf("a dead session must not block: %v", st.Blockers())
	}
	st, _ = InspectIssue(ws, gitx.Git{}, nil, nil)
	if hasBlocker(st, "multiplexer session") {
		t.Errorf("no checker must not block: %v", st.Blockers())
	}
}

// A tmux pane or process rooted in the workspace -- in any session, not only
// the one named after it -- must block reap the same way a live named session
// does, and must name the pane so the operator can kill it.
func TestLiveTmuxPaneBlocksReap(t *testing.T) {
	ws, _ := issueWorkspace(t)
	st, err := InspectIssue(ws, gitx.Git{}, nil, fakeLiveRoots{roots: []string{"tmux pane iss-x:1.0 (pid 4242) at " + ws}})
	if err != nil {
		t.Fatal(err)
	}
	if !hasBlocker(st, "kill it before reaping") {
		t.Errorf("Blockers() = %v; want the live tmux pane to block reap", st.Blockers())
	}
}

func TestNoLiveRootsDoesNotBlockReap(t *testing.T) {
	ws, _ := issueWorkspace(t)
	st, err := InspectIssue(ws, gitx.Git{}, nil, fakeLiveRoots{roots: nil})
	if err != nil {
		t.Fatal(err)
	}
	if hasBlocker(st, "kill it before reaping") {
		t.Errorf("Blockers() = %v; empty roots must not block", st.Blockers())
	}
	// A checker that implements only LiveChecker, not LiveRootChecker, must
	// not block either -- the assertion in InspectIssue must simply skip it.
	st, err = InspectIssue(ws, gitx.Git{}, nil, fakeLive{live: false})
	if err != nil {
		t.Fatal(err)
	}
	if hasBlocker(st, "kill it before reaping") {
		t.Errorf("Blockers() = %v; a checker without LiveRoots must not block", st.Blockers())
	}
}

func TestInspectRefusesNonIssueWorkspace(t *testing.T) {
	dir := t.TempDir()
	m := &manifest.Manifest{Name: "topical"}
	if err := m.Write(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectIssue(dir, gitx.Git{}, nil, nil); err == nil {
		t.Error("InspectIssue accepted a topical workspace")
	}
}

// Reap must delete a git repository despite git's read-only object files, which
// defeat a plain os.RemoveAll on Windows.
func TestReapDeletesReadOnlyGitObjects(t *testing.T) {
	ws, clone := issueWorkspace(t)
	// Confirm the fixture really has read-only objects to trip over.
	var sawReadOnly bool
	_ = filepath.Walk(filepath.Join(clone, ".git", "objects"), func(_ string, fi os.FileInfo, err error) error {
		if err == nil && fi != nil && !fi.IsDir() && fi.Mode().Perm()&0o200 == 0 {
			sawReadOnly = true
		}
		return nil
	})
	if !sawReadOnly {
		t.Log("note: no read-only objects in this fixture; the test is weaker than intended")
	}
	st, err := InspectIssue(ws, gitx.Git{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := Reap(st); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if _, err := os.Stat(ws); !os.IsNotExist(err) {
		t.Error("workspace survived Reap")
	}
}

func hasBlocker(st *IssueState, substr string) bool {
	for _, b := range st.Blockers() {
		if strings.Contains(b, substr) {
			return true
		}
	}
	return false
}

// reap is documented as "run it from inside the workspace". Windows refuses to
// remove a directory that is a process's working directory, so Reap must step
// out first -- otherwise the tree is half-deleted and the error blames "another
// process", after the clones are already gone.
func TestReapFromInsideTheWorkspace(t *testing.T) {
	ws, _ := issueWorkspace(t)
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	if err := os.Chdir(ws); err != nil {
		t.Fatal(err)
	}
	st, err := InspectIssue(ws, gitx.Git{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := Reap(st); err != nil {
		t.Fatalf("Reap from inside the workspace: %v", err)
	}
	if _, err := os.Stat(ws); !os.IsNotExist(err) {
		t.Error("workspace survived Reap")
	}
}

// Reaping a subdirectory of the workspace (a clone) must also work.
func TestReapFromInsideAClone(t *testing.T) {
	ws, clone := issueWorkspace(t)
	orig, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(orig) })
	if err := os.Chdir(clone); err != nil {
		t.Fatal(err)
	}
	st, err := InspectIssue(ws, gitx.Git{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := Reap(st); err != nil {
		t.Fatalf("Reap from inside a clone: %v", err)
	}
	if _, err := os.Stat(ws); !os.IsNotExist(err) {
		t.Error("workspace survived Reap")
	}
}

// TestRemoveAllForceLeavesSymlinkTargetsAlone pins the one thing removeAllForce
// must never do: reach outside the tree it is deleting.
//
// filepath.WalkDir stats with Lstat, so a symlink arrives with IsDir() == false
// and lands in the same branch as a regular file; os.Chmod then FOLLOWS it, so
// the mode is applied to the target. A seat workspace holds
// .claude/skills/<skill> pointing into the live harness at $STELE_HOME, and
// every reap therefore left that directory at 0666 -- a directory with no
// execute bit, which cannot be traversed, so neither a seat nor git could read
// or check out the live tree afterwards. Repaired by hand twice before it was
// root-caused (facet#87, stele-home#16).
//
// The assertion is on the TARGET's mode, not on reap succeeding: reap succeeded
// every single time this fired. The damage was always somewhere else.
func TestRemoveAllForceLeavesSymlinkTargetsAlone(t *testing.T) {
	base := t.TempDir()

	target := filepath.Join(base, "live", "skills", "seat-exit")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Without this the failing run leaves an untraversable directory behind and
	// t.TempDir's own cleanup fails -- the exact symptom, one layer up.
	t.Cleanup(func() { _ = os.Chmod(target, 0o755) })

	ws := filepath.Join(base, "ws")
	if err := os.MkdirAll(filepath.Join(ws, ".claude", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(ws, ".claude", "skills", "seat-exit")); err != nil {
		t.Fatal(err)
	}

	if err := removeAllForce(ws); err != nil {
		t.Fatalf("removeAllForce: %v", err)
	}
	if _, err := os.Stat(ws); !os.IsNotExist(err) {
		t.Error("workspace survived removeAllForce")
	}

	fi, err := os.Stat(target)
	if err != nil {
		t.Fatalf("symlink target is gone: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o755 {
		t.Errorf("symlink target mode = %04o, want 0755: removeAllForce chmod'd through the link", got)
	}
}

func TestIsUnder(t *testing.T) {
	root := filepath.Join("a", "b")
	cases := map[string]bool{
		filepath.Join("a", "b"):      true,
		filepath.Join("a", "b", "c"): true,
		filepath.Join("a"):           false,
		filepath.Join("a", "bb"):     false,
		filepath.Join("x"):           false,
	}
	for p, want := range cases {
		if got := isUnder(root, p); got != want {
			t.Errorf("isUnder(%q, %q) = %v, want %v", root, p, got, want)
		}
	}
}
