package seat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// The verbatim test's first fixture was compact single-line JSON, while the
// real generator emits MarshalIndent'ed output -- so it could not have noticed
// a re-serialisation, which is the one property it existed to protect.
// Indented, multi-line, with the exact spacing json.MarshalIndent produces.
func TestWriteCatalogDoesNotReformatIndentedJSON(t *testing.T) {
	payload := "{\n  \"complete\": true,\n  \"tools\": [\n    {\n      \"name\": \"gad\"\n    }\n  ]\n}"
	dir := t.TempDir()
	bin := filepath.Join(dir, "argano")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ncat <<'JSONEOF'\n"+payload+"\nJSONEOF\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	ws := t.TempDir()
	if _, err := WriteCatalog(ws, bin); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(ws, CatalogFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimRight(string(b), "\n") != payload {
		t.Fatalf("catalog was re-serialised.\n got: %q\nwant: %q", b, payload)
	}
}

// The deadline's stated purpose is that a spawn must not be able to hang on an
// introspection, and spawn is what seats every banco. A hang there is a
// workspace nobody can tell is finished.
func TestWriteCatalogKillsAGeneratorThatHangs(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "argano")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nsleep 600\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	done := make(chan CatalogResult, 1)
	go func() {
		res, _ := writeCatalogWithin(t.TempDir(), bin, 200*time.Millisecond)
		done <- res
	}()
	select {
	case res := <-done:
		if res.OK {
			t.Fatal("a generator that never returns was reported as OK")
		}
		if !strings.Contains(res.Detail, "did not finish") {
			t.Fatalf("detail = %q, want it to name the timeout", res.Detail)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the deadline did not fire: a spawn can hang on an introspection")
	}
}

// A generator that reads stdin must not be handed the terminal.
func TestWriteCatalogGivesTheGeneratorNoStdin(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "argano")
	// Echoes whatever it reads. With no stdin it reads nothing and emits {}.
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '{\"read\":\"'; head -c 20; printf '\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ws := t.TempDir()
	if _, err := WriteCatalog(ws, bin); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(ws, CatalogFile))
	if !strings.Contains(string(b), `"read":""`) {
		t.Fatalf("the generator read something from stdin: %s", b)
	}
}
