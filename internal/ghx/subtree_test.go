package ghx

import (
	"strings"
	"testing"
)

func TestParseSubtreeCarriesEveryFieldAWalkNeeds(t *testing.T) {
	const payload = `{"data":{"repository":{"issue":{
      "number":46,"title":"commission 1","state":"OPEN",
      "repository":{"nameWithOwner":"acme/lab"},
      "labels":{"nodes":[{"name":"type/commission"},{"name":"complexity/3"}]},
      "blockedBy":{"pageInfo":{"hasNextPage":false},"nodes":[
        {"number":9,"state":"CLOSED","repository":{"nameWithOwner":"acme/harness"}}]},
      "subIssuesSummary":{"total":1},
      "subIssues":{"pageInfo":{"hasNextPage":false},"nodes":[
        {"number":282,"title":"seat: c1","state":"OPEN",
         "repository":{"nameWithOwner":"acme/doctrine"},
         "labels":{"nodes":[{"name":"type/seat"}]},
         "blockedBy":{"pageInfo":{"hasNextPage":false},"nodes":[]},
         "subIssuesSummary":{"total":0},
         "subIssues":{"pageInfo":{"hasNextPage":false},"nodes":[]}}]}}}}}`

	st, err := parseSubtree([]byte(payload))
	if err != nil || st == nil {
		t.Fatalf("parseSubtree: %v (st=%v)", err, st)
	}
	if st.Ref.String() != "acme/lab#46" || st.Title != "commission 1" || st.State != "OPEN" {
		t.Fatalf("root = %+v", st)
	}
	if len(st.Labels) != 2 || st.Labels[0] != "type/commission" {
		t.Errorf("labels = %v", st.Labels)
	}
	// The blocker's own state arrives with the edge. Resolving it separately
	// was one request per edge, undeduplicated.
	if len(st.BlockedBy) != 1 || st.BlockedBy[0].State != "CLOSED" ||
		st.BlockedBy[0].Ref.String() != "acme/harness#9" {
		t.Errorf("blockedBy = %+v", st.BlockedBy)
	}
	if len(st.Children) != 1 {
		t.Fatalf("children = %d, want 1", len(st.Children))
	}
	// A cross-repository child, which is the ordinary case here.
	if st.Children[0].Ref.String() != "acme/doctrine#282" {
		t.Errorf("child ref = %s", st.Children[0].Ref)
	}
	if st.MoreChildren {
		t.Error("an exhausted connection reported more children")
	}
}

// The two incomplete cases are DIFFERENT, and a caller has to tell them apart:
// one is finished by paging, the other by asking for a subtree.
func TestParseSubtreeSeparatesTruncatedFromUnreached(t *testing.T) {
	t.Run("truncated: the connection came back and did not end", func(t *testing.T) {
		st := mustParse(t, `{"data":{"repository":{"issue":{
          "number":46,"repository":{"nameWithOwner":"acme/lab"},
          "subIssuesSummary":{"total":40},
          "subIssues":{"pageInfo":{"hasNextPage":true},"nodes":[
            {"number":1,"state":"OPEN","repository":{"nameWithOwner":"acme/lab"}}]}}}}}`)
		if !st.MoreChildren || !st.ConnectionSeen {
			t.Fatalf("MoreChildren=%v ConnectionSeen=%v, want both true", st.MoreChildren, st.ConnectionSeen)
		}
	})

	t.Run("unreached with children: no connection, and the summary says there are some", func(t *testing.T) {
		st := mustParse(t, `{"data":{"repository":{"issue":{
          "number":46,"repository":{"nameWithOwner":"acme/lab"},
          "subIssuesSummary":{"total":3}}}}}`)
		if !st.MoreChildren || st.ConnectionSeen {
			t.Fatalf("MoreChildren=%v ConnectionSeen=%v, want true/false", st.MoreChildren, st.ConnectionSeen)
		}
	})

	// The one that makes a leaf honest. Without the summary, "no children
	// returned" and "children the query never reached" are one answer, and
	// reading it as the first is how a walk loses a subtree while looking fast.
	t.Run("unreached with none: a genuine leaf, complete", func(t *testing.T) {
		st := mustParse(t, `{"data":{"repository":{"issue":{
          "number":46,"repository":{"nameWithOwner":"acme/lab"},
          "subIssuesSummary":{"total":0}}}}}`)
		if st.MoreChildren {
			t.Fatal("a leaf with no children was reported incomplete")
		}
	})

	// No summary at all is UNKNOWN, and unknown must lean toward asking again.
	t.Run("no summary: unknown, so incomplete", func(t *testing.T) {
		st := mustParse(t, `{"data":{"repository":{"issue":{
          "number":46,"repository":{"nameWithOwner":"acme/lab"}}}}}`)
		if !st.MoreChildren {
			t.Fatal("a node whose children are unknown was reported complete")
		}
	})
}

// A PARTIAL RESPONSE IS AN ANSWER. gh exits non-zero whenever the payload
// carries an errors array, and one query covering a whole tree would turn a
// single unreadable issue into total failure -- where the per-node walk
// contained it to that node.
func TestParseSubtreeKeepsWhatResolvedAlongsideErrors(t *testing.T) {
	st := mustParse(t, `{"errors":[{"message":"Could not resolve to an Issue","path":["repository","issue","subIssues","nodes",1]}],
      "data":{"repository":{"issue":{
        "number":46,"title":"commission 1","state":"OPEN",
        "repository":{"nameWithOwner":"acme/lab"},
        "subIssuesSummary":{"total":2},
        "subIssues":{"pageInfo":{"hasNextPage":false},"nodes":[
          {"number":282,"title":"seat: c1","state":"OPEN","repository":{"nameWithOwner":"acme/doctrine"},
           "subIssuesSummary":{"total":0},"subIssues":{"pageInfo":{"hasNextPage":false},"nodes":[]}},
          {"number":0,"title":"","state":"","repository":null}]}}}}}`)

	if st.Title != "commission 1" {
		t.Fatalf("the resolved part was discarded: %+v", st)
	}
	// The entry that resolved is kept.
	if len(st.Children) != 1 || st.Children[0].Ref.String() != "acme/doctrine#282" {
		t.Fatalf("children = %+v, want just the one that resolved", st.Children)
	}
	// And the one that did not is NOT silently gone: the list says it is
	// short, so the caller re-reads it the paged way, where an unreadable
	// child already has a representation that can name itself. Carrying it
	// here would mean a node with no owner, repo or number to print.
	if !st.MoreChildren {
		t.Error("an entry that did not resolve was dropped and the list still called itself complete")
	}
}

