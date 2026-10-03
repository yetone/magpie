package agent

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
)

var reasonixStudioVersions = struct {
	sync.Mutex
	m map[goProgramKey]bool
}{m: map[goProgramKey]bool{}}

// The app's package, rather than its filename, distinguishes Electron Studio
// 2.x from the earlier desktop clients. Read only package.json from the archive;
// never launch the app to ask its version or read the whole bundle into memory.
func reasonixStudioPackage(path string) bool {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return false
	}
	key := goProgramKey{path, st.Size(), st.ModTime()}
	reasonixStudioVersions.Lock()
	defer reasonixStudioVersions.Unlock()
	if detected, ok := reasonixStudioVersions.m[key]; ok {
		return detected
	}
	f, err := os.Open(path)
	if err != nil {
		return false // a temporary file lock must not hide an installed client forever
	}
	defer f.Close()
	detected := reasonixStudioArchiveVersion(f, st.Size())
	reasonixStudioVersions.m[key] = detected
	return detected
}

func reasonixStudioArchiveVersion(r io.ReaderAt, size int64) bool {
	var prefix [16]byte
	if _, err := r.ReadAt(prefix[:], 0); err != nil || binary.LittleEndian.Uint32(prefix[:4]) != 4 {
		return false
	}
	headerSize := int64(binary.LittleEndian.Uint32(prefix[4:8]))
	jsonSize := int64(binary.LittleEndian.Uint32(prefix[12:16]))
	if headerSize < 8 || headerSize > 4<<20 || headerSize > size-8 || jsonSize < 2 || jsonSize > headerSize-8 {
		return false
	}
	header := make([]byte, jsonSize)
	if _, err := r.ReadAt(header, 16); err != nil {
		return false
	}
	var archive struct {
		Files map[string]struct {
			Size     int64  `json:"size"`
			Offset   string `json:"offset"`
			Unpacked bool   `json:"unpacked"`
		} `json:"files"`
	}
	if json.Unmarshal(header, &archive) != nil {
		return false
	}
	entry, ok := archive.Files["package.json"]
	offset, err := strconv.ParseInt(entry.Offset, 10, 64)
	dataSize := size - 8 - headerSize
	if !ok || err != nil || entry.Unpacked || entry.Size <= 0 || entry.Size > 64<<10 || offset < 0 || offset > dataSize || entry.Size > dataSize-offset {
		return false
	}
	metadata := make([]byte, entry.Size)
	if _, err := r.ReadAt(metadata, 8+headerSize+offset); err != nil {
		return false
	}
	var pkg struct{ Name, Version string }
	return json.Unmarshal(metadata, &pkg) == nil && pkg.Name == "reasonix-studio-electron" &&
		strings.HasPrefix(strings.TrimPrefix(strings.TrimSpace(pkg.Version), "v"), "2.")
}
