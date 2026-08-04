package seat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The bytes below are the format, copied from workspaces that already carry
// these files and read back with `od -c`. They are asserted as literals rather
// than rebuilt from marshalScope, because a test that renders the file the same
// way the code does agrees with the code by construction and would not notice a
// format change -- and a separate program reads these files.
const (
	wantSeat  = "w-example-12\n"
	wantScope = "owner/repo#12\nacme/tools#7\n"
)

func TestWriteProducesTheOnDiskFormat(t *testing.T) {
	ws := t.TempDir()
	scope := []Ref{{Repo: "owner/repo", Number: 12}, {Repo: "acme/tools", Number: 7}}
	if err := Write(ws, "w-example-12", scope); err != nil {
		t.Fatalf("Write: %v", err)
	}

	for _, tc := range []struct{ file, want string }{
		{NameFile, wantSeat},
		{ScopeFile, wantScope},
	} {
		got, err := os.ReadFile(filepath.Join(ws, tc.file))
		if err != nil {
			t.Fatalf("read %s: %v", tc.file, err)
		}
		if string(got) != tc.want {
			t.Errorf("%s = %q, want %q", tc.file, got, tc.want)
		}
	}
}

// An operator's own workspace covers no single issue, so it has a name and no
// scope. That has to be the absence of a file rather than an empty one: a reader
// treats "no scope recorded" as nothing to check, and an empty file is a scope
// that permits nothing, which would need an exemption to work around.
func TestWriteWithoutScopeLeavesNoScopeFile(t *testing.T) {
	ws := t.TempDir()
	if err := Write(ws, "w-example-op", nil); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, ScopeFile)); !os.IsNotExist(err) {
		t.Errorf("stat %s: err = %v, want IsNotExist", ScopeFile, err)
	}
	got, err := ReadScope(ws)
	if err != nil || got != nil {
		t.Errorf("ReadScope of a workspace with no scope = %v, %v; want nil, nil", got, err)
	}
}

func TestWriteRefusesABadName(t *testing.T) {
	ws := t.TempDir()
	if err := Write(ws, "w-example-m7.1", nil); err == nil {
		t.Fatal("Write accepted a dotted seat name")
	}
	if _, err := os.Stat(filepath.Join(ws, NameFile)); !os.IsNotExist(err) {
		t.Errorf("a refused name still wrote %s (err = %v)", NameFile, err)
	}
}

func TestValidateName(t *testing.T) {
	tests := []struct {
		name    string
		seat    string
		wantErr bool
		// wantFix is a fragment the message must carry, so a refusal that only
		// names the failure cannot pass.
		wantFix string
	}{
		{"plain", "w-example-12", false, ""},
		{"digits and dashes", "w-example-71", false, ""},
		{"empty", "", true, "--seat"},

		// The dot is the one that has actually cost time. A multiplexer target
		// reads '.' as the pane separator, so this name addresses pane 1 of a
		// different session and every command aimed at the seat goes elsewhere.
		{"dot", "w-example-m7.1", true, "w-example-m71"},
		{"leading dot", ".hidden", true, "hidden"},

		{"slash", "w-example/12", true, "--seat"},
		{"backslash", `w-example\12`, true, "--seat"},
		{"space", "w example", true, "--seat"},
		{"tab", "w\texample", true, "--seat"},
		{"newline", "w-example\n", true, "--seat"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateName(tt.seat)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateName(%q) = %v, wantErr %v", tt.seat, err, tt.wantErr)
			}
			if err == nil {
				return
			}
			if !strings.Contains(err.Error(), tt.wantFix) {
				t.Errorf("ValidateName(%q) = %q, which does not tell the reader the fix (want it to contain %q)",
					tt.seat, err, tt.wantFix)
			}
		})
	}
}

