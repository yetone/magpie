package sessions

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A group of Claude Code files that start alike is judged (ccFamily) by
// reading each one's message uuids: on every start, as what was read is
// kept in memory only, ~0.24 s of CPU and a uuid set a file for a history
// of forks (#2278). What a group comes to depends on nothing but its
// files, as they are and as their parses say they start and end, so it is
// kept, on disk, by those, and a group none of whose files changed is not
// read again.

// ccFamilyVersion changes when a group would be judged otherwise, so what
// an older magpie kept is judged again.
const ccFamilyVersion = 1

// ccFamilyOut is what a group of files came to: of its keys, those that
// are copies and the one each is kept in, and those that are branches and
// the session each is a branch of.
type ccFamilyOut struct {
	Copies   map[string]string `json:"c,omitempty"`
	Branches map[string]string `json:"b,omitempty"`
	Seen     int64             `json:"s"` // the day (Unix) it was last used
}

type ccFamilyFile struct {
	Version int                     `json:"version"`
	Groups  map[string]*ccFamilyOut `json:"groups"`
}

// ccFamilies is what each group came to, by ccGroupSig, as kept in
// ccFamiliesPath. Under mu.
var ccFamilies struct {
	path   string // where it was read from: another HOME's is read anew
	groups map[string]*ccFamilyOut
	dirty  bool
}

// ccFamilyKeep is how many days a group kept and not used again stays.
const ccFamilyKeep = 30

func ccFamiliesPath() string { return filepath.Join(filepath.Dir(CachePath()), "cc-families.json") }

// ccFamiliesLoad reads what was kept, once for each place it is kept in.
// A file that can't be read or parsed is nothing kept: the groups are
// judged by reading their files, as they were before.
func ccFamiliesLoad() {
	p := ccFamiliesPath()
	if ccFamilies.path == p && ccFamilies.groups != nil {
		return
	}
	ccFamilies.path, ccFamilies.groups, ccFamilies.dirty = p, map[string]*ccFamilyOut{}, false
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var f ccFamilyFile
	if json.Unmarshal(b, &f) != nil || f.Version != ccFamilyVersion || f.Groups == nil {
		return
	}
	ccFamilies.groups = f.Groups
}

