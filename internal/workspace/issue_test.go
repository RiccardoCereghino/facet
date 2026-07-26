package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RiccardoCereghino/facet/internal/ghx"
	"github.com/RiccardoCereghino/facet/internal/gitx"
	"github.com/RiccardoCereghino/facet/internal/manifest"
)

type fakePR struct{ pr *ghx.PR }

func (f fakePR) ViewPR(string, string) (*ghx.PR, error) { return f.pr, nil }

// fakePRErr models a lookup that could not run (auth expiry, network blip).
type fakePRErr struct{}

func (fakePRErr) ViewPR(string, string) (*ghx.PR, error) {
	return nil, errors.New("gh auth expired")
}

// failGit fails every command, standing in for a repo whose state cannot be read
// (a held index.lock, a corrupt object store).
type failGit struct{}

func (failGit) Run(string, []string, ...string) (string, error) {
	return "", errors.New("git unavailable")
}

// fetchFailGit delegates to the real git for everything except `fetch`, which
// always errors -- models an unreachable origin (offline clone, DNS failure,
// revoked credentials) without touching any other probe.
type fetchFailGit struct{ gitx.Git }

func (f fetchFailGit) Run(dir string, env []string, args ...string) (string, error) {
	if len(args) > 0 && args[0] == "fetch" {
		return "", errors.New("could not resolve host: origin")
	}
	return f.Git.Run(dir, env, args...)
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
// origin/* refs are stale -- reap must fetch first. This reproduces the
// mandate-3 teardown bug: the commit is landed on origin under a different
// branch name (never the checked-out default -- pushing onto that trips
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

// A fetch failure alone -- no network, unreachable origin -- must not turn
// into a hard block on a clean clone; that would make reap useless offline.
func TestFetchFailureIsStalenessDisclaimerNotBlocker(t *testing.T) {
	ws, _ := issueWorkspace(t)
	st, err := InspectIssue(ws, fetchFailGit{}, nil, nil)
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
	st, err := InspectIssue(ws, fetchFailGit{}, nil, nil)
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
	st, err := InspectIssue(ws, failGit{}, nil, nil)
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
	_ = filepath.Walk(filepath.Join(clone, ".git", "objects"), func(p string, fi os.FileInfo, err error) error {
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
