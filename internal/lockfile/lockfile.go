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
	"time"
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
		f.Close()
		os.Remove(path)
	}()
	return fn()
}

func acquire(path string, opt Options) (*os.File, error) {
	deadline := time.Now().Add(opt.MaxWait)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintf(f, "pid %d\n", os.Getpid()) // for a human debugging a stuck lock
			return f, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		fi, statErr := os.Stat(path)
		if os.IsNotExist(statErr) {
			continue // released between our create and stat; try again at once
		}
		if statErr != nil {
			return nil, statErr
		}
		if time.Since(fi.ModTime()) > opt.StaleAge {
			// Presumed abandoned. Re-check the mtime immediately before removing,
			// so a lock just re-created (and thus fresh) by someone else is left
			// alone rather than clobbered -- narrowing the check-then-remove race.
			if again, err := os.Stat(path); err == nil && time.Since(again.ModTime()) > opt.StaleAge {
				if opt.Warn != nil {
					opt.Warn("breaking stale lock %s (untouched for over %s)", path, opt.StaleAge)
				}
				os.Remove(path)
			}
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("could not acquire lock %s within %s", path, opt.MaxWait)
		}
		time.Sleep(opt.Poll)
	}
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
