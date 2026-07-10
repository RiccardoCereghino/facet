// Command facet manages task-scoped, manifest-declared workspaces over a set of
// git repositories, and spawns ephemeral ones from GitHub issues.
package main

import (
	"fmt"
	"os"

	"github.com/RiccardoCereghino/facet/internal/config"
	"github.com/RiccardoCereghino/facet/internal/ghx"
	"github.com/RiccardoCereghino/facet/internal/gitx"
	"github.com/spf13/cobra"
)

var (
	roots config.Roots
	git   gitx.Runner = gitx.Git{}
	gh    ghx.Client  = ghx.CLI{}
)

func main() {
	root := &cobra.Command{
		Use:   "facet",
		Short: "Task-scoped workspaces over many repos, spawned from GitHub issues",
		Long: "facet manages workspaces: directories that assemble several git repositories\n" +
			"into one task-scoped view, declared in .workspace.json and rebuilt on demand.\n\n" +
			"A workspace entry is either a clone the workspace owns outright, or a link into\n" +
			"a shared repo. `facet spawn` creates a throwaway workspace for one GitHub issue.",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			var err error
			roots, err = config.Load()
			return err
		},
	}
	root.AddCommand(newLsCmd(), newSyncCmd(), newRestoreCmd(), newSpawnCmd(),
		newFileCmd(), newIssuesCmd(), newReapCmd(), newAttachCmd(),
		newNewCmd(), newAddCmd(), newRmCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "facet:", err)
		os.Exit(1)
	}
}
