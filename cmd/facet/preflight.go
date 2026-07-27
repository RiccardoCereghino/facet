package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/RiccardoCereghino/facet/internal/ghx"
	"github.com/spf13/cobra"
)

// newPreflightCmd checks the credential surface the whole fleet shares.
//
// Like newVersionCmd it overrides the root's config-loading pre-run with a
// no-op. A credential check must work anywhere -- outside a workspaces root,
// with a broken routing.json, on a machine where nothing else is set up. The
// thing you reach for when the lab is wrong cannot require the lab to be right.
func newPreflightCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "preflight",
		Short: "Check the GitHub credential the fleet depends on",
		Long: "Reports whether this machine holds a usable GitHub credential: logged in and\n" +
			"active, the right account, a token type that cannot be rotated out from under\n" +
			"us, the scopes the fleet actually calls, git talking SSH, and the push key\n" +
			"present.\n\n" +
			"It reads `gh auth status` and nothing else. That is the point: gh reports a\n" +
			"credential as invalid WITHOUT a valid credential, so this check does not go\n" +
			"blind during the fault it exists to catch. No token value is ever read,\n" +
			"printed or stored, and this command never logs in, out, or refreshes.\n\n" +
			"There is no override flag. A gate with an escape hatch is not a gate.",
		Args:              cobra.NoArgs,
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPreflight(cmd.OutOrStdout())
		},
	}
}

// runPreflight prints one line per check and fails if any problem was found.
func runPreflight(w io.Writer) error {
	req := ghx.FleetRequirements()
	probs, st, err := preflight(req)
	if err != nil {
		return err
	}

	if st != nil && st.LoggedIn {
		fmt.Fprintf(w, "host        %s\n", orDash(st.Host))
		fmt.Fprintf(w, "account     %s\n", orDash(st.Account))
		fmt.Fprintf(w, "token type  %s (value never read)\n", orDash(st.TokenType))
		fmt.Fprintf(w, "scopes      %s\n", orDash(strings.Join(st.Scopes, ", ")))
		fmt.Fprintf(w, "protocol    %s\n", orDash(st.GitProtocol))
		fmt.Fprintf(w, "source      %s\n", orDash(st.ConfigSource))
		fmt.Fprintln(w)
	}

	if len(probs) == 0 {
		fmt.Fprintln(w, "✓ credential preflight passed")
		return nil
	}
	for _, p := range probs {
		fmt.Fprintf(w, "✗ %s\n", p)
	}
	fmt.Fprintln(w)
	return fmt.Errorf("credential preflight failed: %d %s", len(probs), plural(len(probs), "problem", "problems"))
}

// preflight runs every check and returns the problems alongside the status it
// read, so callers can report both.
func preflight(req ghx.Requirements) ([]ghx.Problem, *ghx.AuthStatus, error) {
	st, err := gh.Auth()
	if err != nil {
		return nil, nil, fmt.Errorf("read gh auth status: %w", err)
	}
	probs := ghx.Check(st, req)
	probs = append(probs, ghx.CheckSSHKey(req.SSHKey)...)
	return probs, st, nil
}

// requirePreflight is what commands that cannot work without a credential call
// before doing anything else. It fails loudly and names the command, so the
// refusal is legible at the point of use rather than three calls deeper.
func requirePreflight(w io.Writer, what string) error {
	probs, _, err := preflight(ghx.FleetRequirements())
	if err != nil {
		return err
	}
	if len(probs) == 0 {
		return nil
	}
	for _, p := range probs {
		fmt.Fprintf(w, "✗ %s\n", p)
	}
	fmt.Fprintln(w)
	return fmt.Errorf("%s refused: the GitHub credential is not sound (%d %s). "+
		"Run `facet preflight` for the full report. There is no skip flag: this is "+
		"exactly the failure that went unnoticed until it was mid-operation",
		what, len(probs), plural(len(probs), "problem", "problems"))
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
