// Reading many issues in ONE request, by alias, instead of one request each.
//
// THE COST MODEL IS THE DESIGN CONSTRAINT, AND IT IS NOT THE ONE THAT LOOKS
// OBVIOUS. GitHub bills a GraphQL query on the nodes it COULD return, not the
// ones it does: cost = max(1, possible_nodes/100), against 5000 points an hour.
// Possible nodes MULTIPLY down nesting levels and ADD across aliases.
//
// So nesting is the expensive shape and batching is the cheap one. Measured on
// a real 160-node tree:
//
//	one query nested 4 deep at first:25  ->  ~179,000 billed to return 89
//	                                     ->  ~1,790 points, per query
//	the whole walk that way              ->   4,651 points, 93% of the hour
//	one request per node, first:100      ->  ~21 points x 160 = ~3,360
//
// Neither is survivable: a five-minute tick needs twelve walks an hour. Aliases
// add, so the same tree costs about a node's worth of budget per node, and a
// batch of fifty is one round trip.
//
// AND A STARVED WALK DOES NOT FAIL, IT SHORTENS. Measured: with 15 points left,
// the walk printed 49 of 160 nodes, exited without error and wrote nothing to
// stderr. Every consumer downstream then filters a short tree and reports
// confidently on it. That is why staying under the cap is a correctness
// property here and not a courtesy to GitHub.

package ghx

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Page sizes. Each is billed once per alias rather than multiplied, so these
// are chosen to be big enough to be COMPLETE rather than small enough to be
// cheap -- a truncated read is what costs, and it costs correctness.
//
// batchLabels is the one that bit: at first:5 a node carrying seven labels
// loses two, and if a type/* label is among them the tree derives the wrong
// level from a read that looks fine. Real nodes here carry up to seven.
const (
	batchLabels   = 30
	batchChildren = 50
	batchBlockers = 30
	// batchSize is how many issues ride in one request. Aliases add, so this
	// trades round trips against nothing except response size.
	batchSize = 50
)

// Blocker is one issue that blocks another, carried with its state.
//
// The state is the point: resolving "is this blocker still open" per edge was
// one request each, undeduplicated, so a blocker holding ten issues was
// fetched ten times.
type Blocker struct {
	Ref   IssueRef
	State string
}

// NodeFields is one issue as a batch read returns it, plus the IDENTITY of its
// children -- not their contents. A child's own fields arrive when it is read
// in its turn, which is what keeps cost linear in the node count instead of
// multiplying page sizes together.
type NodeFields struct {
	Ref    IssueRef
	Title  string
	State  string
	Labels []string

	BlockedBy []Blocker
	Children  []IssueRef

	// The three completeness flags. Each connection is paged, and a short read
	// of any of them is indistinguishable from a complete one by looking --
	// which is the whole failure class this file exists inside. A caller must
	// treat any of them being false as "ask again properly", never as "that is
	// all there is".
	LabelsComplete   bool
	BlockersComplete bool
	ChildrenComplete bool

	// Unreadable marks an alias that came back null. The node exists as far as
	// its parent is concerned and could not be read here.
	Unreadable bool
}

