package routing

import (
	"regexp"
	"strconv"
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

// ParseBlockedBy extracts every issue reference from body's
// "Blocked by / waiting on" section -- the account-wide task form's field for
// declared dependencies. Plain-text waits ("account creation (operator)")
// have no `#`, so they never appear here; callers leave that text untouched.
//
// This is deliberately separate from blockedRefs/crossRef above, which serve
// `facet spawn`'s inference: those match the differently-named "Blocked by"
// heading and only owner/repo#n, never a bare #n.
func ParseBlockedBy(body string) []BlockedByRef {
	section := findSection(body, "Blocked by / waiting on")
	if section == "" {
		return nil
	}

	var out []BlockedByRef
	for _, m := range blockedByRefPattern.FindAllStringSubmatchIndex(section, -1) {
		ownerStart, ownerEnd := m[2], m[3]
		if ownerStart == -1 {
			// Bare `#n`: reject it if the preceding character could extend
			// into a token the owner/repo group simply didn't match here.
			if start := m[0]; start > 0 && refBoundaryChar(rune(section[start-1])) {
				continue
			}
		}
		n, err := strconv.Atoi(section[m[4]:m[5]])
		if err != nil {
			continue
		}
		ref := BlockedByRef{Number: n}
		if ownerStart != -1 {
			ref.OwnerRepo = section[ownerStart:ownerEnd]
		}
		out = append(out, ref)
	}
	return out
}
