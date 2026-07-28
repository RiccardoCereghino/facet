// Package lockfile provides a cross-process advisory lock backed by an O_EXCL
// lockfile with an mtime heartbeat. While the guarded work runs, a heartbeat
// re-stamps the lock's mtime, so a waiter can tell a live holder (fresh mtime)
// from a crashed one (stale) and never breaks a lock that is merely slow -- the
// bug a fixed timeout has, where long work past the stale age is torn out from
// under itself and two holders race into one resource.
package lockfile

import (
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/RiccardoCereghino/facet/internal/wait"
)

const (
	// defaultStaleAge is how long a lockfile may go untouched before a waiter
	// presumes the holder crashed. The holder re-stamps every defaultHeartbeat
	// while it works, so "untouched this long" means dead, not slow.
	defaultStaleAge = 5 * time.Minute
	// defaultHeartbeat is how often the holder re-stamps its lock's mtime.
	defaultHeartbeat = 30 * time.Second
	// defaultPoll is how often a waiter re-checks a lock it could not take.
	defaultPoll = time.Second
	// defaultMaxWait bounds the total wait, a backstop against a holder that hangs
	// while still heartbeating. Far above any real operation.
	defaultMaxWait = 60 * time.Minute
	// winCreateRetryBudget bounds the retry loop that works around Windows
	// delete-pending lag when creating a just-released lockfile. See createLock.
	winCreateRetryBudget = 2 * time.Second
)

// Options tunes lock timing. A zero value uses the defaults.
type Options struct {
	StaleAge  time.Duration
	Heartbeat time.Duration
	Poll      time.Duration
	MaxWait   time.Duration
	// Warn, if set, receives a message when a stale lock is broken.
	Warn func(format string, a ...any)
}

func (o Options) withDefaults() Options {
	if o.StaleAge <= 0 {
		o.StaleAge = defaultStaleAge
	}
	if o.Heartbeat <= 0 {
		o.Heartbeat = defaultHeartbeat
	}
	if o.Poll <= 0 {
		o.Poll = defaultPoll
	}
	if o.MaxWait <= 0 {
		o.MaxWait = defaultMaxWait
	}
	return o
}

// With acquires an exclusive lock at path, runs fn while heartbeating the lock's
// mtime, then releases it (stopping the heartbeat before removing the file, so
// nothing touches it after release). It blocks until it can take the lock,
// breaking a lock whose holder has stopped heartbeating.
func With(path string, opt Options, fn func() error) error {
	opt = opt.withDefaults()
	f, err := acquire(path, opt)
	if err != nil {
		return err
	}
	stop := heartbeat(path, opt.Heartbeat)
	defer func() {
		stop() // stop and join the heartbeat before dropping the lock
		_ = f.Close()
		_ = os.Remove(path)
	}()
	return fn()
}

// acquire polls at opt.Poll until it takes the lock, a fatal error rules it
// out, or opt.MaxWait passes. The immediate-retry paths (a released lock
// racing our stat, a just-broken stale lock) loop inside the attempt itself
// rather than through wait.Until's sleep, so they cost no wait at all -- only
// genuine contention pays the poll interval.
func acquire(path string, opt Options) (*os.File, error) {
	var f *os.File
	var ferr error
	ok := wait.Until(time.Now().Add(opt.MaxWait), opt.Poll, func() bool {
		for {
			var err error
			f, err = createLock(path)
			if err == nil {
				_, _ = fmt.Fprintf(f, "pid %d\n", os.Getpid()) // for a human debugging a stuck lock
				ferr = nil
				return true
			}
			if !os.IsExist(err) {
				ferr = err
				return true
			}
			fi, statErr := os.Stat(path)
			if os.IsNotExist(statErr) {
				continue // released between our create and stat; try again at once
			}
			if statErr != nil {
				ferr = statErr
				return true
			}
			if time.Since(fi.ModTime()) > opt.StaleAge {
				// Presumed abandoned. Re-check the mtime immediately before removing,
				// so a lock just re-created (and thus fresh) by someone else is left
				// alone rather than clobbered -- narrowing the check-then-remove race.
				if again, err := os.Stat(path); err == nil && time.Since(again.ModTime()) > opt.StaleAge {
					if opt.Warn != nil {
						opt.Warn("breaking stale lock %s (untouched for over %s)", path, opt.StaleAge)
					}
					_ = os.Remove(path)
				}
				continue
			}
			return false // genuine contention; let wait.Until sleep and recheck the deadline
		}
	})
	if !ok {
		return nil, fmt.Errorf("could not acquire lock %s within %s", path, opt.MaxWait)
	}
	return f, ferr
}

// createLock does the O_CREATE|O_EXCL open that claims the lock. It exists to
// absorb a Windows-only race: when the releasing holder os.Removes the lockfile,
// Windows keeps the name in a delete-pending state until the last handle closes,
// and a create of that same name during the window returns ERROR_ACCESS_DENIED
// (an os.IsPermission error, not os.IsExist). That is transient, not a real
// failure, and it can bite any second acquirer in production, not just the test
// that first surfaced it. So on Windows a permission error is retried with a
// short bounded backoff. POSIX unlinks immediately and has no such window, so
// there this is a single attempt and a permission error stays a hard error.
func createLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err == nil || runtime.GOOS != "windows" {
		return f, err
	}
	deadline := time.Now().Add(winCreateRetryBudget)
	backoff := 5 * time.Millisecond
	for os.IsPermission(err) && time.Now().Before(deadline) {
		time.Sleep(backoff)
		if backoff < 80*time.Millisecond {
			backoff *= 2
		}
		f, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			return f, nil
		}
	}
	// Either the window closed (err is now nil-handled above, or IsExist so the
	// caller's contention path takes over) or the budget ran out (return the last
	// error, now a genuine failure).
	return f, err
}

// heartbeat re-stamps the lock's mtime until the returned stop func is called;
// stop blocks until the heartbeat goroutine has exited, so the caller can drop
// the lock knowing nothing will touch it afterwards.
func heartbeat(path string, interval time.Duration) (stop func()) {
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
				_ = os.Chtimes(path, now, now)
			}
		}
	}()
	return func() {
		close(done)
		<-stopped
	}
}
