// Package ghx wraps the `gh` CLI.
//
// facet shells out to gh rather than talking to the GitHub API directly, so it
// inherits gh's existing authentication -- including the keyring, multiple
// logged-in accounts, and enterprise hosts -- without ever handling a token.
// Every call names its repository explicitly, because more than one account may
// be logged in and the "current" repo is not always the one meant.
package ghx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// Label is a GitHub issue label.
type Label struct {
	Name string `json:"name"`
}

// User is a GitHub account.
type User struct {
	Login string `json:"login"`
}

// Issue is the subset of a GitHub issue facet needs.
type Issue struct {
	Number    int     `json:"number"`
	Title     string  `json:"title"`
	Body      string  `json:"body"`
	URL       string  `json:"url"`
	State     string  `json:"state"`
	Labels    []Label `json:"labels"`
	Assignees []User  `json:"assignees"`
}

// LabelNames returns the issue's labels as plain strings.
func (i *Issue) LabelNames() []string {
	out := make([]string, 0, len(i.Labels))
	for _, l := range i.Labels {
		out = append(out, l.Name)
	}
	return out
}

// IsOpen reports whether the issue is open.
func (i *Issue) IsOpen() bool { return strings.EqualFold(i.State, "OPEN") }

// PR is the subset of a pull request facet needs to decide whether work landed.
type PR struct {
	Number   int    `json:"number"`
	State    string `json:"state"` // OPEN | MERGED | CLOSED
	MergedAt string `json:"mergedAt"`
	URL      string `json:"url"`
}

// ProjectTarget names a Projects v2 board and the single-select field value to
// put an issue in. Everything is named, never an opaque node ID: the IDs GitHub
// wants (PVT_…, PVTSSF_…, and an eight-hex-digit option) are stable but
// unreadable, and would rot silently in a config file. They are resolved from
// these names on each call.
//
// A GitHub issue has no "in progress" state -- it is open or closed. Status is a
// field on the board item, so setting it means finding, and if need be creating,
// that item.
type ProjectTarget struct {
	Owner  string // the org or user that owns the board
	Number int    // the board's number, as in /orgs/<owner>/projects/<number>
	Field  string // a single-select field's name, e.g. "Status"
	Option string // one of that field's option names, e.g. "In progress"
}

// Client is the GitHub surface facet uses. It is an interface so the spawn and
// reap logic can be tested without touching the network.
type Client interface {
	// ViewIssue fetches one issue from repo ("owner/name").
	ViewIssue(repo string, number int) (*Issue, error)
	// DevelopBranch creates a branch on the forge linked to the issue, and
	// returns its name. gh links exactly one repository to an issue.
	DevelopBranch(repo string, number int, base, name string) (string, error)
	// BranchesFor lists branches already linked to the issue.
	BranchesFor(repo string, number int) ([]string, error)
	// ViewPR finds the pull request for a head branch, if any.
	ViewPR(repo, branch string) (*PR, error)
	// SetIssueStatus puts the issue on target's board, if it is not already
	// there, and sets target's field to target's option. issueURL is the issue's
	// html_url, which is what `gh project item-add` takes.
	SetIssueStatus(target ProjectTarget, issueURL string) error
	// SetIssueBody replaces the issue's body.
	SetIssueBody(repo string, number int, body string) error
	// SearchIssues finds issues in repo whose title matches terms, open or
	// closed. Used to catch a duplicate before one is filed.
	SearchIssues(repo, terms string) ([]Issue, error)
	// CreateIssue files one and returns its URL.
	CreateIssue(repo, title, body string, labels []string) (string, error)
}

// CLI is the real client, backed by the gh binary.
type CLI struct{}

var _ Client = CLI{}

func run(args ...string) ([]byte, error) { return runStdin(nil, args...) }

// runStdin runs gh with stdin wired to in, which is how anything long or
// arbitrary -- an issue body -- reaches gh. Passing it as an argument would hit
// the command-line length limit and force us to quote someone's markdown.
func runStdin(in []byte, args ...string) ([]byte, error) {
	cmd := exec.Command("gh", args...)
	if in != nil {
		cmd.Stdin = bytes.NewReader(in)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("gh %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.Bytes(), nil
}

// ViewIssue fetches one issue.
func (CLI) ViewIssue(repo string, number int) (*Issue, error) {
	out, err := run("issue", "view", fmt.Sprint(number), "--repo", repo,
		"--json", "number,title,body,url,state,labels,assignees")
	if err != nil {
		return nil, err
	}
	var iss Issue
	if err := json.Unmarshal(out, &iss); err != nil {
		return nil, fmt.Errorf("parse issue %s#%d: %w", repo, number, err)
	}
	return &iss, nil
}

// DevelopBranch creates an issue-linked branch and returns its name. If the
// branch already exists, gh reports it and we treat that as success.
func (CLI) DevelopBranch(repo string, number int, base, name string) (string, error) {
	args := []string{"issue", "develop", fmt.Sprint(number), "--repo", repo, "--name", name}
	if base != "" {
		args = append(args, "--base", base)
	}
	if _, err := run(args...); err != nil {
		if !strings.Contains(err.Error(), "already exists") {
			return "", err
		}
	}
	return name, nil
}

// BranchesFor lists branches linked to the issue.
func (CLI) BranchesFor(repo string, number int) ([]string, error) {
	out, err := run("issue", "develop", "--list", fmt.Sprint(number), "--repo", repo)
	if err != nil {
		return nil, err
	}
	var branches []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			// `gh issue develop --list` prints "<branch>\t<url>".
			branches = append(branches, strings.TrimSpace(strings.SplitN(line, "\t", 2)[0]))
		}
	}
	return branches, nil
}

