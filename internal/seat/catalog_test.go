package seat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The acceptance clause that is easiest to get wrong: a generation failure
// leaves a file that SAYS it failed. An absent file that means "generation
// failed" is indistinguishable from "not supported yet" -- the same defect
// class as .mode, which is always present so that absence is a defect.
func TestWriteCatalogWritesAFileEvenWhenGenerationFails(t *testing.T) {
	for _, tt := range []struct{ name, bin string }{
		{"no generator at all", ""},
		{"generator not installed", filepath.Join(t.TempDir(), "no-such-argano")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ws := t.TempDir()
			res, err := WriteCatalog(ws, tt.bin)
			if err != nil {
				t.Fatalf("err = %v -- a failure to GENERATE must not be a failure to WRITE", err)
			}
			if res.OK {
				t.Fatal("OK = true with no generator")
			}
			b, rerr := os.ReadFile(filepath.Join(ws, CatalogFile))
			if rerr != nil {
				t.Fatalf("no catalog file was written: %v", rerr)
			}
			var got map[string]any
			if jerr := json.Unmarshal(b, &got); jerr != nil {
				t.Fatalf("the failure file is not valid JSON, so a consumer that only decodes sees a parse error instead of a reason: %v", jerr)
			}
			if got["error"] == nil || got["error"] == "" {
				t.Fatalf("the file does not say what went wrong: %s", b)
			}
			if res.Detail == "" {
				t.Fatal("the result carries no detail, so spawn cannot name the failure in its output")
			}
		})
	}
}

// The happy path: whatever the generator prints is what lands, byte for byte.
// facet does not reformat it -- one answer to "what commands exist".
func TestWriteCatalogWritesTheGeneratorsOutputVerbatim(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "argano")
	const payload = `{"complete":true,"tools":[{"name":"gad"}]}`
	script := "#!/bin/sh\ncat <<'EOF'\n" + payload + "\nEOF\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	ws := t.TempDir()
	res, err := WriteCatalog(ws, bin)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !res.OK {
		t.Fatalf("OK = false: %s", res.Detail)
	}
	b, err := os.ReadFile(filepath.Join(ws, CatalogFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(b)) != payload {
		t.Fatalf("catalog = %q, want the generator's own bytes", b)
	}
}

// A generator that exits non-zero has said its catalog is incomplete. facet
// must not present that as a whole one, so it is a failure here and the file
// says so.
func TestWriteCatalogTreatsANonZeroGeneratorAsAFailure(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "argano")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho '{\"complete\":false}'\necho 'could not introspect facet' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ws := t.TempDir()
	res, err := WriteCatalog(ws, bin)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if res.OK {
		t.Fatal("an incomplete catalog was reported as OK")
	}
	if !strings.Contains(res.Detail, "could not introspect facet") {
		t.Fatalf("detail = %q, want the generator's own stderr", res.Detail)
	}
}
