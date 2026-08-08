// Reading a whole issue subtree in ONE request instead of one per node.
//
// The walk this replaces asked GitHub for a node's children, then asked again
// for each child, and so on -- around 250 sequential requests for a tree that
// nests four deep. At ~300ms of round trip each that is ~90 seconds of waiting
// with the CPU idle 84% of the time, and GraphQL will return the same tree,
// nested, in about one second. The fix is FEWER CALLS, not concurrent ones:
// ten-way concurrency would reach ~10s, where one query reaches ~1s.
//
// TWO LIMITS SHAPE EVERYTHING HERE, and both were measured rather than assumed.
//
// The node budget is MULTIPLICATIVE ACROSS NESTING. Asking for 25 children at
// each of five levels with five labels each is scored at 1,953,125 possible
// nodes and rejected outright against a ceiling of 500,000. Page size and depth
// therefore trade against each other, and neither can be raised alone.
//
// And ONE QUERY IS NOT THE WHOLE TREE. A connection that is not exhausted, and
// a node sitting at the query's deepest rung, both have children this response
// does not contain -- measured on a real tree, one truncated connection at the
// root hid 44% of it. So an answer records what it could NOT see, and the
// caller finishes the job. A walk that stopped at the first response would look
// a hundred times faster while silently losing most of the tree, which is worse
// than being slow.

package ghx

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Blocker is one issue that blocks another, carried with its state.
//
// The state is the point: resolving "is this blocker still open" per edge was
// one request each, undeduplicated, so a blocker holding ten issues was fetched
// ten times.
type Blocker struct {
	Ref   IssueRef
	State string
}

// SubTree is one node and everything below it that a single query reached.
type SubTree struct {
	Ref    IssueRef
	Title  string
	State  string
	Labels []string

	BlockedBy []Blocker
	Children  []SubTree

	// BlockersComplete says every blocking edge is in BlockedBy. That
	// connection is paged like any other, and a node with more blockers than
	// one page holds would otherwise look READY on the strength of the five
	// that fitted -- the one wrong answer a readiness report can give.
	BlockersComplete bool

	// MoreChildren says this node HAS children the response does not contain
	// -- the connection was not exhausted, the node sat at the deepest rung
	// asked for, or its children came back unresolved. Children is incomplete
	// whenever it is set, and a caller must finish the node itself rather than
	// read the short list as the whole answer.
	MoreChildren bool

	// ConnectionSeen says the children connection was in the response at all.
	// With MoreChildren it separates the two reasons a node is incomplete, and
	// they want opposite remedies: a TRUNCATED connection (seen) is finished by
	// paging that one node, where re-asking for its subtree would return the
	// same first page forever; an UNREACHED node (not seen) is finished by
	// asking for its subtree, and paging it would throw away the depth a
	// single query buys.
	ConnectionSeen bool
}

// subtreeMaxPage and subtreeMaxLabels are the page sizes that fit the node
// budget at subtreeDepth levels. Raising any one of the three requires
// lowering another: the budget is a product, not a sum.
const (
	subtreeMaxPage   = 25
	subtreeMaxLabels = 5
	subtreeBlockers  = 5
)

// issueFields is every scalar a walk needs from one issue. subIssuesSummary is
// what makes a leaf of the query honest: without it, "no children returned" and
// "children the query never reached" are the same answer.
const issueFields = `number title state
repository{nameWithOwner}
labels(first:%d){nodes{name}}
blockedBy(first:%d){pageInfo{hasNextPage} nodes{number state repository{nameWithOwner}}}
subIssuesSummary{total}`

// subtreeQuery builds the nested query for a given depth. It is generated
// rather than written out because the nesting is the only thing that varies,
// and a hand-written five-deep query is where a field gets forgotten at one
// level only.
func subtreeQuery(depth int) string {
	if depth < 1 {
		depth = 1
	}
	fields := fmt.Sprintf(issueFields, subtreeMaxLabels, subtreeBlockers)

	// Innermost first, then wrap outward.
	body := fields
	for i := 1; i < depth; i++ {
		body = fmt.Sprintf("%s\nsubIssues(first:%d){pageInfo{hasNextPage}\nnodes{%s}}",
			fields, subtreeMaxPage, body)
	}
	return fmt.Sprintf(
		"query($owner: String!, $repo: String!, $number: Int!) {\n"+
			"repository(owner: $owner, name: $repo) {\n"+
			"issue(number: $number) {\n%s\n}\n}\n}", body)
}

