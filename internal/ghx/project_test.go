package ghx

import "testing"

// A recorded shape of `gh project item-list --format json`. The keys for board
// fields are named after the fields themselves, so this JSON's shape belongs to
// whoever set the board up rather than to facet -- which is exactly why it is
// pinned here.
const itemListSample = `{"items":[
  {"id":"i1","status":"Done","content":{"type":"Issue","number":121,
     "repository":"https://github.com/acme/harness","title":"a"}},
  {"id":"i2","status":"In progress","content":{"type":"Issue","number":72,
     "repository":"https://github.com/acme/lab","title":"b"}},
  {"id":"i3","content":{"type":"Issue","number":9,
     "repository":"https://github.com/acme/lab","title":"no status set"}},
  {"id":"i4","status":"Done","content":{"type":"DraftIssue","title":"a draft"}},
  {"id":"i5","status":"Done","content":{"type":"PullRequest","number":5,
     "repository":"https://github.com/acme/lab","title":"a pr"}}
]}`

func TestParseProjectStatuses(t *testing.T) {
	got, err := parseProjectStatuses([]byte(itemListSample), "Status")
	if err != nil {
		t.Fatalf("err = %v", err)
	}

	want := map[string]string{
		"acme/harness#121": "Done",
		"acme/lab#72":      "In progress",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries %v, want %d", len(got), got, len(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("status[%s] = %q, want %q", k, got[k], v)
		}
	}

	// A draft carries no issue and a pull request is not a node in an issue
	// tree; counting either would inflate a commission's progress with things
	// that are not its work.
	if _, ok := got["acme/lab#5"]; ok {
		t.Error("a pull request was counted as an issue")
	}
	// An item on the board with the field unset is not "Done" and not
	// "In progress" -- it must be absent, so the caller can say so.
	if _, ok := got["acme/lab#9"]; ok {
		t.Error("an item with no status was given one")
	}
}

// "status" is what a human types; "Status" is what the board calls the field.
// resolveOption already matches names case-insensitively for the write path,
// so the read path has to agree or the two disagree about the same board.
func TestParseProjectStatusesMatchesFieldNameCaseInsensitively(t *testing.T) {
	got, err := parseProjectStatuses([]byte(itemListSample), "status")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got["acme/lab#72"] != "In progress" {
		t.Errorf("lowercase field name found %d entries, want the same as %q", len(got), "Status")
	}
}

func TestItemIssueKeyHandlesRepoURLs(t *testing.T) {
	item := map[string]any{"content": map[string]any{
		"type": "Issue", "number": float64(46),
		"repository": "https://github.com/acme/lab/",
	}}
	// A trailing slash is the difference between "acme/lab" and "lab/" --
	// silently producing the wrong key, which then matches nothing in the tree.
	if key, ok := itemIssueKey(item); !ok || key != "acme/lab#46" {
		t.Errorf("itemIssueKey = %q, %v; want acme/lab#46, true", key, ok)
	}
}
