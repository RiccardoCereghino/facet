package main

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/RiccardoCereghino/facet/internal/ghx"
	"github.com/RiccardoCereghino/facet/internal/routing"
)

// commentGH is the narrow surface these commands need.
type commentGH interface {
	IssueComments(repo string, number int) ([]ghx.Comment, error)
	PostComment(repo string, number int, body string) (string, error)
	EditComment(repo string, commentID int64, body string) (string, error)
}

// newCommentCmd groups reading and writing issue comments.
//
// The point of it is `last --kind plan`: where a decision is revised by
// posting it again, the newest one is the one that counts, and finding it by
// eye in a long thread is how the wrong revision gets acted on.
//
// facet does not know what a "plan" is. Kinds are named regexps in the routing
// file, exactly as the label rules already are -- so this works for whatever
// an adopter's comments happen to look like, and for nothing by default.
func newCommentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "comment",
		Short: "Read and write issue comments",
		Long: "Comments, filtered by kind.\n\n" +
			"A kind is a named regexp in the routing file's `commentKinds` block --\n" +
			"facet knows that some comments have kinds, never which ones. Without that\n" +
			"block --kind refuses and --grep still works.",
	}
	cmd.AddCommand(newCommentListCmd(), newCommentLastCmd(),
		newCommentPostCmd(), newCommentEditCmd())
	return cmd
}

func newCommentListCmd() *cobra.Command {
	var kind, grep string
	cmd := &cobra.Command{
		Use:   "list <owner/repo#n>",
		Short: "List an issue's comments, oldest first",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := parseIssueRef(args[0])
			if err != nil {
				return err
			}
			return runCommentList(cmd.OutOrStdout(), gh, ref, kind, grep, false)
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "only comments of this declared kind")
	cmd.Flags().StringVar(&grep, "grep", "", "only comments whose body matches this regexp")
	return cmd
}

func newCommentLastCmd() *cobra.Command {
	var kind, grep string
	cmd := &cobra.Command{
		Use:   "last <owner/repo#n>",
		Short: "Print the most recent matching comment in full",
		Long: "The newest comment that matches, printed whole.\n\n" +
			"Where a decision is revised by posting it again, this is the one that\n" +
			"counts. It reports how many matched, so a thread carrying one plan and a\n" +
			"thread carrying five do not look the same.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := parseIssueRef(args[0])
			if err != nil {
				return err
			}
			return runCommentList(cmd.OutOrStdout(), gh, ref, kind, grep, true)
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "only comments of this declared kind")
	cmd.Flags().StringVar(&grep, "grep", "", "only comments whose body matches this regexp")
	return cmd
}

func newCommentPostCmd() *cobra.Command {
	var bodyFile string
	cmd := &cobra.Command{
		Use:   "post <owner/repo#n> --body-file <path>",
		Short: "Add a comment",
		Long: "The body comes from a file, never an argument. Backticks in a shell\n" +
			"argument run as command substitution and silently eat the text around\n" +
			"them, which has mangled filed issues more than once.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := parseIssueRef(args[0])
			if err != nil {
				return err
			}
			body, err := readBodyFile(bodyFile)
			if err != nil {
				return err
			}
			return runCommentPost(cmd.OutOrStdout(), gh, ref, body)
		},
	}
	cmd.Flags().StringVar(&bodyFile, "body-file", "", "file holding the comment body; - for stdin")
	return cmd
}

func newCommentEditCmd() *cobra.Command {
	var bodyFile string
	var id int64
	cmd := &cobra.Command{
		Use:   "edit <owner/repo#n> --id <comment-id> --body-file <path>",
		Short: "Replace a comment's body",
		Long: "--id is the COMMENT's own id, from `facet comment list` -- not the issue\n" +
			"number and not the comment's position in the thread.\n\n" +
			"GitHub permits editing only comments this credential wrote, and refuses\n" +
			"the rest, so this cannot rewrite anyone else's words.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := parseIssueRef(args[0])
			if err != nil {
				return err
			}
			if id == 0 {
				return fmt.Errorf("--id is required: the comment's own id, which `facet comment list` prints")
			}
			body, err := readBodyFile(bodyFile)
			if err != nil {
				return err
			}
			return runCommentEdit(cmd.OutOrStdout(), gh, ref, id, body)
		},
	}
	cmd.Flags().Int64Var(&id, "id", 0, "the comment's id, from `facet comment list`")
	cmd.Flags().StringVar(&bodyFile, "body-file", "", "file holding the new body; - for stdin")
	return cmd
}

