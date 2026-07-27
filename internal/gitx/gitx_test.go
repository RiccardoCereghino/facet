package gitx

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// RunTimeout exists for exactly one reason: a network operation (fetch) must
// degrade under a blackholed connection or captive portal, not hang the
// caller forever. A fake can model a fast failure, but not a real hang -- so
// this test proves the bound against a real, genuinely stalling git
// subprocess, using git's "ext" remote helper to run a script that sleeps
// far longer than the timeout. It is deterministic, not a race against a
// real network: the script's sleep duration is fixed and far exceeds the
// timeout given to RunTimeout.
func TestRunTimeoutKillsAHangingFetch(t *testing.T) {
	dir := t.TempDir()
	g := Git{}
	mustRun := func(args ...string) {
		t.Helper()
		if _, err := g.Run(dir, nil, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	mustRun("init", "-q", "-b", "main")
	mustRun("config", "user.email", "t@example.com")
	mustRun("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("hi\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	mustRun("add", "-A")
	mustRun("commit", "-qm", "init")

	// A remote-ext helper that hangs far longer than the timeout below --
	// standing in for a blackholed connection or a captive portal, which no
	// fake Runner can reproduce.
	hang := filepath.Join(dir, "hang.sh")
	if err := os.WriteFile(hang, []byte("#!/bin/sh\nsleep 5\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	_, err := g.RunTimeout(dir, nil, 300*time.Millisecond,
		"-c", "protocol.ext.allow=always", "fetch", "ext::"+hang)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("RunTimeout returned no error for a fetch that never completes")
	}
	// Worst case is the 300ms context timeout plus gitx's waitDelay (the
	// grace period Wait() gives a killed process's orphaned grandchild to
	// close the inherited stdout/stderr pipe -- see gitx.go). Comfortably
	// under the script's 5s sleep: if this bound didn't hold, elapsed would
	// land at ~5s instead, which is exactly the bug this test exists to
	// catch (it did, on the first cut of this fix -- WaitDelay was missing).
	if elapsed > 4*time.Second {
		t.Fatalf("RunTimeout did not bound the hang: took %v against a 300ms timeout and a 5s hang", elapsed)
	}
}

func TestRunTimeoutSucceedsWithinBudget(t *testing.T) {
	dir := t.TempDir()
	g := Git{}
	if _, err := g.RunTimeout(dir, nil, 5*time.Second, "init", "-q", "-b", "main"); err != nil {
		t.Fatalf("RunTimeout: %v", err)
	}
	if !IsRepo(dir) {
		t.Error("RunTimeout did not run the command")
	}
}
