package ghx

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// IssueRef names one issue anywhere on the forge. The parent and dependency
// graphs are both cross-repository -- a child routinely lives in a different
// repository from its parent -- so a bare number is never enough to identify
// an edge's far end.
type IssueRef struct {
	Owner  string
	Repo   string
	Number int
}

// String renders the ref the way issue bodies and humans write it.
func (r IssueRef) String() string {
	return fmt.Sprintf("%s/%s#%d", r.Owner, r.Repo, r.Number)
}

// OwnerRepo is the "owner/name" half, which is what every other method here
// takes as its repo argument.
func (r IssueRef) OwnerRepo() string { return r.Owner + "/" + r.Repo }

// splitRepo takes "owner/name" apart, refusing anything else rather than
// guessing -- a half-formed repo silently becomes a 404 three calls later.
func splitRepo(what, repo string) (owner, name string, err error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return "", "", fmt.Errorf("%s: %q is not owner/repo", what, repo)
	}
	return owner, name, nil
}

// issueParentQuery reads an issue's parent under GitHub's sub-issues feature.
//
// GRAPHQL IS NOT AN OPTIMISATION HERE, IT IS THE ONLY WAY. The REST issues API
// exposes an issue's CHILDREN (`/sub_issues`) but has no read for its parent:
// the `parent` key is ABSENT from the REST issue object rather than null, so
// the natural `--jq '.parent // "none"'` answers "none" for every issue in
// existence, and a REST implementation reads every child as unparented while
// looking like it worked. Measured against a live wired pair on 2026-08-03:
// `gh api repos/o/r/issues/N --jq 'has("parent")'` returns false for an issue
// whose parent this query returns.
const issueParentQuery = `query($owner: String!, $repo: String!, $number: Int!) {
  repository(owner: $owner, name: $repo) {
    issue(number: $number) {
      parent {
        number
        repository { owner { login } name }
      }
    }
  }
}`

// IssueParent reports an issue's parent, and whether it has one at all.
//
// The bool distinguishes "asked, and there is none" (err nil, ok false) from
// "could not tell" (err non-nil). A caller that must not treat an unreadable
// parent as an absent one -- which is every caller that reports on shape --
// reads ok only when err is nil.
//
// This direction is IMMEDIATELY consistent: a parent wired moments earlier is
// visible here at once. Its inverse, [CLI.IssueChildren], is not. Verify a
// write from this side.
func (CLI) IssueParent(repo string, number int) (IssueRef, bool, error) {
	owner, name, err := splitRepo("IssueParent", repo)
	if err != nil {
		return IssueRef{}, false, err
	}
	out, err := run("api", "graphql",
		"-f", "query="+issueParentQuery,
		"-f", "owner="+owner,
		"-f", "repo="+name,
		"-F", "number="+strconv.Itoa(number),
	)
	if err != nil {
		return IssueRef{}, false, err
	}

	ref, ok, err := parseIssueParent(out)
	if err != nil {
		return IssueRef{}, false, fmt.Errorf("parse parent of %s#%d: %w", repo, number, err)
	}
	return ref, ok, nil
}

