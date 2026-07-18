// Package mirror keeps bare mirrors of remote repositories on local disk, so
// that workspace clones can be made from a filesystem path instead of the
// network.
//
// This is not merely a speed trick. When git clones from a local path it uses
// the --local optimisation and hardlinks .git/objects, so a dozen workspaces
// holding the same repo cost one copy of its object store. Each clone still gets
// an independent .git -- its own refs, its own index -- and repacking or garbage
// collecting either side is safe, because a hardlink keeps the inode alive.
// (That is why this uses hardlinks rather than --shared/alternates.)
//
// Correctness never depends on a mirror being fresh. Every clone's origin is
// repointed at the canonical URL, so a stale mirror costs a few extra objects on
// the next fetch and nothing more. A failed mirror update is therefore a
// warning, not an error.
package mirror

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/RiccardoCereghino/facet/internal/gitx"
)

// ErrNotRemote reports a URL that names a local path, which is never mirrored.
var ErrNotRemote = errors.New("not a remote URL")

// DefaultMaxAge is how long a mirror may go unfetched before Update refreshes it.
const DefaultMaxAge = 10 * time.Minute

const (
	// staleLockAge is how long a lockfile may go untouched before a waiter
	// presumes the holder crashed. The holder re-stamps its lock every
	// lockHeartbeat while it works, so "untouched this long" means dead, not
	// slow -- a clone that legitimately runs for many minutes keeps its lock
	// fresh and is never broken.
	staleLockAge = 5 * time.Minute
	// lockHeartbeat is how often the holder re-stamps its lock's mtime.
	lockHeartbeat = 30 * time.Second
	// lockPoll is how often a waiter re-checks a lock it could not take.
	lockPoll = time.Second
	// maxLockWait bounds the total wait, a backstop against a holder that hangs
	// while still heartbeating (a wedged git). Far above any real clone.
	maxLockWait = 60 * time.Minute
)

// scpLike matches git's [user@]host:path syntax. The host must not be a single
// letter, or a Windows drive ("C:\repo") would parse as a host.
var scpLike = regexp.MustCompile(`^(?:[^@/\\]+@)?([^:/\\]{2,}):(.+)$`)

// IsLocalPath reports whether raw names a path on this machine rather than a
// remote repository.
func IsLocalPath(raw string) bool {
	if raw == "" {
		return false
	}
	if filepath.IsAbs(raw) || strings.HasPrefix(raw, ".") {
		return true
	}
	// C:\repo or C:/repo
	if len(raw) >= 3 && raw[1] == ':' && (raw[2] == '\\' || raw[2] == '/') {
		return true
	}
	return strings.HasPrefix(raw, "file://")
}

// PathFor maps a repository URL to its mirror directory beneath root:
//
//	git@github.com:owner/repo.git   -> root/github.com/owner/repo.git
//	https://host/group/sub/repo.git -> root/host/group/sub/repo.git
//
// The host is part of the path so two repos with the same name on different
// forges cannot collide, and the full path is preserved because forges like
// GitLab nest groups arbitrarily deep.
func PathFor(root, raw string) (string, error) {
	if IsLocalPath(raw) {
		return "", ErrNotRemote
	}
	var host, repoPath string

	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return "", fmt.Errorf("parse %q: %w", raw, err)
		}
		if u.Host == "" || u.Path == "" {
			return "", fmt.Errorf("%q: %w", raw, ErrNotRemote)
		}
		host = u.Hostname() // drops any :port, which does not belong in a path
		repoPath = u.Path
	} else if m := scpLike.FindStringSubmatch(raw); m != nil {
		host, repoPath = m[1], m[2]
	} else {
		return "", fmt.Errorf("%q: %w", raw, ErrNotRemote)
	}

	repoPath = strings.Trim(strings.ReplaceAll(repoPath, "\\", "/"), "/")
	repoPath = strings.TrimSuffix(repoPath, ".git")
	if repoPath == "" {
		return "", fmt.Errorf("%q: %w", raw, ErrNotRemote)
	}

	segs := []string{root, host}
	for _, s := range strings.Split(repoPath, "/") {
		if s == "" || s == "." || s == ".." {
			return "", fmt.Errorf("%q: unsafe path segment %q", raw, s)
		}
		segs = append(segs, s)
	}
	return filepath.Join(segs...) + ".git", nil
}

// Store manages the mirrors beneath Root.
type Store struct {
	Root   string
	Git    gitx.Runner
	MaxAge time.Duration
	// Report receives progress lines; nil discards them.
	Report func(format string, a ...any)
	// Warn receives non-fatal problems; nil discards them.
	Warn func(format string, a ...any)
	// LockHeartbeat overrides how often a held lock is re-stamped. Zero uses
	// lockHeartbeat; exposed mainly so tests can drive the heartbeat quickly.
	LockHeartbeat time.Duration
}

func (s *Store) report(f string, a ...any) {
	if s.Report != nil {
		s.Report(f, a...)
	}
}
func (s *Store) warn(f string, a ...any) {
	if s.Warn != nil {
		s.Warn(f, a...)
	}
}

func (s *Store) maxAge() time.Duration {
	if s.MaxAge == 0 {
		return DefaultMaxAge
	}
	return s.MaxAge
}

