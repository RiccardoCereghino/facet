//go:build windows

package fslink

import (
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	ioReparseTagMountPoint = 0xA0000003 // a directory junction
	fsctlSetReparsePoint   = 0x000900A4
)

// Kind names what this platform's links are, for user-facing output.
const Kind = "junction"

// Create makes link a directory junction pointing at target. Junctions need no
// privilege and no Developer Mode, unlike os.Symlink on Windows.
func Create(link, target string) error {
	target, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	if err := os.Mkdir(link, 0o777); err != nil {
		return err
	}
	if err := setMountPoint(link, target); err != nil {
		os.Remove(link)
		return err
	}
	return nil
}

func setMountPoint(link, target string) error {
	linkPtr, err := windows.UTF16PtrFromString(link)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(linkPtr, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", link, err)
	}
	defer windows.CloseHandle(h)

	// SubstituteName is the NT-namespace path the kernel follows; PrintName is
	// the display form. Both are NUL-terminated inside the path buffer.
	subst, err := windows.UTF16FromString(`\??\` + target)
	if err != nil {
		return err
	}
	print16, err := windows.UTF16FromString(target)
	if err != nil {
		return err
	}
	substBytes := (len(subst) - 1) * 2 // excluding the NUL
	printBytes := (len(print16) - 1) * 2
	pathBytes := (len(subst) + len(print16)) * 2

	// REPARSE_DATA_BUFFER: 8-byte header, 8 bytes of MountPointReparseBuffer
	// fields, then subst\0 print\0.
	buf := make([]byte, 8+8+pathBytes)
	putU32 := func(off int, v uint32) { *(*uint32)(unsafe.Pointer(&buf[off])) = v }
	putU16 := func(off int, v uint16) { *(*uint16)(unsafe.Pointer(&buf[off])) = v }

	putU32(0, ioReparseTagMountPoint)
	putU16(4, uint16(8+pathBytes)) // ReparseDataLength: everything after the header
	putU16(6, 0)                   // Reserved
	putU16(8, 0)                   // SubstituteNameOffset
	putU16(10, uint16(substBytes)) // SubstituteNameLength
	putU16(12, uint16(substBytes+2))
	putU16(14, uint16(printBytes))
	for i, c := range subst {
		putU16(16+i*2, c)
	}
	for i, c := range print16 {
		putU16(16+len(subst)*2+i*2, c)
	}

	var ret uint32
	if err := windows.DeviceIoControl(h, fsctlSetReparsePoint, &buf[0], uint32(len(buf)), nil, 0, &ret, nil); err != nil {
		return fmt.Errorf("set reparse point on %s: %w", link, err)
	}
	return nil
}

// reparseTag returns path's reparse tag, or 0 when it is not a reparse point.
func reparseTag(path string) (uint32, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var fd windows.Win32finddata
	h, err := windows.FindFirstFile(p, &fd)
	if err != nil {
		return 0, err
	}
	windows.FindClose(h)
	if fd.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0 {
		return 0, nil
	}
	return fd.Reserved0, nil // holds the reparse tag on reparse points
}

// IsLink reports whether path is a directory junction. A plain directory, a
// symlink, or any other reparse point is not one.
func IsLink(path string) (bool, error) {
	tag, err := reparseTag(path)
	if err != nil {
		return false, err
	}
	return tag == ioReparseTagMountPoint, nil
}

// Read returns the junction's target. ok is false when path is not a junction.
func Read(path string) (target string, ok bool, err error) {
	isJunction, err := IsLink(path)
	if err != nil || !isJunction {
		return "", false, nil // a missing or non-junction path is not an error here
	}
	target, err = os.Readlink(path)
	if err != nil {
		return "", false, err
	}
	return target, true, nil
}
