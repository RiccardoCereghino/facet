// Command facet manages task-scoped, manifest-declared workspaces over a set of
// git repositories, and spawns ephemeral ones from GitHub issues.
package main

import (
	"fmt"
	"os"
	"runtime/debug"

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

// version is set by the linker for release builds (-X main.version=v1.2.3). Left
// at "dev", it falls back to the module version stamped into the binary, which
// `go install ...@latest` fills in.
var version = "dev"

func buildVersion() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return version
}

func main() {
	root := &cobra.Command{
		Use:   "facet",
		Short: "Task-scoped workspaces over many repos, spawned from GitHub issues",
		Long: "facet manages workspaces: directories that assemble several git repositories\n" +
			"into one task-scoped view, declared in .workspace.json and rebuilt on demand.\n\n" +
			"A workspace entry is either a clone the workspace owns outright, or a link into\n" +
			"a shared repo. `facet spawn` creates a throwaway workspace for one GitHub issue.",
		Version:       buildVersion(),
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			var err error
			roots, err = config.Load()
			return err
		},
	}
	root.SetVersionTemplate("facet {{.Version}}\n")
	root.AddCommand(newLsCmd(), newSyncCmd(), newRestoreCmd(), newSpawnCmd(),
		newFileCmd(), newIssuesCmd(), newReapCmd(),
		newNewCmd(), newAddCmd(), newRmCmd(), newVersionCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "facet:", err)
		os.Exit(1)
	}
}

// newVersionCmd prints the build version. It overrides the root's config-loading
// pre-run with a no-op: reporting the version must work anywhere, even outside a
// configured workspaces root.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "version",
		Short:             "Print the facet version",
		Args:              cobra.NoArgs,
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		RunE: func(*cobra.Command, []string) error {
			fmt.Println("facet", buildVersion())
			return nil
		},
	}
}