// ccGroupSig names a group by what judging it reads of each of its files:
// key, path, size, change time, and the parse's first and last message and
// start. Of two groups with one name, each file is the same.
func ccGroupSig(g []ccOne) string {
	lines := make([]string, len(g))
	for i, o := range g {
		m := o.st.more()
		lines[i] = o.f.key + "\x00" + o.f.path + "\x00" + strconv.FormatInt(o.f.size, 10) + "\x00" +
			strconv.FormatInt(o.f.mod.UnixNano(), 10) + "\x00" + m.Root + "\x00" + m.Leaf + "\x00" +
			strconv.FormatInt(o.st.Start.UnixNano(), 10)
	}
	slices.Sort(lines)
	h := sha256.New()
	for _, l := range lines {
		h.Write([]byte(l))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

func ccToday() int64 { return time.Now().Unix() / 86400 }

// ccFamilyKept is what the group named sig came to when it was judged, nil
// when it wasn't kept.
func ccFamilyKept(sig string) *ccFamilyOut {
	o := ccFamilies.groups[sig]
	if o != nil {
		if d := ccToday(); o.Seen != d {
			o.Seen, ccFamilies.dirty = d, true
		}
	}
	return o
}

// ccFamilyKeepOut keeps what the group named sig came to.
func ccFamilyKeepOut(sig string, copies, branches map[string]string) {
	ccFamilies.groups[sig] = &ccFamilyOut{Copies: copies, Branches: branches, Seen: ccToday()}
	ccFamilies.dirty = true
}

// keptWriter puts the writes of one kept file in order: one encoded
// before the last written is not written over it.
type keptWriter struct {
	sync.Mutex
	made, wrote    int // generations: under mu, and under the Mutex
	sync.WaitGroup     // the writes under way, which saved waits for
}

// write writes b to p behind the caller, who holds mu.
func (w *keptWriter) write(p string, b []byte) {
	w.made++
	gen := w.made
	w.Add(1)
	go func() {
		defer w.Done()
		w.Lock()
		defer w.Unlock()
		if gen < w.wrote {
			return
		}
		w.wrote = gen
		writeFileAtomic(p, b)
	}()
}

var ccFamilyWrites, ccRestWrites keptWriter

// ccFamiliesSave writes what was kept when something changed, groups not
// used for ccFamilyKeep days left out. Encoded under mu; written behind
// it, through a temp file renamed over the last.
func ccFamiliesSave() {
	if !ccFamilies.dirty {
		return
	}
	ccFamilies.dirty = false
	old := ccToday() - ccFamilyKeep
	for k, o := range ccFamilies.groups {
		if o.Seen < old {
			delete(ccFamilies.groups, k)
		}
	}
	b, err := json.Marshal(ccFamilyFile{Version: ccFamilyVersion, Groups: ccFamilies.groups})
	if err != nil {
		return
	}
	ccFamilyWrites.write(ccFamilies.path, b)
}

// writeFileAtomic writes b to p through a temp file renamed over it.
func writeFileAtomic(p string, b []byte) {
	dir := filepath.Dir(p)
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(p)+"-*")
	if err != nil {
		return
	}
	_, err = tmp.Write(b)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil || os.Rename(tmp.Name(), p) != nil {
		os.Remove(tmp.Name())
	}
}

// ccFamiliesForget lets what was read of the kept groups and rests go,
// as a new start has none of it; what is on disk stays. Under mu.
func ccFamiliesForget() {
	ccFamilyWrites.Wait()
	ccRestWrites.Wait()
	ccFamilies.path, ccFamilies.groups, ccFamilies.dirty = "", nil, false
	ccRestKept.path, ccRestKept.dirty = "", false
	clear(ccRestMemo)
}

// What a copy or branch holds that the file it is kept in doesn't (ccRest)
// is kept on disk as well: reading it needs the keeper's uuids, and each
// start read them again (~0.25 s of CPU, #2278). It is kept as the parses
// are (encodeCache), so one made in another time zone or by another
// version of the parser is read again.

var ccRestKept struct {
	path  string // where ccRestMemo was read from
	dirty bool   // ccRestMemo changed since
}

func ccRestPath() string { return filepath.Join(filepath.Dir(CachePath()), "cc-rest.json") }

// ccRestKey names a rest by both files as they were when it was read.
func ccRestKey(copyPath string, m *ccRestOf) string {
	return copyPath + "\x00" + m.keeper + "\x00" + strconv.FormatInt(m.size, 10) + "\x00" +
		strconv.FormatInt(m.mod, 10) + "\x00" + strconv.FormatInt(m.ksize, 10) + "\x00" + strconv.FormatInt(m.kmod, 10)
}

// ccRestLoad reads the kept rests into ccRestMemo, once for each place
// they are kept in. Under mu.
func ccRestLoad() {
	p := ccRestPath()
	if ccRestKept.path == p {
		return
	}
	ccRestKept.path, ccRestKept.dirty = p, false
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	files, ok := decodeCache(b, zone())
	if !ok {
		return
	}
	for k, st := range files {
		f := strings.Split(k, "\x00")
		if len(f) != 6 || st == nil {
			continue
		}
		var n [4]int64
		bad := false
		for i := range n {
			v, err := strconv.ParseInt(f[2+i], 10, 64)
			n[i], bad = v, bad || err != nil
		}
		if bad {
			continue
		}
		if _, ok := ccRestMemo[f[0]]; !ok {
			ccRestMemo[f[0]] = &ccRestOf{size: n[0], mod: n[1], keeper: f[1], ksize: n[2], kmod: n[3], st: st}
		}
	}
}

// ccRestSave writes the rests when they changed. Encoded under mu, written
// behind it.
func ccRestSave() {
	if !ccRestKept.dirty || ccRestKept.path == "" {
		return
	}
	ccRestKept.dirty = false
	files := make(map[string]*state, len(ccRestMemo))
	for p, m := range ccRestMemo {
		files[ccRestKey(p, m)] = m.st
	}
	var b bytes.Buffer
	if encodeCache(&b, cacheFile{Version: cacheVersion, Zone: zone(), Parsers: parseVersions, Files: files}) != nil {
		return
	}
	ccRestWrites.write(ccRestKept.path, b.Bytes())
}
