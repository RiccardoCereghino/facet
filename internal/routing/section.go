package routing

import "strings"

// scopeHeading is the section `facet spawn` writes the confirmed repo set into.
const scopeHeading = "Repos in scope"

// UpsertScope records repos in the issue body's "Repos in scope" section and
// reports whether the body changed.
//
// `facet spawn` prints its inference and waits for a human. That decision is
// worth keeping: written back, the next spawn reads it as a scope-field rather
// than guessing from labels again, and the board's "Repos involved" fills in for
// issues that were never filed through the form.
//
// It rewrites part of someone's issue body, so it is deliberately timid. The
// neighbouring sections come back byte for byte, an existing heading keeps
// whatever level the author used, an empty repo set is never written, and a body
// that already says the right thing is left alone -- so spawning twice does not
// churn the issue's edit history.
func UpsertScope(body string, repos []string) (out string, changed bool) {
	if len(repos) == 0 {
		return body, false
	}
	want := strings.Join(repos, ", ")

	s, ok := findScopeSection(body)
	if !ok {
		// No such section: append one. `##` matches the hand-written issues; the
		// parser accepts any level from `##` to `######`.
		sep := "\n\n"
		switch {
		case strings.TrimSpace(body) == "":
			body, sep = "", ""
		case strings.HasSuffix(body, "\n"):
			sep = "\n"
		}
		return body + sep + "## " + scopeHeading + "\n\n" + want + "\n", true
	}

	if strings.TrimSpace(body[s.contentStart:s.end]) == want {
		return body, false
	}

	head := strings.Repeat("#", s.level) + " " + scopeHeading
	rest := body[s.end:] // begins at the next heading, or is empty
	if rest == "" {
		return body[:s.headStart] + head + "\n\n" + want + "\n", true
	}
	return body[:s.headStart] + head + "\n\n" + want + "\n\n" + rest, true
}

// section locates one markdown section of a body.
type section struct {
	headStart    int // offset of the '#' that opens the heading
	contentStart int // offset just past the heading text
	end          int // offset of the next heading, or len(body)
	level        int // how many '#' the author used
}

// findScopeSection locates the "Repos in scope" section, at whatever heading
// level. Content bounds are kept apart from the heading: comparing the heading
// itself against the desired content is how an "idempotent" rewrite ends up
// rewriting on every run.
func findScopeSection(body string) (section, bool) {
	locs := sectionHeading.FindAllStringSubmatchIndex(body, -1)
	for i, loc := range locs {
		if !strings.EqualFold(strings.TrimSpace(body[loc[2]:loc[3]]), scopeHeading) {
			continue
		}
		s := section{headStart: loc[0], contentStart: loc[1], end: len(body)}
		if i+1 < len(locs) {
			s.end = locs[i+1][0]
		}
		for _, c := range body[s.headStart:] {
			if c != '#' {
				break
			}
			s.level++
		}
		return s, true
	}
	return section{}, false
}
