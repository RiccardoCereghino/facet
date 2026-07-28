//go:build windows

package gitx

import (
	"os/exec"
	"strconv"
)

// configureCancel makes a context-killed git process take its whole process
// tree down with it. exec.Cmd's default Cancel only kills the direct child;
// a remote helper git spawns for `fetch`/`clone` (ssh, git-remote-https,
// git-remote-ext) inherits that child's console and is not itself killed.
// On Unix an orphan like that is harmless to callers (a killed process's
// open files can still be unlinked), but Windows locks a file for as long as
// any process holds it open, so an orphan surviving past RunTimeout's return
// can block a caller's own cleanup of the same directory. `taskkill /T`
// walks and kills the whole tree; this package already shells out to real
// tools rather than reimplement OS process-tree semantics in Go.
func configureCancel(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		return exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	}
}
