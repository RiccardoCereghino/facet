//go:build !windows

package ghx

import (
	"fmt"
	"io/fs"
)

// keyPermission checks that the private key is not readable by group or other.
// This is the real check: on Unix the mode bits are what ssh itself enforces,
// and ssh refuses to use a key others can read.
//
// It never returns a not-applicable reason: on this platform the check applies.
func keyPermission(path string, mode fs.FileMode) (*Problem, string) {
	if mode&0o077 != 0 {
		return &Problem{
			Check: "ssh key permissions",
			Want:  "0600 at " + path,
			Got:   fmt.Sprintf("%#o", mode),
			Why:   sshKeyWhy + " ssh refuses to use a key others can read.",
		}, ""
	}
	return nil, ""
}