// IssueSubtree reads one issue and as much of the tree below it as depth
// levels of nesting reach.
//
// A PARTIAL RESPONSE IS AN ANSWER, NOT A FAILURE. gh exits non-zero whenever
// the response carries an errors array, and with one query covering a whole
// tree that would turn a single unreadable issue into a total failure -- where
// the per-node walk contained it to one node. So the payload is parsed
// whichever way gh exited: anything that did resolve is kept, and anything that
// did not is marked so the caller reads it and finishes the node itself.
func (CLI) IssueSubtree(repo string, number, depth int) (*SubTree, error) {
	owner, name, err := splitRepo("IssueSubtree", repo)
	if err != nil {
		return nil, err
	}
	out, runErr := runTolerant("api", "graphql",
		"-f", "query="+subtreeQuery(depth),
		"-f", "owner="+owner,
		"-f", "repo="+name,
		"-F", "number="+strconv.Itoa(number),
	)
	st, parseErr := parseSubtree(out)
	if parseErr != nil || st == nil {
		// Nothing usable came back. The original failure is the one worth
		// reporting -- a decode error on an error payload describes the
		// symptom rather than the cause.
		if runErr != nil {
			return nil, runErr
		}
		if parseErr != nil {
			return nil, fmt.Errorf("parse subtree of %s#%d: %w", repo, number, parseErr)
		}
		return nil, fmt.Errorf("%s#%d: no such issue", repo, number)
	}
	return st, nil
}

// subtreeNode mirrors the query. Pointers where GraphQL can null a field on a
// partial response, so "absent" stays distinguishable from "empty".
type subtreeNode struct {
	Number     int    `json:"number"`
	Title      string `json:"title"`
	State      string `json:"state"`
	Repository *struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
	Labels *struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	BlockedBy *struct {
		PageInfo struct {
			HasNextPage bool `json:"hasNextPage"`
		} `json:"pageInfo"`
		Nodes []struct {
			Number     int    `json:"number"`
			State      string `json:"state"`
			Repository *struct {
				NameWithOwner string `json:"nameWithOwner"`
			} `json:"repository"`
		} `json:"nodes"`
	} `json:"blockedBy"`
	SubIssuesSummary *struct {
		Total int `json:"total"`
	} `json:"subIssuesSummary"`
	SubIssues *struct {
		PageInfo struct {
			HasNextPage bool `json:"hasNextPage"`
		} `json:"pageInfo"`
		Nodes []subtreeNode `json:"nodes"`
	} `json:"subIssues"`
}

func parseSubtree(raw []byte) (*SubTree, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var envelope struct {
		Data *struct {
			Repository *struct {
				Issue *subtreeNode `json:"issue"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	if envelope.Data == nil || envelope.Data.Repository == nil || envelope.Data.Repository.Issue == nil {
		return nil, nil
	}
	st := convertSubtree(*envelope.Data.Repository.Issue)
	return &st, nil
}

func convertSubtree(n subtreeNode) SubTree {
	out := SubTree{Title: n.Title, State: n.State}
	if n.Repository != nil {
		if owner, name, ok := strings.Cut(n.Repository.NameWithOwner, "/"); ok {
			out.Ref = IssueRef{Owner: owner, Repo: name, Number: n.Number}
		}
	}
	if n.Labels != nil {
		for _, l := range n.Labels.Nodes {
			out.Labels = append(out.Labels, l.Name)
		}
	}
	if n.BlockedBy != nil {
		out.BlockersComplete = !n.BlockedBy.PageInfo.HasNextPage
		for _, b := range n.BlockedBy.Nodes {
			owner, name := "", ""
			if b.Repository != nil {
				var ok bool
				owner, name, ok = strings.Cut(b.Repository.NameWithOwner, "/")
				if !ok {
					owner, name = "", ""
				}
			}
			if owner == "" || name == "" {
				// An edge that came back unidentifiable. DROPPING IT WOULD
				// READ AS READY, so the list stops being a list and the
				// caller re-reads it the paged way.
				out.BlockersComplete = false
				continue
			}
			out.BlockedBy = append(out.BlockedBy, Blocker{
				Ref:   IssueRef{Owner: owner, Repo: name, Number: b.Number},
				State: b.State,
			})
		}
	}

	if n.SubIssues == nil {
		// The query did not reach this rung, or the connection did not
		// resolve. Either way its children are unknown -- and the summary is
		// what tells "unknown but empty" from "unknown and there are some".
		out.MoreChildren = n.SubIssuesSummary == nil || n.SubIssuesSummary.Total > 0
		return out
	}
	out.ConnectionSeen = true
	for _, c := range n.SubIssues.Nodes {
		child := convertSubtree(c)
		if child.Ref.Owner == "" || child.Ref.Repo == "" {
			// A child nobody can name cannot be walked, reported against, or
			// told apart from one that is simply absent. Treat the whole list
			// as short so the paged read names it in the ordinary place.
			out.MoreChildren = true
			continue
		}
		out.Children = append(out.Children, child)
	}
	if n.SubIssues.PageInfo.HasNextPage {
		out.MoreChildren = true
	}
	return out
}
