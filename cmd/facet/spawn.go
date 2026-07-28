package main

import (
	"fmt"
	"strconv"

	"github.com/RiccardoCereghino/facet/internal/mux"
	"github.com/spf13/cobra"
)

func newSpawnCmd() *cobra.Command {
	var (
		repo        string
		clones      []string
		addClones   []string
		rmClones    []string
		slug        string
		base        string
		yes         bool
		noBranch    bool
		dryRun      bool
		attach      bool
		noAttach    bool
		ownSession  bool
		muxName     string
		noWriteback bool
		agent       agentFlags
	)
	cmd := &cobra.Command{
		Use:   "spawn <issue-number>",
		Short: "Create an ephemeral workspace for one GitHub issue",
		Long: "Reads the issue, works out which repositories it needs, shows you why, and\n" +
			"waits. On confirmation it creates an issue-linked branch, clones each repo\n" +
			"from the local mirror, and writes a CLAUDE.md carrying the issue body and the\n" +
			"durable hazards recorded for its areas.\n\n" +
			"Labels alone cannot decide the repo set: the same topic label is used in\n" +
			"several repos, and a cross-repo dependency lives in the issue body. So the\n" +
			"inference is always shown and never silently trusted.\n\n" +
			"It sets the workspace up and stops there, spawning no shell: it just prints\n" +
			"where to work. Opening a multiplexer and starting the agent are yours --\n" +
			"pass --attach to have facet open it (tmux or Windows Terminal).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			number, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("issue number: %w", err)
			}
			return runSpawn(spawnOpts{
				Number: number, Repo: repo, Clones: clones, Add: addClones, Remove: rmClones,
				Slug: slug, Base: base, Yes: yes, NoBranch: noBranch, DryRun: dryRun,
				Attach: attach, NoAttach: noAttach, OwnSession: ownSession, Mux: muxName,
				NoWriteback: noWriteback, Agent: agent.resolve(cmd),
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&repo, "repo", "", "the issue's home repository, as owner/name (required)")
	f.StringSliceVar(&clones, "clone", nil, "replace the inferred repo set entirely")
	f.StringSliceVar(&addClones, "add", nil, "add repos to the inferred set")
	f.StringSliceVar(&rmClones, "rm", nil, "drop repos from the inferred set")
	f.StringVar(&slug, "slug", "", "override the slug derived from the issue title")
	f.StringVar(&base, "base", "main", "base branch for the issue branch")
	f.BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	f.BoolVar(&noBranch, "no-branch", false, "do not create or check out an issue branch")
	f.BoolVar(&dryRun, "dry-run", false, "show the inference and exit, creating nothing")
	f.BoolVar(&attach, "attach", false, "open the workspace in a multiplexer after setup (default: spawn nothing, just print where to work)")
	f.BoolVar(&noAttach, "no-attach", false, "no-op; not opening is now the default")
	f.BoolVar(&ownSession, "session", false, "with --attach, open in a session of its own rather than as a window")
	f.StringVar(&muxName, "mux", "", "multiplexer to use: tmux, wt, or none")
	f.BoolVar(&noWriteback, "no-writeback", false, "do not record the confirmed repo set in the issue body")
	agent.register(cmd)
	return cmd
}

// muxFor resolves a launcher by name, or picks the best available.
func muxFor(name string) mux.Launcher {
	if name != "" {
		return mux.ByName(name)
	}
	return mux.Pick()
}

type spawnOpts struct {
	Number                       int
	Repo                         string
	Clones, Add, Remove          []string
	Slug, Base                   string
	Yes, NoBranch, DryRun        bool
	Attach, NoAttach, OwnSession bool
	Mux                          string
	// NoWriteback leaves the issue body alone. The confirmed repo set is then
	// re-inferred on every spawn.
	NoWriteback bool
	// Agent is what to run for the operator once the workspace is ready: in the
	// pane when --attach opened one, in this terminal otherwise.
	Agent agentChoice
}

func issueBranchName(number int, slug string) string {
	return fmt.Sprintf("feature/%d-%s", number, slug)
}
