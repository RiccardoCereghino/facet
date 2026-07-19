package lockfile

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestWithReleasesLock(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "x.lock")
	ran := false
	if err := With(lock, Options{}, func() error { ran = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Error("fn did not run")
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Error("lockfile survived With")
	}
}

// Only one holder may run the guarded work at a time.
func TestWithSerialises(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "x.lock")
	var mu sync.Mutex
	inside, peak := 0, 0
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = With(lock, Options{Poll: time.Millisecond}, func() error {
				mu.Lock()
				inside++
				if inside > peak {
					peak = inside
				}
				mu.Unlock()
				time.Sleep(20 * time.Millisecond)
				mu.Lock()
				inside--
				mu.Unlock()
				return nil
			})
		}()
	}
	wg.Wait()
	if peak != 1 {
		t.Errorf("lock allowed %d concurrent holders, want 1", peak)
	}
}

// A lock whose holder crashed (ancient mtime, no heartbeat) is broken so a later
// waiter is not blocked forever.
func TestWithBreaksStaleLock(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "x.lock")
	if err := os.WriteFile(lock, []byte("pid 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	ran, warned := false, false
	err := With(lock, Options{StaleAge: time.Minute, Warn: func(string, ...any) { warned = true }},
		func() error { ran = true; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Error("stale lock was not broken")
	}
	if !warned {
		t.Error("no warning on breaking a stale lock")
	}
}

// A live holder's heartbeat keeps its lock fresh, so a peer does not judge it
// abandoned while the work legitimately outlives StaleAge.
func TestHeartbeatKeepsLockFresh(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "x.lock")
	err := With(lock, Options{StaleAge: 50 * time.Millisecond, Heartbeat: 10 * time.Millisecond},
		func() error {
			// Backdate as if aged; the heartbeat must bring it forward.
			old := time.Now().Add(-time.Hour)
			_ = os.Chtimes(lock, old, old)
			time.Sleep(120 * time.Millisecond) // several heartbeats
			fi, err := os.Stat(lock)
			if err != nil {
				return err
			}
			if age := time.Since(fi.ModTime()); age > 50*time.Millisecond {
				t.Errorf("heartbeat did not refresh the lock; mtime age = %s", age)
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
}
