//go:build !windows

package fslink

import (
	"os"
	"path/filepath"
)

// Kind names what this platform's links are, for user-facing output.
const Kind = "symlink"

// Create makes link a symlink pointing at target.
func Create(link, target string) error {
	target, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	return os.Symlink(target, link)
}

// IsLink reports whether path is a symlink.
func IsLink(path string) (bool, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return fi.Mode()&os.ModeSymlink != 0, nil
}

// Read returns the symlink's target. ok is false when path is not a symlink.
func Read(path string) (target string, ok bool, err error) {
	isLink, err := IsLink(path)
	if err != nil || !isLink {
		return "", false, nil
	}
	target, err = os.Readlink(path)
	if err != nil {
		return "", false, err
	}
	return target, true, nil
}
