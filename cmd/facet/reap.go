package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/RiccardoCereghino/facet/internal/config"
	"github.com/RiccardoCereghino/facet/internal/mux"
	"github.com/RiccardoCereghino/facet/internal/workspace"
	"github.com/spf13/cobra"
)

func newIssuesCmd() *cobra.Command {
	var offline bool
	cmd := &cobra.Command{
		Use:   "issues",
		Short: "List every ephemeral issue workspace and whether it can be reaped",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var pr workspace.PRLookup
			if !offline {
				pr = gh
			}
			states, err := workspace.ListIssues(roots, git, pr, muxLive())
			if err != nil {
				return err
			}
			if len(states) == 0 {
				fmt.Println("no issue workspaces")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "WORKSPACE\tISSUE\tBRANCH\tSTATE\tPR\tSIZE")
			for _, s := range states {
				state := "reapable"
				if b := s.Blockers(); len(b) > 0 {
					state = fmt.Sprintf("held (%d)", len(b))
				}
				pr := "-"
				if s.PR != nil {
					pr = fmt.Sprintf("#%d %s", s.PR.Number, strings.ToLower(s.PR.State))
				}
				branch := s.Issue.Branch
				if branch == "" {
					branch = "-"
				}
				fmt.Fprintf(w, "%s\t%s#%d\t%s\t%s\t%s\t%s\n",
					s.Name, s.Issue.Repo, s.Issue.Number, branch, state, pr, humanBytes(s.SizeBytes))
			}
			if err := w.Flush(); err != nil {
				return err
			}
			for _, s := range states {
				if b := s.Blockers(); len(b) > 0 {
					fmt.Printf("\n%s:\n", s.Name)
					for _, r := range b {
						fmt.Printf("  - %s\n", r)
					}
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&offline, "offline", false, "skip the pull-request lookup")
	return cmd
}

func newReapCmd() *cobra.Command {
	var (
		path  string
		force bool
		yes   bool
	)
	cmd := &cobra.Command{
		Use:   "reap",
		Short: "Delete an ephemeral issue workspace once its work has landed",
		Long: "Refuses while there are unpushed commits, uncommitted changes, an open pull\n" +
			"request, or a live multiplexer session.\n\n" +
			"The shared mirror is never touched: a clone's objects are hardlinks, so\n" +
			"deleting the workspace drops those names and leaves the mirror's own intact.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ws, err := config.ResolveWorkspace(path)
			if err != nil {
				return err
			}
			st, err := workspace.InspectIssue(ws, git, gh, muxLive())
			if err != nil {
				return err
			}
			fmt.Printf("%s  (%s#%d, %s)\n", st.Name, st.Issue.Repo, st.Issue.Number, humanBytes(st.SizeBytes))

			blockers := st.Blockers()
			if len(blockers) > 0 {
				fmt.Println("\nheld by:")
				for _, b := range blockers {
					fmt.Printf("  - %s\n", b)
				}
				if !force {
					return fmt.Errorf("refusing to reap; resolve the above, or pass --force to delete anyway")
				}
				fmt.Println("\n--force: deleting despite the above.")
			}
			if !yes && !confirm(fmt.Sprintf("Delete %s?", st.Dir)) {
				fmt.Println("aborted.")
				return nil
			}
			if l := mux.Pick(); l != nil {
				_ = l.Kill(st.Name)
			}
			if err := workspace.Reap(st); err != nil {
				return err
			}
			fmt.Printf("removed %s\n", st.Dir)
			fmt.Println("the shared mirror was not touched.")
			return nil
		},
	}
	cmd.Flags().StringVar(&path, "path", "", "issue workspace (default: working directory)")
	cmd.Flags().BoolVar(&force, "force", false, "delete even when work would be lost")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	return cmd
}

func newAttachCmd() *cobra.Command {
	var path string
	cmd := &cobra.Command{
		Use:   "attach",
		Short: "Open, or rejoin, an issue workspace's multiplexer session",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ws, err := config.ResolveWorkspace(path)
			if err != nil {
				return err
			}
			st, err := workspace.InspectIssue(ws, git, nil, nil)
			if err != nil {
				return err
			}
			return openSession(ws, st.Name, st.Issue.Home, st.Issue.Number, "")
		},
	}
	cmd.Flags().StringVar(&path, "path", "", "issue workspace (default: working directory)")
	return cmd
}

// muxLive returns a session checker, or nil when no multiplexer is available.
func muxLive() workspace.LiveChecker {
	if l := mux.Pick(); l != nil {
		return l
	}
	return nil
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.0f%c", float64(n)/float64(div), "KMGT"[exp])
}
