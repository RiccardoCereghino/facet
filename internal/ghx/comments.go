package ghx

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Comment is one issue comment. ID is the comment's own database id, which is
// what editing takes -- it is not the issue number and not the comment's
// position in the thread, so "the third comment" is not addressable.
type Comment struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	URL       string `json:"html_url"`
	User      User   `json:"user"`
}

// Edited reports whether the comment has been changed since it was posted.
// Worth surfacing wherever a comment is read as a decision: an edited plan is
// not the plan that was agreed to.
func (c Comment) Edited() bool { return c.UpdatedAt != "" && c.UpdatedAt != c.CreatedAt }

// IssueComments lists an issue's comments oldest-first, which is the order the
// REST API returns and the order that makes "the last one" meaningful.
//
// Paginated: an issue that has accumulated a hundred comments is exactly the
// issue whose latest plan someone is trying to find, so a silent first-page
// answer would be wrong precisely where it matters most.
func (CLI) IssueComments(repo string, number int) ([]Comment, error) {
	out, err := run("api", "--paginate",
		fmt.Sprintf("repos/%s/issues/%d/comments", repo, number))
	if err != nil {
		return nil, err
	}
	all, err := parseComments(out)
	if err != nil {
		return nil, fmt.Errorf("parse comments of %s#%d: %w", repo, number, err)
	}
	return all, nil
}

// parseComments decodes `gh api --paginate` output: one JSON array per page,
// concatenated rather than merged, so `[...][...]` for anything past the first
// page. See parseDependencyEdges for why this is a stream decode.
func parseComments(out []byte) ([]Comment, error) {
	dec := json.NewDecoder(strings.NewReader(string(out)))
	var all []Comment
	for dec.More() {
		var page []Comment
		if err := dec.Decode(&page); err != nil {
			return nil, err
		}
		all = append(all, page...)
	}
	return all, nil
}

// PostComment adds a comment and returns its URL.
//
// The body goes over stdin as JSON rather than in an argument: comments carry
// arbitrary markdown, and an argument would hit the command-line length limit
// on anything substantial and force us to quote someone's backticks.
func (CLI) PostComment(repo string, number int, body string) (string, error) {
	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return "", err
	}
	out, err := runStdin(payload, "api",
		fmt.Sprintf("repos/%s/issues/%d/comments", repo, number),
		"-X", "POST", "--input", "-", "--jq", ".html_url")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// EditComment replaces a comment's body. commentID is the id from
// [Comment.ID].
//
// GitHub permits editing only comments the credential owns, and refuses others
// with a 403 -- so this cannot rewrite someone else's words, and the refusal is
// the API's rather than something facet has to enforce.
func (CLI) EditComment(repo string, commentID int64, body string) (string, error) {
	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return "", err
	}
	out, err := runStdin(payload, "api",
		fmt.Sprintf("repos/%s/issues/comments/%d", repo, commentID),
		"-X", "PATCH", "--input", "-", "--jq", ".html_url")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
