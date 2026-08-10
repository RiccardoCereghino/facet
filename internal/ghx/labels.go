package ghx

import (
	"encoding/json"
	"fmt"
	"strings"
)

// RepoLabel is a label as a REPOSITORY defines it, rather than as an issue
// carries it.
//
// [Label] is the issue-side view and is only ever a name. This is the
// definition -- the thing that has to exist before any issue can be given it,
// and the thing that is missing when `--add-label` answers "not found".
type RepoLabel struct {
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description"`
}

// RepoLabels lists every label DEFINED in repo.
//
// It goes through `gh api --paginate` rather than `gh label list`, and the
// reason is the failure this whole area is about: `gh label list` defaults to
// **30** labels and reports no truncation, so a repository with more would have
// its later labels read as absent -- a parity check that invents a gap, in the
// command written to find real ones. `--paginate` reads all of them.
func (CLI) RepoLabels(repo string) ([]RepoLabel, error) {
	if _, _, err := splitRepo("RepoLabels", repo); err != nil {
		return nil, err
	}
	out, err := run("api", "--paginate",
		fmt.Sprintf("repos/%s/labels?per_page=%d", repo, restPerPage))
	if err != nil {
		return nil, err
	}
	labels, err := parseRepoLabels(out)
	if err != nil {
		return nil, fmt.Errorf("parse labels of %s: %w", repo, err)
	}
	return labels, nil
}

// parseRepoLabels decodes what `gh api --paginate` returns for a label list.
//
// --paginate CONCATENATES one JSON array per page rather than merging them, so
// a repository with more than one page of labels comes back as `[...][...]`,
// which json.Unmarshal will not accept. Same decoder loop, and the same
// reasoning, as parseDependencyEdges.
func parseRepoLabels(out []byte) ([]RepoLabel, error) {
	dec := json.NewDecoder(strings.NewReader(string(out)))
	var all []RepoLabel
	for dec.More() {
		var page []RepoLabel
		if err := dec.Decode(&page); err != nil {
			return nil, err
		}
		all = append(all, page...)
	}
	return all, nil
}

// CreateLabel defines a label in repo.
//
// It refuses rather than updates when the label already exists -- `gh label
// create --force` would overwrite someone's colour and description, and this is
// only ever called to close a gap that has already been established by reading.
// An existing label is therefore evidence that the read was wrong, which is
// worth an error rather than a silent edit.
func (CLI) CreateLabel(repo string, l RepoLabel) error {
	if _, _, err := splitRepo("CreateLabel", repo); err != nil {
		return err
	}
	args := []string{"label", "create", l.Name, "--repo", repo}
	if l.Color != "" {
		args = append(args, "--color", l.Color)
	}
	if l.Description != "" {
		args = append(args, "--description", l.Description)
	}
	_, err := run(args...)
	return err
}
