//go:build windows

package usage

import (
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// fileBasicInfo mirrors FILE_BASIC_INFO as returned by
// GetFileInformationByHandleEx(FileBasicInfo). All four times are FILETIME
// values (int64, 100ns since 1601) and FileAttributes is followed by explicit
// padding so the structure stays 40 bytes on every architecture.
type fileBasicInfo struct {
	CreationTime   int64
	LastAccessTime int64
	LastWriteTime  int64
	ChangeTime     int64
	FileAttributes uint32
	_              uint32
}

// statLogFile opens the log for attribute access only, allowing concurrent
// writers and rotation to rename or delete it while the handle is open. The
// returned FileInfo preserves the underlying os.FileInfo (inode/size/mtime
// semantics) and carries the change-time stamp.
func statLogFile(path string) (os.FileInfo, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(
		p,
		windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		// Go's opener handles long paths on Windows versions where the raw
		// API requires an extended path prefix. Keep the same-handle stamp.
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return statLogHandle(f)
	}
	f := os.NewFile(uintptr(h), path)
	if f == nil {
		windows.CloseHandle(h)
		return nil, os.ErrInvalid
	}
	info, err := statLogHandle(f)
	// The caller owns the snapshot FileInfo, not the handle. Close our own.
	f.Close()
	return info, err
}

// statLogHandle stamps the FileInfo reported by the caller's open handle. The
// same handle is used for both the portable stat and the change-time query so
// the two can never observe different files. A handle that does not support
// the attribute query yields the plain FileInfo with an empty stamp rather
// than a stat error, keeping the fingerprint fallback intact.
func statLogHandle(f *os.File) (os.FileInfo, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	return stampLogHandle(f, info), nil
}

func stampLogHandle(f *os.File, info os.FileInfo) os.FileInfo {
	if f == nil || info == nil {
		return info
	}
	var raw fileBasicInfo
	if err := windows.GetFileInformationByHandleEx(
		windows.Handle(f.Fd()),
		windows.FileBasicInfo,
		(*byte)(unsafe.Pointer(&raw)),
		uint32(unsafe.Sizeof(raw)),
	); err != nil {
		return info
	}
	if raw.ChangeTime == 0 {
		return info
	}
	changed := time.Unix(raw.ChangeTime/10_000_000-11_644_473_600, raw.ChangeTime%10_000_000*100)
	return logFileInfo{FileInfo: info, changeTime: changed}
}

// nativeLogChangeTime extracts the time captured at snapshot time. It never
// re-opens or re-stats the path: an old snapshot must keep its old stamp even
// after the log has been rewritten or appended to.
func nativeLogChangeTime(info os.FileInfo) time.Time {
	if stamped, ok := info.(logFileInfo); ok {
		return stamped.changeTime
	}
	return time.Time{}
}