// SetIssueBody replaces the issue's body, reading it from stdin so that neither
// its length nor its markdown has to survive a command line.
//
// This edits someone's issue. Call it only with a body you produced by rewriting
// the one you just read, and only when it actually differs.
func (CLI) SetIssueBody(repo string, number int, body string) error {
	_, err := runStdin([]byte(body), "issue", "edit", fmt.Sprint(number),
		"--repo", repo, "--body-file", "-")
	return err
}

// SearchIssues finds issues in repo whose TITLE matches terms, open or closed.
//
// Scoped to the title on purpose: a body-wide search on a platform monorepo
// matches half the backlog, and a duplicate check nobody trusts is a duplicate
// check nobody runs. Closed issues count -- refiling something we decided
// against is the expensive kind of duplicate.
func (CLI) SearchIssues(repo, terms string) ([]Issue, error) {
	if strings.TrimSpace(terms) == "" {
		return nil, nil
	}
	out, err := run("issue", "list", "--repo", repo, "--state", "all", "--limit", "10",
		"--search", "in:title "+terms, "--json", "number,title,url,state")
	if err != nil {
		return nil, err
	}
	var found []Issue
	if err := json.Unmarshal(out, &found); err != nil {
		return nil, fmt.Errorf("parse search results: %w", err)
	}
	return found, nil
}

// CreateIssue files an issue and returns its URL. The body goes through stdin,
// for the same reason SetIssueBody's does.
func (CLI) CreateIssue(repo, title, body string, labels []string) (string, error) {
	args := []string{"issue", "create", "--repo", repo, "--title", title, "--body-file", "-"}
	for _, l := range labels {
		args = append(args, "--label", l)
	}
	out, err := runStdin([]byte(body), args...)
	if err != nil {
		return "", err
	}
	// gh prints the URL on the last non-empty line.
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(lines[len(lines)-1]), nil
}

// SetIssueStatus adds the issue to the board and sets one single-select field.
//
// `gh project item-add` is idempotent: an issue already on the board comes back
// with the item ID it already had, and nothing is duplicated. So there is no
// need to list the board first, and no race between checking and adding.
func (CLI) SetIssueStatus(t ProjectTarget, issueURL string) error {
	num := fmt.Sprint(t.Number)

	var proj struct {
		ID string `json:"id"`
	}
	out, err := run("project", "view", num, "--owner", t.Owner, "--format", "json")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(out, &proj); err != nil {
		return fmt.Errorf("parse project %s/%s: %w", t.Owner, num, err)
	}

	fieldID, optionID, err := resolveOption(t)
	if err != nil {
		return err
	}

	var item struct {
		ID string `json:"id"`
	}
	out, err = run("project", "item-add", num, "--owner", t.Owner, "--url", issueURL, "--format", "json")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(out, &item); err != nil {
		return fmt.Errorf("parse project item for %s: %w", issueURL, err)
	}

	_, err = run("project", "item-edit", "--id", item.ID, "--project-id", proj.ID,
		"--field-id", fieldID, "--single-select-option-id", optionID)
	return err
}

// resolveOption turns the field and option *names* in t into the node IDs the
// API wants. Names are matched case-insensitively: "in progress" is what a human
// types, "In progress" is what the board calls it.
func resolveOption(t ProjectTarget) (fieldID, optionID string, err error) {
	out, err := run("project", "field-list", fmt.Sprint(t.Number), "--owner", t.Owner,
		"--limit", "100", "--format", "json")
	if err != nil {
		return "", "", err
	}
	var fields struct {
		Fields []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Type    string `json:"type"`
			Options []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"options"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(out, &fields); err != nil {
		return "", "", fmt.Errorf("parse fields of project %s/%d: %w", t.Owner, t.Number, err)
	}
	for _, f := range fields.Fields {
		if !strings.EqualFold(f.Name, t.Field) {
			continue
		}
		if f.Type != "ProjectV2SingleSelectField" {
			return "", "", fmt.Errorf("project %s/%d: field %q is a %s, not a single-select",
				t.Owner, t.Number, f.Name, f.Type)
		}
		for _, o := range f.Options {
			if strings.EqualFold(o.Name, t.Option) {
				return f.ID, o.ID, nil
			}
		}
		names := make([]string, 0, len(f.Options))
		for _, o := range f.Options {
			names = append(names, o.Name)
		}
		return "", "", fmt.Errorf("project %s/%d: field %q has no option %q; it has: %s",
			t.Owner, t.Number, f.Name, t.Option, strings.Join(names, ", "))
	}
	return "", "", fmt.Errorf("project %s/%d has no field %q", t.Owner, t.Number, t.Field)
}

// ViewPR finds the PR whose head is branch. A missing PR is (nil, nil).
func (CLI) ViewPR(repo, branch string) (*PR, error) {
	out, err := run("pr", "view", branch, "--repo", repo, "--json", "number,state,mergedAt,url")
	if err != nil {
		if strings.Contains(err.Error(), "no pull requests found") ||
			strings.Contains(err.Error(), "Could not resolve") {
			return nil, nil
		}
		return nil, err
	}
	var pr PR
	if err := json.Unmarshal(out, &pr); err != nil {
		return nil, err
	}
	return &pr, nil
}
