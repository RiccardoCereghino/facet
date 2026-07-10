// Package routing decides which repositories a GitHub issue needs.
//
// Labels alone cannot answer that. They describe a topic, not a repository, and
// the same topic label is used in several repos. The decisive evidence lives in
// the issue body: cross-references like `owner/repo#41`, "Blocked by" lines, and
// -- for issues filed through the form -- an explicit "Repos in scope" field.
//
// So Infer combines three sources, records *why* each repository was chosen, and
// leaves the final say to a human: `facet spawn` prints the derivation and waits.
package routing

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/RiccardoCereghino/facet/internal/ghx"
)

// Repo describes one repository facet can put in a workspace.
type Repo struct {
	Dir     string            `json:"dir"`
	URL     string            `json:"url"`
	Remotes map[string]string `json:"remotes,omitempty"`
	// LFS false clones Git-LFS pointers rather than blobs. Absent means blobs.
	LFS *bool `json:"lfs,omitempty"`
}

// Routing is the project-specific data facet reads. It lives outside the binary
// on purpose: facet itself knows nothing about any particular organisation.
type Routing struct {
	Version int             `json:"version"`
	Repos   map[string]Repo `json:"repos"`
	// OwnerRepoToKey maps "owner/name" as GitHub spells it to a repo key.
	OwnerRepoToKey map[string]string `json:"ownerRepoToKey"`
	// Aliases maps loose spellings in an issue body to a repo key.
	Aliases map[string]string `json:"aliases"`
	// AreaMap maps a label to the repos that label usually implies.
	AreaMap map[string][]string `json:"areaMap"`
	// KnowledgeByArea maps a label to the knowledge fragments worth inlining.
	KnowledgeByArea map[string][]string `json:"knowledgeByArea"`
	// PathHints maps a path prefix seen in a body to a repo key. Suggestions only.
	PathHints map[string]string `json:"pathHints"`
}

// Load reads a routing file.
func Load(path string) (*Routing, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read routing file: %w", err)
	}
	var r Routing
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := r.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &r, nil
}

// Validate catches a routing file that names repos it never defines.
func (r *Routing) Validate() error {
	known := func(key string) bool { _, ok := r.Repos[key]; return ok }
	for owner, key := range r.OwnerRepoToKey {
		if !known(key) {
			return fmt.Errorf("ownerRepoToKey[%q] = %q, which is not in repos", owner, key)
		}
	}
	for alias, key := range r.Aliases {
		if !known(key) {
			return fmt.Errorf("aliases[%q] = %q, which is not in repos", alias, key)
		}
	}
	for label, keys := range r.AreaMap {
		for _, k := range keys {
			if !known(k) {
				return fmt.Errorf("areaMap[%q] names %q, which is not in repos", label, k)
			}
		}
	}
	for _, key := range r.PathHints {
		if !known(key) {
			return fmt.Errorf("pathHints names %q, which is not in repos", key)
		}
	}
	return nil
}

// Selection is one repository, and the evidence that put it in the workspace.
type Selection struct {
	Key     string
	Reasons []string
	// Home marks the repository the issue was filed in. Only it gets the branch.
	Home bool
}

// Hint is a low-confidence suggestion: something in the body smells like a
// repository, but not strongly enough to add it unasked.
type Hint struct {
	Key    string
	Reason string
}

// crossRef matches `owner/repo#123` anywhere in a body.
var crossRef = regexp.MustCompile(`\b([A-Za-z0-9._-]+/[A-Za-z0-9._-]+)#(\d+)\b`)

// blockedByLine matches a line that declares a dependency inline, e.g.
// "Blocked by: o/r#1", "- **Blocked by** o/r#1", "## Blocked by o/r#1".
var blockedByLine = regexp.MustCompile(`(?im)^\s*(?:#{1,6}\s*)?(?:[-*]\s*)?(?:\*\*)?blocked[ -]by(?:\*\*)?\s*:?(.*)$`)

