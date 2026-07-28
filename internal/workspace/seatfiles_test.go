package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/RiccardoCereghino/facet/internal/gitx"
	"github.com/RiccardoCereghino/facet/internal/manifest"
)

// A workspace's .seat and .scope are facts about the moment it was created, and
// nothing that rebuilds a workspace from its manifest may touch them.
//
// The reason is asymmetric. Missing files mean a workspace whose owner is not
// recorded, which is visible and fixable. Rewritten files mean a workspace that
// confidently reports the wrong owner, which is neither -- and the whole value
// of writing identity down is that a reader can trust what it says. Sync also
// runs against workspaces that already have work in them, so "make it match the
// manifest" must stop at the manifest's edge, exactly as it already does for a
// clone that may hold the only copy of unpushed work.
//
// Sync does not write these files today. This exists so that it cannot start:
// the change that added them would pass every other test in this package.
func TestSyncLeavesTheSeatFilesAlone(t *testing.T) {
	roots, ws, origin := setup(t)
	m := &manifest.Manifest{Name: "w", Clones: map[string]string{"repo": origin}}
	if err := m.Write(ws); err != nil {
		t.Fatal(err)
	}

	// Written the way a workspace created before any of this would have them:
	// contents that Sync has no way to re-derive, so a rewrite cannot coincide
	// with what is already there.
	files := map[string][]byte{
		".seat":  []byte("w-example-original\n"),
		".scope": []byte("owner/repo#12\nacme/tools#7\n"),
	}
	for name, want := range files {
		if err := os.WriteFile(filepath.Join(ws, name), want, 0o666); err != nil {
			t.Fatal(err)
		}
	}

	// Twice: once creating the clone, once with everything already in place,
	// because the two paths through Sync are different.
	for i := range 2 {
		if err := Sync(roots, ws, gitx.Git{}, quiet(), SyncOptions{}); err != nil {
			t.Fatalf("sync %d: %v", i+1, err)
		}
		for name, want := range files {
			got, err := os.ReadFile(filepath.Join(ws, name))
			if err != nil {
				t.Fatalf("sync %d: %s is gone: %v", i+1, name, err)
			}
			if string(got) != string(want) {
				t.Errorf("sync %d rewrote %s: got %q, want %q", i+1, name, got, want)
			}
		}
	}
}

// The same for --prune, which is the one option that deletes anything. It exists
// to remove links the manifest no longer names; an unversioned file at the
// workspace root is not a link and is not its business.
func TestSyncPruneLeavesTheSeatFilesAlone(t *testing.T) {
	roots, ws, origin := setup(t)
	m := &manifest.Manifest{Name: "w", Clones: map[string]string{"repo": origin}}
	if err := m.Write(ws); err != nil {
		t.Fatal(err)
	}
	seatPath := filepath.Join(ws, ".seat")
	if err := os.WriteFile(seatPath, []byte("w-example-original\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := Sync(roots, ws, gitx.Git{}, quiet(), SyncOptions{Prune: true}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(seatPath)
	if err != nil {
		t.Fatalf("sync --prune deleted .seat: %v", err)
	}
	if string(got) != "w-example-original\n" {
		t.Errorf(".seat = %q, want it untouched", got)
	}
}
