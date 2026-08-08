package ghx

import (
	"strings"
	"testing"
)

func refOf(owner, repo string, n int) IssueRef {
	return IssueRef{Owner: owner, Repo: repo, Number: n}
}

// !! THE COST GATE. !! GitHub bills a query on the nodes it COULD return, not
// the ones it does, against 5000 points an hour -- and a walk that goes over
// does not fail, it SHORTENS. Measured: with 15 points left, a walk printed 49
// of 160 nodes, exited zero and wrote nothing to stderr.
//
// So the budget is a correctness property, and this test is the only thing
// that can check it without spending it. The numbers below come from a real
// tree; changing a page size in batch.go changes them, which is the point.
func TestAWalkStaysWellUnderTheHourlyBudget(t *testing.T) {
	const (
		hourlyBudget = 5000
		// caryatidNodes is the real tree this was built against, measured.
		caryatidNodes = 160
		// ticksPerHour is what the console's derivation loop runs at, and it
		// is the number the budget has to divide by -- not once an hour.
		ticksPerHour = 12
	)

	perWalk := BilledPoints(caryatidNodes)
	perHour := perWalk * ticksPerHour

	t.Logf("a %d-node walk bills %d possible nodes = %d points; %d ticks/hour = %d of %d",
		caryatidNodes, BilledNodes(caryatidNodes), perWalk, ticksPerHour, perHour, hourlyBudget)

	if perHour > hourlyBudget {
		t.Fatalf("a %d-node walk costs %d points, so %d an hour is %d against a budget of %d -- "+
			"the derivation would run out and return a SHORT TREE, silently",
			caryatidNodes, perWalk, ticksPerHour, perHour, hourlyBudget)
	}

	// And leave room for everything else on the same account: the interactive
	// console's synchronous walks, and every seat's own gh usage. Half the
	// budget for the loop is the line.
	if perHour > hourlyBudget/2 {
		t.Errorf("the loop alone would take %d of %d points an hour, leaving too little "+
			"for the console and for the seats sharing this account", perHour, hourlyBudget)
	}
}

// The shape that made the first attempt unaffordable, kept as arithmetic so
// nobody re-derives it the expensive way. Nesting MULTIPLIES page sizes down
// the levels; aliases ADD.
func TestNestingIsWhatCostsAndBatchingIsWhatDoesNot(t *testing.T) {
	// A query nested four rungs at first:25 with five labels and five
	// blockers, which is what a single-query design looks like.
	nested := 0
	reach := 1
	for rung := 1; rung <= 3; rung++ {
		reach *= 25
		nested += reach + reach*5 + reach*5
	}
	nestedPoints := nested / 100

	batchedPoints := BilledPoints(160)
	if nestedPoints <= batchedPoints {
		t.Fatalf("the arithmetic no longer shows nesting as the expensive shape: "+
			"nested=%d batched=%d", nestedPoints, batchedPoints)
	}
	t.Logf("one nested query bills ~%d points; the whole batched walk bills %d",
		nestedPoints, batchedPoints)
}

// A label list cut short does not fail -- it changes the level a node is
// assigned, because the level comes from a type/* label that may be the one
// that fell off the end. Real nodes here carry seven.
func TestTheLabelPageIsBigEnoughToBeComplete(t *testing.T) {
	const observedMostLabelsOnOneIssue = 7
	if batchLabels <= observedMostLabelsOnOneIssue {
		t.Fatalf("labels are read %d at a time and a real issue carries %d -- "+
			"a truncated label list silently changes a node's level",
			batchLabels, observedMostLabelsOnOneIssue)
	}
}

