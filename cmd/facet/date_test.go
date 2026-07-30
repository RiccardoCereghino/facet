package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// A fixed instant so the CEST/UTC trap the issue describes is exercised
// directly, not by whatever offset happens to be live when the test runs.
// 17:00 CEST (+2) is 15:00 UTC -- the same two-hour gap that turned a real
// two-minute loop into a reported two hours.
func fixedNow(t *testing.T) time.Time {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Rome")
	if err != nil {
		t.Skipf("no Europe/Rome tzdata on this machine: %v", err)
	}
	return time.Date(2026, 7, 30, 17, 0, 0, 0, loc)
}

func TestFormatNowUTCDefault(t *testing.T) {
	now := fixedNow(t)
	got := formatNow(now, false)
	want := "2026-07-30T15:00:00Z"
	if got != want {
		t.Errorf("formatNow(now, false) = %q, want %q", got, want)
	}
}

// The exact bug this issue is about: a local time read with no offset is
// indistinguishable from UTC and silently means something two hours off. This
// asserts --local's output is never that shape -- it must always carry an
// explicit offset.
func TestFormatNowLocalAlwaysCarriesOffset(t *testing.T) {
	now := fixedNow(t)
	got := formatNow(now, true)
	want := "2026-07-30T17:00:00+02:00"
	if got != want {
		t.Errorf("formatNow(now, true) = %q, want %q", got, want)
	}
	if !strings.Contains(got, "+") && !strings.HasSuffix(got, "Z") {
		t.Errorf("local rendering %q carries no offset marker", got)
	}
}

// The red path the issue's Acceptance section asks for: the case that
// motivated this command. A watermark silently ahead of now -- exactly the
// 74-minutes-in-the-future bug -- must be refused, not accepted.
func TestCheckRefusesAFutureTimestamp(t *testing.T) {
	now := fixedNow(t)
	future := now.Add(74 * time.Minute).UTC().Format(time.RFC3339)

	var buf bytes.Buffer
	err := runDateCheck(&buf, now, future)
	if err == nil {
		t.Fatalf("a timestamp 74 minutes in the future must be refused; buf = %q", buf.String())
	}
	if !strings.Contains(err.Error(), "future") {
		t.Errorf("refusal must say why: %v", err)
	}
	t.Logf("refusal: %v", err)
}

// The ordinary case: a timestamp genuinely in the past must be accepted, not
// refused -- the command must not become paranoid about every timestamp.
func TestCheckAcceptsAPastTimestamp(t *testing.T) {
	now := fixedNow(t)
	past := now.Add(-2 * time.Minute).UTC().Format(time.RFC3339)

	var buf bytes.Buffer
	if err := runDateCheck(&buf, now, past); err != nil {
		t.Fatalf("a timestamp 2 minutes in the past must not be refused: %v", err)
	}
	if !strings.Contains(buf.String(), "past") {
		t.Errorf("output should report the timestamp as in the past, got %q", buf.String())
	}
}

// A timestamp barely ahead of now -- within ordinary clock skew -- must not
// be refused; only genuinely-wrong timestamps should trip this. And it must
// be reported honestly as still being in the future, never as a negative
// amount "in the past" (facet!79, F2): that is a sentence about the direction
// of time that is wrong on its face, in the one command whose subject is not
// misreading the direction of time.
func TestCheckToleratesSmallClockSkew(t *testing.T) {
	now := fixedNow(t)
	almostNow := now.Add(2 * time.Second).UTC().Format(time.RFC3339)

	var buf bytes.Buffer
	if err := runDateCheck(&buf, now, almostNow); err != nil {
		t.Errorf("2 seconds of skew must not be refused: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "is -") {
		t.Errorf("a timestamp still ahead of now must never be reported with a negative duration, got %q", out)
	}
	if !strings.Contains(out, "future") {
		t.Errorf("a timestamp ahead of now, even within tolerance, must say so, got %q", out)
	}
}

func TestCheckRejectsAMalformedTimestamp(t *testing.T) {
	now := fixedNow(t)
	var buf bytes.Buffer
	err := runDateCheck(&buf, now, "not-a-timestamp")
	if err == nil {
		t.Fatal("a malformed timestamp must be rejected, not silently accepted")
	}
}

// Command-layer tests: everything above calls runDateCheck/formatNow directly
// and never exercised RunE's own dispatch -- which is exactly where facet!79's
// F1 lived (an empty --check silently fell through to the default branch).
// These go through newDateCmd() and Execute() so that dispatch is what's
// under test, not just the functions it calls.

// The defect itself: `--check ""` must refuse, not silently print the current
// time and exit 0. That silent fallthrough is the exact class of bug this
// command exists to eliminate -- a watch that gets a well-formed timestamp and
// a zero exit from an unset watermark reads as "all clear."
func TestDateCmdEmptyCheckRefuses(t *testing.T) {
	cmd := newDateCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--check", ""})

	err := cmd.Execute()
	if err == nil {
		t.Fatalf("--check with an empty value must refuse, not silently print the current time; got output %q", out.String())
	}
}

// The `--check=` spelling must refuse the same way as `--check ""`.
func TestDateCmdEmptyCheckEqualsFormRefuses(t *testing.T) {
	cmd := newDateCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--check="})

	if err := cmd.Execute(); err == nil {
		t.Fatalf("--check= must refuse, not silently print the current time; got output %q", out.String())
	}
}

// With no --check at all, the command must still print the current time --
// the refusal above must not have broken the ordinary no-flags path.
func TestDateCmdNoCheckPrintsCurrentTime(t *testing.T) {
	cmd := newDateCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("facet date with no flags must not error: %v", err)
	}
	if _, err := time.Parse(time.RFC3339, strings.TrimSpace(out.String())); err != nil {
		t.Errorf("output %q does not parse as RFC 3339: %v", out.String(), err)
	}
}

// A real --check value must actually dispatch to the check path and refuse a
// genuinely future timestamp, through the full command, not just the helper.
func TestDateCmdCheckDispatchesAndRefusesFuture(t *testing.T) {
	cmd := newDateCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	future := time.Now().Add(74 * time.Minute).UTC().Format(time.RFC3339)
	cmd.SetArgs([]string{"--check", future})

	err := cmd.Execute()
	if err == nil {
		t.Fatalf("a real 74-minutes-future --check value must be refused through the command layer")
	}
}

// The acceptance criterion: output usable directly as a jq comparison against
// createdAt from `gh --json`, which is RFC 3339 UTC. A round-trip through
// time.Parse must reproduce the same instant.
func TestUTCOutputRoundTripsAsRFC3339(t *testing.T) {
	now := fixedNow(t)
	out := formatNow(now, false)
	parsed, err := time.Parse(time.RFC3339, out)
	if err != nil {
		t.Fatalf("formatNow's default output does not parse as RFC 3339: %v", err)
	}
	if !parsed.Equal(now) {
		t.Errorf("round-tripped time %v != original %v", parsed, now)
	}
}
