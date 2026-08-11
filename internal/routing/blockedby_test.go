package routing

import (
	"reflect"
	"testing"
)

// ParseBlockedBy reads the account-wide task form's real field name, which is
// "Blocked by / waiting on" -- not "Blocked by", the heading blockedRefs above
// looks for. These fixtures mirror how the form actually renders the field.
func TestParseBlockedBy(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []BlockedByRef
	}{
		{
			name: "bare same-repo ref",
			body: "### Blocked by / waiting on\n\n#41\n",
			want: []BlockedByRef{{Number: 41}},
		},
		{
			name: "cross-repo ref",
			body: "### Blocked by / waiting on\n\nacme/infra-core#41\n",
			want: []BlockedByRef{{OwnerRepo: "acme/infra-core", Number: 41}},
		},
		{
			name: "mixed refs and plain-text waits in the same section",
			body: "### Blocked by / waiting on\n\n#5, acme/infra-core#41, account creation (operator)\n",
			want: []BlockedByRef{{Number: 5}, {OwnerRepo: "acme/infra-core", Number: 41}},
		},
		{
			name: "plain text only, no refs",
			body: "### Blocked by / waiting on\n\naccount creation (operator)\n",
			want: nil,
		},
		{
			name: "form placeholder for an unanswered field",
			body: "### Blocked by / waiting on\n\n_No response_\n",
			want: nil,
		},
		{
			name: "section absent entirely",
			body: "### Summary\n\nnothing blocks this\n",
			want: nil,
		},
		{
			name: "the old heading spawn's inference uses does not match",
			body: "### Blocked by\n\n#41\n",
			want: nil,
		},
		{
			name: "a ref-like tail on a larger token is not a bare ref",
			body: "### Blocked by / waiting on\n\nsee PR#3 for context\n",
			want: nil,
		},
		{
			name: "stops at the next heading",
			body: "### Blocked by / waiting on\n\n#5\n\n### Blocking\n\n#9\n",
			want: []BlockedByRef{{Number: 5}},
		},
		{
			name: "the same ref named twice dedupes to one",
			body: "### Blocked by / waiting on\n\n- #5\n\nAlso see #5 and acme/infra-core#41, acme/infra-core#41 again.\n",
			want: []BlockedByRef{{Number: 5}, {OwnerRepo: "acme/infra-core", Number: 41}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A NIL RECEIVER MUST PARSE EXACTLY AS THE PACKAGE FUNCTION DID.
			// facet#104 adds an answer for one input that was silently
			// dropped; it must add nothing else, and a caller with no routing
			// file must lose nothing it had.
			got := (*Routing)(nil).ParseBlockedBy(tt.body)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("(nil).ParseBlockedBy(%q) = %#v, want %#v", tt.body, got, tt.want)
			}
			// And with a routing table that does NOT name any of these
			// prefixes, the answer is the same one: resolution is the only
			// thing routing adds.
			if got := shorthandRouting().ParseBlockedBy(tt.body); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("routed ParseBlockedBy(%q) = %#v, want %#v", tt.body, got, tt.want)
			}
		})
	}
}

// shorthandRouting is a table whose keys are what the shorthand cases below
// name. "harness" is a key spelled differently from its repository, which is
// the whole point: the shorthand is what a person writes, not what GitHub does.
func shorthandRouting() *Routing {
	return &Routing{
		Repos: map[string]Repo{
			"harness": {}, "infra-core": {}, "lab": {},
		},
		OwnerRepoToKey: map[string]string{
			"acme/stele-home": "harness",
			"acme/infra-core": "infra-core",
			"acme/lab":        "lab",
		},
		// Aliases are deliberately NOT consulted -- see repoForShorthand.
		Aliases: map[string]string{"site": "lab", "pr": "lab"},
	}
}

// !! facet#104. !! A prefix naming a repo in `repos` IS a reference. It was
// rejected silently, so the dependency existed only as prose and no edge was
// ever wired -- and shorthand is the dominant form in these bodies, because it
// is how people write when the repo is obvious from context.
func TestParseBlockedByResolvesRepoShorthand(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []BlockedByRef
	}{
		{
			name: "a prefix naming a repo key resolves to that repo",
			body: "### Blocked by / waiting on\n\nharness#121\n",
			want: []BlockedByRef{{OwnerRepo: "acme/stele-home", Number: 121}},
		},
		{
			name: "matched case-insensitively, as KeyForRepo already does",
			body: "### Blocked by / waiting on\n\nHarness#121\n",
			want: []BlockedByRef{{OwnerRepo: "acme/stele-home", Number: 121}},
		},
		{
			name: "a key whose spelling matches its repo name",
			body: "### Blocked by / waiting on\n\ninfra-core#41\n",
			want: []BlockedByRef{{OwnerRepo: "acme/infra-core", Number: 41}},
		},
		{
			name: "shorthand and full form for one repo dedupe to one edge",
			body: "### Blocked by / waiting on\n\nharness#121 and acme/stele-home#121\n",
			want: []BlockedByRef{{OwnerRepo: "acme/stele-home", Number: 121}},
		},
		{
			name: "shorthand beside a bare ref and a full ref",
			body: "### Blocked by / waiting on\n\n#5, harness#121, acme/infra-core#41\n",
			want: []BlockedByRef{
				{Number: 5},
				{OwnerRepo: "acme/stele-home", Number: 121},
				{OwnerRepo: "acme/infra-core", Number: 41},
			},
		},
		{
			// THE REGRESSION CASE, NAMED IN THE ISSUE AND NOT OPTIONAL.
			name: "PR#3 is still a word, because PR names no repo",
			body: "### Blocked by / waiting on\n\nsee PR#3 for context\n",
			want: nil,
		},
		{
			// The guard refBoundaryChar exists for, one word along.
			name: "an ordinary word with a number is still not a reference",
			body: "### Blocked by / waiting on\n\nwaiting on ticket#9 from the vendor\n",
			want: nil,
		},
		{
			// An ALIAS is not a repo key. Aliases are loose spellings by
			// design, so admitting them would read prose as a dependency.
			name: "an alias does not resolve",
			body: "### Blocked by / waiting on\n\nsite#3\n",
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shorthandRouting().ParseBlockedBy(tt.body)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseBlockedBy(%q) = %#v, want %#v", tt.body, got, tt.want)
			}
		})
	}
}

// An ambiguous key resolves to NOTHING rather than to whichever spelling map
// iteration reached first -- which would make the answer differ between runs.
// An edge wired to the wrong repository is worse than one not wired at all,
// because only the second is visible as missing.
func TestParseBlockedByRefusesAnAmbiguousKey(t *testing.T) {
	r := &Routing{
		Repos: map[string]Repo{"harness": {}},
		OwnerRepoToKey: map[string]string{
			"acme/stele-home": "harness",
			"other/stele":     "harness",
		},
	}
	if got := r.ParseBlockedBy("### Blocked by / waiting on\n\nharness#121\n"); got != nil {
		t.Errorf("an ambiguous key resolved to %#v; want nothing", got)
	}
}

// A key present in `repos` but absent from `ownerRepoToKey` has no GitHub
// spelling to resolve to. Validate() permits that -- it only checks the
// reverse -- so this is reachable from a real routing file.
func TestParseBlockedByNeedsAGitHubSpelling(t *testing.T) {
	r := &Routing{Repos: map[string]Repo{"harness": {}}}
	if got := r.ParseBlockedBy("### Blocked by / waiting on\n\nharness#121\n"); got != nil {
		t.Errorf("a key with no owner/name resolved to %#v; want nothing", got)
	}
}
