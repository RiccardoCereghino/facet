package routing

import (
	"regexp"
	"strconv"
	"strings"
)

// BlockedByRef is one issue reference found in a "Blocked by / waiting on"
// section: a same-repo `#n`, or a cross-repo `owner/repo#n`.
type BlockedByRef struct {
	OwnerRepo string // "" for a same-repo `#n`; "owner/name" otherwise
	Number    int
}

// blockedByRefPattern matches `owner/repo#n` (group 1 set) or a bare `#n`
// (group 1 empty). The owner/repo half, when present, sits directly against
// the `#` -- that is what stops "acme/gateway#5" from also being read as the
// bare ref "#5".
var blockedByRefPattern = regexp.MustCompile(`([A-Za-z0-9._-]+/[A-Za-z0-9._-]+)?#(\d+)`)

// refBoundaryChar reports whether r could be part of a repo-path token
// ("owner/name" or a plain word). A bare `#n` immediately preceded by one of
// these is not a standalone reference -- it is the tail of a token this
// pattern's optional group failed to capture (e.g. the "3" in "PR#3"), not a
// dependency.
func refBoundaryChar(r rune) bool {
	return r == '/' || r == '.' || r == '-' || r == '_' ||
		(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// tokenBefore returns the run of repo-path characters immediately before i --
// the prefix a bare `#n` is stuck to. "PR" in "PR#3", "harness" in
// "harness#121", "" when the reference stands on its own.
func tokenBefore(s string, i int) string {
	j := i
	for j > 0 && refBoundaryChar(rune(s[j-1])) {
		j--
	}
	return s[j:i]
}

// repoForShorthand resolves a bare prefix to the repository it names, if it
// names one at all.
//
// THE ROUTING TABLE ALREADY HOLDS THE ANSWER, which is the whole reason this is
// fixable without new configuration and without guessing: `repos` says which
// keys exist, and `ownerRepoToKey` says how GitHub spells each one. A prefix
// matching a key is a reference; one that does not is a word.
//
// IT IS `repos`, NOT `aliases`, AND THAT IS DELIBERATE. Aliases are loose
// spellings by design -- "site", "dns", "cv" -- so admitting them would read
// "site#3" in prose as a dependency. The narrow set is the one whose members
// are unambiguously repository names.
//
// An ambiguous key -- two owner/name spellings mapping to it -- resolves to
// nothing rather than to whichever the map iteration reached first. A
// dependency edge wired to the wrong repository is worse than one not wired at
// all, because only the second is visible as missing.
func (r *Routing) repoForShorthand(prefix string) (string, bool) {
	if r == nil || prefix == "" || strings.Contains(prefix, "/") {
		return "", false
	}
	key := ""
	for k := range r.Repos {
		if strings.EqualFold(k, prefix) {
			key = k
			break
		}
	}
	if key == "" {
		return "", false
	}
	found := ""
	for ownerRepo, k := range r.OwnerRepoToKey {
		if k != key {
			continue
		}
		if found != "" && found != ownerRepo {
			return "", false // ambiguous: do not pick one
		}
		found = ownerRepo
	}
	return found, found != ""
}

// ParseBlockedBy extracts every issue reference from body's
// "Blocked by / waiting on" section -- the account-wide task form's field for
// declared dependencies. Plain-text waits ("account creation (operator)")
// have no `#`, so they never appear here; callers leave that text untouched.
//
// This is deliberately separate from blockedRefs/crossRef above, which serve
// `facet spawn`'s inference: those match the differently-named "Blocked by"
// heading and only owner/repo#n, never a bare #n.
//
// IT IS A METHOD BECAUSE A REPO SHORTHAND CANNOT BE RESOLVED WITHOUT THE
// ROUTING TABLE (facet#104). `harness#121` is neither `owner/repo#n` nor a
// standalone `#n`: refBoundaryChar correctly refuses to read the `121` in
// `PR#3` as a reference, and correctly-by-the-same-rule refused this one, where
// the prefix genuinely names a repository. The rejection was SILENT, so every
// dependency written that way -- and shorthand is the dominant form, because it
// is how people write when the repo is obvious from context -- has been dropped
// since the feature shipped, existing only as prose.
//
// A NIL RECEIVER PARSES EXACTLY AS THIS DID BEFORE: owner/repo#n and a
// standalone #n, with shorthand unresolved. A caller with no routing file loses
// nothing it had, and gains nothing it cannot justify.
func (r *Routing) ParseBlockedBy(body string) []BlockedByRef {
	section := findSection(body, "Blocked by / waiting on")
	if section == "" {
		return nil
	}

	var out []BlockedByRef
	seen := map[BlockedByRef]bool{}
	for _, m := range blockedByRefPattern.FindAllStringSubmatchIndex(section, -1) {
		ownerStart, ownerEnd := m[2], m[3]
		shorthand := ""
		if ownerStart == -1 {
			// Bare `#n`: reject it if the preceding character could extend
			// into a token the owner/repo group simply didn't match here --
			// UNLESS that token names a repository, which is exactly the
			// information refBoundaryChar lacks and routing has.
			if start := m[0]; start > 0 && refBoundaryChar(rune(section[start-1])) {
				repo, ok := r.repoForShorthand(tokenBefore(section, start))
				if !ok {
					continue
				}
				shorthand = repo
			}
		}
		n, err := strconv.Atoi(section[m[4]:m[5]])
		if err != nil {
			continue
		}
		ref := BlockedByRef{Number: n, OwnerRepo: shorthand}
		if ownerStart != -1 {
			ref.OwnerRepo = section[ownerStart:ownerEnd]
		}
		// The same dependency named twice in one section (a bullet list that
		// also mentions it in prose, say) must produce exactly one edge --
		// not one POST per mention.
		if seen[ref] {
			continue
		}
		seen[ref] = true
		out = append(out, ref)
	}
	return out
}
