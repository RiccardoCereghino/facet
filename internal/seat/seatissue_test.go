package seat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSeatIssueRoundTrips is the ordinary path: the spawner writes it, anyone
// reads it back, and the bytes on disk are the wire contract a separate reader
// (gad's internal/record) parses.
func TestSeatIssueRoundTrips(t *testing.T) {
	ws := t.TempDir()
	want := Ref{Repo: "RiccardoCereghino/stele", Number: 151}

	if err := WriteSeatIssue(ws, want); err != nil {
		t.Fatalf("WriteSeatIssue: %v", err)
	}

	// The on-disk form is fixed by the reader written against it: one ref,
	// newline-terminated.
	b, err := os.ReadFile(filepath.Join(ws, SeatIssueFile))
	if err != nil {
		t.Fatalf("reading %s: %v", SeatIssueFile, err)
	}
	if got, w := string(b), "RiccardoCereghino/stele#151\n"; got != w {
		t.Errorf("%s = %q, want %q", SeatIssueFile, got, w)
	}

	got, have, err := ReadSeatIssue(ws)
	if err != nil {
		t.Fatalf("ReadSeatIssue: %v", err)
	}
	if !have {
		t.Fatal("ReadSeatIssue reports none recorded after writing one")
	}
	if got != want {
		t.Errorf("ReadSeatIssue = %v, want %v", got, want)
	}
}

// TestMissingSeatIssueIsNotAnError — no file means none recorded, which is the
// state of every workspace created before this existed and of any workspace an
// operator drives directly. It must keep working without an exemption.
func TestMissingSeatIssueIsNotAnError(t *testing.T) {
	ws := t.TempDir()

	ref, have, err := ReadSeatIssue(ws)
	if err != nil {
		t.Fatalf("a workspace with no %s errored: %v", SeatIssueFile, err)
	}
	if have {
		t.Errorf("reports a seat issue %v where no file exists", ref)
	}
}

// TestPresentButEmptySeatIssueIsAnError is the distinction facet#81 asks for by
// name: missing must be distinguishable from empty, because "no seat issue" and
// "the spawner meant to write one and did not" are both defensible readings and
// guessing either way hides a spawn defect.
func TestPresentButEmptySeatIssueIsAnError(t *testing.T) {
	for _, body := range []string{"", "\n", "  \n\t\n", "# only a comment\n"} {
		ws := t.TempDir()
		if err := os.WriteFile(filepath.Join(ws, SeatIssueFile), []byte(body), 0o666); err != nil {
			t.Fatalf("writing %s: %v", SeatIssueFile, err)
		}

		_, have, err := ReadSeatIssue(ws)
		if err == nil {
			t.Errorf("a present-but-empty %s (%q) read as have=%v with no error; want an error", SeatIssueFile, body, have)
			continue
		}
		if !strings.Contains(err.Error(), "names no issue") {
			t.Errorf("%q errored with %v, want it to say the file names no issue", body, err)
		}
	}
}

// TestMalformedSeatIssueIsRefused — a nearly-right ref is worse than a refused
// one, the same argument ParseRef already makes: the reader simply would not
// match it, and the workspace would look wrong for a reason nothing states.
func TestMalformedSeatIssueIsRefused(t *testing.T) {
	for _, body := range []string{
		"not a reference\n",
		"RiccardoCereghino/stele\n",
		"RiccardoCereghino/stele#\n",
		"RiccardoCereghino/stele#zero\n",
		"RiccardoCereghino/stele#0\n",
		"stele#151\n",
	} {
		ws := t.TempDir()
		if err := os.WriteFile(filepath.Join(ws, SeatIssueFile), []byte(body), 0o666); err != nil {
			t.Fatalf("writing %s: %v", SeatIssueFile, err)
		}
		if _, _, err := ReadSeatIssue(ws); err == nil {
			t.Errorf("a malformed %s (%q) was accepted", SeatIssueFile, body)
		}
	}
}

// TestSeatIssueAllowsANoteBeneathTheRef mirrors what .seat already permits under
// the seat name.
func TestSeatIssueAllowsANoteBeneathTheRef(t *testing.T) {
	ws := t.TempDir()
	body := "# the seat record\nRiccardoCereghino/stele#151\nseated 2026-07-31 by w-lapidary-m9d\n"
	if err := os.WriteFile(filepath.Join(ws, SeatIssueFile), []byte(body), 0o666); err != nil {
		t.Fatalf("writing %s: %v", SeatIssueFile, err)
	}

	got, have, err := ReadSeatIssue(ws)
	if err != nil || !have {
		t.Fatalf("ReadSeatIssue = %v, %v, %v", got, have, err)
	}
	if want := (Ref{Repo: "RiccardoCereghino/stele", Number: 151}); got != want {
		t.Errorf("ReadSeatIssue = %v, want %v", got, want)
	}
}

// TestWriteSeatIssueIsIndependentOfWrite — a workspace may have a seat and a
// scope and no seat issue, which is every workspace created before this file
// existed. Write must not have started producing one.
func TestWriteSeatIssueIsIndependentOfWrite(t *testing.T) {
	ws := t.TempDir()
	if err := Write(ws, "m9-gad247", []Ref{{Repo: "RiccardoCereghino/gad", Number: 247}}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, SeatIssueFile)); !os.IsNotExist(err) {
		t.Errorf("Write created %s; it writes .seat and .scope only", SeatIssueFile)
	}
}
