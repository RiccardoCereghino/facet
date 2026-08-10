package ghx

import "testing"

func TestParseRepoLabels(t *testing.T) {
	t.Run("one page", func(t *testing.T) {
		got, err := parseRepoLabels([]byte(
			`[{"name":"type/work","color":"fbca04","description":"the work"},
			  {"name":"bug","color":"d73a4a"}]`))
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(got) != 2 || got[0].Name != "type/work" || got[0].Color != "fbca04" {
			t.Fatalf("got %+v", got)
		}
		if got[0].Description != "the work" {
			t.Errorf("description = %q, want it carried: a created label copies it", got[0].Description)
		}
	})

	// THE TRAP THIS WHOLE READ EXISTS TO AVOID. `gh api --paginate`
	// concatenates one array per page rather than merging them, so a
	// repository with more than a page of labels comes back as [...][...] --
	// which a single Unmarshal rejects, and which a naive "read the first
	// array" would silently truncate. A truncated label list makes a defined
	// label read as MISSING, in the command written to find missing ones.
	t.Run("two concatenated pages", func(t *testing.T) {
		got, err := parseRepoLabels([]byte(
			`[{"name":"a","color":"111111"}][{"name":"b","color":"222222"}]`))
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(got) != 2 || got[1].Name != "b" {
			t.Fatalf("got %+v, want both pages", got)
		}
	})

	t.Run("a repository with no labels", func(t *testing.T) {
		got, err := parseRepoLabels([]byte(`[]`))
		if err != nil || len(got) != 0 {
			t.Fatalf("got %+v, err %v", got, err)
		}
	})
}
