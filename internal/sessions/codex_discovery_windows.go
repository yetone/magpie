package sessions

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func init() { codexDiscoveryStat = statCodexDiscovery }

func statCodexDiscovery(path string) (os.FileInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	// FILE_BASIC_INFO has four 64-bit times, attributes and explicit padding.
	var basic struct {
		Creation, Access, Write, Change int64
		Attributes                      uint32
		_                               uint32
	}
	if err := windows.GetFileInformationByHandleEx(windows.Handle(f.Fd()), windows.FileBasicInfo,
		(*byte)(unsafe.Pointer(&basic)), uint32(unsafe.Sizeof(basic))); err != nil || basic.Change == 0 {
		return info, nil // no stamp means content comparisons are not cached
	}
	return codexDiscoveryInfo{info, fmt.Sprint(basic.Change)}, nil
}