// safeIdent guards the one place a value is interpolated into a query rather
// than passed as a variable. GraphQL has no variables for ALIASES or for the
// repository arguments of an aliased selection, so the owner and name are
// written into the document -- and anything that could close a string and open
// a new selection has to be impossible rather than unlikely.
func safeIdent(s string) bool {
	if s == "" || len(s) > 100 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

const batchFragment = `fragment F on Issue {
  number
  title
  state
  repository { nameWithOwner }
  labels(first: %d) { pageInfo { hasNextPage } nodes { name } }
  blockedBy(first: %d) { pageInfo { hasNextPage } nodes { number state repository { nameWithOwner } } }
  subIssues(first: %d) { pageInfo { hasNextPage } nodes { number repository { nameWithOwner } } }
}`

// batchQuery writes one document that reads every ref given.
//
// The child selection carries only what NAMES a child -- its number and repo.
// Asking for a child's labels here would nest a connection inside a connection
// and multiply the bill by the page size, for fields that arrive anyway when
// that child is read in its own right.
func batchQuery(refs []IssueRef) (string, error) {
	var sb strings.Builder
	sb.WriteString("query {\n  rateLimit { cost remaining limit }\n")
	for i, r := range refs {
		if !safeIdent(r.Owner) || !safeIdent(r.Repo) || r.Number <= 0 {
			return "", fmt.Errorf("refusing to build a query for %q: not a well-formed issue reference", r.String())
		}
		fmt.Fprintf(&sb, "  n%d: repository(owner: %q, name: %q) { issue(number: %d) { ...F } }\n",
			i, r.Owner, r.Repo, r.Number)
	}
	sb.WriteString("}\n")
	fmt.Fprintf(&sb, batchFragment, batchLabels, batchBlockers, batchChildren)
	return sb.String(), nil
}

// IssueNodes reads every ref given, in as few requests as the batch size
// allows, and returns them in the same order.
//
// A PARTIAL RESPONSE IS AN ANSWER. gh exits non-zero whenever the payload
// carries an errors array, and one request covering fifty issues would
// otherwise turn a single unreadable one into fifty failures. Anything that
// resolved is kept; anything that did not is marked Unreadable and left for
// the caller to report where it can be named.
func (c CLI) IssueNodes(refs []IssueRef) ([]NodeFields, error) {
	out := make([]NodeFields, 0, len(refs))
	for start := 0; start < len(refs); start += batchSize {
		end := min(start+batchSize, len(refs))
		got, err := c.issueBatch(refs[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, got...)
	}
	return out, nil
}

func (CLI) issueBatch(refs []IssueRef) ([]NodeFields, error) {
	q, err := batchQuery(refs)
	if err != nil {
		return nil, err
	}
	raw, runErr := runTolerant("api", "graphql", "-f", "query="+q)
	got, parseErr := parseBatch(raw, refs)
	if got == nil {
		if runErr != nil {
			return nil, runErr
		}
		if parseErr != nil {
			return nil, fmt.Errorf("parse batch of %d issue(s): %w", len(refs), parseErr)
		}
		return nil, fmt.Errorf("batch of %d issue(s): no data came back", len(refs))
	}
	return got, nil
}

type batchNode struct {
	Number     int    `json:"number"`
	Title      string `json:"title"`
	State      string `json:"state"`
	Repository *struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
	Labels *struct {
		PageInfo pageFlag `json:"pageInfo"`
		Nodes    []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	BlockedBy *struct {
		PageInfo pageFlag `json:"pageInfo"`
		Nodes    []struct {
			Number     int    `json:"number"`
			State      string `json:"state"`
			Repository *struct {
				NameWithOwner string `json:"nameWithOwner"`
			} `json:"repository"`
		} `json:"nodes"`
	} `json:"blockedBy"`
	SubIssues *struct {
		PageInfo pageFlag `json:"pageInfo"`
		Nodes    []struct {
			Number     int `json:"number"`
			Repository *struct {
				NameWithOwner string `json:"nameWithOwner"`
			} `json:"repository"`
		} `json:"nodes"`
	} `json:"subIssues"`
}

type pageFlag struct {
	HasNextPage bool `json:"hasNextPage"`
}

// parseBatch maps the aliased response back onto the refs that asked for it.
// The alias index is what carries that mapping: a null entry is a node that
// could not be read, and it must stay in position rather than shifting every
// answer after it onto the wrong issue.
func parseBatch(raw []byte, refs []IssueRef) ([]NodeFields, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var envelope struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	if envelope.Data == nil {
		return nil, nil
	}

	out := make([]NodeFields, len(refs))
	for i, r := range refs {
		out[i] = NodeFields{Ref: r, Unreadable: true}
		entry, ok := envelope.Data["n"+strconv.Itoa(i)]
		if !ok {
			continue
		}
		var wrapper struct {
			Issue *batchNode `json:"issue"`
		}
		if err := json.Unmarshal(entry, &wrapper); err != nil || wrapper.Issue == nil {
			continue
		}
		out[i] = convertBatchNode(r, *wrapper.Issue)
	}
	return out, nil
}

func convertBatchNode(ref IssueRef, n batchNode) NodeFields {
	f := NodeFields{Ref: ref, Title: n.Title, State: n.State}

	if n.Labels != nil {
		f.LabelsComplete = !n.Labels.PageInfo.HasNextPage
		for _, l := range n.Labels.Nodes {
			f.Labels = append(f.Labels, l.Name)
		}
	}

	if n.BlockedBy != nil {
		f.BlockersComplete = !n.BlockedBy.PageInfo.HasNextPage
		for _, b := range n.BlockedBy.Nodes {
			r, ok := refFrom(b.Repository, b.Number)
			if !ok {
				// Dropping it would read as "nothing blocks this", which is
				// the one wrong answer a readiness report can give.
				f.BlockersComplete = false
				continue
			}
			f.BlockedBy = append(f.BlockedBy, Blocker{Ref: r, State: b.State})
		}
	}

	if n.SubIssues != nil {
		f.ChildrenComplete = !n.SubIssues.PageInfo.HasNextPage
		for _, c := range n.SubIssues.Nodes {
			r, ok := refFrom(c.Repository, c.Number)
			if !ok {
				// A child nobody can name cannot be walked or reported
				// against, so the list declares itself short instead.
				f.ChildrenComplete = false
				continue
			}
			f.Children = append(f.Children, r)
		}
	}

	return f
}

func refFrom(repo *struct {
	NameWithOwner string `json:"nameWithOwner"`
}, number int) (IssueRef, bool) {
	if repo == nil || number <= 0 {
		return IssueRef{}, false
	}
	owner, name, ok := strings.Cut(repo.NameWithOwner, "/")
	if !ok || owner == "" || name == "" {
		return IssueRef{}, false
	}
	return IssueRef{Owner: owner, Repo: name, Number: number}, true
}

// BilledNodes is what GitHub will charge one batch of n issues, in POSSIBLE
// nodes, by its documented formula: connections ADD across aliases and
// MULTIPLY down nesting. Exposed so a test can hold the cost of a walk to a
// budget without spending any of it -- the arithmetic is the whole model, and
// the model is what a reader cannot check by looking at the query.
func BilledNodes(issues int) int {
	perIssue := batchLabels + batchBlockers + batchChildren
	return issues * perIssue
}

// BilledPoints converts possible nodes to the rate-limit points GitHub charges.
func BilledPoints(issues int) int {
	n := BilledNodes(issues)
	pts := n / 100
	if n%100 != 0 {
		pts++
	}
	return max(pts, 1)
}
