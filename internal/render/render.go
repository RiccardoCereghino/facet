// Package render produces the CLAUDE.md that greets whoever -- human or agent --
// opens a spawned workspace.
package render

import (
	"bytes"
	"embed"
	"fmt"
	"regexp"
	"strings"
	"text/template"
	"time"

	"github.com/RiccardoCereghino/facet/internal/ghx"
	"github.com/RiccardoCereghino/facet/internal/knowledge"
	"github.com/RiccardoCereghino/facet/internal/routing"
)

//go:embed templates/*.tmpl
var templates embed.FS

// Selection is one repo row in the generated file.
type Selection struct {
	Key  string
	Dir  string
	Home bool
	Why  string
}

// Fragment wraps a knowledge fragment with its staleness, computed once.
type Fragment struct {
	knowledge.Fragment
	Stale bool
}

// IssueData is everything the issue template needs.
type IssueData struct {
	Workspace string
	Repo      string
	Issue     *ghx.Issue
	// Body is the issue body with its headings demoted, so they nest under the
	// generated document's own structure instead of colliding with it.
	Body           string
	Labels         []string
	Assignees      []string
	Branch         string
	HomeDir        string
	Selections     []Selection
	Hints          []routing.Hint
	Fragments      []Fragment
	FragmentErrors []string
}

// atxHeading matches a markdown heading line.
var atxHeading = regexp.MustCompile(`^(#{1,6})(\s+\S)`)

// fenceInfo parses a code-fence line into its marker byte and run length. ok is
// false when the line is not a fence: fewer than three leading ` or ~ after up
// to three spaces of indent. rest is whatever follows the run, used to reject a
// closing fence that carries an info string.
func fenceInfo(line string) (marker byte, runLen int, rest string, ok bool) {
	i := 0
	for i < len(line) && i < 3 && line[i] == ' ' {
		i++
	}
	if i >= len(line) || (line[i] != '`' && line[i] != '~') {
		return 0, 0, "", false
	}
	marker = line[i]
	j := i
	for j < len(line) && line[j] == marker {
		j++
	}
	if j-i < 3 {
		return 0, 0, "", false
	}
	return marker, j - i, line[j:], true
}

// DemoteHeadings pushes every heading in md down by n levels, so an issue body
// that opens with "## Task" does not collide with the surrounding document's own
// "## Task". Headings inside fenced code blocks are left alone -- a YAML comment
// is not a heading -- and nothing is pushed past h6.
//
// Fence tracking follows CommonMark: a block opened by ``` closes only on a
// backtick fence at least as long, and one opened by ~~~ only on a tilde fence.
// A mismatched marker inside a block is content, not a toggle -- otherwise a
// crafted issue body could desync the state and slip a heading out of (or into)
// a code block, letting untrusted text pose as one of this document's own
// sections.
func DemoteHeadings(md string, n int) string {
	if n <= 0 {
		return md
	}
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	var fenceChar byte
	var fenceLen int
	inFence := false
	for i, line := range lines {
		marker, runLen, rest, isFence := fenceInfo(line)
		if inFence {
			// Only a fence of the same marker, at least as long, with no trailing
			// info string closes the block.
			if isFence && marker == fenceChar && runLen >= fenceLen && strings.TrimSpace(rest) == "" {
				inFence = false
			}
			continue
		}
		if isFence {
			inFence, fenceChar, fenceLen = true, marker, runLen
			continue
		}
		m := atxHeading.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		level := len(m[1]) + n
		if level > 6 {
			level = 6
		}
		lines[i] = strings.Repeat("#", level) + line[len(m[1]):]
	}
	return strings.Join(lines, "\n")
}

// IssueClaudeMD renders the workspace's CLAUDE.md.
func IssueClaudeMD(d IssueData) ([]byte, error) {
	t, err := template.ParseFS(templates, "templates/issue.md.tmpl")
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, d); err != nil {
		return nil, err
	}
	// Collapse runs of blank lines the template's conditionals leave behind.
	out := squeezeBlankLines(buf.String())
	return []byte(out), nil
}

func squeezeBlankLines(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	var out []string
	blank := 0
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, strings.TrimRight(l, " \t"))
	}
	return strings.TrimLeft(strings.Join(out, "\n"), "\n")
}

// BuildIssueData assembles the template input from the pieces spawn has gathered.
func BuildIssueData(workspace, repo, branch, homeDir string, iss *ghx.Issue,
	sel []routing.Selection, hints []routing.Hint, r *routing.Routing,
	frags []knowledge.Fragment, fragErrs []error, now time.Time) IssueData {

	d := IssueData{
		Workspace: workspace, Repo: repo, Issue: iss,
		// The body lands under an h2; demote by two so its own h2s become h4s.
		Body:   DemoteHeadings(iss.Body, 2),
		Labels: iss.LabelNames(), Branch: branch, HomeDir: homeDir,
		Hints: hints,
	}
	for _, a := range iss.Assignees {
		d.Assignees = append(d.Assignees, a.Login)
	}
	for _, s := range sel {
		d.Selections = append(d.Selections, Selection{
			Key: s.Key, Dir: r.Repos[s.Key].Dir, Home: s.Home,
			Why: strings.Join(s.Reasons, "; "),
		})
	}
	for _, f := range frags {
		d.Fragments = append(d.Fragments, Fragment{Fragment: f, Stale: f.IsStale(now)})
	}
	for _, e := range fragErrs {
		d.FragmentErrors = append(d.FragmentErrors, e.Error())
	}
	return d
}

// Slug turns an issue title into a short, filesystem-safe branch component.
func Slug(title string, max int) string {
	var b strings.Builder
	lastDash := true // suppress a leading dash
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	s := strings.Trim(b.String(), "-")
	if max > 0 && len(s) > max {
		s = s[:max]
		// Do not end mid-word.
		if i := strings.LastIndexByte(s, '-'); i > max/2 {
			s = s[:i]
		}
		s = strings.Trim(s, "-")
	}
	if s == "" {
		s = "issue"
	}
	return s
}

// WorkspaceName is the directory name for an issue workspace.
func WorkspaceName(prefix, homeKey string, number int, slug string) string {
	return fmt.Sprintf("%s%s-%d-%s", prefix, homeKey, number, slug)
}