// parseIssueParent is split out so the absent-parent case can be tested
// directly. That case is the whole hazard: a null parent and a REST-shaped
// response with no parent key at all decode identically here, and only one of
// them means "there is no parent".
func parseIssueParent(out []byte) (IssueRef, bool, error) {
	var resp struct {
		Data struct {
			Repository struct {
				Issue struct {
					Parent *graphIssueRef `json:"parent"`
				} `json:"issue"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return IssueRef{}, false, err
	}
	p := resp.Data.Repository.Issue.Parent
	if p == nil {
		return IssueRef{}, false, nil
	}
	return p.ref(), true, nil
}

// graphIssueRef is the shape both graph queries return for an issue.
type graphIssueRef struct {
	Number     int `json:"number"`
	Repository struct {
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
		Name string `json:"name"`
	} `json:"repository"`
}

func (g graphIssueRef) ref() IssueRef {
	return IssueRef{Owner: g.Repository.Owner.Login, Repo: g.Repository.Name, Number: g.Number}
}

// issueChildrenQuery lists an issue's sub-issues, one page at a time, with the
// three fields a tree walk needs to render a child WITHOUT a second read.
//
// facet#105: this used to return only the ref, so a walk paid a second `gh`
// process (ViewIssue) per child just to learn its title/state/labels --
// two processes per node instead of one query per PARENT. Those fields are
// GitHub's own on subIssues, so asking for them here removes the second call
// entirely rather than tuning it.
//
// It is paginated rather than capped at `first: 100` because a silent
// truncation reads exactly like a complete answer, and this result is what
// shape reports are built from. `labels(first: 20)` is not paginated: a
// truncated label list only means a rarely-used label goes unseen, not a
// missing child, and no issue in this bottega carries anywhere near 20.
const issueChildrenQuery = `query($owner: String!, $repo: String!, $number: Int!, $after: String) {
  repository(owner: $owner, name: $repo) {
    issue(number: $number) {
      subIssues(first: 100, after: $after) {
        pageInfo { hasNextPage endCursor }
        nodes {
          number
          state
          title
          repository { owner { login } name }
          labels(first: 20) { nodes { name } }
        }
      }
    }
  }
}`

// SubIssue is one child returned by [CLI.IssueChildren]: the ref plus the
// fields a tree walk needs to render it, read in the SAME call rather than a
// second `gh issue view` per child (facet#105).
type SubIssue struct {
	Ref    IssueRef
	Title  string
	// State is GitHub's non-nullable OPEN/CLOSED enum on a real issue. Empty
	// here means the node's fields could not be resolved -- GraphQL can
	// return a partial response with some list entries unresolved -- and is
	// read as "unreadable", the same fact [CLI.ViewIssue] failing used to
	// carry for a child.
	State  string
	Labels []string
}

// graphSubIssue is the shape issueChildrenQuery's nodes return.
type graphSubIssue struct {
	Number     int    `json:"number"`
	State      string `json:"state"`
	Title      string `json:"title"`
	Repository struct {
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
		Name string `json:"name"`
	} `json:"repository"`
	Labels struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
}

func (g graphSubIssue) subIssue() SubIssue {
	labels := make([]string, 0, len(g.Labels.Nodes))
	for _, l := range g.Labels.Nodes {
		labels = append(labels, l.Name)
	}
	return SubIssue{
		Ref:    IssueRef{Owner: g.Repository.Owner.Login, Repo: g.Repository.Name, Number: g.Number},
		Title:  g.Title,
		State:  g.State,
		Labels: labels,
	}
}

// IssueChildren lists an issue's sub-issues, in the order GitHub returns
// them, each carrying the title/state/labels a tree walk needs -- no second
// call per child.
//
// EVENTUALLY CONSISTENT, unlike [CLI.IssueParent]. An edge written moments ago
// may not appear here yet, so this must never be used to confirm a write just
// made -- it will report a false failure and invite wiring the edge twice.
// Nothing in GitHub's schema offers an immediately-consistent way to list
// children; only reading a parent from a known child's side is immediate.
// Requesting more fields on subIssues does not change this: the consistency
// behaviour belongs to the edge, not to which fields are read off it.
func (CLI) IssueChildren(repo string, number int) ([]SubIssue, error) {
	owner, name, err := splitRepo("IssueChildren", repo)
	if err != nil {
		return nil, err
	}

	var out []SubIssue
	after := ""
	for {
		args := []string{"api", "graphql",
			"-f", "query=" + issueChildrenQuery,
			"-f", "owner=" + owner,
			"-f", "repo=" + name,
			"-F", "number=" + strconv.Itoa(number),
		}
		// gh sends an omitted variable as null, which is what page one wants;
		// passing an empty string instead makes GitHub reject the cursor.
		if after != "" {
			args = append(args, "-f", "after="+after)
		}
		raw, err := run(args...)
		if err != nil {
			return nil, err
		}
		page, err := parseIssueChildrenPage(raw)
		if err != nil {
			return nil, fmt.Errorf("parse children of %s#%d: %w", repo, number, err)
		}
		out = append(out, page.nodes...)
		if !page.hasNextPage || page.endCursor == "" {
			return out, nil
		}
		after = page.endCursor
	}
}

// issueChildrenPage is one page of issueChildrenQuery's answer, decoded.
type issueChildrenPage struct {
	nodes       []SubIssue
	hasNextPage bool
	endCursor   string
}

// parseIssueChildrenPage is split out so the field mapping -- especially an
// unresolved node's empty State reading as "could not be read" -- can be
// tested directly against a fixture, the same reason [parseIssueParent] is
// its own function.
func parseIssueChildrenPage(raw []byte) (issueChildrenPage, error) {
	var resp struct {
		Data struct {
			Repository struct {
				Issue struct {
					SubIssues struct {
						PageInfo struct {
							HasNextPage bool   `json:"hasNextPage"`
							EndCursor   string `json:"endCursor"`
						} `json:"pageInfo"`
						Nodes []graphSubIssue `json:"nodes"`
					} `json:"subIssues"`
				} `json:"issue"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return issueChildrenPage{}, err
	}
	page := resp.Data.Repository.Issue.SubIssues
	nodes := make([]SubIssue, 0, len(page.Nodes))
	for _, n := range page.Nodes {
		nodes = append(nodes, n.subIssue())
	}
	return issueChildrenPage{
		nodes:       nodes,
		hasNextPage: page.PageInfo.HasNextPage,
		endCursor:   page.PageInfo.EndCursor,
	}, nil
}

// AddSubIssue makes child a sub-issue of repo#number.
//
// childID is the numeric DATABASE id, not the issue number -- the two are
// never the same value, and both ways of getting it wrong were measured on
// 2026-08-03 against a scratch pair:
//
//	-f sub_issue_id=<id>      422  `"5049244556"` is not of type `integer`
//	-F sub_issue_id=<number>  404  the wrong kind of identifier, silently
//
// Hence -F (a typed field) carrying an id from [CLI.IssueID]. The id is a
// ~10-digit global integer, which is why it is an int64 and not an int.
//
// An issue has exactly one parent: calling this on a child that already has
// one does NOT move it -- it 422s ("Sub issue may only have one parent").
// Detach the existing edge with [CLI.RemoveSubIssue] first.
func (CLI) AddSubIssue(repo string, number int, childID int64) error {
	_, err := run("api", fmt.Sprintf("repos/%s/issues/%d/sub_issues", repo, number),
		"-X", "POST", "-F", fmt.Sprintf("sub_issue_id=%d", childID))
	return err
}

// RemoveSubIssue detaches child (by database id) from repo#number as its
// sub-issue parent.
//
// The REST endpoint is the SINGULAR "sub_issue", unlike AddSubIssue's plural
// "sub_issues" -- one character apart and a 404 if confused. This exists
// because AddSubIssue's own doc comment was wrong: POSTing to a child that
// already has a parent does not move it, it 422s ("Sub issue may only have
// one parent"). A move is DELETE this edge, then POST the new one -- there is
// no atomic move on this API.
func (CLI) RemoveSubIssue(repo string, number int, childID int64) error {
	_, err := run("api", fmt.Sprintf("repos/%s/issues/%d/sub_issue", repo, number),
		"-X", "DELETE", "-F", fmt.Sprintf("sub_issue_id=%d", childID))
	return err
}

// BlockedBy lists the issues that must land before repo#number can proceed.
//
// The dependency graph is NOT the parent graph: this says what must happen
// first, while a parent says what a thing is part of. Unlike the parent edge,
// this one reads correctly over REST in both directions -- verified on
// 2026-08-03 by walking a `blocking` edge and confirming the far end reports
// the matching `blocked_by`.
func (CLI) BlockedBy(repo string, number int) ([]IssueRef, error) {
	return dependencyEdge(repo, number, "blocked_by")
}

// Blocking lists the issues that cannot start until repo#number lands -- the
// inverse edge, and the one that answers "what does merging this unlock".
func (CLI) Blocking(repo string, number int) ([]IssueRef, error) {
	return dependencyEdge(repo, number, "blocking")
}

func dependencyEdge(repo string, number int, direction string) ([]IssueRef, error) {
	out, err := run("api", "--paginate",
		fmt.Sprintf("repos/%s/issues/%d/dependencies/%s", repo, number, direction))
	if err != nil {
		return nil, err
	}
	refs, err := parseDependencyEdges(out)
	if err != nil {
		return nil, fmt.Errorf("parse %s of %s#%d: %w", direction, repo, number, err)
	}
	return refs, nil
}

// parseDependencyEdges decodes what `gh api --paginate` returns for a
// dependency endpoint.
//
// --paginate CONCATENATES one JSON array per page rather than merging them, so
// a second page turns the output into `[...][...]`, which is not a document
// json.Unmarshal will accept. Decoding a stream is not a refinement here: a
// single Unmarshal silently works for every issue with under thirty
// dependencies and fails only on the ones big enough to matter.
func parseDependencyEdges(out []byte) ([]IssueRef, error) {
	dec := json.NewDecoder(strings.NewReader(string(out)))
	var refs []IssueRef
	for dec.More() {
		var page []struct {
			Number     int `json:"number"`
			Repository struct {
				FullName string `json:"full_name"`
			} `json:"repository"`
		}
		if err := dec.Decode(&page); err != nil {
			return nil, err
		}
		for _, e := range page {
			owner, name, err := splitRepo("dependency", e.Repository.FullName)
			if err != nil {
				return nil, err
			}
			refs = append(refs, IssueRef{Owner: owner, Repo: name, Number: e.Number})
		}
	}
	return refs, nil
}
