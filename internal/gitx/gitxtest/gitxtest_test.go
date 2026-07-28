package gitxtest

import (
	"errors"
	"testing"
)

func TestZeroValueSucceedsAndRecords(t *testing.T) {
	var r Runner
	out, err := r.Run("dir", nil, "status")
	if err != nil || out != "" {
		t.Fatalf("Run() = %q, %v; want a silent success", out, err)
	}
	if len(r.Calls) != 1 || r.Calls[0][0] != "status" {
		t.Fatalf("Calls = %v; want one recorded call", r.Calls)
	}
}

func TestFailMatchesReturnErr(t *testing.T) {
	want := errors.New("boom")
	r := &Runner{
		Fail: func(args []string) bool { return len(args) > 0 && args[0] == "fetch" },
		Err:  want,
	}
	if _, err := r.Run("", nil, "status"); err != nil {
		t.Fatalf("status should not match Fail, got %v", err)
	}
	if _, err := r.Run("", nil, "fetch", "origin"); err != want {
		t.Fatalf("Run(fetch) = %v, want %v", err, want)
	}
	if _, err := r.RunTimeout("", nil, 0, "fetch"); err != want {
		t.Fatalf("RunTimeout(fetch) = %v, want %v", err, want)
	}
}

func TestRealIsDelegatedToWhenFailDoesNotMatch(t *testing.T) {
	real := &Runner{} // stands in for a real Runner in this test
	r := &Runner{
		Real: real,
		Fail: func(args []string) bool { return len(args) > 0 && args[0] == "fetch" },
		Err:  errors.New("boom"),
	}
	if _, err := r.Run("dir", nil, "status"); err != nil {
		t.Fatalf("Run(status) = %v, want nil (delegated to Real)", err)
	}
	if len(real.Calls) != 1 || real.Calls[0][0] != "status" {
		t.Fatalf("Real.Calls = %v; want the delegated call recorded on Real too", real.Calls)
	}
}

func TestOnCallRunsBeforeFailIsChecked(t *testing.T) {
	var seen []string
	r := &Runner{
		OnCall: func(_ string, args []string) { seen = append(seen, args[0]) },
		Fail:   func(args []string) bool { return true },
		Err:    errors.New("boom"),
	}
	_, _ = r.Run("", nil, "clone")
	if len(seen) != 1 || seen[0] != "clone" {
		t.Fatalf("OnCall did not observe the failed call: %v", seen)
	}
}

func TestCallFindsTheFirstMatch(t *testing.T) {
	r := &Runner{}
	_, _ = r.Run("", nil, "status")
	_, _ = r.Run("", nil, "clone", "--mirror", "x")
	_, _ = r.Run("", nil, "clone", "--mirror", "y")

	got := r.Call(func(a []string) bool { return len(a) > 0 && a[0] == "clone" })
	if len(got) == 0 || got[len(got)-1] != "x" {
		t.Fatalf("Call returned %v, want the first clone call", got)
	}
	if r.Call(func(a []string) bool { return len(a) > 0 && a[0] == "push" }) != nil {
		t.Fatal("Call matched a command that was never run")
	}
}
