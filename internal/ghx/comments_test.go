package ghx

import "testing"

func TestParseComments(t *testing.T) {
	t.Run("one page, oldest first", func(t *testing.T) {
		out := []byte(`[
		  {"id":1,"body":"## Plan","created_at":"2026-08-03T10:00:00Z","updated_at":"2026-08-03T10:00:00Z"},
		  {"id":2,"body":"## Plan -- revision 2","created_at":"2026-08-03T11:00:00Z","updated_at":"2026-08-03T11:00:00Z"}
		]`)
		got, err := parseComments(out)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d comments, want 2", len(got))
		}
		// "the last plan is the one that goes in" only means anything if the
		// order is the order they were posted in.
		if got[0].ID != 1 || got[1].ID != 2 {
			t.Errorf("order = %d, %d; want 1, 2 (oldest first)", got[0].ID, got[1].ID)
		}
	})

	// The issue with a hundred comments is precisely the one whose latest plan
	// someone is hunting for, so a first-page-only answer would be wrong exactly
	// where it is most needed.
	t.Run("two concatenated pages", func(t *testing.T) {
		out := []byte(`[{"id":1,"body":"a"}][{"id":2,"body":"b"}]`)
		got, err := parseComments(out)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d comments, want 2 -- the second page was dropped", len(got))
		}
	})
}

func TestCommentEdited(t *testing.T) {
	unedited := Comment{CreatedAt: "2026-08-03T10:00:00Z", UpdatedAt: "2026-08-03T10:00:00Z"}
	edited := Comment{CreatedAt: "2026-08-03T10:00:00Z", UpdatedAt: "2026-08-03T12:00:00Z"}
	if unedited.Edited() {
		t.Error("an untouched comment reported as edited")
	}
	if !edited.Edited() {
		t.Error("an edited comment reported as untouched: an edited plan is not the plan that was agreed")
	}
	// A comment with no timestamps at all must not claim to have been edited.
	if (Comment{}).Edited() {
		t.Error("a zero comment reported as edited")
	}
}
