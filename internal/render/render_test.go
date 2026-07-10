package render

import (
	"strings"
	"testing"
)

func TestDemoteHeadings(t *testing.T) {
	tests := []struct {
		name, in, want string
		by             int
	}{
		{"h2 becomes h4", "## Task\ntext\n", "#### Task\ntext\n", 2},
		{"h1 becomes h3", "# Title\n", "### Title\n", 2},
		{"caps at h6", "###### deep\n", "###### deep\n", 2},
		{"h5 clamps to h6", "##### five\n", "###### five\n", 2},
		{"no headings untouched", "plain text\n- bullet\n", "plain text\n- bullet\n", 2},
		{"zero is identity", "## Task\n", "## Task\n", 0},
		{"hash without space is not a heading", "#hashtag\n", "#hashtag\n", 2},
		{
			// A YAML comment inside a fence is not a heading. Demoting it would
			// corrupt the snippet the issue is asking someone to apply.
			name: "fenced code is left alone",
			in:   "## Task\n\n```yaml\n# a comment\nspec:\n  x: 1\n```\n\n## Next\n",
			want: "#### Task\n\n```yaml\n# a comment\nspec:\n  x: 1\n```\n\n#### Next\n",
			by:   2,
		},
		{
			name: "tilde fences too",
			in:   "~~~\n# not a heading\n~~~\n## real\n",
			want: "~~~\n# not a heading\n~~~\n#### real\n",
			by:   2,
		},
		{
			name: "indented fence still counts",
			in:   "  ```\n# inside\n  ```\n## after\n",
			want: "  ```\n# inside\n  ```\n#### after\n",
			by:   2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DemoteHeadings(tt.in, tt.by); got != tt.want {
				t.Errorf("DemoteHeadings(%q, %d)\n got %q\nwant %q", tt.in, tt.by, got, tt.want)
			}
		})
	}
}

func TestSlug(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Rehearse a database restore: nothing has ever been restored", "rehearse-a-database-restore-nothing-has"},
		{"API: prod overlay", "api-prod-overlay"},
		{"  leading and trailing  ", "leading-and-trailing"},
		{"Punctuation!!! everywhere???", "punctuation-everywhere"},
		{"CAPS and 123 numbers", "caps-and-123-numbers"},
		{"", "issue"},
		{"!!!", "issue"},
		{"a", "a"},
	}
	for _, tt := range tests {
		if got := Slug(tt.in, 40); got != tt.want {
			t.Errorf("Slug(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSlugNeverEndsWithDash(t *testing.T) {
	for _, in := range []string{
		"exactly forty characters of title here ok",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa word",
		"a b c d e f g h i j k l m n o p q r s t u v w x y z",
	} {
		got := Slug(in, 40)
		if strings.HasPrefix(got, "-") || strings.HasSuffix(got, "-") {
			t.Errorf("Slug(%q) = %q; has a dangling dash", in, got)
		}
		if len(got) > 40 {
			t.Errorf("Slug(%q) = %q; %d chars, over the cap", in, got, len(got))
		}
	}
}

func TestWorkspaceName(t *testing.T) {
	if got := WorkspaceName("iss-", "platform", 67, "do-the-thing"); got != "iss-platform-67-do-the-thing" {
		t.Errorf("got %q", got)
	}
}

func TestSqueezeBlankLines(t *testing.T) {
	got := squeezeBlankLines("a\n\n\n\nb\n   \n\nc\n")
	want := "a\n\nb\n\nc\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
