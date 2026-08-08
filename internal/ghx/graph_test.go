package ghx

import "testing"

func TestParseIssueParent(t *testing.T) {
	t.Run("a parent that exists", func(t *testing.T) {
		out := []byte(`{"data":{"repository":{"issue":{"parent":{"number":282,
			"repository":{"owner":{"login":"acme"},"name":"doctrine"}}}}}}`)
		ref, ok, err := parseIssueParent(out)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if !ok {
			t.Fatal("ok = false, want true: the payload carries a parent")
		}
		if got, want := ref.String(), "acme/doctrine#282"; got != want {
			t.Errorf("ref = %q, want %q", got, want)
		}
	})

	t.Run("null parent is not an error", func(t *testing.T) {
		out := []byte(`{"data":{"repository":{"issue":{"parent":null}}}}`)
		_, ok, err := parseIssueParent(out)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if ok {
			t.Error("ok = true, want false: an unparented issue is valid, not an error")
		}
	})

	// The reason this whole path is GraphQL. A REST issue object has no
	// `parent` key at all -- it is ABSENT rather than null -- so it decodes
	// here to exactly the same "no parent" as a genuine null. That is why the
	// REST endpoint cannot be used for this direction: it cannot express the
	// difference, and every child reads as unparented.
	t.Run("a REST issue body decodes as unparented, which is why REST cannot answer this", func(t *testing.T) {
		out := []byte(`{"number":75,"title":"a real issue that HAS a parent","state":"open"}`)
		_, ok, err := parseIssueParent(out)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if ok {
			t.Fatal("ok = true: the fixture would have to carry a parent key, which REST never does")
		}
	})
}

func TestParseDependencyEdges(t *testing.T) {
	t.Run("one page", func(t *testing.T) {
		out := []byte(`[{"number":48,"repository":{"full_name":"acme/lab"}}]`)
		refs, err := parseDependencyEdges(out)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(refs) != 1 || refs[0].String() != "acme/lab#48" {
			t.Errorf("refs = %v, want [acme/lab#48]", refs)
		}
	})

	// gh --paginate CONCATENATES arrays instead of merging them, so anything
	// past the first page is `[...][...]` -- not a document json.Unmarshal
	// accepts. A single Unmarshal passes every small fixture and fails only on
	// the issues big enough to matter, which is the worst possible shape for a
	// bug.
	t.Run("two concatenated pages", func(t *testing.T) {
		out := []byte(`[{"number":1,"repository":{"full_name":"acme/lab"}}]` +
			`[{"number":2,"repository":{"full_name":"acme/other"}}]`)
		refs, err := parseDependencyEdges(out)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(refs) != 2 {
			t.Fatalf("got %d refs, want 2 -- pages were not both read", len(refs))
		}
		if refs[0].String() != "acme/lab#1" || refs[1].String() != "acme/other#2" {
			t.Errorf("refs = %v", refs)
		}
	})

	t.Run("no dependencies", func(t *testing.T) {
		refs, err := parseDependencyEdges([]byte(`[]`))
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(refs) != 0 {
			t.Errorf("refs = %v, want none", refs)
		}
	})
}

// facet#105: title/state/labels must come back from THIS query, or the whole
// point -- one call per parent instead of two per node -- is lost the moment
// a caller reaches for a second read to fill them in.
func TestSplitRepoRefusesAmbiguity(t *testing.T) {
	for _, bad := range []string{"", "lab", "/lab", "acme/"} {
		if _, _, err := splitRepo("test", bad); err == nil {
			t.Errorf("splitRepo(%q) accepted it; a half-formed repo becomes a 404 three calls later", bad)
		}
	}
	owner, name, err := splitRepo("test", "acme/lab")
	if err != nil || owner != "acme" || name != "lab" {
		t.Errorf("splitRepo(acme/lab) = %q, %q, %v", owner, name, err)
	}
}
