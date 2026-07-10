package fslink

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateReadRemove(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	link := filepath.Join(root, "link")
	if err := os.Mkdir(target, 0o777); err != nil {
		t.Fatal(err)
	}
	canary := filepath.Join(target, "canary.txt")
	if err := os.WriteFile(canary, []byte("do not delete me"), 0o666); err != nil {
		t.Fatal(err)
	}

	if err := Create(link, target); err != nil {
		t.Fatalf("Create: %v", err)
	}

	ok, err := IsLink(link)
	if err != nil || !ok {
		t.Fatalf("IsLink(link) = %v, %v; want true, nil", ok, err)
	}

	got, ok, err := Read(link)
	if err != nil || !ok {
		t.Fatalf("Read(link) = %q, %v, %v", got, ok, err)
	}
	wantAbs, _ := filepath.Abs(target)
	if got != wantAbs {
		t.Errorf("Read(link) = %q, want %q", got, wantAbs)
	}

	// The link must be traversable.
	entries, err := os.ReadDir(link)
	if err != nil || len(entries) != 1 {
		t.Fatalf("ReadDir through link: %d entries, %v", len(entries), err)
	}

	// Remove drops the link, never the target's contents.
	if err := Remove(link); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Error("link survived Remove")
	}
	if _, err := os.Stat(canary); err != nil {
		t.Fatalf("!!! Remove damaged the target: %v", err)
	}
}

// The property that protects the real repos: Remove must refuse anything that
// is not a link, rather than recursing into it.
func TestRemoveRefusesPlainDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "plain")
	if err := os.Mkdir(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o666); err != nil {
		t.Fatal(err)
	}

	ok, err := IsLink(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("a plain directory reported as a link")
	}

	err = Remove(dir)
	var notLink *ErrNotALink
	if !errors.As(err, &notLink) {
		t.Fatalf("Remove(plain dir) = %v; want ErrNotALink", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "f")); err != nil {
		t.Fatalf("!!! Remove touched a plain directory: %v", err)
	}
}

func TestRemoveRefusesPlainFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(f, []byte("x"), 0o666); err != nil {
		t.Fatal(err)
	}
	var notLink *ErrNotALink
	if err := Remove(f); !errors.As(err, &notLink) {
		t.Fatalf("Remove(file) = %v; want ErrNotALink", err)
	}
	if _, err := os.Stat(f); err != nil {
		t.Fatal("!!! Remove deleted a plain file")
	}
}

func TestRemoveMissingIsNoOp(t *testing.T) {
	if err := Remove(filepath.Join(t.TempDir(), "nope")); err != nil {
		t.Errorf("Remove(missing) = %v; want nil", err)
	}
}

func TestReadNonLink(t *testing.T) {
	dir := t.TempDir()
	if _, ok, err := Read(dir); ok || err != nil {
		t.Errorf("Read(plain dir) = ok:%v err:%v; want false, nil", ok, err)
	}
}