func TestBatchQueryReadsEveryRefAndMetersItself(t *testing.T) {
	refs := []IssueRef{
		refOf("acme", "lab", 46),
		refOf("acme", "doctrine", 282),
	}
	q, err := batchQuery(refs)
	if err != nil {
		t.Fatalf("batchQuery: %v", err)
	}
	for _, want := range []string{
		`n0: repository(owner: "acme", name: "lab") { issue(number: 46)`,
		`n1: repository(owner: "acme", name: "doctrine") { issue(number: 282)`,
		"rateLimit { cost remaining limit }",
		"fragment F on Issue",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("the query is missing %q:\n%s", want, q)
		}
	}
	// A child selection must NOT ask for the child's labels: nesting a
	// connection inside a connection multiplies the bill by the page size,
	// for fields that arrive anyway when that child is read in its own right.
	childSel := q[strings.Index(q, "subIssues(first:"):]
	if strings.Contains(childSel[:strings.Index(childSel, "}")+1], "labels") {
		t.Error("the child selection asks for labels, which multiplies the bill by the child page size")
	}
}

// The one place a value is written into the query text rather than passed as a
// variable. GraphQL has no variables for aliases or for an aliased selection's
// repository arguments, so anything that could close a string and open a new
// selection has to be impossible rather than unlikely.
func TestBatchQueryRefusesARefItCannotSafelyWrite(t *testing.T) {
	for name, bad := range map[string]IssueRef{
		"a quote in the owner":  refOf(`acme") { x } y: repository(owner: "evil`, "lab", 1),
		"a quote in the repo":   refOf("acme", `lab") { x`, 1),
		"a brace in the owner":  refOf("acme{", "lab", 1),
		"an empty owner":        refOf("", "lab", 1),
		"a zero issue number":   refOf("acme", "lab", 0),
		"a negative issue":      refOf("acme", "lab", -3),
		"a newline in the repo": refOf("acme", "lab\nx", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := batchQuery([]IssueRef{bad}); err == nil {
				t.Fatalf("built a query for %+v instead of refusing", bad)
			}
		})
	}
}

func TestParseBatchMapsAnswersBackByAlias(t *testing.T) {
	refs := []IssueRef{
		refOf("acme", "lab", 46),
		refOf("acme", "doctrine", 282),
		refOf("acme", "harness", 999),
	}
	const payload = `{"data":{
      "n0":{"issue":{"number":46,"title":"commission 1","state":"OPEN",
        "repository":{"nameWithOwner":"acme/lab"},
        "labels":{"pageInfo":{"hasNextPage":false},"nodes":[{"name":"type/commission"}]},
        "blockedBy":{"pageInfo":{"hasNextPage":false},"nodes":[
          {"number":9,"state":"CLOSED","repository":{"nameWithOwner":"acme/harness"}}]},
        "subIssues":{"pageInfo":{"hasNextPage":false},"nodes":[
          {"number":282,"repository":{"nameWithOwner":"acme/doctrine"}}]}}},
      "n1":{"issue":{"number":282,"title":"seat: c1","state":"OPEN",
        "repository":{"nameWithOwner":"acme/doctrine"},
        "labels":{"pageInfo":{"hasNextPage":false},"nodes":[]},
        "blockedBy":{"pageInfo":{"hasNextPage":false},"nodes":[]},
        "subIssues":{"pageInfo":{"hasNextPage":false},"nodes":[]}}},
      "n2":{"issue":null}}}`

	got, err := parseBatch([]byte(payload), refs)
	if err != nil {
		t.Fatalf("parseBatch: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d answers for 3 refs", len(got))
	}
	// A null entry must stay IN POSITION. Dropping it shifts every answer
	// after it onto the wrong issue, which is a wrong answer rather than a
	// missing one.
	if got[2].Ref != refs[2] || !got[2].Unreadable {
		t.Fatalf("the unreadable entry did not hold its place: %+v", got[2])
	}
	if got[0].Title != "commission 1" || len(got[0].Children) != 1 ||
		got[0].Children[0].String() != "acme/doctrine#282" {
		t.Fatalf("n0 = %+v", got[0])
	}
	if len(got[0].BlockedBy) != 1 || got[0].BlockedBy[0].State != "CLOSED" {
		t.Fatalf("blockers did not carry their state: %+v", got[0].BlockedBy)
	}
	if !got[0].LabelsComplete || !got[0].ChildrenComplete || !got[0].BlockersComplete {
		t.Errorf("a complete answer reported itself short: %+v", got[0])
	}
}