// Update ensures a mirror of raw exists and is reasonably fresh, returning its
// path. A fetch failure is reported but not fatal: the mirror only seeds the
// initial hardlinked copy, and every clone's origin points at the real forge.
func (s *Store) Update(raw string) (string, error) {
	path, err := PathFor(s.Root, raw)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return "", err
	}
	err = s.withLock(path, func() error {
		if _, statErr := os.Stat(filepath.Join(path, "HEAD")); os.IsNotExist(statErr) {
			s.report("mirror: creating %s", path)
			// The second -c is not redundant: the first configures this command,
			// the second persists into the new repo, so later fetches inside the
			// mirror can also write paths past Windows' MAX_PATH.
			if _, err := s.Git.Run("", nil,
				"-c", "core.longpaths=true", "clone", "--mirror",
				"-c", "core.longpaths=true", raw, path,
			); err != nil {
				return fmt.Errorf("mirror clone %s: %w", raw, err)
			}
			s.stamp(path) // just fetched, by definition
			return nil
		}
		// Repair mirrors created before core.longpaths was persisted.
		if _, err := s.Git.Run(path, nil, "config", "core.longpaths", "true"); err != nil {
			s.warn("could not set core.longpaths on %s: %v", path, err)
		}
		if s.fresh(path) {
			return nil
		}
		s.report("mirror: fetching %s", path)
		if _, err := s.Git.Run(path, nil, "remote", "update", "--prune"); err != nil {
			// Not fatal: origin is the forge, so a stale mirror only costs objects.
			s.warn("mirror fetch failed for %s (using stale mirror): %v", raw, err)
			return nil
		}
		s.stamp(path)
		return nil
	})
	if err != nil {
		return "", err
	}
	return path, nil
}

// stampFile records when facet last fetched a mirror. git's own FETCH_HEAD is
// not usable for this: `clone --mirror` never writes one, so a brand-new mirror
// would look infinitely stale and be re-fetched on the very next sync.
const stampFile = "facet-fetched"

func (s *Store) stamp(path string) {
	f, err := os.Create(filepath.Join(path, stampFile))
	if err != nil {
		s.warn("could not stamp mirror %s: %v", path, err)
		return
	}
	f.Close()
}

// fresh reports whether the mirror was fetched within MaxAge.
func (s *Store) fresh(path string) bool {
	fi, err := os.Stat(filepath.Join(path, stampFile))
	if err != nil {
		return false
	}
	return time.Since(fi.ModTime()) < s.maxAge()
}

// Resolve implements workspace.SourceResolver: clone from the mirror, then point
// origin back at the canonical URL so fetches and pushes reach the forge.
//
// A URL that names a local path is passed straight through unmirrored.
func (s *Store) Resolve(raw string) (src, setOriginTo string, err error) {
	path, err := s.Update(raw)
	if errors.Is(err, ErrNotRemote) {
		return raw, "", nil
	}
	if err != nil {
		return "", "", err
	}
	return path, raw, nil
}

// withLock serialises mirror creation and fetching across processes: two agents
// spawning workspaces at once will contend for the same mirror.
//
// The lock is a file created O_EXCL. While the guarded work runs, a heartbeat
// re-stamps the lock's mtime, so a waiter can tell a live holder (fresh mtime)
// from a crashed one (stale) and never breaks a clone that is merely slow -- the
// bug a fixed timeout had, where a large clone past staleLockAge was torn out
// from under itself and two clones raced into one mirror.
func (s *Store) withLock(mirrorPath string, fn func() error) error {
	lock := mirrorPath + ".lock"
	f, err := s.acquireLock(lock)
	if err != nil {
		return err
	}
	stop := s.heartbeat(lock)
	defer func() {
		stop() // stop and join the heartbeat before dropping the lock
		f.Close()
		os.Remove(lock)
	}()
	return fn()
}

// acquireLock blocks until it creates the lockfile, breaking a lock whose holder
// has stopped heartbeating (and so is presumed dead).
func (s *Store) acquireLock(lock string) (*os.File, error) {
	deadline := time.Now().Add(maxLockWait)
	for {
		f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintf(f, "pid %d\n", os.Getpid()) // for a human debugging a stuck lock
			return f, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		fi, statErr := os.Stat(lock)
		if os.IsNotExist(statErr) {
			continue // released between our create and stat; try again at once
		}
		if statErr != nil {
			return nil, statErr
		}
		if time.Since(fi.ModTime()) > staleLockAge {
			// Presumed abandoned. Re-check the mtime immediately before removing,
			// so a lock just re-created (and thus fresh) by someone else is left
			// alone rather than clobbered -- narrowing the check-then-remove race.
			if again, err := os.Stat(lock); err == nil && time.Since(again.ModTime()) > staleLockAge {
				s.warn("breaking stale mirror lock %s (untouched for over %s)", lock, staleLockAge)
				os.Remove(lock)
			}
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("could not acquire mirror lock %s within %s", lock, maxLockWait)
		}
		time.Sleep(lockPoll)
	}
}

// heartbeat re-stamps the lock's mtime until the returned stop func is called;
// stop blocks until the heartbeat goroutine has exited, so the caller can drop
// the lock knowing nothing will touch it afterwards.
func (s *Store) heartbeat(lock string) (stop func()) {
	interval := s.LockHeartbeat
	if interval <= 0 {
		interval = lockHeartbeat
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				now := time.Now()
				_ = os.Chtimes(lock, now, now)
			}
		}
	}()
	return func() {
		close(done)
		<-stopped
	}
}
