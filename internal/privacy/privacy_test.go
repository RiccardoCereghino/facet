package privacy

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// denyList reads the words that must not appear in this repository.
//
// They come from `.denylist` at the repository root (gitignored, one word per
// line, `#` comments) or from FACET_DENYLIST (comma-separated). They are not
// checked in: a public repository containing a list of an employer's internal
// system names would be exactly the disclosure this guard exists to prevent.
func denyList(t *testing.T, root string) []string {
	t.Helper()
	if env := os.Getenv("FACET_DENYLIST"); env != "" {
		var out []string
		for _, w := range strings.Split(env, ",") {
			if w = strings.ToLower(strings.TrimSpace(w)); w != "" {
				out = append(out, w)
			}
		}
		return out
	}
	f, err := os.Open(filepath.Join(root, ".denylist"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, strings.ToLower(line))
	}
	return out
}

// scanTree walks root and returns the repo-relative paths of files containing a
// denied word (case-insensitively).
//
// It scans every text file, skipping only version-control/build directories,
// binary files, and the denylist itself. An allowlist of extensions -- the old
// approach -- would wave through a leaked name in a `.sh`, a `Dockerfile`, an
// `.env`, or any extensionless file.
func scanTree(root string, deny []string) ([]string, error) {
	var hits []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "dist":
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == ".denylist" {
			return nil // the list of forbidden words is not itself a leak
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.IndexByte(b, 0) >= 0 {
			return nil // binary: not source a name could hide in
		}
		lower := strings.ToLower(string(b))
		for _, word := range deny {
			if strings.Contains(lower, word) {
				rel, _ := filepath.Rel(root, path)
				hits = append(hits, rel)
				break // one report per file is enough; do not echo the word
			}
		}
		return nil
	})
	return hits, err
}

// TestNoOrganisationNamesInSource walks the tree and fails if any denied word
// appears. Extend `.denylist` (or FACET_DENYLIST) rather than deleting this test.
func TestNoOrganisationNamesInSource(t *testing.T) {
	root := filepath.Join("..", "..")
	deny := denyList(t, root)
	if len(deny) == 0 {
		// Fail closed where it matters. In CI an unconfigured guard is a silent
		// hole -- it passes green having checked nothing -- so demand the list,
		// supplied out-of-band (e.g. a FACET_DENYLIST secret). Locally, where a
		// contributor cannot hold the list, skip rather than block their build.
		if os.Getenv("CI") != "" {
			t.Fatal("no .denylist and no FACET_DENYLIST in CI: the organisation-name " +
				"guard would pass without checking anything. Set the FACET_DENYLIST " +
				"secret (comma-separated) or commit a private .denylist; see the package doc.")
		}
		t.Skip("no .denylist and no FACET_DENYLIST: nothing to check locally. " +
			"Configure one before publishing; see the package doc.")
	}

	hits, err := scanTree(root, deny)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range hits {
		t.Errorf("%s contains a denied word: facet must carry no organisation's names", rel)
	}
}

// The guard must actually catch a leak, so run the *real* scan over a tree with a
// planted word. The word is built by concatenation so its contiguous form never
// appears in this file -- otherwise the repo-wide scan above could trip over this
// very fixture.
func TestGuardDetectsAPlantedWord(t *testing.T) {
	word := "planted" + "orgname"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "leak.go"), []byte("package x // "+word+"\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	hits, err := scanTree(dir, []string{word})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0] != "leak.go" {
		t.Fatalf("the scan did not catch the planted word: hits = %v", hits)
	}
}

// A file type an extension allowlist would have skipped (here an extensionless
// Dockerfile, but equally a `.sh` or `.env`) must still be scanned.
func TestGuardScansBeyondAnExtensionAllowlist(t *testing.T) {
	word := "planted" + "orgname"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch # "+word+"\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	hits, err := scanTree(dir, []string{word})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("an extensionless file must be scanned: hits = %v", hits)
	}
}

// The denylist file holds the forbidden words by definition; scanning it would
// make the guard trip on its own configuration.
func TestGuardSkipsTheDenylistFile(t *testing.T) {
	word := "planted" + "orgname"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".denylist"), []byte(word+"\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	hits, err := scanTree(dir, []string{word})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf(".denylist must not be scanned: hits = %v", hits)
	}
}

// FACET_DENYLIST must win over the file, so a one-off check needs no edits.
func TestEnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".denylist"), []byte("fromfile\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if got := denyList(t, dir); len(got) != 1 || got[0] != "fromfile" {
		t.Fatalf("file list = %v", got)
	}
	t.Setenv("FACET_DENYLIST", "fromenv, other")
	got := denyList(t, dir)
	if len(got) != 2 || got[0] != "fromenv" || got[1] != "other" {
		t.Errorf("env list = %v", got)
	}
}
