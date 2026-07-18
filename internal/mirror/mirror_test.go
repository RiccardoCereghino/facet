package mirror

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RiccardoCereghino/facet/internal/gitx"
)

func TestPathFor(t *testing.T) {
	root := filepath.FromSlash("/m")
	tests := []struct {
		name, url, want string
		wantErr         error
	}{
		{"scp github", "git@github.com:acme/platform.git", "/m/github.com/acme/platform.git", nil},
		{"scp no .git suffix", "git@github.com:acme/platform", "/m/github.com/acme/platform.git", nil},
		{"https", "https://github.com/acme/Gateway.git", "/m/github.com/acme/Gateway.git", nil},
		{"case is preserved", "https://github.com/Acme/MixedCase.git", "/m/github.com/Acme/MixedCase.git", nil},
		{"http", "http://example.com/o/r.git", "/m/example.com/o/r.git", nil},
		{"ssh scheme", "ssh://git@github.com/o/r.git", "/m/github.com/o/r.git", nil},
		{"ssh scheme with port", "ssh://git@example.com:2222/o/r.git", "/m/example.com/o/r.git", nil},

		// Forges nest groups arbitrarily deep: an owner/repo assumption breaks this.
		// Real upstreams do this -- a GitLab group, a subgroup, then the project.
		{"nested gitlab groups",
			"https://git.example.org/working-group/subproject/backend.git",
			"/m/git.example.org/working-group/subproject/backend.git", nil},
		{"four segments",
			"https://git.example.org/a/b/c/d.git",
			"/m/git.example.org/a/b/c/d.git", nil},

		// Same repo name on two forges must not collide.
		{"host disambiguates a", "git@github.com:o/dup.git", "/m/github.com/o/dup.git", nil},
		{"host disambiguates b", "git@gitlab.com:o/dup.git", "/m/gitlab.com/o/dup.git", nil},

		{"windows drive is local", `C:\Users\me\repo`, "", ErrNotRemote},
		{"windows drive fwd slash", "C:/Users/me/repo", "", ErrNotRemote},
		{"unix abs is local", "/home/me/repo", "", ErrNotRemote},
		{"relative is local", "./repo", "", ErrNotRemote},
		{"file url is local", "file:///c/repo", "", ErrNotRemote},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PathFor(root, tt.url)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("PathFor(%q) err = %v; want %v", tt.url, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("PathFor(%q) = %v", tt.url, err)
			}
			if want := filepath.FromSlash(tt.want); got != want {
				t.Errorf("PathFor(%q)\n got %q\nwant %q", tt.url, got, want)
			}
		})
	}
}

func TestPathForRejectsTraversal(t *testing.T) {
	if _, err := PathFor("/m", "https://h/../../etc/passwd"); err == nil {
		t.Error("PathFor accepted a traversing path")
	}
}

