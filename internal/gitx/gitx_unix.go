//go:build !windows

package gitx

import "os/exec"

// configureCancel leaves cmd.Cancel unset on this platform, so a killed
// context falls back to the exec package's default: cmd.Process.Kill() on
// the direct child. See gitx_windows.go for why the two platforms differ.
func configureCancel(cmd *exec.Cmd) {}