// Infer chooses the repositories for an issue filed in homeRepo ("owner/name").
//
// The home repository is always included. Beyond that: an explicit "Repos in
// scope" field, when present, is authoritative and replaces the label guess --
// the author said what they meant. Otherwise the area map supplies a prior.
// Cross-references and "Blocked by" lines are always added, because a dependency
// in another repo is evidence regardless of how the issue was labelled.
func (r *Routing) Infer(homeRepo string, iss *ghx.Issue) ([]Selection, []Hint) {
	reasons := map[string][]string{}
	add := func(key, reason string) {
		if _, ok := r.Repos[key]; !ok {
			return
		}
		for _, existing := range reasons[key] {
			if existing == reason {
				return
			}
		}
		reasons[key] = append(reasons[key], reason)
	}

	homeKey := r.OwnerRepoToKey[homeRepo]
	if homeKey != "" {
		add(homeKey, "home")
	}

	// The form field wins over labels, when the author filled it in.
	scoped := r.scopeField(iss.Body)
	if len(scoped) > 0 {
		for _, k := range scoped {
			add(k, "scope-field")
		}
	} else {
		for _, label := range iss.LabelNames() {
			for _, k := range r.AreaMap[label] {
				add(k, "label:"+label)
			}
		}
	}

	// Cross-repo evidence applies either way.
	blocked := blockedRefs(iss.Body)
	for _, m := range crossRef.FindAllStringSubmatch(iss.Body, -1) {
		ownerRepo, num := m[1], m[2]
		key := r.OwnerRepoToKey[ownerRepo]
		if key == "" || key == homeKey {
			continue
		}
		ref := ownerRepo + "#" + num
		if blocked[ref] {
			add(key, "blocked-by:"+ref)
		} else {
			add(key, "xref:"+ref)
		}
	}

	out := make([]Selection, 0, len(reasons))
	for key, rs := range reasons {
		sort.Strings(rs)
		out = append(out, Selection{Key: key, Reasons: rs, Home: key == homeKey})
	}
	// Home first, then alphabetical, so the output is stable.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Home != out[j].Home {
			return out[i].Home
		}
		return out[i].Key < out[j].Key
	})
	return out, r.hints(iss.Body, reasons)
}

// blockedRefs returns the `owner/repo#n` an issue declares as dependencies,
// whether written inline ("Blocked by: o/r#1") or under the issue form's own
// `### Blocked by` heading.
func blockedRefs(body string) map[string]bool {
	out := map[string]bool{}
	collect := func(s string) {
		for _, ref := range crossRef.FindAllString(s, -1) {
			out[ref] = true
		}
	}
	for _, m := range blockedByLine.FindAllStringSubmatch(body, -1) {
		collect(m[1])
	}
	collect(findSection(body, "Blocked by"))
	return out
}

// sectionHeading splits a body into markdown sections. An issue form renders
// each field under `### Label`, but hand-written issues use whatever level they
// please -- the real ones use `## Blocked by`.
var sectionHeading = regexp.MustCompile(`(?m)^#{2,6}\s+(.*?)\s*$`)

// scopeField reads the "Repos in scope" field an issue form produces. It returns
// nil when the field is absent, empty, or answered "unsure".
func (r *Routing) scopeField(body string) []string {
	section := findSection(body, "Repos in scope")
	if section == "" {
		return nil
	}
	var keys []string
	seen := map[string]bool{}
	for _, tok := range strings.FieldsFunc(section, func(c rune) bool {
		return c == ',' || c == '\n' || c == ' ' || c == '\t' || c == '\r'
	}) {
		tok = strings.ToLower(strings.Trim(tok, "`*_-"))
		switch tok {
		case "", "unsure", "_no", "response_", "_no_response_", "none":
			continue
		}
		key, ok := r.Repos[tok]
		_ = key
		resolved := tok
		if !ok {
			if alias, hit := r.Aliases[tok]; hit {
				resolved = alias
			} else {
				continue
			}
		}
		if !seen[resolved] {
			seen[resolved] = true
			keys = append(keys, resolved)
		}
	}
	return keys
}

// findSection returns the body text under a `### <name>` heading.
func findSection(body, name string) string {
	locs := sectionHeading.FindAllStringSubmatchIndex(body, -1)
	for i, loc := range locs {
		heading := strings.TrimSpace(body[loc[2]:loc[3]])
		if !strings.EqualFold(heading, name) {
			continue
		}
		start := loc[1]
		end := len(body)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		return strings.TrimSpace(body[start:end])
	}
	return ""
}

// hints reports repositories the body gestures at but that nothing selected.
// They are printed, never added: a path fragment is a weak signal.
func (r *Routing) hints(body string, chosen map[string][]string) []Hint {
	var out []Hint
	seen := map[string]bool{}
	for prefix, key := range r.PathHints {
		if _, already := chosen[key]; already || seen[key] {
			continue
		}
		if strings.Contains(body, prefix) {
			seen[key] = true
			out = append(out, Hint{Key: key, Reason: "body mentions " + prefix})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Keys returns just the repo keys of a selection, in order.
func Keys(sel []Selection) []string {
	out := make([]string, len(sel))
	for i, s := range sel {
		out[i] = s.Key
	}
	return out
}

// Fragments returns the knowledge fragment names matching an issue's labels.
func (r *Routing) Fragments(labels []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, l := range labels {
		for _, f := range r.KnowledgeByArea[l] {
			if !seen[f] {
				seen[f] = true
				out = append(out, f)
			}
		}
	}
	sort.Strings(out)
	return out
}