// A node that resolved ENOUGH TO BE NAMED but not fully is a different case,
// and it keeps the convention the walk already reads: no state means
// unreadable. Only a node that cannot be named at all falls back to paging.
func TestAChildThatCanBeNamedIsCarriedAsUnreadable(t *testing.T) {
	st := mustParse(t, `{"data":{"repository":{"issue":{
      "number":46,"repository":{"nameWithOwner":"acme/lab"},
      "subIssuesSummary":{"total":1},
      "subIssues":{"pageInfo":{"hasNextPage":false},"nodes":[
        {"number":99,"title":"","state":"","repository":{"nameWithOwner":"acme/harness"}}]}}}}}`)

	if len(st.Children) != 1 || st.Children[0].Ref.String() != "acme/harness#99" {
		t.Fatalf("children = %+v, want the nameable one carried", st.Children)
	}
	if st.Children[0].State != "" {
		t.Errorf("an unresolved node came back with a state: %+v", st.Children[0])
	}
	if st.MoreChildren {
		t.Error("a nameable-but-unreadable child made the whole list read as short")
	}
}

func TestParseSubtreeReportsNothingUsableAsNothing(t *testing.T) {
	for name, payload := range map[string]string{
		"errors only":  `{"errors":[{"message":"API rate limit already exceeded"}]}`,
		"null issue":   `{"data":{"repository":{"issue":null}}}`,
		"empty body":   ``,
		"null repo":    `{"data":{"repository":null}}`,
		"no data key":  `{}`,
		"not an issue": `{"data":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			st, err := parseSubtree([]byte(payload))
			if err != nil {
				t.Fatalf("a payload with nothing in it errored instead of answering nothing: %v", err)
			}
			if st != nil {
				t.Fatalf("got a subtree from %s: %+v", name, st)
			}
		})
	}
}

// The node budget is MULTIPLICATIVE across nesting, so depth and page size
// trade against each other and neither can be raised alone. Five rungs at
// these page sizes is rejected outright by the API; four is not.
func TestSubtreeQueryNestsExactlyAsDeepAsAsked(t *testing.T) {
	for depth := 1; depth <= 5; depth++ {
		q := subtreeQuery(depth)
		if got := strings.Count(q, "subIssues(first:"); got != depth-1 {
			t.Errorf("depth %d nests %d connection(s), want %d", depth, got, depth-1)
		}
		// Every rung must ask for the same fields. A hand-written nested query
		// is exactly where one level quietly loses a field.
		if got := strings.Count(q, "subIssuesSummary{total}"); got != depth {
			t.Errorf("depth %d asks for the summary %d time(s), want %d", depth, got, depth)
		}
		if got := strings.Count(q, "labels(first:"); got != depth {
			t.Errorf("depth %d asks for labels %d time(s), want %d", depth, got, depth)
		}
		if got := strings.Count(q, "blockedBy(first:"); got != depth {
			t.Errorf("depth %d asks for blockers %d time(s), want %d", depth, got, depth)
		}
	}
}

func mustParse(t *testing.T, payload string) *SubTree {
	t.Helper()
	st, err := parseSubtree([]byte(payload))
	if err != nil {
		t.Fatalf("parseSubtree: %v", err)
	}
	if st == nil {
		t.Fatal("parseSubtree returned nothing")
	}
	return st
}

// An edge or a child that came back unidentifiable must not be DROPPED.
// Silently losing one reads as "nothing blocks this" and "no more children" --
// both of which are the answer that lets work through.
func TestAnUnidentifiableEntryMakesTheListIncompleteRatherThanShort(t *testing.T) {
	t.Run("a blocker with no repository", func(t *testing.T) {
		st := mustParse(t, `{"data":{"repository":{"issue":{
          "number":46,"repository":{"nameWithOwner":"acme/lab"},
          "blockedBy":{"pageInfo":{"hasNextPage":false},"nodes":[
            {"number":9,"state":"OPEN","repository":{"nameWithOwner":"acme/harness"}},
            {"number":0,"state":"","repository":null}]},
          "subIssuesSummary":{"total":0}}}}}`)
		if st.BlockersComplete {
			t.Error("an unresolvable blocker was dropped and the list still called itself complete")
		}
	})

	t.Run("a child with no repository", func(t *testing.T) {
		st := mustParse(t, `{"data":{"repository":{"issue":{
          "number":46,"repository":{"nameWithOwner":"acme/lab"},
          "subIssuesSummary":{"total":2},
          "subIssues":{"pageInfo":{"hasNextPage":false},"nodes":[
            {"number":282,"state":"OPEN","repository":{"nameWithOwner":"acme/doctrine"},
             "subIssuesSummary":{"total":0}},
            {"number":0,"state":"","repository":null}]}}}}}`)
		if !st.MoreChildren {
			t.Error("an unnameable child was dropped and the list still called itself complete")
		}
	})
}
