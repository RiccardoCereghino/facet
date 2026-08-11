package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/RiccardoCereghino/facet/internal/ghx"
	"github.com/RiccardoCereghino/facet/internal/routing"
	"github.com/RiccardoCereghino/facet/internal/tree"
)

// Confidence is the THIRD VALUE, and on this verb it is not decoration.
//
// A span computed from a partial read is WORSE THAN NO SPAN, because a MISSING
// REPO READS AS DISJOINT -- and disjoint is precisely the answer that
// authorises putting two groupings into one slot. The failure is silent at the
// point of reading and lands much later as a collision nobody can trace back.
type Confidence string

const (
	// SpanCurrent means every node below this one was read.
	SpanCurrent Confidence = "CURRENT"
	// SpanPartial means some were not. The span shown is a LOWER BOUND: repos
	// may be missing from it, so it may not be compared for disjointness.
	SpanPartial Confidence = "PARTIAL"
	// SpanBlind means nothing below this one could be read at all.
	SpanBlind Confidence = "COULD NOT LOOK"
)

// spanRow is one node and where its work lands.
type spanRow struct {
	Ref   string `json:"ref"`
	Title string `json:"title"`
	// Filed is the repository the node itself lives in, which is NOT the
	// question this verb answers and is reported so the two can be compared.
	Filed string `json:"filed"`
	// Span is where the work below it lands: every repository its descendants
	// are filed in, plus every repository they DECLARE. Sorted, deduplicated.
	Span []string `json:"span"`
	// Elsewhere is true when the node's own repository is not in its span --
	// the case that produced facet#149's two hand-derivation errors, and one
	// that is invisible in every other view.
	Elsewhere bool `json:"elsewhere"`
	// Confidence and Unread say whether the span may be trusted for a
	// disjointness comparison.
	Confidence Confidence `json:"confidence"`
	Unread     int        `json:"unread,omitempty"`
	// Descendants is the number of nodes the span was computed from. A span of
	// one repo over 40 descendants and over 1 are different facts.
	Descendants int `json:"descendants"`
}

type spanReport struct {
	Root string    `json:"root"`
	Rows []spanRow `json:"rows"`
}

// runTreeSpans reports where the work below a node lands.
func runTreeSpans(w io.Writer, gh treeGH, ref ghx.IssueRef, asJSON bool) error {
	root, route, err := walk(gh, ref, -1)
	if err != nil {
		// Nothing was read, so nothing is reported and nothing is ruled out.
		return withCode(exitCantLook, err)
	}

	report := spanReport{Root: ref.String(), Rows: []spanRow{}}
	// OVER A HOLDER, ONE ROW PER CHILD; over a single node, one row. A holder
	// is what a composer actually has in front of it -- the question is never
	// "what is this one node's span" but "which of these may share a slot".
	subjects := root.Children
	if len(subjects) == 0 {
		subjects = []*tree.Node{root}
	}

	for _, n := range subjects {
		report.Rows = append(report.Rows, spanOf(gh, route, n))
	}

	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return err
		}
	} else {
		writeSpanLines(w, report)
	}

	// EXIT 2 WHEN ANY ROW IS NOT CURRENT, alongside `tree doctor` and `tree
	// labels`. Finding a span is exit 0 -- a span is a report, not a verdict --
	// so the only failure this verb has is not having been able to look, and a
	// partial span is exactly that wearing a plausible answer.
	//
	// facet#149's body cites `tree orphans` as the pattern and quotes it as
	// "exit 1 for could-not-look". That was the defect facet#145 removes, and
	// copying it faithfully would have shipped it in a brand-new verb.
	var degraded []string
	for _, r := range report.Rows {
		if r.Confidence != SpanCurrent {
			degraded = append(degraded, r.Ref)
		}
	}
	if len(degraded) > 0 {
		return withCode(exitCantLook, fmt.Errorf(
			"%d of %d span(s) computed from a partial read: %s\n"+
				"  a MISSING repo reads as DISJOINT, and disjoint is the answer that authorises sharing a slot\n"+
				"fix: re-run when the read succeeds; do not compare these for disjointness",
			len(degraded), len(report.Rows), strings.Join(degraded, ", ")))
	}
	return nil
}

