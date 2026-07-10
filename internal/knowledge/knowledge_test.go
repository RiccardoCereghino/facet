package knowledge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0o666); err != nil {
		t.Fatal(err)
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "area-x", `---
area: x
source_workspace: somewhere
last_reviewed: 2026-07-09
kind: invariants
---
# X

- A trap.
- Another trap.
`)
	f, err := Load(dir, "area-x")
	if err != nil {
		t.Fatal(err)
	}
	if f.Meta.Area != "x" || f.Meta.SourceWorkspace != "somewhere" {
		t.Errorf("meta = %+v", f.Meta)
	}
	if !strings.HasPrefix(f.Body, "# X") || strings.Contains(f.Body, "---") {
		t.Errorf("body not stripped of frontmatter: %q", f.Body)
	}
	if got := f.Meta.Reviewed().Format("2006-01-02"); got != "2026-07-09" {
		t.Errorf("Reviewed = %s", got)
	}
}

// A fragment that has drifted into holding status must fail loudly, because the
// whole point of the split is that it does not compete with the living doc.
func TestLoadRejectsNonInvariantKind(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "area-x", "---\narea: x\nkind: status\n---\nbody\n")
	if _, err := Load(dir, "area-x"); err == nil || !strings.Contains(err.Error(), "invariants") {
		t.Errorf("Load = %v; want a complaint about kind", err)
	}
}

func TestNoFrontmatterIsAllowed(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "area-x", "# just a body\n")
	f, err := Load(dir, "area-x")
	if err != nil {
		t.Fatal(err)
	}
	if f.Body != "# just a body" {
		t.Errorf("body = %q", f.Body)
	}
	if !f.IsStale(time.Now()) {
		t.Error("a fragment with no review date should read as stale")
	}
}

func TestUnterminatedFrontmatter(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "area-x", "---\narea: x\nbody with no closing delimiter\n")
	if _, err := Load(dir, "area-x"); err == nil {
		t.Error("accepted unterminated frontmatter")
	}
}

// A missing fragment must never block a spawn: it is reported and skipped.
func TestLoadAllSkipsMissing(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "area-a", "---\narea: a\nkind: invariants\n---\nA\n")
	got, errs := LoadAll(dir, []string{"area-a", "area-missing"})
	if len(got) != 1 || got[0].Name != "area-a" {
		t.Errorf("got %v", got)
	}
	if len(errs) != 1 {
		t.Errorf("errs = %v; want one", errs)
	}
}

func TestIsStale(t *testing.T) {
	now := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	fresh := Fragment{Meta: Meta{LastReviewed: "2026-07-01"}}
	old := Fragment{Meta: Meta{LastReviewed: "2025-01-01"}}
	if fresh.IsStale(now) {
		t.Error("a nine-day-old fragment is not stale")
	}
	if !old.IsStale(now) {
		t.Error("an eighteen-month-old fragment is stale")
	}
}

// A fragment saved by a Windows editor may start with a UTF-8 BOM, which would
// otherwise stop the frontmatter delimiter from matching at position zero.
func TestBOMIsStripped(t *testing.T) {
	dir := t.TempDir()
	bom := string([]byte{0xEF, 0xBB, 0xBF})
	write(t, dir, "area-x", bom+"---\narea: x\nkind: invariants\n---\nbody\n")
	f, err := Load(dir, "area-x")
	if err != nil {
		t.Fatalf("a UTF-8 BOM defeated frontmatter parsing: %v", err)
	}
	if f.Meta.Area != "x" {
		t.Errorf("meta = %+v; the BOM was not stripped", f.Meta)
	}
}
