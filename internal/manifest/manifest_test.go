package manifest

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// Fixtures reproducing, byte for byte, every structural variant found among the
// live manifests this format was lifted from. Content is anonymised; formatting,
// key sets and key order are not.
var live = []string{"alpha", "beta", "gamma", "delta", "epsilon", "zeta"}

// Schema keys grew over time, and the previous implementation only rewrote a
// manifest when an origin changed -- which a clones-only workspace never
// triggers. So most manifests predate `lfs`, one predates `remotes`, and the
// junction-based one never had `clones` at all. Only gamma carries all eight.
var missingKeys = map[string][]string{
	"alpha":   {"lfs"},
	"beta":    {"lfs"},
	"gamma":   {},
	"delta":   {"remotes", "lfs"},
	"epsilon": {"clones", "remotes", "lfs"},
	"zeta":    {"lfs"},
}

func lineAt(lines []string, i int) string {
	if i < len(lines) {
		return lines[i]
	}
	return "<end of file>"
}

func readLive(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "manifests", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// insertedKey matches a line facet is allowed to add: an empty container for a
// schema key the file predates. Anything else appearing in the output is a bug.
var insertedKey = regexp.MustCompile(`^  "(clones|remotes|lfs)": (\{\}|\[\]),$`)

// TestGolden is the byte-compat gate.
//
// facet writes a manifest only to add or change real data. It must never
// reformat, reorder, re-escape or otherwise churn the user's versioned config
// repo. We prove that by marshalling each live manifest and asserting the output
// is the input plus, at most, the empty schema keys that file is missing.
func TestGolden(t *testing.T) {
	for _, name := range live {
		t.Run(name, func(t *testing.T) {
			want := readLive(t, name)
			m, err := Unmarshal(want)
			if err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			got, err := m.Marshal()
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}

			// Walk both line streams in order. got must equal want except for
			// inserted empty-schema-key lines; anything else is churn.
			gotLines := strings.Split(string(got), "\n")
			wantLines := strings.Split(string(want), "\n")
			var added []string
			gi, wi := 0, 0
			for gi < len(gotLines) {
				if wi < len(wantLines) && gotLines[gi] == wantLines[wi] {
					gi, wi = gi+1, wi+1
					continue
				}
				if mm := insertedKey.FindStringSubmatch(gotLines[gi]); mm != nil {
					added = append(added, mm[1])
					gi++
					continue
				}
				t.Fatalf("line %d churned.\n  want: %q\n  got:  %q", gi+1, lineAt(wantLines, wi), gotLines[gi])
			}
			if wi != len(wantLines) {
				t.Fatalf("facet dropped %d line(s), starting at %q", len(wantLines)-wi, lineAt(wantLines, wi))
			}
			if w := missingKeys[name]; len(added)+len(w) > 0 && !reflect.DeepEqual(added, w) {
				t.Errorf("inserted keys = %v, want %v", added, w)
			}
		})
	}
}

// gamma is the only fixture already written by the current schema. It must
// round-trip byte for byte, with nothing inserted. This pins the format itself:
// two-space indent, key order, empty maps as {}, trailing newline, LF.
func TestGoldenExactByteMatch(t *testing.T) {
	want := readLive(t, "gamma")
	m, err := Unmarshal(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("gamma must round-trip exactly.\n--- want ---\n%q\n--- got ---\n%q", want, got)
	}
}

// Writing is a fixed point: normalizing an already-normalized manifest is a no-op.
func TestMarshalIsIdempotent(t *testing.T) {
	for _, name := range live {
		t.Run(name, func(t *testing.T) {
			m1, err := Unmarshal(readLive(t, name))
			if err != nil {
				t.Fatal(err)
			}
			once, err := m1.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			m2, err := Unmarshal(once)
			if err != nil {
				t.Fatal(err)
			}
			twice, err := m2.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			if string(once) != string(twice) {
				t.Error("Marshal is not idempotent")
			}
		})
	}
}

// Normalization must not lose or invent data.
func TestRoundTripPreservesMeaning(t *testing.T) {
	for _, name := range live {
		t.Run(name, func(t *testing.T) {
			before, err := Unmarshal(readLive(t, name))
			if err != nil {
				t.Fatal(err)
			}
			b, err := before.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			after, err := Unmarshal(b)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Errorf("round trip changed the manifest:\nbefore %+v\nafter  %+v", before, after)
			}
		})
	}
}

func TestIssueKeyIsOptional(t *testing.T) {
	plain := &Manifest{Name: "w", Description: "d"}
	b, err := plain.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"issue"`) {
		t.Errorf("ordinary manifest grew an issue key:\n%s", b)
	}
	if !strings.Contains(string(b), `"links": {}`) || !strings.Contains(string(b), `"transient": []`) {
		t.Errorf("nil maps/slices did not render as {} / []:\n%s", b)
	}

	iss := &Manifest{
		Name:   "iss-x-1",
		Clones: map[string]string{"a": "git@h:o/a.git"},
		Issue:  &Issue{Repo: "o/a", Number: 1, Branch: "1-x", Home: "a", Labels: []string{"area/x"}},
	}
	b, err = iss.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	back, err := Unmarshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if !back.IsIssueWorkspace() || back.Issue.Number != 1 || back.Issue.Home != "a" {
		t.Errorf("issue block did not survive the round trip: %+v", back.Issue)
	}
}

// Go's encoder escapes < > & by default; PowerShell 7 (Newtonsoft) does not.
func TestNoHTMLEscaping(t *testing.T) {
	m := &Manifest{Name: "w", Description: "a & b <c> 'd'"}
	b, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"a & b <c> 'd'"`) {
		t.Errorf("description was escaped:\n%s", b)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		m    Manifest
		want string
	}{
		{"link and clone", Manifest{Name: "w",
			Links:  map[string]string{"x": "X"},
			Clones: map[string]string{"x": "u"}}, "both a link and a clone"},
		{"orphan remote", Manifest{Name: "w",
			Remotes: map[string]map[string]string{"x": {"upstream": "u"}}}, "remotes declared for non-clone"},
		{"orphan lfs", Manifest{Name: "w",
			LFS: map[string]bool{"x": false}}, "lfs declared for non-clone"},
		{"valid", Manifest{Name: "w",
			Clones:  map[string]string{"x": "u"},
			Remotes: map[string]map[string]string{"x": {"upstream": "u"}},
			LFS:     map[string]bool{"x": false}}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.m.ensureInit()
			err := tt.m.Validate()
			if tt.want == "" {
				if err != nil {
					t.Fatalf("want no error, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want error containing %q, got %v", tt.want, err)
			}
		})
	}
}

func TestWriteReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	m := &Manifest{Name: "w", Description: "d", Clones: map[string]string{"a": "u"}}
	if err := m.Write(dir); err != nil {
		t.Fatal(err)
	}
	back, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if back.Name != "w" || back.Clones["a"] != "u" {
		t.Errorf("got %+v", back)
	}
	b, _ := os.ReadFile(Path(dir))
	if b[len(b)-1] != '\n' {
		t.Error("missing trailing newline")
	}
	if strings.Contains(string(b), "\r\n") {
		t.Error("wrote CRLF; the format is LF (.gitattributes says eol=lf)")
	}
}

func TestReadMissingManifest(t *testing.T) {
	_, err := Read(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "facet new") {
		t.Errorf("want a helpful error pointing at `facet new`, got %v", err)
	}
}