// spanOf computes one node's span from everything below it, the node included.
func spanOf(gh treeGH, route *routing.Routing, n *tree.Node) spanRow {
	row := spanRow{
		Ref:        n.Ref.String(),
		Title:      n.Title,
		Filed:      n.Ref.OwnerRepo(),
		Confidence: SpanCurrent,
	}
	if n.Err != nil {
		// The node itself did not read, so nothing below it was reached.
		row.Confidence = SpanBlind
		row.Unread = 1
		return row
	}

	repos := map[string]bool{}
	nodes := append([]*tree.Node{n}, n.Descendants()...)
	row.Descendants = len(nodes) - 1

	for _, d := range nodes {
		if d.Err != nil {
			row.Unread++
			continue
		}
		repos[d.Ref.OwnerRepo()] = true
		// AND WHAT THE ISSUE ITSELF DECLARES. This is the half that separates
		// "where the issues live" from "where the work lands", and it is read
		// through the SAME inference `facet spawn` uses to decide what to
		// clone -- so the answer is "the repositories a seat working this would
		// be given", rather than a second opinion invented here.
		for _, r := range declaredRepos(gh, route, d.Ref) {
			repos[r] = true
		}
	}
	if row.Unread > 0 {
		row.Confidence = SpanPartial
	}

	for r := range repos {
		row.Span = append(row.Span, r)
	}
	sort.Strings(row.Span)
	row.Elsewhere = !repos[n.Ref.OwnerRepo()]
	return row
}

// declaredRepos reads an issue's BODY and asks routing which repositories its
// work touches, as owner/name.
//
// THIS EXTRA READ IS THE WHOLE COST OF BEING RIGHT. `IssueChildren` returns
// title, state and labels but not the body, so a span built from the walk alone
// could only answer WHERE THE ISSUES LIVE -- the easy question, and the wrong
// one. Measured on this repository's own grouping: every open child is a facet
// issue because that is where the issues live, while the work lands in facet
// AND lab-workspaces. A verb answering the easy question would have reproduced
// exactly the table that was already derived wrongly by hand, confidently.
//
// The reads are conditional REST, so a repeat over an unchanged tree is 304s
// and costs nothing.
//
// A FAILED READ CONTRIBUTES NOTHING AND IS NOT AN ERROR HERE, because the
// node's own repository is already in the span from the tree: the span degrades
// to the easy answer for that node rather than vanishing. The row's Unread
// count is what says the span is a lower bound.
func declaredRepos(gh treeGH, route *routing.Routing, ref ghx.IssueRef) []string {
	if route == nil {
		return nil
	}
	iss, err := gh.ViewIssue(ref.OwnerRepo(), ref.Number)
	if err != nil || iss == nil {
		return nil
	}
	sel, _ := route.Infer(ref.OwnerRepo(), iss)
	var out []string
	for _, key := range routing.Keys(sel) {
		if full := ownerRepoForKey(route, key); full != "" {
			out = append(out, full)
		}
	}
	return out
}

// ownerRepoForKey turns a routing key back into the owner/name GitHub uses, so
// a span is one vocabulary rather than a mixture of keys and repositories.
func ownerRepoForKey(route *routing.Routing, key string) string {
	for ownerRepo, k := range route.OwnerRepoToKey {
		if k == key {
			return ownerRepo
		}
	}
	return ""
}

func writeSpanLines(w io.Writer, report spanReport) {
	for _, r := range report.Rows {
		span := strings.Join(r.Span, ", ")
		if span == "" {
			span = "-"
		}
		_, _ = fmt.Fprintf(w, "%-34s %s\n", r.Ref, span)
		if r.Confidence != SpanCurrent {
			// FIRST, and loudly. A degraded span that reads like a clean one is
			// the entire hazard: a missing repo reads as disjoint.
			_, _ = fmt.Fprintf(w, "%-34s   !! %s -- %d node(s) unread; this span is a LOWER BOUND and must not be compared for disjointness\n",
				"", r.Confidence, r.Unread)
		}
		if r.Elsewhere {
			// The difference facet#149's own comment asks for by name: it is
			// where the two hand-derivation errors came from, and it is
			// invisible in every current view.
			_, _ = fmt.Fprintf(w, "%-34s   (filed in %s, which is NOT in its span)\n", "", r.Filed)
		}
	}
	_, _ = fmt.Fprintln(w, "\n  a span is where the work below a node LANDS, not where its issues live")
	_, _ = fmt.Fprintln(w, "  it reports spans; whether two of them may share a slot is doctrine, and facet holds none")
}
