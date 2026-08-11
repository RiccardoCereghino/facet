package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/RiccardoCereghino/facet/internal/ghx"
)

// orphanGH is the one method this command needs. Declared separately from
// treeGH for the reason treeGH itself exists: a test should script what the
// command calls, not twenty methods it does not.
type orphanGH interface {
	OpenIssueParents(repo string) ([]ghx.Parentage, error)
}

// orphanReport is the --json shape.
//
// The per-repository counts are in it deliberately. "9 orphans" means something
// very different in a repository with 12 open issues than in one with 300, and
// a consumer that only ever sees the list cannot tell those apart -- nor tell a
// repository with no orphans from one that was never read.
type orphanReport struct {
	Repos   []orphanRepo  `json:"repos"`
	Orphans []orphanEntry `json:"orphans"`
}

type orphanRepo struct {
	Repo    string `json:"repo"`
	Open    int    `json:"open"`
	Orphans int    `json:"orphans"`
	// Error is the read that failed, if it did. Present rather than fatal: one
	// unreadable repository must not blank out the answer for the others, and
	// a report that silently omitted it would be read as "nothing there".
	Error string `json:"error,omitempty"`
}

type orphanEntry struct {
	Ref    string `json:"ref"`
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Title  string `json:"title"`
}

// runTreeOrphans reports the open issues in each repo that have no parent.
//
// FINDING SOME IS NOT A FAILURE. Only an unreadable repository is, and it is
// named rather than merely counted -- the whole value of this command is
// saying what is not in the tree, so a repository it could not list is exactly
// the answer a caller must not be allowed to miss.
//
// AN UNREADABLE REPOSITORY IS exitCantLook, NOT exitLooked. facet#138 gave
// `tree doctor` the distinction that 1 means "I looked, and here is what is
// wrong" and 2 means "I could not look"; `tree labels` took it too. This verb
// landed in the same block deliberately without it, because the typed codes
// then lived only in #138's unmerged branch and adopting them would have made
// an independent block into a chain. That branch merged, so the reason is gone
// and the code was not (facet#145).
//
// FOR THIS VERB THE SET IS 0 AND 2, WITH NO 1 AT ALL, and that is the whole
// shape rather than an omission: finding orphans is exit 0 (facet#116 -- an
// unparented issue is a valid issue and this is a question, not a verdict), so
// the only failure it has left is not having been able to ask. `tree labels`
// answers 1 for a finding-plus-unreadable because it HAS a finding code; this
// one does not, so the both-case cannot arise.
func runTreeOrphans(w io.Writer, gh orphanGH, repos []string, asJSON bool) error {
	var report orphanReport
	report.Orphans = []orphanEntry{}
	var failed []string

	for _, repo := range repos {
		row := orphanRepo{Repo: repo}
		issues, err := gh.OpenIssueParents(repo)
		if err != nil {
			row.Error = err.Error()
			failed = append(failed, repo)
			report.Repos = append(report.Repos, row)
			continue
		}
		row.Open = len(issues)
		for _, iss := range issues {
			if iss.HasParent {
				continue
			}
			row.Orphans++
			report.Orphans = append(report.Orphans, orphanEntry{
				Ref:    iss.Ref.String(),
				Repo:   iss.Ref.OwnerRepo(),
				Number: iss.Ref.Number,
				Title:  iss.Title,
			})
		}
		report.Repos = append(report.Repos, row)
	}

	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return err
		}
	} else {
		writeOrphanLines(w, report)
	}

	if len(failed) > 0 {
		return withCode(exitCantLook, fmt.Errorf("could not read %s: %s\n"+
			"  the issues there are neither reported nor ruled out\n"+
			"fix: check the repository exists and this credential can see its issues",
			plural(len(failed), "repository", "repositories"), strings.Join(failed, ", ")))
	}
	return nil
}

// writeOrphanLines prints the refs first and the arithmetic last.
//
// The ref/title lines are tab-separated and carry nothing else, matching `tree
// list` so the same pipelines work. The summary follows rather than leads,
// because the caveat it carries is the one a reader must not skip: a
// parentless issue is a question.
func writeOrphanLines(w io.Writer, report orphanReport) {
	for _, o := range report.Orphans {
		_, _ = fmt.Fprintf(w, "%s\t%s\n", o.Ref, truncate(o.Title, 70))
	}
	_, _ = fmt.Fprintln(w)
	for _, r := range report.Repos {
		if r.Error != "" {
			// FIRST LINE ONLY, and the whole thing is still in --json. A gh
			// failure carries the entire request back in its message -- for a
			// GraphQL read that is the whole multi-line query, which buries
			// the summary it is printed beside. The reason for the failure is
			// on the last line of that message, not the first, so the last is
			// what is shown.
			_, _ = fmt.Fprintf(w, "  %-40s COULD NOT READ: %s\n", r.Repo, lastLine(r.Error))
			continue
		}
		_, _ = fmt.Fprintf(w, "  %-40s %d of %d open %s unparented\n",
			r.Repo, r.Orphans, r.Open, plural(r.Open, "issue", "issues"))
	}
	_, _ = fmt.Fprintln(w, "\n  not every unparented issue is a defect -- this is the set, not a verdict")
}

// lastLine is the readable end of a multi-line error. Nothing is discarded:
// the full text is what --json carries, and a caller that wants it has it.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
