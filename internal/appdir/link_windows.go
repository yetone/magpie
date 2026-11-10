//go:build windows

package appdir

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

// dirLink makes link a link to the folder target. Windows lets a user make
// a symlink only with Developer Mode on or as an administrator (#973);
// without that it is a junction, which every user can make and every
// program follows as it does a symlink to a folder. A junction can't point
// at a network share: that is still the symlink's error.
func dirLink(target, link string) error {
	err := symlink(target, link)
	if err == nil || !errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
		return err
	}
	if jerr := Junction(target, link); jerr != nil {
		return err
	}
	return nil
}

// symlink is os.Symlink; a test has it refused as without Developer Mode.
var symlink = os.Symlink

// Junction makes link a junction to the folder target: an empty folder
// given a mount-point reparse point. Nothing is left at link when it fails.
func Junction(target, link string) error {
	abs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	if v := filepath.VolumeName(abs); len(v) != 2 || v[1] != ':' {
		return errors.New("a junction needs a local drive path: " + abs)
	}
	if err := os.Mkdir(link, 0o755); err != nil {
		return err
	}
	if err := setMountPoint(link, abs); err != nil {
		os.Remove(link)
		return err
	}
	return nil
}

func setMountPoint(dir, target string) error {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	sub := utf16.Encode([]rune(`\??\` + target))
	shown := utf16.Encode([]rune(target))
	// REPARSE_DATA_BUFFER, MountPointReparseBuffer: both names end in a NUL
	path := make([]uint16, 0, len(sub)+len(shown)+2)
	path = append(append(append(append(path, sub...), 0), shown...), 0)
	data := make([]byte, 8+2*len(path))
	binary.LittleEndian.PutUint16(data[0:], 0)
	binary.LittleEndian.PutUint16(data[2:], uint16(2*len(sub)))
	binary.LittleEndian.PutUint16(data[4:], uint16(2*(len(sub)+1)))
	binary.LittleEndian.PutUint16(data[6:], uint16(2*len(shown)))
	for i, c := range path {
		binary.LittleEndian.PutUint16(data[8+2*i:], c)
	}
	buf := make([]byte, 8+len(data))
	binary.LittleEndian.PutUint32(buf[0:], windows.IO_REPARSE_TAG_MOUNT_POINT)
	binary.LittleEndian.PutUint16(buf[4:], uint16(len(data)))
	copy(buf[8:], data)
	var n uint32
	return windows.DeviceIoControl(h, windows.FSCTL_SET_REPARSE_POINT, &buf[0], uint32(len(buf)), nil, 0, &n, nil)
}
