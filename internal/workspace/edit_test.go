package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/RiccardoCereghino/facet/internal/gitx"
	"github.com/RiccardoCereghino/facet/internal/manifest"
)

func TestNewAddRemove(t *testing.T) {
	roots, _, origin := setup(t)
	rep := quiet()

	ws, err := New(roots, gitx.Git{}, rep, NewOptions{Name: "demo", Description: "d"}, SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := AddClone(roots, ws, gitx.Git{}, rep, "repo", origin, nil, nil, false, SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ws, "repo", "README")); err != nil {
		t.Fatalf("clone not created: %v", err)
	}

	// A clone's checkout survives removal from the manifest: it may hold the only
	// copy of unpushed work.
	res, err := Remove(ws, "repo", rep)
	if err != nil {
		t.Fatal(err)
	}
	if !res.WasClone || res.CheckoutLeft == "" {
		t.Errorf("Remove(clone) = %+v; the checkout must be reported as left behind", res)
	}
	if _, err := os.Stat(filepath.Join(ws, "repo", "README")); err != nil {
		t.Error("!!! Remove deleted a clone's checkout")
	}
	m, err := manifest.Read(ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, still := m.Clones["repo"]; still {
		t.Error("manifest entry survived")
	}
}

func TestNewRefusesExisting(t *testing.T) {
	roots, _, _ := setup(t)
	if _, err := New(roots, gitx.Git{}, quiet(), NewOptions{Name: "demo"}, SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(roots, gitx.Git{}, quiet(), NewOptions{Name: "demo"}, SyncOptions{}); err == nil {
		t.Error("New overwrote an existing workspace")
	}
}

func TestAddCloneRefusesNameUsedByALink(t *testing.T) {
	roots, ws, origin := setup(t)
	m := &manifest.Manifest{Name: "w", Links: map[string]string{"x": "proj"}}
	if err := m.Write(ws); err != nil {
		t.Fatal(err)
	}
	err := AddClone(roots, ws, gitx.Git{}, quiet(), "x", origin, nil, nil, false, SyncOptions{})
	if err == nil {
		t.Error("AddClone accepted a name already used by a link")
	}
}

func TestRemoveUnknownEntry(t *testing.T) {
	_, ws, _ := setup(t)
	m := &manifest.Manifest{Name: "w"}
	if err := m.Write(ws); err != nil {
		t.Fatal(err)
	}
	if _, err := Remove(ws, "nope", quiet()); err == nil {
		t.Error("Remove accepted an unknown entry")
	}
}
