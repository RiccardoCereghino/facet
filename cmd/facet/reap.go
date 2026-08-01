package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/RiccardoCereghino/facet/internal/config"
	"github.com/RiccardoCereghino/facet/internal/mux"
	"github.com/RiccardoCereghino/facet/internal/routing"
	"github.com/RiccardoCereghino/facet/internal/workspace"
	"github.com/spf13/cobra"
)

func newIssuesCmd() *cobra.Command {
	var offline bool
	cmd := &cobra.Command{
		Use:   "issues",
		Short: "List every ephemeral issue workspace and whether it can be reaped",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
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
			_, _ = fmt.Fprintln(w, "WORKSPACE\tISSUE\tBRANCH\tSTATE\tPR\tSIZE")
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
				_, _ = fmt.Fprintf(w, "%s\t%s#%d\t%s\t%s\t%s\t%s\n",
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
			"request, a live multiplexer session, or a tmux pane or process still rooted\n" +
			"in the workspace.\n\n" +
			"The shared mirror is never touched: a clone's objects are hardlinks, so\n" +
			"deleting the workspace drops those names and leaves the mirror's own intact.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			ws, err := reapTarget(path, args)
			if err != nil {
				return err
			}
			st, err := workspace.InspectIssue(ws, git, gh, muxLive())
			if err != nil {
				return err
			}
			fmt.Printf("%s  (%s#%d, %s)\n", st.Name, st.Issue.Repo, st.Issue.Number, humanBytes(st.SizeBytes))
			for _, n := range st.Notes() {
				fmt.Println(n)
			}

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
			// Kill any lingering session first. A live session holds file
			// handles on the working tree, which on some platforms would
			// refuse the removeAll below.
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

// reapTarget resolves the workspace to reap from --path and an optional
// positional argument, which name the same thing.
//
// `facet reap <name>` used to answer `unknown command "<name>" for "facet reap"`:
// the command took --path only, and cobra.NoArgs rejected the positional. That is
// the first shape every operator reaches for, and the error names the wrong
// problem -- it reads as "reap is broken", not as "reap wants a flag". It has cost
// time more than once and is documented as a trap in prose, which is where a CLI
// puts things it has decided not to fix (facet#83).
//
// A positional is taken as a path, and falls back to <workspaces root>/<name>
// when it is a bare name and no such path exists: a workspace is referred to by
// name far more often than by path, and that fallback is what makes
// `facet reap iss-73` mean what it looks like it means.
//
// Both forms at once is an error, never a precedence rule. Two arguments that
// disagree about which directory to delete must not have a silent winner.
func reapTarget(path string, args []string) (string, error) {
	if len(args) == 0 {
		return config.ResolveWorkspace(path)
	}
	name := args[0]
	if path != "" {
		return "", fmt.Errorf("name the workspace once: --path %s and %q disagree; drop one", path, name)
	}
	if _, err := os.Stat(name); err != nil && !strings.ContainsRune(name, filepath.Separator) {
		if byName := filepath.Join(roots.Workspaces, name); byName != name {
			if _, err := os.Stat(byName); err == nil {
				return config.ResolveWorkspace(byName)
			}
		}
	}
	return config.ResolveWorkspace(name)
}

func newAttachCmd() *cobra.Command {
	var (
		path       string
		ownSession bool
		switchTo   bool
		agent      agentFlags
	)
	cmd := &cobra.Command{
		Use:   "attach",
		Short: "Open, or rejoin, an issue workspace in the multiplexer",
		Long: "Inside a tmux session this adds the workspace as a new window, because\n" +
			"sessions do not nest and attaching from within one would seize this client.\n" +
			"It does this even when the workspace already has a session of its own:\n" +
			"being moved out of the session you are typing in is never a default. Pass\n" +
			"--switch to be moved.\n\n" +
			"Outside tmux it attaches to the workspace's own session, creating it from\n" +
			"scratch if needed.\n\n" +
			"The pane runs claude with Remote Control, so the session is reachable off\n" +
			"this host, and its URL is printed here once the pane has it. --remote=false\n" +
			"runs claude without it; --claude=false leaves a plain login shell. Setting\n" +
			"FACET_AGENT overrides all of that with its own command.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if ownSession && switchTo {
				return fmt.Errorf("--session and --switch are opposites: one makes a session, the other joins one")
			}
			ws, err := config.ResolveWorkspace(path)
			if err != nil {
				return err
			}
			st, err := workspace.InspectIssue(ws, git, nil, nil)
			if err != nil {
				return err
			}
			_, asTab := mux.AutoOpen(muxFor(""), ownSession)
			a := agent.resolve(cmd).agentOpts
			// The session-name prefix is a property of the host, not of
			// spawning, so an attached pane wants it too. Routing is not
			// required to open a workspace, so a failure to read it costs
			// the prefix and nothing else.
			if route, err := routing.Load(roots.Routing); err == nil {
				a.SessionNamePrefix = route.SpawnSessionPrefix()
			}
			// `facet attach` means "show me this workspace" -- not "move
			// me". The window it adds is focused, because you asked to go
			// there; --switch is what moves the whole client to the
			// workspace's own session.
			return openSession(ws, st.Name, st.Issue.Home, st.Issue.Number, openOpts{
				AsTab: asTab, Focus: true, Switch: switchTo, Agent: a,
			})
		},
	}
	cmd.Flags().StringVar(&path, "path", "", "issue workspace (default: working directory)")
	cmd.Flags().BoolVar(&ownSession, "session", false, "open in a session of its own instead of a window (must not already be inside tmux)")
	cmd.Flags().BoolVar(&switchTo, "switch", false, "move this client to the workspace's own tmux session, when it has one")
	agent.register(cmd)
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
