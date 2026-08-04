package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/RiccardoCereghino/facet/internal/ghx"
)

// windowsNote is a Windows-shaped not-applicable notice. The real one comes
// from ghx and only exists on that platform; this stands in for it so the
// DISPLAY rule can be exercised on every host.
const windowsNote = "ssh key permissions were NOT checked on this platform. " +
	"Verify it yourself with `icacls %USERPROFILE%\\.ssh\\id_ed25519`."

// withFakes points the command's two seams at scripted values: a sound
// credential, and an SSH-key check that reports one skipped check.
func withFakes(t *testing.T, note string) {
	t.Helper()
	prevGH := gh
	gh = &fakeGH{}
	t.Cleanup(func() { gh = prevGH })

	prevCheck := checkSSHKey
	checkSSHKey = func(string) ([]ghx.Problem, string) { return nil, note }
	t.Cleanup(func() { checkSSHKey = prevCheck })
}

// TestRunPreflightDisplaysTheSkippedCheck is the guard on the rule itself.
//
// The rule -- a user must never read a green preflight and believe something
// was verified that was not -- is a statement about WHAT IS DISPLAYED. ghx
// guarantees the notice is produced; only this guarantees it reaches the
// operator, in the right place, next to a verdict that admits it.
//
// It asserts on the WHOLE BUFFER of the function RunE actually calls. Testing
// passLine and writeNotes in isolation cannot catch a runPreflight that drops
// the notes, hardcodes an unqualified pass, or prints the notice below the
// green tick -- ordering is a property of the composition, not of either piece.
func TestRunPreflightDisplaysTheSkippedCheck(t *testing.T) {
	withFakes(t, windowsNote)

	var buf bytes.Buffer
	if err := runPreflight(&buf); err != nil {
		t.Fatalf("a sound credential with one inapplicable check still passes: %v", err)
	}
	out := buf.String()

	noteAt := strings.Index(out, windowsNote)
	if noteAt < 0 {
		t.Fatalf("the notice never reached the operator:\n%s", out)
	}
	tickAt := strings.Index(out, "✓")
	if tickAt < 0 {
		t.Fatalf("no verdict line at all:\n%s", out)
	}
	// Ordering, actually exercised: a caveat printed under the green tick is a
	// caveat nobody reads.
	if noteAt > tickAt {
		t.Errorf("the notice must precede the verdict, not follow it:\n%s", out)
	}
	// And the verdict itself must admit it, so the tick cannot be read alone.
	verdict := out[tickAt:]
	if !strings.Contains(verdict, "did NOT run") {
		t.Errorf("the verdict line must qualify itself; got %q", strings.TrimSpace(verdict))
	}
}

// TestRunPreflightFullPassIsUnqualified is the other half: where every check
// ran, the verdict must NOT be hedged. A preflight that always warns is a
// preflight nobody distinguishes -- the qualifier has to mean something.
func TestRunPreflightFullPassIsUnqualified(t *testing.T) {
	withFakes(t, "")

	var buf bytes.Buffer
	if err := runPreflight(&buf); err != nil {
		t.Fatalf("runPreflight: %v", err)
	}
	out := buf.String()

	if strings.Contains(out, "did NOT run") || strings.Contains(out, "! ") {
		t.Errorf("nothing was skipped, so nothing may be reported as skipped:\n%s", out)
	}
	if !strings.Contains(out, "✓ credential preflight passed\n") {
		t.Errorf("want an unqualified pass:\n%s", out)
	}
}

// TestRequirePreflightDisplaysTheSkippedCheck: `facet spawn` proceeds on a
// sound credential, and when it does it must still say which part of the
// surface went unchecked. Guarded because it is behaviour that is easy to
// "simplify" away -- the gate opens either way, so nothing else would notice.
func TestRequirePreflightDisplaysTheSkippedCheck(t *testing.T) {
	withFakes(t, windowsNote)

	var buf bytes.Buffer
	if err := requirePreflight(&buf, "spawn", false); err != nil {
		t.Fatalf("a sound credential must not block spawn: %v", err)
	}
	if !strings.Contains(buf.String(), windowsNote) {
		t.Errorf("spawn proceeded without saying what went unchecked:\n%s", buf.String())
	}
}

