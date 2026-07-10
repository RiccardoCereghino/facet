package routing

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/RiccardoCereghino/facet/internal/ghx"
)

func load(t *testing.T) *Routing {
	t.Helper()
	r, err := Load(filepath.Join("..", "..", "testdata", "routing.json"))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func issue(t *testing.T, name string) *ghx.Issue {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "issues", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var iss ghx.Issue
	if err := json.Unmarshal(b, &iss); err != nil {
		t.Fatal(err)
	}
	return &iss
}

func TestInfer(t *testing.T) {
	r := load(t)
	tests := []struct {
		name     string
		fixture  string
		home     string
		wantKeys []string
		// wantReasons, when set, is checked for the named key.
		reasonKey  string
		wantReason []string
		wantHints  []string
	}{
		{
			// The case the whole design turns on. This issue is labelled
			// area/backups with NO terraform label, but its body says it is
			// blocked by an issue in the infra repo. Label-only routing would
			// clone `platform` alone and the work would stall.
			name:    "cross-repo blocker with no matching label",
			fixture: "backups-blocked", home: "acme/platform",
			wantKeys:  []string{"platform", "infra"},
			reasonKey: "infra",
			wantReason: []string{
				"blocked-by:acme/infra-core#41", // the body
				"label:area/backups",            // and the prior agrees
			},
		},
		{
			// A plain cross-reference, not a declared blocker.
			name:    "xref is distinguished from blocked-by",
			fixture: "epic-environments", home: "acme/platform",
			wantKeys:  []string{"platform", "infra"},
			reasonKey: "infra",
			wantReason: []string{
				"label:area/environments",
				"xref:acme/infra-core#43",
			},
			// legacyapp has no label anywhere; it can only ever be a hint.
			wantHints: []string{"legacyapp"},
		},
		{
			// No area/* label and no cross-refs: the home repo, and nothing else.
			name:    "label-less issue collapses to home",
			fixture: "no-area-labels", home: "acme/platform",
			wantKeys:  []string{"platform"},
			reasonKey: "platform", wantReason: []string{"home"},
		},
		{
			// A component label fans out to repos that are not the home repo.
			name:    "component label fans out",
			fixture: "component-label", home: "acme/platform",
			wantKeys:  []string{"platform", "k8s-stack", "widget-backend", "widgetapi"},
			reasonKey: "widgetapi", wantReason: []string{"label:widget"},
		},
		{
			// Home follows the repo the issue was filed in, not its labels.
			name:    "home is the repo, not the label",
			fixture: "infra-home", home: "acme/infra-core",
			wantKeys:  []string{"infra", "platform"},
			reasonKey: "infra", wantReason: []string{"home", "label:area/security", "label:area/terraform"},
		},
		{
			// The author said what they meant: the scope field replaces the
			// label guess entirely. area/backups would have added `platform`
			// via the area map; it must not appear except as home.
			name:    "scope field overrides the area map",
			fixture: "form-with-scope", home: "acme/platform",
			wantKeys:  []string{"platform", "gateway", "infra"},
			reasonKey: "infra", wantReason: []string{"scope-field"},
		},
		{
			// An unanswered scope field falls back to labels.
			name:    "unsure scope field falls back to labels",
			fixture: "form-unsure", home: "acme/platform",
			wantKeys:  []string{"platform", "infra"},
			reasonKey: "infra", wantReason: []string{"label:area/terraform"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sel, hints := r.Infer(tt.home, issue(t, tt.fixture))
			if got := Keys(sel); !reflect.DeepEqual(got, tt.wantKeys) {
				t.Errorf("keys = %v, want %v", got, tt.wantKeys)
			}
			if !sel[0].Home {
				t.Errorf("first selection %q is not the home repo", sel[0].Key)
			}
			for _, s := range sel[1:] {
				if s.Home {
					t.Errorf("more than one home repo: %q", s.Key)
				}
			}
			if tt.reasonKey != "" {
				for _, s := range sel {
					if s.Key != tt.reasonKey {
						continue
					}
					if !reflect.DeepEqual(s.Reasons, tt.wantReason) {
						t.Errorf("reasons for %q = %v, want %v", s.Key, s.Reasons, tt.wantReason)
					}
				}
			}
			var gotHints []string
			for _, h := range hints {
				gotHints = append(gotHints, h.Key)
			}
			if len(tt.wantHints) > 0 && !reflect.DeepEqual(gotHints, tt.wantHints) {
				t.Errorf("hints = %v, want %v", gotHints, tt.wantHints)
			}
		})
	}
}

// A hint must never silently become a selection.
func TestHintsAreNotSelections(t *testing.T) {
	r := load(t)
	sel, hints := r.Infer("acme/platform", issue(t, "epic-environments"))
	for _, h := range hints {
		for _, s := range sel {
			if s.Key == h.Key {
				t.Errorf("%q appears as both a hint and a selection", h.Key)
			}
		}
	}
	if len(hints) == 0 {
		t.Error("expected a hint for the unlabelled system named in the body")
	}
}

// An issue's own repo must never be added twice, nor listed as a cross-ref to
// itself.
func TestHomeRepoSelfReferenceIsIgnored(t *testing.T) {
	r := load(t)
	iss := &ghx.Issue{Number: 1, Body: "see acme/platform#2 and acme/infra-core#3"}
	sel, _ := r.Infer("acme/platform", iss)
	if got := Keys(sel); !reflect.DeepEqual(got, []string{"platform", "infra"}) {
		t.Fatalf("keys = %v", got)
	}
	for _, s := range sel {
		if s.Key == "platform" && !reflect.DeepEqual(s.Reasons, []string{"home"}) {
			t.Errorf("home repo picked up a self-xref: %v", s.Reasons)
		}
	}
}

// A body referencing an unknown repository must not invent a clone entry.
func TestUnknownRepoIsIgnored(t *testing.T) {
	r := load(t)
	iss := &ghx.Issue{Number: 1, Body: "blocked by torvalds/linux#42"}
	sel, _ := r.Infer("acme/platform", iss)
	if got := Keys(sel); !reflect.DeepEqual(got, []string{"platform"}) {
		t.Errorf("keys = %v; an unknown repo leaked in", got)
	}
}

func TestBlockedByVariants(t *testing.T) {
	r := load(t)
	for _, body := range []string{
		"Blocked by: acme/infra-core#41",
		"**Blocked by:** acme/infra-core#41",
		"- Blocked by acme/infra-core#41",
		"blocked-by: acme/infra-core#41",
		// The issue form renders its fields as h3 headings...
		"### Blocked by\n\nacme/infra-core#41\n",
		// ...but hand-written issues pick their own level. The real ones use h2.
		"## Blocked by\nacme/infra-core#41\n\n## Acceptance\nnothing\n",
		"# Blocked by\nacme/infra-core#41\n",
	} {
		sel, _ := r.Infer("acme/platform", &ghx.Issue{Body: body})
		var reasons []string
		for _, s := range sel {
			if s.Key == "infra" {
				reasons = s.Reasons
			}
		}
		if len(reasons) != 1 || reasons[0] != "blocked-by:acme/infra-core#41" {
			t.Errorf("body %q -> reasons %v; want blocked-by", body, reasons)
		}
	}
}

func TestFragments(t *testing.T) {
	r := load(t)
	got := r.Fragments([]string{"area/backups", "widget", "P0-critical"})
	want := []string{"area-backups", "area-identity", "area-monitoring", "area-widget"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Fragments = %v, want %v", got, want)
	}
}

// A routing file that names a repo it never defines is a configuration bug and
// must fail loudly at load, not silently drop the repo at spawn time.
func TestValidateCatchesDanglingKeys(t *testing.T) {
	cases := map[string]string{
		"ownerRepoToKey": `{"repos":{"a":{"dir":"a","url":"u"}},"ownerRepoToKey":{"o/r":"nope"}}`,
		"aliases":        `{"repos":{"a":{"dir":"a","url":"u"}},"aliases":{"x":"nope"}}`,
		"areaMap":        `{"repos":{"a":{"dir":"a","url":"u"}},"areaMap":{"area/x":["nope"]}}`,
		"pathHints":      `{"repos":{"a":{"dir":"a","url":"u"}},"pathHints":{"p/":"nope"}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			var r Routing
			if err := json.Unmarshal([]byte(body), &r); err != nil {
				t.Fatal(err)
			}
			if err := r.Validate(); err == nil {
				t.Errorf("Validate accepted a dangling %s key", name)
			}
		})
	}
}

// The real routing file, if present on this machine, must be valid. This is the
// test that catches a typo in the data before `facet spawn` does.
func TestRealRoutingFileIfPresent(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	p := filepath.Join(home, "Workspaces", ".tools", "routing.json")
	if _, err := os.Stat(p); err != nil {
		t.Skip("no local routing file")
	}
	if _, err := Load(p); err != nil {
		t.Errorf("local routing file is invalid: %v", err)
	}
}

// GitHub renders a `multiple: true` dropdown into the issue body as its selected
// values, and it is not contractual whether they arrive newline- or
// comma-separated. Both must parse, and so must the untouched-field marker.
func TestScopeFieldRenderings(t *testing.T) {
	r := load(t)
	base := "### Summary\n\nx\n\n### Repos in scope\n\n%s\n\n### Acceptance\n\ny\n"
	tests := map[string][]string{
		"newlines":        {"gateway\ninfra-core"},
		"commas":          {"gateway, infra-core"},
		"commas no space": {"gateway,infra-core"},
		"backticked":      {"`gateway`, `infra-core`"},
		"bulleted":        {"- gateway\n- infra-core"},
		"trailing blank":  {"gateway\ninfra-core\n"},
	}
	for name, bodies := range tests {
		t.Run(name, func(t *testing.T) {
			for _, sel := range bodies {
				got, _ := r.Infer("acme/platform", &ghx.Issue{Body: fmt.Sprintf(base, sel)})
				keys := Keys(got)
				if !reflect.DeepEqual(keys, []string{"platform", "gateway", "infra"}) {
					t.Errorf("%q -> %v", sel, keys)
				}
			}
		})
	}
}

// A `checkboxes` field renders EVERY option into the body, checked or not:
//
//	- [x] gateway
//	- [ ] infra-core
//
// Splitting that on whitespace leaves the bare token `infra-core`, which resolves --
// so a naive parser selects every option and `facet spawn` clones the world. The
// issue forms use `dropdown` for exactly this reason, but a body may still arrive
// with task-list syntax (a hand-written issue, or a form someone "improved"), so an
// unchecked box must never count as a selection.
func TestScopeFieldCheckboxes(t *testing.T) {
	r := load(t)
	base := "### Summary\n\nx\n\n### Repos in scope\n\n%s\n\n### Acceptance\n\ny\n"
	tests := map[string]struct {
		section string
		want    []string
	}{
		"one checked, rest unchecked": {
			"- [x] gateway\n- [ ] infra-core\n- [ ] widgetapi",
			[]string{"platform", "gateway"},
		},
		"two checked": {
			"- [x] gateway\n- [x] infra-core\n- [ ] widgetapi",
			[]string{"platform", "gateway", "infra"},
		},
		"uppercase X": {
			"- [X] gateway\n- [ ] infra-core",
			[]string{"platform", "gateway"},
		},
		"asterisk bullets": {
			"* [x] gateway\n* [ ] infra-core",
			[]string{"platform", "gateway"},
		},
		// Nothing ticked is the same as an unanswered field: fall back to the home repo.
		"none checked": {
			"- [ ] gateway\n- [ ] infra-core",
			[]string{"platform"},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, _ := r.Infer("acme/platform", &ghx.Issue{Body: fmt.Sprintf(base, tc.section)})
			if keys := Keys(got); !reflect.DeepEqual(keys, tc.want) {
				t.Errorf("%q\n got %v\nwant %v", tc.section, keys, tc.want)
			}
		})
	}
}

// An unfilled optional field renders as "_No response_". It must not be read as a
// repository, and it must not suppress the label fallback.
func TestScopeFieldNoResponse(t *testing.T) {
	r := load(t)
	body := "### Repos in scope\n\n_No response_\n\n### Blocked by\n\n_No response_\n"
	got, _ := r.Infer("acme/platform", &ghx.Issue{
		Body:   body,
		Labels: []ghx.Label{{Name: "area/terraform"}},
	})
	if keys := Keys(got); !reflect.DeepEqual(keys, []string{"platform", "infra"}) {
		t.Errorf("keys = %v; an empty scope field must fall back to labels", keys)
	}
}