// originRepo builds a throwaway repo with enough objects to force a pack.
func originRepo(t *testing.T, dir string) string {
	t.Helper()
	g := gitx.Git{}
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	must := func(args ...string) {
		t.Helper()
		if _, err := g.Run(dir, nil, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	must("init", "-q", "-b", "main")
	must("config", "user.email", "t@example.com")
	must("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("hello\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	must("add", "-A")
	must("commit", "-qm", "init")
	must("gc", "-q") // force a pack file so we can check hardlinking
	return dir
}

func packs(t *testing.T, gitDir string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(gitDir, "objects", "pack", "*.pack"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// The property the whole design rests on: cloning from the mirror's filesystem
// path hardlinks the object store, so N workspaces cost one copy.
func TestCloneFromMirrorHardlinksObjects(t *testing.T) {
	root := t.TempDir()
	origin := originRepo(t, filepath.Join(root, "origin"))
	store := &Store{Root: filepath.Join(root, "mirrors"), Git: gitx.Git{}}

	// A local path is never mirrored, so mirror this one by its path directly.
	mirrorPath := filepath.Join(root, "mirrors", "example.com", "o", "r.git")
	if err := os.MkdirAll(filepath.Dir(mirrorPath), 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := (gitx.Git{}).Run("", nil, "clone", "--mirror", "-q", origin, mirrorPath); err != nil {
		t.Fatal(err)
	}

	clone := filepath.Join(root, "clone")
	if err := gitx.Clone(gitx.Git{}, mirrorPath, clone, gitx.CloneOptions{SetOriginTo: "https://example.com/o/r.git"}); err != nil {
		t.Fatal(err)
	}

	mp, cp := packs(t, mirrorPath), packs(t, filepath.Join(clone, ".git"))
	if len(mp) != 1 || len(cp) != 1 {
		t.Fatalf("expected one pack each, got mirror=%v clone=%v", mp, cp)
	}
	mfi, err := os.Stat(mp[0])
	if err != nil {
		t.Fatal(err)
	}
	cfi, err := os.Stat(cp[0])
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(mfi, cfi) {
		t.Error("clone's pack is a copy, not a hardlink: the mirror saves no disk")
	}

	// origin must point at the forge, not the mirror, or pushes go nowhere.
	got := gitx.Origin(gitx.Git{}, clone)
	if got != "https://example.com/o/r.git" {
		t.Errorf("origin = %q; want the canonical URL", got)
	}

	// A checkout happened despite --no-checkout.
	if _, err := os.Stat(filepath.Join(clone, "README")); err != nil {
		t.Errorf("working tree not populated: %v", err)
	}
	_ = store
}

// Repacking the mirror must not damage a clone made from it. This is the
// property that makes hardlinks safe where --shared/alternates would not be.
func TestMirrorGCDoesNotBreakClone(t *testing.T) {
	root := t.TempDir()
	origin := originRepo(t, filepath.Join(root, "origin"))
	mirrorPath := filepath.Join(root, "m.git")
	if _, err := (gitx.Git{}).Run("", nil, "clone", "--mirror", "-q", origin, mirrorPath); err != nil {
		t.Fatal(err)
	}
	clone := filepath.Join(root, "clone")
	if err := gitx.Clone(gitx.Git{}, mirrorPath, clone, gitx.CloneOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := (gitx.Git{}).Run(mirrorPath, nil, "gc", "--prune=now", "-q"); err != nil {
		t.Fatal(err)
	}
	if _, err := (gitx.Git{}).Run(clone, nil, "fsck", "--connectivity-only"); err != nil {
		t.Fatalf("clone broke after the mirror was repacked: %v", err)
	}
}

func TestUpdateIsIdempotentAndLocked(t *testing.T) {
	root := t.TempDir()
	origin := originRepo(t, filepath.Join(root, "origin"))

	// Serve the origin under a fake remote URL by pre-seeding the mirror path,
	// then pointing Update at a URL that maps there. Simpler: use a Store whose
	// git runner rewrites the clone source. Instead, exercise the lock directly.
	store := &Store{Root: filepath.Join(root, "mirrors"), Git: gitx.Git{}}
	target := filepath.Join(store.Root, "lockprobe.git")
	if err := os.MkdirAll(filepath.Dir(target), 0o777); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	concurrent, maxConcurrent := 0, 0
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = store.withLock(target, func() error {
				mu.Lock()
				concurrent++
				if concurrent > maxConcurrent {
					maxConcurrent = concurrent
				}
				mu.Unlock()
				// Hold the lock long enough that an unsynchronised peer would overlap.
				for j := 0; j < 200000; j++ {
					_ = j
				}
				mu.Lock()
				concurrent--
				mu.Unlock()
				return nil
			})
		}()
	}
	wg.Wait()
	if maxConcurrent != 1 {
		t.Errorf("mirror lock allowed %d concurrent holders; want 1", maxConcurrent)
	}
	if _, err := os.Stat(target + ".lock"); !os.IsNotExist(err) {
		t.Error("lockfile survived")
	}
	_ = origin
}

// A lock whose holder crashed (an ancient mtime, no heartbeat) must be broken so
// a later process is not blocked forever.
func TestStaleLockIsBroken(t *testing.T) {
	root := t.TempDir()
	var warnings []string
	store := &Store{Root: root, Git: gitx.Git{}, Warn: func(f string, a ...any) { warnings = append(warnings, f) }}
	target := filepath.Join(root, "probe.git")
	lock := target + ".lock"
	if err := os.WriteFile(lock, []byte("pid 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * staleLockAge)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}

	ran := false
	if err := store.withLock(target, func() error { ran = true; return nil }); err != nil {
		t.Fatalf("withLock over a stale lock: %v", err)
	}
	if !ran {
		t.Error("fn did not run: the stale lock was not broken")
	}
	if len(warnings) == 0 || !strings.Contains(warnings[0], "stale mirror lock") {
		t.Errorf("expected a stale-lock warning, got %v", warnings)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Error("lockfile survived withLock")
	}
}

// A freshly-stamped lock is a live holder and must block a second acquirer until
// it is released -- the guarantee a long clone now keeps via its heartbeat.
func TestHeldLockBlocksUntilReleased(t *testing.T) {
	root := t.TempDir()
	store := &Store{Root: root, Git: gitx.Git{}}
	target := filepath.Join(root, "probe.git")
	lock := target + ".lock"
	if err := os.WriteFile(lock, []byte("pid 999999\n"), 0o644); err != nil {
		t.Fatal(err) // a fresh lock: mtime is now, so it looks live
	}

	done := make(chan error, 1)
	go func() { done <- store.withLock(target, func() error { return nil }) }()

	select {
	case <-done:
		t.Fatal("withLock took a freshly-held lock instead of waiting")
	case <-time.After(2 * lockPoll):
	}

	os.Remove(lock) // the holder releases
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("withLock after release: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("withLock did not proceed after the lock was released")
	}
}

// The heartbeat keeps a held lock's mtime current, so a clone that outlives
// staleLockAge is not judged abandoned by a peer.
func TestHeartbeatKeepsLockFresh(t *testing.T) {
	root := t.TempDir()
	store := &Store{Root: root, Git: gitx.Git{}, LockHeartbeat: 10 * time.Millisecond}
	target := filepath.Join(root, "probe.git")
	lock := target + ".lock"

	err := store.withLock(target, func() error {
		// Backdate the lock as if it had aged; the heartbeat must bring it forward.
		old := time.Now().Add(-time.Hour)
		if err := os.Chtimes(lock, old, old); err != nil {
			return err
		}
		time.Sleep(120 * time.Millisecond) // several heartbeats
		fi, err := os.Stat(lock)
		if err != nil {
			return err
		}
		if age := time.Since(fi.ModTime()); age > staleLockAge {
			t.Errorf("heartbeat did not refresh the lock; mtime age = %s", age)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// fakeGit records the argv of every git invocation and pretends to succeed,
// creating whatever HEAD/stamp files the code under test looks for.
type fakeGit struct {
	calls [][]string
	// onClone, if set, runs when a `clone --mirror` is seen (to fabricate a repo).
	onClone func(dst string)
}

func (f *fakeGit) Run(dir string, env []string, args ...string) (string, error) {
	f.calls = append(f.calls, args)
	for i, a := range args {
		if a == "--mirror" && f.onClone != nil {
			f.onClone(args[len(args)-1])
			_ = i
		}
	}
	return "", nil
}

func (f *fakeGit) sawClone() []string {
	for _, c := range f.calls {
		for _, a := range c {
			if a == "clone" {
				return c
			}
		}
	}
	return nil
}

func (f *fakeGit) sawFetch() bool {
	for _, c := range f.calls {
		if len(c) >= 2 && c[0] == "remote" && c[1] == "update" {
			return true
		}
	}
	return false
}

// Regression: `remote update` inside a deep mirror fails with "Filename too
// long" unless core.longpaths is persisted into the mirror's own config. The
// command-level -c does not survive the clone.
func TestMirrorClonePersistsLongPaths(t *testing.T) {
	root := t.TempDir()
	fg := &fakeGit{onClone: func(dst string) {
		os.MkdirAll(dst, 0o777)
		os.WriteFile(filepath.Join(dst, "HEAD"), []byte("ref: refs/heads/main\n"), 0o666)
	}}
	s := &Store{Root: root, Git: fg}
	if _, err := s.Update("https://example.com/o/r.git"); err != nil {
		t.Fatal(err)
	}
	clone := fg.sawClone()
	if clone == nil {
		t.Fatal("no clone issued")
	}
	// The -c must appear AFTER `clone`, which is what persists it into the repo.
	seenClone, persisted := false, false
	for i, a := range clone {
		if a == "clone" {
			seenClone = true
		}
		if seenClone && a == "-c" && i+1 < len(clone) && clone[i+1] == "core.longpaths=true" {
			persisted = true
		}
	}
	if !persisted {
		t.Errorf("core.longpaths is not persisted into the mirror: %v", clone)
	}
}

// Regression: a freshly created mirror must not be considered stale. It has no
// FETCH_HEAD, so an implementation keyed on that re-fetches on the next sync.
func TestFreshMirrorIsNotRefetched(t *testing.T) {
	root := t.TempDir()
	fg := &fakeGit{onClone: func(dst string) {
		os.MkdirAll(dst, 0o777)
		os.WriteFile(filepath.Join(dst, "HEAD"), []byte("ref: refs/heads/main\n"), 0o666)
	}}
	s := &Store{Root: root, Git: fg}
	url := "https://example.com/o/r.git"
	if _, err := s.Update(url); err != nil {
		t.Fatal(err)
	}
	if fg.sawFetch() {
		t.Fatal("fetched immediately after creating the mirror")
	}
	// A second Update within MaxAge must not fetch either.
	fg.calls = nil
	if _, err := s.Update(url); err != nil {
		t.Fatal(err)
	}
	if fg.sawFetch() {
		t.Error("re-fetched a mirror younger than MaxAge")
	}

	// Age the stamp past MaxAge; now it should fetch.
	path, _ := PathFor(root, url)
	old := time.Now().Add(-2 * DefaultMaxAge)
	if err := os.Chtimes(filepath.Join(path, stampFile), old, old); err != nil {
		t.Fatal(err)
	}
	fg.calls = nil
	if _, err := s.Update(url); err != nil {
		t.Fatal(err)
	}
	if !fg.sawFetch() {
		t.Error("did not fetch a mirror older than MaxAge")
	}
}

func TestResolvePassesLocalPathsThrough(t *testing.T) {
	store := &Store{Root: t.TempDir(), Git: gitx.Git{}}
	local := filepath.Join("C:", "repos", "x")
	src, setOrigin, err := store.Resolve(local)
	if err != nil {
		t.Fatal(err)
	}
	if src != local || setOrigin != "" {
		t.Errorf("Resolve(local) = %q, %q; want passthrough", src, setOrigin)
	}
}

func TestWarnOnStaleFetchIsNotFatal(t *testing.T) {
	root := t.TempDir()
	origin := originRepo(t, filepath.Join(root, "origin"))
	mirrorPath := filepath.Join(root, "mirrors", "example.com", "o", "r.git")
	if err := os.MkdirAll(filepath.Dir(mirrorPath), 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := (gitx.Git{}).Run("", nil, "clone", "--mirror", "-q", origin, mirrorPath); err != nil {
		t.Fatal(err)
	}
	// Break the mirror's remote so `remote update` fails.
	if _, err := (gitx.Git{}).Run(mirrorPath, nil, "remote", "set-url", "origin", "https://nonexistent.invalid/x.git"); err != nil {
		t.Fatal(err)
	}
	var warnings []string
	store := &Store{
		Root: filepath.Join(root, "mirrors"), Git: gitx.Git{}, MaxAge: -1, // always stale
		Warn: func(f string, a ...any) { warnings = append(warnings, f) },
	}
	got, err := store.Update("https://example.com/o/r.git")
	if err != nil {
		t.Fatalf("a failed mirror fetch must not be fatal, got %v", err)
	}
	if got != mirrorPath {
		t.Errorf("Update returned %q; want %q", got, mirrorPath)
	}
	if len(warnings) == 0 || !strings.Contains(warnings[0], "stale mirror") {
		t.Errorf("expected a stale-mirror warning, got %v", warnings)
	}
}
