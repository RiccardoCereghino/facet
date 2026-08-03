package ghx

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ProjectStatuses reads one single-select field for every item on a board and
// returns it keyed by "owner/repo#n".
//
// A GitHub issue is open or closed and nothing else -- there is no "in
// progress" anywhere in the issues API. That state exists only as a field on a
// Projects v2 item, which is why deriving how far a tree has got requires a
// board at all.
//
// It is a bulk read on purpose: answering "how many of this commission's
// descendants are done" per-issue would be one call per node, and the whole
// point is to answer it for a tree at once.
//
// The field is matched case-insensitively and read out of a generic decode
// rather than a typed one, because gh renders a board's custom fields as keys
// named after the fields themselves -- so the shape of this JSON depends on
// how the board was set up, not on anything facet controls.
func (CLI) ProjectStatuses(owner string, projectNumber int, field string) (map[string]string, error) {
	out, err := run("project", "item-list", fmt.Sprint(projectNumber),
		"--owner", owner, "--limit", "1000", "--format", "json")
	if err != nil {
		return nil, err
	}

	statuses, err := parseProjectStatuses(out, field)
	if err != nil {
		return nil, fmt.Errorf("parse items of project %s/%d: %w", owner, projectNumber, err)
	}
	return statuses, nil
}

// parseProjectStatuses pulls one named field out of `gh project item-list
// --format json`. Split out because the shape it reads is not facet's to
// control -- gh names each key after the board's own field -- so it is worth
// pinning against a recorded sample rather than trusting the shape.
func parseProjectStatuses(out []byte, field string) (map[string]string, error) {
	var resp struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, err
	}

	statuses := make(map[string]string, len(resp.Items))
	for _, item := range resp.Items {
		key, ok := itemIssueKey(item)
		if !ok {
			// A draft item, or a pull request: neither is an issue in a tree.
			continue
		}
		for k, v := range item {
			if !strings.EqualFold(k, field) {
				continue
			}
			if s, ok := v.(string); ok && s != "" {
				statuses[key] = s
			}
		}
	}
	return statuses, nil
}

// itemIssueKey renders a board item's content as "owner/repo#n", or reports
// that the item is not an issue. gh gives the repository as a URL rather than
// a full name, so the owner and repo are the last two path segments.
func itemIssueKey(item map[string]any) (string, bool) {
	content, ok := item["content"].(map[string]any)
	if !ok {
		return "", false
	}
	if t, ok := content["type"].(string); ok && !strings.EqualFold(t, "Issue") {
		return "", false
	}
	num, ok := content["number"].(float64)
	if !ok {
		return "", false
	}
	repoURL, ok := content["repository"].(string)
	if !ok {
		return "", false
	}
	parts := strings.Split(strings.TrimSuffix(repoURL, "/"), "/")
	if len(parts) < 2 {
		return "", false
	}
	owner, name := parts[len(parts)-2], parts[len(parts)-1]
	return fmt.Sprintf("%s/%s#%d", owner, name, int(num)), true
}