func readBodyFile(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("--body-file is required\nfix: write the body to a file and pass it, or pass - for stdin")
	}
	var b []byte
	var err error
	if path == "-" {
		b, err = io.ReadAll(os.Stdin)
	} else {
		b, err = os.ReadFile(path)
	}
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(b)) == "" {
		return "", fmt.Errorf("%s is empty: a comment with no body is a note to nobody", path)
	}
	return string(b), nil
}

// matcher builds the filter from --kind and --grep. They compose: a kind and a
// pattern both apply.
func matcher(route *routing.Routing, kind, grep string) (func(ghx.Comment) bool, error) {
	var res []*regexp.Regexp
	if kind != "" {
		re, err := route.CommentKind(kind)
		if err != nil {
			return nil, err
		}
		res = append(res, re)
	}
	if grep != "" {
		re, err := regexp.Compile(grep)
		if err != nil {
			return nil, fmt.Errorf("--grep %q: %w", grep, err)
		}
		res = append(res, re)
	}
	return func(c ghx.Comment) bool {
		for _, re := range res {
			if !re.MatchString(c.Body) {
				return false
			}
		}
		return true
	}, nil
}

func runCommentList(w io.Writer, gh commentGH, ref ghx.IssueRef, kind, grep string, lastOnly bool) error {
	route, err := loadRouting()
	if err != nil {
		return err
	}
	match, err := matcher(route, kind, grep)
	if err != nil {
		return err
	}
	all, err := gh.IssueComments(ref.OwnerRepo(), ref.Number)
	if err != nil {
		return err
	}

	var hits []ghx.Comment
	for _, c := range all {
		if match(c) {
			hits = append(hits, c)
		}
	}

	if len(hits) == 0 {
		// Say what was searched. "No plan on this issue" and "no comment kinds
		// configured" must not print the same thing.
		_, _ = fmt.Fprintf(w, "no matching comments on %s (%d comments searched%s)\n",
			ref, len(all), describeFilter(kind, grep))
		return nil
	}

	if lastOnly {
		c := hits[len(hits)-1]
		_, _ = fmt.Fprintf(w, "%s  comment %d of %d matching%s\n",
			ref, len(hits), len(hits), describeFilter(kind, grep))
		_, _ = fmt.Fprintf(w, "id %d  posted %s%s\n", c.ID, c.CreatedAt, editedNote(c))
		_, _ = fmt.Fprintf(w, "%s\n%s\n", strings.Repeat("-", 60), c.Body)
		return nil
	}

	for _, c := range hits {
		_, _ = fmt.Fprintf(w, "%d\t%s%s\t%s\n", c.ID, c.CreatedAt, editedNote(c), firstLine(c.Body))
	}
	return nil
}

// editedNote surfaces an edit wherever a comment is read as a decision: an
// edited plan is not the plan that was agreed to.
func editedNote(c ghx.Comment) string {
	if c.Edited() {
		return " (edited " + c.UpdatedAt + ")"
	}
	return ""
}

func describeFilter(kind, grep string) string {
	var parts []string
	if kind != "" {
		parts = append(parts, "kind "+kind)
	}
	if grep != "" {
		parts = append(parts, "grep "+grep)
	}
	if len(parts) == 0 {
		return ""
	}
	return "; " + strings.Join(parts, ", ")
}

func firstLine(body string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(body), "\n")
	return truncate(line, 70)
}

func runCommentPost(w io.Writer, gh commentGH, ref ghx.IssueRef, body string) error {
	url, err := gh.PostComment(ref.OwnerRepo(), ref.Number, body)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(w, url)
	return nil
}

func runCommentEdit(w io.Writer, gh commentGH, ref ghx.IssueRef, id int64, body string) error {
	url, err := gh.EditComment(ref.OwnerRepo(), id, body)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(w, url)
	return nil
}
