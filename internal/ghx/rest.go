package ghx

import (
	"encoding/json"
	"fmt"
	"strings"
)

// SubIssue is one child of an issue, as the children read returns it.
type SubIssue struct {
	Ref   IssueRef
	Title string
	// State is OPEN or CLOSED on a real issue. Empty here means the node's
	// fields could not be resolved, and is read as "unreadable" -- the same
	// fact a failed per-node read used to carry.
	State  string
	Labels []string
}

// restPerPage asks for a whole child list in one response where possible. 100
// is GitHub's maximum; anything beyond it is a second page, which is detected
// rather than assumed away.
const restPerPage = 100

// restIssue is the subset of REST's issue object the tree needs. It is the
// same information the GraphQL children query returned, from an endpoint that
// can be asked conditionally -- which is the whole reason for the move.
type restIssue struct {
	Number     int    `json:"number"`
	Title      string `json:"title"`
	State      string `json:"state"`
	Repository *struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

func (r restIssue) ref(fallback IssueRef) (IssueRef, bool) {
	if r.Repository != nil {
		if owner, name, ok := strings.Cut(r.Repository.FullName, "/"); ok && owner != "" && name != "" {
			return IssueRef{Owner: owner, Repo: name, Number: r.Number}, true
		}
	}
	// A sub-issue in the SAME repository omits the repository object, so the
	// parent's repo is the answer -- not a failure.
	if r.Number > 0 {
		return IssueRef{Owner: fallback.Owner, Repo: fallback.Repo, Number: r.Number}, true
	}
	return IssueRef{}, false
}

// IssueChildren lists an issue's sub-issues, conditionally.
//
// ONE REQUEST PER NODE, AND FREE WHEN NOTHING CHANGED. REST's sub_issues
// endpoint returns whole child objects -- number, title, state, labels -- so
// this single call carries everything the walk needs about a child, the way the
// GraphQL children query did. What it adds is an ETag: the same call on an
// unchanged issue answers 304 and costs nothing at all.
//
// A SECOND PAGE IS NOT SILENTLY DROPPED. Conditional requests are per URL, so
// paging past the first page would mean caching a partial list under the same
// key. Where a next page exists this falls back to a full uncached read, which
// costs what it always did and is complete.
func (c CLI) IssueChildren(repo string, number int) ([]SubIssue, error) {
	owner, name, err := splitRepo("IssueChildren", repo)
	if err != nil {
		return nil, err
	}
	parent := IssueRef{Owner: owner, Repo: name, Number: number}
	path := fmt.Sprintf("repos/%s/issues/%d/sub_issues?per_page=%d", repo, number, restPerPage)

	res, err := condGet(path)
	if err != nil {
		return nil, err
	}
	if res.More {
		return c.issueChildrenAllPages(repo, number, parent)
	}
	return decodeChildren(res.Body, parent)
}

// issueChildrenAllPages is the uncached path for an issue with more children
// than one page holds. It cannot be conditional, so it is not cheap -- it is
// merely correct, which is the property that must not be traded.
func (CLI) issueChildrenAllPages(repo string, number int, parent IssueRef) ([]SubIssue, error) {
	out, err := run("api", "--paginate",
		fmt.Sprintf("repos/%s/issues/%d/sub_issues?per_page=%d", repo, number, restPerPage))
	if err != nil {
		return nil, err
	}
	return decodeChildren(concatArrays(out), parent)
}

func decodeChildren(body []byte, parent IssueRef) ([]SubIssue, error) {
	if len(body) == 0 {
		return nil, nil
	}
	var raw []restIssue
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse children of %s: %w", parent, err)
	}
	out := make([]SubIssue, 0, len(raw))
	for _, r := range raw {
		ref, ok := r.ref(parent)
		if !ok {
			continue
		}
		labels := make([]string, 0, len(r.Labels))
		for _, l := range r.Labels {
			labels = append(labels, l.Name)
		}
		out = append(out, SubIssue{
			Ref: ref, Title: r.Title,
			// REST spells state lower case where GraphQL shouts it. Everything
			// downstream compares case-insensitively, and an empty state still
			// means unreadable, which is the distinction that matters.
			State:  strings.ToUpper(r.State),
			Labels: labels,
		})
	}
	return out, nil
}

// ViewIssue fetches one issue, conditionally.
//
// It moved off `gh issue view` -- which is GraphQL, and so is billed in full
// every time -- onto the REST endpoint carrying the same fields, which can be
// asked conditionally and costs nothing when the issue has not changed.
func (CLI) ViewIssue(repo string, number int) (*Issue, error) {
	res, err := condGet(fmt.Sprintf("repos/%s/issues/%d", repo, number))
	if err != nil {
		return nil, err
	}
	var iss Issue
	if err := json.Unmarshal(res.Body, &iss); err != nil {
		return nil, fmt.Errorf("parse issue %s#%d: %w", repo, number, err)
	}
	iss.State = strings.ToUpper(iss.State)
	return &iss, nil
}

// concatArrays joins the several JSON arrays `gh api --paginate` prints back
// to back into one. gh emits one array per page rather than a single document.
func concatArrays(raw []byte) []byte {
	parts := strings.Split(strings.TrimSpace(string(raw)), "][")
	if len(parts) == 1 {
		return raw
	}
	return []byte(strings.Join(parts, ","))
}
