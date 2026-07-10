package routing

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Conventions are the rules an issue must satisfy to be filed. Like everything
// else in the routing file they are data: facet knows that *some* labels are
// required, never which ones. A routing file without a conventions block
// enforces nothing.
type Conventions struct {
	// TitlePattern is a Go regexp the title must match. Empty means any title.
	TitlePattern string `json:"titlePattern,omitempty"`
	// RequireOneOf maps a human name for a group ("priority") to the labels in
	// it. Exactly one must be present.
	RequireOneOf map[string][]string `json:"requireOneOf,omitempty"`
	// RequirePrefix maps a human name ("area") to a label prefix ("area/"). At
	// least one label must carry it; several are fine.
	RequirePrefix map[string]string `json:"requirePrefix,omitempty"`
}

// Check reports every way title and labels violate c, not just the first. An
// agent that has to rediscover one rule per attempt will simply stop trying.
func (c *Conventions) Check(title string, labels []string) error {
	if c == nil {
		return nil
	}
	has := func(name string) bool {
		for _, l := range labels {
			if l == name {
				return true
			}
		}
		return false
	}

	var errs []error
	if c.TitlePattern != "" {
		re, err := regexp.Compile(c.TitlePattern)
		if err != nil {
			return fmt.Errorf("titlePattern %q: %w", c.TitlePattern, err)
		}
		if !re.MatchString(title) {
			errs = append(errs, fmt.Errorf(
				"title %q does not match %s -- use `component: imperative statement`",
				title, c.TitlePattern))
		}
	}

	// Map order is randomised, and an error message that changes between runs is
	// not one anybody can test or diff.
	for _, group := range sortedKeys(c.RequireOneOf) {
		want := c.RequireOneOf[group]
		var got []string
		for _, l := range want {
			if has(l) {
				got = append(got, l)
			}
		}
		switch len(got) {
		case 1:
		case 0:
			errs = append(errs, fmt.Errorf("%s: expected one of %s", group, strings.Join(want, ", ")))
		default:
			errs = append(errs, fmt.Errorf("%s: expected exactly one of %s, got %s",
				group, strings.Join(want, ", "), strings.Join(got, ", ")))
		}
	}

	for _, group := range sortedKeys(c.RequirePrefix) {
		prefix := c.RequirePrefix[group]
		found := false
		for _, l := range labels {
			if strings.HasPrefix(l, prefix) {
				found = true
				break
			}
		}
		if !found {
			errs = append(errs, fmt.Errorf("%s: expected at least one label starting with %q", group, prefix))
		}
	}
	return errors.Join(errs...)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// validate reports a conventions block that could never be satisfied. A bad
// regexp must fail when the routing file loads, not when someone files an issue.
func (c *Conventions) validate() error {
	if c == nil {
		return nil
	}
	if c.TitlePattern != "" {
		if _, err := regexp.Compile(c.TitlePattern); err != nil {
			return fmt.Errorf("conventions.titlePattern: %w", err)
		}
	}
	for _, group := range sortedKeys(c.RequireOneOf) {
		if len(c.RequireOneOf[group]) == 0 {
			return fmt.Errorf("conventions.requireOneOf[%q] is empty: no label could satisfy it", group)
		}
	}
	for _, group := range sortedKeys(c.RequirePrefix) {
		if c.RequirePrefix[group] == "" {
			return fmt.Errorf("conventions.requirePrefix[%q] is empty: every label would satisfy it", group)
		}
	}
	return nil
}

// SearchTerms reduces a title to the words worth searching for, so `facet file`
// can look for a duplicate before it creates one.
//
// The component prefix is dropped: "gateway-admin:" is shared by a dozen issues
// and would match all of them. Short words are dropped because GitHub's search
// ignores them anyway, and a colon left in the query would be read as a
// qualifier rather than as text.
func SearchTerms(title string) string {
	s := title
	if i := strings.Index(s, ":"); i >= 0 {
		s = s[i+1:]
	}
	var out []string
	for _, w := range strings.Fields(s) {
		w = strings.Trim(w, "`*_,.()[]\"'")
		if len(w) <= 3 {
			continue
		}
		out = append(out, w)
		if len(out) == 8 {
			break
		}
	}
	if len(out) == 0 {
		return strings.TrimSpace(s)
	}
	return strings.Join(out, " ")
}
