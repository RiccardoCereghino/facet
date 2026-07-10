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
}

// CLI is the real client, backed by the gh binary.
type CLI struct{}

var _ Client = CLI{}

func run(args ...string) ([]byte, error) {
	cmd := exec.Command("gh", args...)
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
