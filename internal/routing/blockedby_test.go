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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseBlockedBy(tt.body)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseBlockedBy(%q) = %#v, want %#v", tt.body, got, tt.want)
			}
		})
	}
}
