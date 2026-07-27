package main

import (
	"bytes"
	"strings"
	"testing"
)

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
