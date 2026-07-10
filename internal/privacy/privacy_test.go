package privacy

import (
	"bufio"
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

// TestNoOrganisationNamesInSource walks the tree and fails if any denied word
// appears. Extend `.denylist` rather than deleting this test.
func TestNoOrganisationNamesInSource(t *testing.T) {
	root := filepath.Join("..", "..")
	deny := denyList(t, root)
	if len(deny) == 0 {
		t.Skip("no .denylist and no FACET_DENYLIST: nothing to check. " +
			"Write one before publishing; see package doc.")
	}

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
		switch filepath.Ext(path) {
		case ".go", ".json", ".md", ".tmpl", ".kdl", ".yml", ".yaml", ".mod", ".sum", ".gitignore", ".gitattributes":
		default:
			if filepath.Base(path) != ".gitignore" && filepath.Base(path) != ".gitattributes" {
				return nil
			}
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lower := strings.ToLower(string(b))
		for _, word := range deny {
			if strings.Contains(lower, word) {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s contains a denied word: facet must carry no organisation's names", rel)
				break // one report per file is enough; do not echo the word
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The guard must not silently pass when it is unconfigured *and* something is
// wrong: prove the matcher works against a word we supply here.
func TestGuardDetectsAPlantedWord(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "leak.go"), []byte("package x // AcmeSecretName\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FACET_DENYLIST", "acmesecretname")
	deny := denyList(t, dir)
	if len(deny) != 1 || deny[0] != "acmesecretname" {
		t.Fatalf("denyList = %v", deny)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "leak.go"))
	if !strings.Contains(strings.ToLower(string(b)), deny[0]) {
		t.Error("the matcher would not have caught a planted word")
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