func TestParseBatchReportsEveryShortReadAsShort(t *testing.T) {
	refs := []IssueRef{refOf("acme", "lab", 46)}

	t.Run("a paged connection", func(t *testing.T) {
		got, _ := parseBatch([]byte(`{"data":{"n0":{"issue":{
          "number":46,"repository":{"nameWithOwner":"acme/lab"},
          "labels":{"pageInfo":{"hasNextPage":true},"nodes":[{"name":"a"}]},
          "blockedBy":{"pageInfo":{"hasNextPage":true},"nodes":[]},
          "subIssues":{"pageInfo":{"hasNextPage":true},"nodes":[]}}}}}`), refs)
		f := got[0]
		if f.LabelsComplete || f.BlockersComplete || f.ChildrenComplete {
			t.Fatalf("a paged connection reported itself complete: %+v", f)
		}
	})

	// An entry that came back unnameable must not be DROPPED: losing a blocker
	// reads as "nothing blocks this", and losing a child reads as "no more
	// children". Both are the answer that lets work through.
	t.Run("an entry with no repository", func(t *testing.T) {
		got, _ := parseBatch([]byte(`{"data":{"n0":{"issue":{
          "number":46,"repository":{"nameWithOwner":"acme/lab"},
          "labels":{"pageInfo":{"hasNextPage":false},"nodes":[]},
          "blockedBy":{"pageInfo":{"hasNextPage":false},"nodes":[
            {"number":9,"state":"OPEN","repository":null}]},
          "subIssues":{"pageInfo":{"hasNextPage":false},"nodes":[
            {"number":7,"repository":null}]}}}}}`), refs)
		f := got[0]
		if f.BlockersComplete {
			t.Error("an unnameable blocker was dropped and the list still called itself complete")
		}
		if f.ChildrenComplete {
			t.Error("an unnameable child was dropped and the list still called itself complete")
		}
	})
}

// A PARTIAL RESPONSE IS AN ANSWER. gh exits non-zero whenever the payload
// carries an errors array, and one request covering fifty issues would
// otherwise turn a single unreadable one into fifty failures.
func TestParseBatchKeepsWhatResolvedAlongsideErrors(t *testing.T) {
	refs := []IssueRef{refOf("acme", "lab", 46), refOf("acme", "lab", 47)}
	got, err := parseBatch([]byte(`{"errors":[{"message":"Could not resolve to an Issue"}],
      "data":{"n0":{"issue":{"number":46,"title":"kept","state":"OPEN",
        "repository":{"nameWithOwner":"acme/lab"},
        "labels":{"pageInfo":{"hasNextPage":false},"nodes":[]},
        "blockedBy":{"pageInfo":{"hasNextPage":false},"nodes":[]},
        "subIssues":{"pageInfo":{"hasNextPage":false},"nodes":[]}}},
      "n1":null}}`), refs)
	if err != nil {
		t.Fatalf("parseBatch: %v", err)
	}
	if got[0].Title != "kept" || got[0].Unreadable {
		t.Fatalf("the resolved half was discarded: %+v", got[0])
	}
	if !got[1].Unreadable {
		t.Fatalf("the unresolved half was not marked: %+v", got[1])
	}
}

func TestParseBatchReportsNothingUsableAsNothing(t *testing.T) {
	refs := []IssueRef{refOf("acme", "lab", 46)}
	for name, payload := range map[string]string{
		"errors only": `{"errors":[{"message":"API rate limit already exceeded"}]}`,
		"empty body":  ``,
		"no data key": `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := parseBatch([]byte(payload), refs)
			if err != nil {
				t.Fatalf("errored instead of answering nothing: %v", err)
			}
			if got != nil {
				t.Fatalf("got answers from %s: %+v", name, got)
			}
		})
	}
}
