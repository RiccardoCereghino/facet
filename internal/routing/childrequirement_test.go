package routing

import (
	"encoding/json"
	"strings"
	"testing"
)

// !! THE COMPATIBILITY CLAUSE. !! `requiresChildren: true` is what every
// routing file written before facet#146 says, and it must keep meaning exactly
// what it meant: report a CLOSED node holding nothing.
//
// If this decoded to "always" instead, every holder created moments before the
// run -- a record of work not yet wired under it -- would be reported, and the
// check would be turned off by whoever got tired of the noise first.
func TestRequiresChildrenDecodesTheOldBoolean(t *testing.T) {
	tests := []struct {
		json string
		want ChildRequirement
	}{
		{`true`, ChildrenRequiredWhenClosed},
		{`false`, ChildrenNotRequired},
		{`"closed"`, ChildrenRequiredWhenClosed},
		{`"always"`, ChildrenRequiredAlways},
		{`""`, ChildrenNotRequired},
	}
	for _, tt := range tests {
		var got ChildRequirement
		if err := json.Unmarshal([]byte(tt.json), &got); err != nil {
			t.Fatalf("Unmarshal(%s): %v", tt.json, err)
		}
		if got != tt.want {
			t.Errorf("Unmarshal(%s) = %q, want %q", tt.json, got, tt.want)
		}
	}
}

// An unrecognised value is REFUSED, not treated as "not required". A typo that
// silently disables a check is the exact failure this area keeps producing: the
// check goes quiet, and quiet reads as clean.
func TestRequiresChildrenRefusesAValueItDoesNotKnow(t *testing.T) {
	for _, bad := range []string{`"sometimes"`, `"Closed"`, `3`, `["always"]`} {
		var got ChildRequirement
		err := json.Unmarshal([]byte(bad), &got)
		if err == nil {
			t.Errorf("Unmarshal(%s) = %q with no error; a value nobody understands must not read as 'not required'", bad, got)
		}
	}
}

// The refusal names what is acceptable, so the fix does not need this source
// file.
func TestRequiresChildrenRefusalNamesTheValues(t *testing.T) {
	var got ChildRequirement
	err := json.Unmarshal([]byte(`"sometimes"`), &got)
	if err == nil {
		t.Fatal("a bogus value was accepted")
	}
	for _, want := range []string{"closed", "always"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
}

func TestChildRequirementDemands(t *testing.T) {
	tests := []struct {
		req            ChildRequirement
		closed, demand bool
	}{
		{ChildrenNotRequired, true, false},
		{ChildrenNotRequired, false, false},
		{ChildrenRequiredWhenClosed, true, true},
		// The whole point of keeping two values: an OPEN holder is not a defect.
		{ChildrenRequiredWhenClosed, false, false},
		{ChildrenRequiredAlways, true, true},
		// And the case facet#146 exists for: an OPEN block with nothing in it.
		{ChildrenRequiredAlways, false, true},
	}
	for _, tt := range tests {
		if got := tt.req.Demands(tt.closed); got != tt.demand {
			t.Errorf("%q.Demands(closed=%v) = %v, want %v", tt.req, tt.closed, got, tt.demand)
		}
	}
}

// It survives a whole level, which is how it is actually read.
func TestRequiresChildrenDecodesInsideALevel(t *testing.T) {
	var s Structure
	src := `{"levels":[
		{"name":"holder","requiresChildren":true},
		{"name":"block","optional":true,"requiresChildren":"always"},
		{"name":"issue"}
	]}`
	if err := json.Unmarshal([]byte(src), &s); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if s.Levels[0].RequiresChildren != ChildrenRequiredWhenClosed {
		t.Errorf("holder = %q, want the old boolean's meaning", s.Levels[0].RequiresChildren)
	}
	if s.Levels[1].RequiresChildren != ChildrenRequiredAlways {
		t.Errorf("block = %q, want always", s.Levels[1].RequiresChildren)
	}
	if s.Levels[2].RequiresChildren != ChildrenNotRequired {
		t.Errorf("issue = %q, want nothing required", s.Levels[2].RequiresChildren)
	}
	// AND IT IS MEANINGFUL ON AN OPTIONAL RUNG, which is the question facet#146
	// poses. Optional says the rung may be SKIPPED; this says a node that IS
	// there must hold something. They are orthogonal, and the skippability is
	// the REASON an empty node at that rung is a defect.
	if !s.Levels[1].Optional {
		t.Fatal("the block rung is not optional in this fixture")
	}
	if !s.Levels[1].RequiresChildren.Demands(false) {
		t.Error("an optional rung cannot ask to hold children, so the two are not orthogonal")
	}
}
