package main

import (
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
)

// futureSkew is how far past "now" a timestamp may be before facet date --check
// refuses it. Real clock skew and the time between reading the clock and
// running the check are both sub-second in practice; this is generous enough
// to absorb that without being generous enough to hide the bug this command
// exists to catch -- a watermark misread as 74 minutes in the future.
const futureSkew = 5 * time.Second

// newDateCmd returns `facet date`, the fleet's one canonical timestamp source
// (facet#71). Like newVersionCmd and newPreflightCmd it overrides the root's
// config-loading pre-run with a no-op: a timestamp must be available anywhere,
// with no workspaces root and no routing.json.
func newDateCmd() *cobra.Command {
	var (
		local bool
		check string
	)
	cmd := &cobra.Command{
		Use:   "date",
		Short: "Print the current time, or check a timestamp against it",
		Long: "Emits RFC 3339 in UTC by default -- the format GitHub's API returns and\n" +
			"compares against, so the output can be used directly in a jq comparison\n" +
			"against createdAt/mergedAt with no conversion.\n\n" +
			"--local renders the current time with its local offset instead, for a human\n" +
			"reading it. Every rendering this command produces carries an explicit UTC or\n" +
			"offset marker -- never a bare local time with no offset, which is the exact\n" +
			"shape that turned a two-minute gap into a reported two hours, and later left a\n" +
			"watch dead for 74 minutes because its watermark was silently in the future.\n\n" +
			"--check <timestamp> answers that second failure directly: it refuses (and\n" +
			"reports by how much) a timestamp that is unexpectedly still ahead of now.",
		Args:              cobra.NoArgs,
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		RunE: func(cmd *cobra.Command, _ []string) error {
			now := time.Now()
			if cmd.Flags().Changed("check") {
				// An empty --check must refuse, not silently fall through to
				// printing the current time: that is the exact defect class
				// this command exists to eliminate. A watch calling
				// `facet date --check "$WATERMARK"` with WATERMARK unset or
				// blank from a failed substitution would otherwise see a
				// well-formed timestamp and a zero exit -- the same false
				// all-clear that left a watch dead for 74 minutes, reproduced
				// inside the tool meant to catch it (facet!79, F1).
				if check == "" {
					return fmt.Errorf("--check was given an empty timestamp -- refusing rather than silently not checking")
				}
				return runDateCheck(cmd.OutOrStdout(), now, check)
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), formatNow(now, local))
			return err
		},
	}
	cmd.Flags().BoolVar(&local, "local", false, "render with the local offset instead of UTC")
	cmd.Flags().StringVar(&check, "check", "", "an RFC 3339 timestamp to check against now, instead of printing one")
	return cmd
}

// formatNow renders now as RFC 3339, in UTC unless local is set. RFC3339's
// layout always includes an explicit offset (Z for UTC, +hh:mm otherwise), so
// neither branch can ever produce a bare local time with no offset.
func formatNow(now time.Time, local bool) string {
	if local {
		return now.Format(time.RFC3339)
	}
	return now.UTC().Format(time.RFC3339)
}

// runDateCheck parses ts and compares it against now, refusing (a non-nil
// error, meant to set a non-zero exit code) when ts is more than futureSkew
// ahead of now -- the shape that left a watch dead for 74 minutes because
// nothing ever refused the watermark that caused it.
func runDateCheck(w io.Writer, now time.Time, ts string) error {
	parsed, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return fmt.Errorf("not an RFC 3339 timestamp: %w", err)
	}
	ahead := parsed.Sub(now)
	if ahead > futureSkew {
		return fmt.Errorf("%s is %s in the future (now is %s) -- refusing a timestamp this far ahead of now",
			ts, ahead.Round(time.Second), formatNow(now, false))
	}
	// A timestamp inside the tolerance window can still be nanoseconds to
	// futureSkew *ahead* of now, and reporting that as a negative amount "in
	// the past" is a sentence about the direction of time that is wrong on
	// its face -- in the one command whose subject is not misreading the
	// direction of time (facet!79, F2). Report each direction honestly.
	if ahead > 0 {
		_, err = fmt.Fprintf(w, "%s is %s in the future (now is %s), within the %s skew tolerance\n",
			ts, ahead.Round(time.Millisecond), formatNow(now, false), futureSkew)
		return err
	}
	_, err = fmt.Fprintf(w, "%s is %s in the past (now is %s)\n", ts, (-ahead).Round(time.Second), formatNow(now, false))
	return err
}
