//go:build windows

package ghx

import "io/fs"

// notApplicableWindows is what the preflight prints on Windows in place of a
// permission verdict. It is not an error and not a pass -- it is the statement
// that this half of the check did not run, which the caller is required to
// surface.
//
// The wording matters. A user reading a green preflight must not come away
// believing their key permissions were verified. That is the whole difference
// between a documented platform carve-out and a lie.
const notApplicableWindows = "ssh key permissions were NOT checked on this platform. " +
	"Go's os.FileMode does not represent NTFS ACLs, so a mode test here would " +
	"pass or fail for reasons unrelated to who can actually read the key. " +
	"Win32-OpenSSH enforces an ACL rule of its own -- verify it yourself with " +
	"`icacls %USERPROFILE%\\.ssh\\id_ed25519`, which should list your account " +
	"and nothing else. Implementing this properly needs DACL enumeration via " +
	"golang.org/x/sys/windows -- proposed, not yet done."

// keyPermission declines to judge on Windows, and says so.
//
// The alternative shapes were considered and rejected: deleting the check, or
// loosening the assertion until a synthesised mode passes everywhere, would
// each trade a real check for a green tick on every platform, including the
// ones where the check works. The existence half above is platform-neutral and
// still runs here.
func keyPermission(string, fs.FileMode) (*Problem, string) {
	return nil, notApplicableWindows
}