func TestParseRef(t *testing.T) {
	tests := []struct {
		in      string
		want    Ref
		wantErr bool
	}{
		{"owner/repo#1", Ref{Repo: "owner/repo", Number: 1}, false},
		{"acme/some-tool#4321", Ref{Repo: "acme/some-tool", Number: 4321}, false},
		{"  owner/repo#2  ", Ref{Repo: "owner/repo", Number: 2}, false},

		{"owner/repo", Ref{}, true},   // no number at all
		{"#7", Ref{}, true},           // no repo
		{"repo#7", Ref{}, true},       // unqualified: a reader cannot resolve it
		{"owner/#7", Ref{}, true},     // half a repo
		{"/repo#7", Ref{}, true},      //
		{"owner/repo#0", Ref{}, true}, // issue numbers start at 1
		{"owner/repo#-3", Ref{}, true},
		{"owner/repo#seven", Ref{}, true},
		{"owner/repo#", Ref{}, true},
		{"own er/repo#7", Ref{}, true},
		{"", Ref{}, true},

		// landing:owner/repo -- facet#97: a repo the workspace's PRs land in
		// without claiming any issue there.
		{"landing:owner/repo", Ref{Repo: "owner/repo", Landing: true}, false},
		{"  landing:owner/repo  ", Ref{Repo: "owner/repo", Landing: true}, false},
		{"landing:owner/some-tool", Ref{Repo: "owner/some-tool", Landing: true}, false},
		{"landing:owner", Ref{}, true},  // no repo half
		{"landing:owner/", Ref{}, true}, // half a repo
		{"landing:/repo", Ref{}, true},  //
		{"landing:", Ref{}, true},       // nothing at all
		{"landing:own er/repo", Ref{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseRef(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseRef(%q) = %v, %v; wantErr %v", tt.in, got, err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Errorf("ParseRef(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseRefsDropsDuplicatesAndKeepsOrder(t *testing.T) {
	got, err := ParseRefs([]string{"owner/repo#12", "acme/tools#7", "owner/repo#12"})
	if err != nil {
		t.Fatalf("ParseRefs: %v", err)
	}
	want := []Ref{{Repo: "owner/repo", Number: 12}, {Repo: "acme/tools", Number: 7}}
	if len(got) != len(want) {
		t.Fatalf("ParseRefs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ParseRefs[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestAppendScopeIsAdditiveAndIdempotent(t *testing.T) {
	ws := t.TempDir()
	if err := Write(ws, "w-example-12", []Ref{{Repo: "owner/repo", Number: 12}}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	added, err := AppendScope(ws, []Ref{{Repo: "acme/tools", Number: 7}})
	if err != nil {
		t.Fatalf("AppendScope: %v", err)
	}
	if len(added) != 1 || added[0].String() != "acme/tools#7" {
		t.Errorf("AppendScope reported %v as new, want [acme/tools#7]", added)
	}

	// Again, with one already present and one new: only the new one is added,
	// and the file gains exactly one line.
	added, err = AppendScope(ws, []Ref{{Repo: "acme/tools", Number: 7}, {Repo: "owner/repo", Number: 99}})
	if err != nil {
		t.Fatalf("AppendScope: %v", err)
	}
	if len(added) != 1 || added[0].String() != "owner/repo#99" {
		t.Errorf("AppendScope reported %v as new, want [owner/repo#99]", added)
	}

	got, err := os.ReadFile(filepath.Join(ws, ScopeFile))
	if err != nil {
		t.Fatal(err)
	}
	want := "owner/repo#12\nacme/tools#7\nowner/repo#99\n"
	if string(got) != want {
		t.Errorf("%s = %q, want %q", ScopeFile, got, want)
	}
}

// AppendScope is the one entry point that can meet a scope file somebody edited
// by hand. It must refuse rather than silently drop the line it cannot read.
func TestAppendScopeRefusesAnUnreadableFile(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, ScopeFile), []byte("owner/repo#12\nnot a reference\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := AppendScope(ws, []Ref{{Repo: "acme/tools", Number: 7}}); err == nil {
		t.Fatal("AppendScope accepted a scope file with an unparseable line")
	} else if !strings.Contains(err.Error(), "line 2") {
		t.Errorf("error %q does not say which line is wrong", err)
	}
}

// RemoveScope is add's opposite (facet#112): a boundary that could only
// widen was a ratchet, and a wrong entry was permanent. This is the basic
// shape -- drop one, keep the rest, report which was actually removed.
func TestRemoveScopeDropsOnlyWhatWasAsked(t *testing.T) {
	ws := t.TempDir()
	if err := Write(ws, "w-example-12", []Ref{
		{Repo: "owner/repo", Number: 12},
		{Repo: "acme/tools", Number: 7},
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	removed, remaining, err := RemoveScope(ws, []Ref{{Repo: "acme/tools", Number: 7}})
	if err != nil {
		t.Fatalf("RemoveScope: %v", err)
	}
	if len(removed) != 1 || removed[0].String() != "acme/tools#7" {
		t.Errorf("removed = %v, want [acme/tools#7]", removed)
	}
	if len(remaining) != 1 || remaining[0].String() != "owner/repo#12" {
		t.Errorf("remaining = %v, want [owner/repo#12]", remaining)
	}

	got, err := ReadScope(ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].String() != "owner/repo#12" {
		t.Errorf("scope on disk = %v, want [owner/repo#12]", got)
	}
}

// Removing something never present is a no-op, matching AppendScope's
// idempotence in the other direction.
func TestRemoveScopeOfAnAbsentEntryIsANoOp(t *testing.T) {
	ws := t.TempDir()
	if err := Write(ws, "w-example-12", []Ref{{Repo: "owner/repo", Number: 12}}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	removed, remaining, err := RemoveScope(ws, []Ref{{Repo: "acme/tools", Number: 7}})
	if err != nil {
		t.Fatalf("RemoveScope: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("removed = %v, want none", removed)
	}
	if len(remaining) != 1 || remaining[0].String() != "owner/repo#12" {
		t.Errorf("remaining = %v, want [owner/repo#12] unchanged", remaining)
	}
}

// Removing the last entry deletes .scope rather than leaving an empty file --
// the same "absent means nothing recorded" rule Write follows.
func TestRemoveScopeToEmptyDeletesTheFile(t *testing.T) {
	ws := t.TempDir()
	if err := Write(ws, "w-example-12", []Ref{{Repo: "owner/repo", Number: 12}}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if _, _, err := RemoveScope(ws, []Ref{{Repo: "owner/repo", Number: 12}}); err != nil {
		t.Fatalf("RemoveScope: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, ScopeFile)); !os.IsNotExist(err) {
		t.Errorf("stat %s = %v, want IsNotExist", ScopeFile, err)
	}
	got, err := ReadScope(ws)
	if err != nil {
		t.Fatalf("ReadScope after delete: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ReadScope = %v, want none", got)
	}
}

// SetScope always writes, even when the new value equals the old -- it is an
// assignment, not a merge -- and reports the previous value the way tree
// wire reports the parent an edge moved a child away from.
func TestSetScopeReplacesAndReportsThePrevious(t *testing.T) {
	ws := t.TempDir()
	if err := Write(ws, "w-example-12", []Ref{
		{Repo: "owner/repo", Number: 12},
		{Repo: "acme/tools", Number: 7},
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	previous, err := SetScope(ws, []Ref{{Repo: "acme/tools", Number: 99}})
	if err != nil {
		t.Fatalf("SetScope: %v", err)
	}
	if len(previous) != 2 {
		t.Errorf("previous = %v, want the 2 original entries", previous)
	}

	got, err := ReadScope(ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].String() != "acme/tools#99" {
		t.Errorf("scope on disk = %v, want [acme/tools#99]", got)
	}
}

// SetScope with no refs clears the scope entirely, deleting the file --
// the documented way to empty a workspace's scope rather than hand-editing.
func TestSetScopeToNothingDeletesTheFile(t *testing.T) {
	ws := t.TempDir()
	if err := Write(ws, "w-example-12", []Ref{{Repo: "owner/repo", Number: 12}}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if _, err := SetScope(ws, nil); err != nil {
		t.Fatalf("SetScope: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, ScopeFile)); !os.IsNotExist(err) {
		t.Errorf("stat %s = %v, want IsNotExist", ScopeFile, err)
	}
}

func TestReadScopeIgnoresBlankLines(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, ScopeFile), []byte("owner/repo#12\n\n  \nacme/tools#7\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	got, err := ReadScope(ws)
	if err != nil {
		t.Fatalf("ReadScope: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("ReadScope = %v, want 2 entries", got)
	}
}

func TestReadName(t *testing.T) {
	ws := t.TempDir()
	if got, err := ReadName(ws); got != "" || err != nil {
		t.Errorf("ReadName of a workspace with no seat = %q, %v; want \"\", nil", got, err)
	}
	if err := Write(ws, "w-example-12", nil); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadName(ws); got != "w-example-12" || err != nil {
		t.Errorf("ReadName = %q, %v; want w-example-12, nil", got, err)
	}
}

// The read-back exists because a write that reports success and does not land is
// this codebase's most repeated failure. A read-back nobody has seen catch
// anything is decoration, so this hands it a file whose contents are not what
// was asked for and requires it to say so.
func TestVerifyCatchesAWriteThatDidNotLand(t *testing.T) {
	p := filepath.Join(t.TempDir(), NameFile)
	if err := os.WriteFile(p, []byte("w-someone-else\n"), 0o666); err != nil {
		t.Fatal(err)
	}

	err := verify(p, []byte("w-example-12\n"))
	if err == nil {
		t.Fatal("verify passed on a file that does not hold what was written")
	}
	// It has to name both, or the reader cannot tell which end is wrong.
	for _, want := range []string{"w-someone-else", "w-example-12"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}

	if err := verify(p, []byte("w-someone-else\n")); err != nil {
		t.Errorf("verify failed on a file that does hold what was written: %v", err)
	}
}

func TestVerifyReportsAMissingFile(t *testing.T) {
	if err := verify(filepath.Join(t.TempDir(), "absent"), []byte("x\n")); err == nil {
		t.Fatal("verify passed on a file that does not exist")
	}
}

// FindNearest is gad's own walk, not config.FindWorkspace's (facet#68): the
// nearest ancestor holding .seat, not the nearest .workspace.json.
func TestFindNearestWalksUpToTheNearestSeatFile(t *testing.T) {
	root := t.TempDir()
	if err := Write(root, "w-outer", nil); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(sub, 0o777); err != nil {
		t.Fatal(err)
	}

	got, err := FindNearest(sub)
	if err != nil {
		t.Fatalf("FindNearest: %v", err)
	}
	if got != root {
		t.Errorf("FindNearest(%s) = %s, want %s", sub, got, root)
	}
}

// A NEARER .seat below the walk's starting point must win over a farther one
// -- this is the exact shape a clone-seeded seat has: its own .seat sits
// between the working directory and the workspace root's.
func TestFindNearestPrefersTheCloserSeatFile(t *testing.T) {
	root := t.TempDir()
	if err := Write(root, "w-outer", nil); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(root, "repo")
	if err := os.MkdirAll(inner, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := Write(inner, "w-inner", nil); err != nil {
		t.Fatal(err)
	}

	got, err := FindNearest(inner)
	if err != nil {
		t.Fatalf("FindNearest: %v", err)
	}
	if got != inner {
		t.Errorf("FindNearest(%s) = %s, want the nearer %s, not the outer one", inner, got, inner)
	}
}

// No .seat anywhere in the ancestry must be a plain error, not a panic or a
// silent empty string -- callers for whom this is a legitimate state (an
// unseeded workspace) are expected to catch it and fall back.
func TestFindNearestErrorsWhenNoSeatFileExistsAnywhere(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "no-seat-here")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := FindNearest(dir); err == nil {
		t.Fatal("FindNearest found a seat file that was never written")
	}
}