// TestRequirePreflightRefusesByDefault is the other half of the amendment on
// facet#109: the bypass is opt-in, never the default. An unsound credential
// with bypass=false must still refuse -- exactly as before the bypass existed.
func TestRequirePreflightRefusesByDefault(t *testing.T) {
	prevGH := gh
	gh = &fakeGH{auth: &ghx.AuthStatus{State: ghx.StateAbsent}}
	t.Cleanup(func() { gh = prevGH })

	var buf bytes.Buffer
	err := requirePreflight(&buf, "spawn", false)
	if err == nil {
		t.Fatal("an unsound credential was not refused")
	}
	if !strings.Contains(err.Error(), "spawn refused") {
		t.Errorf("error = %q, want it to name spawn's own refusal", err)
	}
	if !strings.Contains(err.Error(), "--unsound-credential") {
		t.Errorf("the refusal must name the bypass; got %q", err)
	}
}

// TestRequirePreflightBypassProceedsAndSaysSo is the Sculptor's ruling on
// facet#109 made concrete: bypass=true on an unsound credential proceeds
// (returns nil) but says so out loud, naming the flag and what was skipped --
// the whole point being that this is no longer an undocumented, silent
// escape hatch like `facet new` used to be.
func TestRequirePreflightBypassProceedsAndSaysSo(t *testing.T) {
	prevGH := gh
	gh = &fakeGH{auth: &ghx.AuthStatus{State: ghx.StateAbsent}}
	t.Cleanup(func() { gh = prevGH })

	var buf bytes.Buffer
	if err := requirePreflight(&buf, "spawn", true); err != nil {
		t.Fatalf("the bypass did not open the gate: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "UNSOUND") {
		t.Errorf("bypass must say the credential was unsound; got %q", out)
	}
	if !strings.Contains(out, "--unsound-credential") {
		t.Errorf("bypass must name the flag that authorised it; got %q", out)
	}
	if !strings.Contains(out, "spawn") {
		t.Errorf("bypass must name which command proceeded unsound; got %q", out)
	}
}

// TestPassLineNeverOverstatesItself is the assertion that keeps the Windows
// carve-out from becoming a lie.
//
// The permission half of the SSH-key check cannot run on Windows (see
// ghx.CheckSSHKey). That is acceptable only while the OUTPUT says so. A user
// must never read a green preflight on Windows and believe their key
// permissions were verified.
//
// This runs on every platform, because the reporting contract is what is being
// asserted, not the platform.
func TestPassLineNeverOverstatesItself(t *testing.T) {
	if got := passLine(nil); got != "✓ credential preflight passed" {
		t.Errorf("with every check applied, the pass line must be unqualified; got %q", got)
	}

	got := passLine([]string{"ssh key permissions were NOT checked on this platform. …"})
	if !strings.Contains(got, "did NOT run") {
		t.Errorf("a pass with a skipped check must say so on the pass line itself; got %q", got)
	}
	if strings.TrimSpace(got) == "✓ credential preflight passed" {
		t.Error("the qualified pass must not be indistinguishable from a full pass")
	}
}

// TestNotesPrintBeforeTheVerdict: a note printed after the green tick is a note
// nobody reads. It has to land above the line it qualifies.
func TestNotesPrintBeforeTheVerdict(t *testing.T) {
	var buf bytes.Buffer
	writeNotes(&buf, []string{"ssh key permissions were NOT checked on this platform."})
	out := buf.String()

	if !strings.HasPrefix(out, "! ") {
		t.Errorf("a note must be visually distinct from ✓ and ✗; got %q", out)
	}
	if !strings.Contains(out, "NOT checked") {
		t.Errorf("the note must survive to the output verbatim; got %q", out)
	}

	buf.Reset()
	writeNotes(&buf, nil)
	if buf.Len() != 0 {
		t.Errorf("no notes must print nothing at all; got %q", buf.String())
	}
}
