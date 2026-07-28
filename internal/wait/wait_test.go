package wait

import (
	"testing"
	"time"
)

func TestUntilReturnsTrueAssoonAsAttemptSucceeds(t *testing.T) {
	calls := 0
	ok := Until(time.Now().Add(time.Second), time.Millisecond, func() bool {
		calls++
		return calls == 3
	})
	if !ok {
		t.Fatal("Until returned false for an attempt that did succeed")
	}
	if calls != 3 {
		t.Errorf("calls = %d, want exactly 3 (Until must stop polling once attempt succeeds)", calls)
	}
}

func TestUntilReturnsFalseWhenDeadlinePasses(t *testing.T) {
	start := time.Now()
	ok := Until(start.Add(20*time.Millisecond), 5*time.Millisecond, func() bool {
		return false
	})
	if ok {
		t.Fatal("Until returned true for an attempt that never succeeded")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Until did not bound the wait: took %v against a 20ms deadline", elapsed)
	}
}

func TestUntilNeverSleepsOnImmediateSuccess(t *testing.T) {
	start := time.Now()
	ok := Until(start.Add(time.Hour), time.Hour, func() bool { return true })
	if !ok {
		t.Fatal("Until returned false for an attempt that succeeded on the first try")
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("Until slept before checking the first attempt: took %v", elapsed)
	}
}
