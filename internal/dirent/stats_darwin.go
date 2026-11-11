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
// read: each name is copied. They are kept the way listings are, not in
// a sync.Pool: a collection empties a pool, a Get on another P doesn't
// take the buffer the last Put left on this one, and -race drops one Put
// in four, so a poll made a new 64 KiB buffer after each (#5990).
var bufs struct {
	sync.Mutex
	free []*[]byte
}

// keptBufs is how many read buffers are kept: as many as folders read at
// once in a poll, give or take.
const keptBufs = 4

func getBuf() *[]byte {
	bufs.Lock()
	defer bufs.Unlock()
	if n := len(bufs.free); n > 0 {
		b := bufs.free[n-1]
		bufs.free[n-1] = nil
		bufs.free = bufs.free[:n-1]
		return b
	}
	b := make([]byte, 64<<10)
	return &b
}

func putBuf(b *[]byte) {
	bufs.Lock()
	defer bufs.Unlock()
	if len(bufs.free) < keptBufs {
		bufs.free = append(bufs.free, b)
	}
}

// stats reads dir's entries with getattrlistbulk: a Sessions read stats
// each of ~21k session files on every 15 s poll, ~10 µs a file on APFS,
// and this is one call for every few hundred.
// old is dir's last listing, whose names are taken where they are the same.
func stats(dir string, out, old Listing) (Listing, bool) {
	out = out[:0]
	ok := each(dir, func(name []byte, s Stat) bool {
		out = append(out, Entry{nameOf(name, old), s})
		return true
	})
	if !ok {
		return nil, false
	}
	return out, true
}

// eachIn is Each where the system has getattrlistbulk.
func eachIn(dir string, f func(name string, s Stat) bool) bool {
	return each(dir, func(name []byte, s Stat) bool {
		return f(unsafe.String(&name[0], len(name)), s)
	})
}

// each reads dir's entries with getattrlistbulk and gives f each one's
// name, in the call's buffer, and Stat, until f says stop. It is false
// when the folder or an entry couldn't be read.
func each(dir string, f func(name []byte, s Stat) bool) bool {
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	defer unix.Close(fd)
	al := attrList{bitmapCount: attrBitMapCount, common: attrCmnReturned | attrCmnName | attrCmnObjType | attrCmnModTime, file: attrFileDataLen}
	bp := getBuf()
	defer putBuf(bp)
	buf := *bp
	for {
		n, _, errno := syscall.Syscall6(unix.SYS_GETATTRLISTBULK, uintptr(fd), uintptr(unsafe.Pointer(&al)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, 0)
		if errno == syscall.EINTR {
			continue
		}
		if errno != 0 {
			return false
		}
		if n == 0 {
			return true
		}
		b := buf
		for range int(n) {
			name, s, ok := entry(b)
			if !ok {
				return false
			}
			if !f(name, s) {
				return true
			}
			b = b[binary.NativeEndian.Uint32(b):]
		}
	}
}

// entry reads one entry of getattrlistbulk's answer: its length, the
// attributes it returned, then each in the order of their bits, a file's
// data length after the common ones. name is in b.
func entry(b []byte) (name []byte, s Stat, ok bool) {
	if len(b) < 4 {
		return nil, s, false
	}
	size := int(binary.NativeEndian.Uint32(b))
	if size < 4+20 || size > len(b) {
		return nil, s, false
	}
	e := b[:size]
	common := binary.NativeEndian.Uint32(e[4:])
	file := binary.NativeEndian.Uint32(e[16:])
	p := e[24:]
	if common&attrCmnName != 0 {
		if len(p) < 8 {
			return nil, s, false
		}
		off, n := int(int32(binary.NativeEndian.Uint32(p))), int(binary.NativeEndian.Uint32(p[4:]))
		at := len(e) - len(p) + off
		if off < 0 || n < 1 || at+n > len(e) {
			return nil, s, false
		}
		name = e[at : at+n-1]
		p = p[8:]
	}
	typ := uint32(0)
	if common&attrCmnObjType != 0 {
		if len(p) < 4 {
			return nil, s, false
		}
		typ = binary.NativeEndian.Uint32(p)
		p = p[4:]
	}
	if common&attrCmnModTime != 0 {
		if len(p) < 16 {
			return nil, s, false
		}
		s.Mod = int64(binary.NativeEndian.Uint64(p))*1e9 + int64(binary.NativeEndian.Uint64(p[8:]))
		p = p[16:]
	}
	if file&attrFileDataLen != 0 {
		if len(p) < 8 {
			return nil, s, false
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
	if len(name) == 0 || common&attrCmnName == 0 || common&attrCmnObjType == 0 || common&attrCmnModTime == 0 || typ == vReg && file&attrFileDataLen == 0 {
		return nil, s, false
	}
	return name, s, true
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
