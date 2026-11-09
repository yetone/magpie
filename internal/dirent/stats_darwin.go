package dirent

import (
	"encoding/binary"
	"io/fs"
	"slices"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// getattrlistbulk's attributes, from <sys/attr.h> and <sys/vnode.h>.
const (
	attrBitMapCount   = 5
	attrCmnName       = 0x00000001
	attrCmnObjType    = 0x00000008
	attrCmnModTime    = 0x00000400
	attrCmnReturned   = 0x80000000
	attrFileDataLen   = 0x00000200
	vReg, vDir, vLink = 1, 2, 5
)

type attrList struct {
	bitmapCount uint16
	_           uint16
	common      uint32
	vol         uint32
	dir         uint32
	file        uint32
	fork        uint32
}

// bufs are getattrlistbulk's answers, one a folder being read, kept for
// the next of a poll's few hundred Codex day folders and Claude Code
// projects (#2278). Nothing is kept of one once its folder's entries are
// read: each name is copied.
var bufs = sync.Pool{New: func() any { b := make([]byte, 64<<10); return &b }}

// stats reads dir's entries with getattrlistbulk: a Sessions read stats
// each of ~21k session files on every 15 s poll, ~10 µs a file on APFS,
// and this is one call for every few hundred.
// old is dir's last listing, whose names are taken where they are the same.
func stats(dir string, out, old Listing) (Listing, bool) {
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, false
	}
	defer unix.Close(fd)
	al := attrList{bitmapCount: attrBitMapCount, common: attrCmnReturned | attrCmnName | attrCmnObjType | attrCmnModTime, file: attrFileDataLen}
	bp := bufs.Get().(*[]byte)
	defer bufs.Put(bp)
	buf := *bp
	out = out[:0]
	for {
		n, _, errno := syscall.Syscall6(unix.SYS_GETATTRLISTBULK, uintptr(fd), uintptr(unsafe.Pointer(&al)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, 0)
		if errno == syscall.EINTR {
			continue
		}
		if errno != 0 {
			return nil, false
		}
		if n == 0 {
			return out, true
		}
		b := buf
		for range int(n) {
			if !entry(b, &out, old) {
				return nil, false
			}
			b = b[binary.NativeEndian.Uint32(b):]
		}
	}
}

// entry reads one entry of getattrlistbulk's answer into out: its length,
// the attributes it returned, then each in the order of their bits, a
// file's data length after the common ones. A name old has is its string.
func entry(b []byte, out *Listing, old Listing) bool {
	if len(b) < 4 {
		return false
	}
	size := int(binary.NativeEndian.Uint32(b))
	if size < 4+20 || size > len(b) {
		return false
	}
	e := b[:size]
	common := binary.NativeEndian.Uint32(e[4:])
	file := binary.NativeEndian.Uint32(e[16:])
	p := e[24:]
	var s Stat
	var name string
	if common&attrCmnName != 0 {
		if len(p) < 8 {
			return false
		}
		off, n := int(int32(binary.NativeEndian.Uint32(p))), int(binary.NativeEndian.Uint32(p[4:]))
		at := len(e) - len(p) + off
		if off < 0 || n < 1 || at+n > len(e) {
			return false
		}
		name = nameOf(e[at:at+n-1], old)
		p = p[8:]
	}
	typ := uint32(0)
	if common&attrCmnObjType != 0 {
		if len(p) < 4 {
			return false
		}
		typ = binary.NativeEndian.Uint32(p)
		p = p[4:]
	}
	if common&attrCmnModTime != 0 {
		if len(p) < 16 {
			return false
		}
		s.Mod = int64(binary.NativeEndian.Uint64(p))*1e9 + int64(binary.NativeEndian.Uint64(p[8:]))
		p = p[16:]
	}
	if file&attrFileDataLen != 0 {
		if len(p) < 8 {
			return false
		}
		s.Size = int64(binary.NativeEndian.Uint64(p))
	}
	switch typ {
	case vReg:
	case vDir:
		s.Mode = fs.ModeDir
	case vLink:
		s.Mode = fs.ModeSymlink
	default:
		s.Mode = fs.ModeIrregular
	}
	if name == "" || common&attrCmnName == 0 || common&attrCmnObjType == 0 || common&attrCmnModTime == 0 || typ == vReg && file&attrFileDataLen == 0 {
		return false
	}
	*out = append(*out, Entry{name, s})
	return true
}

// nameOf is b as a string, old's where old has it.
func nameOf(b []byte, old Listing) string {
	if len(b) > 0 && len(old) > 0 {
		v := unsafe.String(&b[0], len(b)) // compared only, never kept
		if i, ok := slices.BinarySearchFunc(old, v, func(e Entry, v string) int { return strings.Compare(e.Name, v) }); ok {
			return old[i].Name
		}
	}
	return string(b)
}
