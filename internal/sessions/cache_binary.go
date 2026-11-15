package sessions

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"maps"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"time"
	"unique"
)

// cacheMagic starts a kept file written in binary. The JSON one it
// replaced took json.Unmarshal 320 to 440ms of every start for a long
// history's 40MB, and json's reflection allocated ~20MB to encode it on
// each save; this form is a few times smaller and read and written without
// reflection. A file without it is the old JSON, read once and written
// back in this form; an older magpie finds this one invalid and reads its
// files again, as it would any file of another version.
const cacheMagic = "MGPSC\x01"

// what a kept state has, past its fixed fields (encodeState)
const (
	stHead = 1 << iota
	stContentHash
	stCdRead
	stTotal
	stClaude
	stCodexIdentity
	stMore
)

// encodeCache writes c in the binary form, a file's parse at a time:
// a header (the magic, version, zone and parsers), the number of files,
// and each file's name and its state, the state's length first so the
// states can be read on several goroutines.
func encodeCache(w io.Writer, c cacheFile) error {
	b := make([]byte, 0, 256<<10)
	b = append(b, cacheMagic...)
	b = binary.AppendUvarint(b, uint64(c.Version))
	b = appendStr(b, c.Zone)
	b = binary.AppendUvarint(b, uint64(len(c.Parsers)))
	for _, k := range slices.Sorted(maps.Keys(c.Parsers)) {
		b = appendStr(b, k)
		b = binary.AppendVarint(b, int64(c.Parsers[k]))
	}
	if c.Files == nil {
		b = append(b, 0) // no files at all, as JSON's null
		_, err := w.Write(b)
		return err
	}
	b = append(b, 1)
	b = binary.AppendUvarint(b, uint64(len(c.Files)))
	var rec []byte
	for _, k := range slices.Sorted(maps.Keys(c.Files)) {
		b = appendStr(b, k)
		st := c.Files[k]
		if st == nil {
			b = append(b, 0)
		} else {
			var err error
			if rec, err = st.appendBinary(rec[:0]); err != nil {
				return err
			}
			b = binary.AppendUvarint(b, uint64(len(rec))+1)
			b = append(b, rec...)
		}
		if len(b) >= 256<<10 {
			if _, err := w.Write(b); err != nil {
				return err
			}
			b = b[:0]
		}
	}
	_, err := w.Write(b)
	return err
}

func appendStr(b []byte, s string) []byte {
	b = binary.AppendUvarint(b, uint64(len(s)))
	return append(b, s...)
}

func appendTokens(b []byte, t Tokens) []byte {
	b = binary.AppendVarint(b, int64(t.Input))
	b = binary.AppendVarint(b, int64(t.Output))
	b = binary.AppendVarint(b, int64(t.CacheRead))
	b = binary.AppendVarint(b, int64(t.CacheWrite))
	return binary.AppendVarint(b, int64(t.CacheWrite1h))
}

// appendTime is t, as time's own binary form (its instant and offset),
// or a 0 for the zero time.
func appendTime(b []byte, t time.Time) ([]byte, error) {
	if t == (time.Time{}) {
		return append(b, 0), nil
	}
	n := len(b)
	b = append(b, 0)
	b, err := t.AppendBinary(b)
	if err != nil {
		return nil, err
	}
	b[n] = byte(len(b) - n - 1)
	return b, nil
}

func appendNamedTokens(b []byte, n named[Tokens]) []byte {
	b = binary.AppendUvarint(b, uint64(len(n)))
	for _, e := range n {
		b = appendStr(b, e.name)
		b = appendTokens(b, e.v)
	}
	return b
}

func appendNamedInt(b []byte, n named[int]) []byte {
	b = binary.AppendUvarint(b, uint64(len(n)))
	for _, e := range n {
		b = appendStr(b, e.name)
		b = binary.AppendVarint(b, int64(e.v))
	}
	return b
}

// appendJSON is v as JSON, its length first: what few states have
// (Claude Code's revision window, a Codex identity, StateMore) is kept as
// json.Marshal has it, and read back by json.Unmarshal.
func appendJSON(b []byte, v any) ([]byte, error) {
	j, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	b = binary.AppendUvarint(b, uint64(len(j)))
	return append(b, j...), nil
}

// appendBinary is st in the binary form. Every field the JSON form kept is
// here (TestCacheBinaryKnowsEveryField).
func (st *state) appendBinary(b []byte) ([]byte, error) {
	var flags uint64
	if st.Head != (digest{}) {
		flags |= stHead
	}
	if st.ContentHash != (digest{}) {
		flags |= stContentHash
	}
	if st.CdRead {
		flags |= stCdRead
	}
	if st.Total != nil {
		flags |= stTotal
	}
	if st.Claude != nil {
		flags |= stClaude
	}
	if st.CodexIdentity != nil {
		flags |= stCodexIdentity
	}
	if st.StateMore != nil {
		flags |= stMore
	}
	b = binary.AppendUvarint(b, flags)
	b = appendStr(b, st.Agent)
	if flags&stHead != 0 {
		b = append(b, st.Head[:]...)
	}
	b = binary.AppendVarint(b, int64(st.HeadSize))
	if flags&stContentHash != 0 {
		b = append(b, st.ContentHash[:]...)
	}
	b = binary.AppendVarint(b, st.Size)
	b = binary.AppendVarint(b, st.Mod)
	b = binary.AppendVarint(b, st.Off)
	b = appendStr(b, st.ID)
	b = appendStr(b, st.Cwd)
	b = appendStr(b, st.Title)
	var err error
	if b, err = appendTime(b, st.Start); err != nil {
		return nil, err
	}
	if b, err = appendTime(b, st.Last); err != nil {
		return nil, err
	}
	b = appendNamedTokens(b, st.Models)
	b = binary.AppendUvarint(b, uint64(len(st.Days)))
	for _, e := range st.Days {
		b = appendStr(b, e.name)
		d := e.v
		if d == nil {
			b = append(b, 0)
			continue
		}
		b = append(b, 1)
		b = appendNamedTokens(b, d.Models)
		b = binary.AppendVarint(b, d.Active)
		b = binary.AppendUvarint(b, uint64(len(d.Hours)))
		for _, h := range d.Hours {
			b = binary.AppendUvarint(b, h)
		}
		b = binary.AppendVarint(b, int64(d.Prompts))
		b = binary.AppendVarint(b, int64(d.Replies))
		b = appendNamedInt(b, d.Tools)
		b = appendNamedInt(b, d.Skills)
	}
	b = appendStr(b, st.Entry)
	b = appendStr(b, st.Msg)
	b = appendStr(b, st.MsgModel)
	b = appendTokens(b, st.MsgUse)
	b = appendStr(b, st.MsgDay)
	b = appendStr(b, st.Model)
	if st.Total != nil {
		b = appendTokens(b, *st.Total)
	}
	b = appendStr(b, st.Provider)
	if st.Claude != nil {
		if b, err = appendJSON(b, st.Claude); err != nil {
			return nil, err
		}
	}
	if st.CodexIdentity != nil {
		if b, err = appendJSON(b, st.CodexIdentity); err != nil {
			return nil, err
		}
	}
	if st.StateMore != nil {
		if b, err = appendJSON(b, st.StateMore); err != nil {
			return nil, err
		}
	}
	return b, nil
}

// reader reads the binary form; a read past its end, or of a number that
// isn't one, leaves it bad and every later read empty.
type reader struct {
	b   []byte
	bad bool
}

func (r *reader) fail() { r.bad, r.b = true, nil }

func (r *reader) uvarint() uint64 {
	v, n := binary.Uvarint(r.b)
	if n <= 0 {
		r.fail()
		return 0
	}
	r.b = r.b[n:]
	return v
}

func (r *reader) varint() int64 {
	v, n := binary.Varint(r.b)
	if n <= 0 {
		r.fail()
		return 0
	}
	r.b = r.b[n:]
	return v
}

func (r *reader) int() int { return int(r.varint()) }

func (r *reader) take(n uint64) []byte {
	if n > uint64(len(r.b)) {
		r.fail()
		return nil
	}
	out := r.b[:n]
	r.b = r.b[n:]
	return out
}

func (r *reader) byte() byte {
	if b := r.take(1); len(b) == 1 {
		return b[0]
	}
	return 0
}

// str is a copy of a string read: the bytes read are the mapped file's.
func (r *reader) str() string { return string(r.take(r.uvarint())) }

// name is a string many states repeat, held once (unique).
func (r *reader) name() string {
	b := r.take(r.uvarint())
	if len(b) == 0 {
		return ""
	}
	return unique.Make(string(b)).Value()
}

// count is a number of things to read, each at least a byte long: more
// than are left is a bad file, not a large allocation.
func (r *reader) count() int {
	n := r.uvarint()
	if n > uint64(len(r.b)) {
		r.fail()
		return 0
	}
	return int(n)
}

func (r *reader) tokens() Tokens {
	return Tokens{Input: r.int(), Output: r.int(), CacheRead: r.int(), CacheWrite: r.int(), CacheWrite1h: r.int()}
}

func (r *reader) time() time.Time {
	n := r.byte()
	if n == 0 {
		return time.Time{}
	}
	var t time.Time
	if t.UnmarshalBinary(r.take(uint64(n))) != nil {
		r.fail()
	}
	return t
}

// sorted is whether name comes after the last entry's: a named is kept in
// its names' order, which find's binary search needs.
func sorted[V any](n named[V], name string) bool {
	return len(n) == 0 || n[len(n)-1].name < name
}

func (r *reader) namedTokens() named[Tokens] {
	k := r.count()
	if k == 0 {
		return nil
	}
	n := make(named[Tokens], 0, k)
	for range k {
		name := r.name()
		if !sorted(n, name) {
			r.fail()
		}
		n = append(n, entry[Tokens]{name, r.tokens()})
		if r.bad {
			return nil
		}
	}
	return n
}

func (r *reader) namedInt() named[int] {
	k := r.count()
	if k == 0 {
		return nil
	}
	n := make(named[int], 0, k)
	for range k {
		name := r.name()
		if !sorted(n, name) {
			r.fail()
		}
		n = append(n, entry[int]{name, r.int()})
		if r.bad {
			return nil
		}
	}
	return n
}

// json reads a value appendJSON wrote into v.
func (r *reader) json(v any) {
	j := r.take(r.uvarint())
	if r.bad || json.Unmarshal(j, v) != nil {
		r.fail()
	}
}

// state reads what appendBinary wrote, and fails on anything left over.
func (r *reader) state() *state {
	st := &state{}
	flags := r.uvarint()
	st.Agent = r.name()
	if flags&stHead != 0 {
		copy(st.Head[:], r.take(uint64(len(st.Head))))
	}
	st.HeadSize = int32(r.varint())
	st.CdRead = flags&stCdRead != 0
	if flags&stContentHash != 0 {
		copy(st.ContentHash[:], r.take(uint64(len(st.ContentHash))))
	}
	st.Size, st.Mod, st.Off = r.varint(), r.varint(), r.varint()
	st.ID = r.str()
	st.Cwd = r.name()
	st.Title = r.name()
	st.Start, st.Last = r.time(), r.time()
	st.Models = r.namedTokens()
	if k := r.count(); k > 0 {
		st.Days = make(named[*day], 0, k)
		for range k {
			name := r.name()
			if !sorted(st.Days, name) {
				r.fail()
			}
			var d *day
			if r.byte() == 1 {
				d = &day{Models: r.namedTokens(), Active: r.varint()}
				if h := r.count(); h > 0 {
					d.Hours = make(hours, h)
					for i := range d.Hours {
						d.Hours[i] = r.uvarint()
					}
				}
				d.Prompts, d.Replies = r.int(), r.int()
				d.Tools, d.Skills = r.namedInt(), r.namedInt()
			}
			if r.bad {
				return nil
			}
			st.Days = append(st.Days, entry[*day]{name, d})
		}
	}
	st.Entry = r.name()
	st.Msg = r.str()
	st.MsgModel = r.name()
	st.MsgUse = r.tokens()
	st.MsgDay = r.name()
	st.Model = r.name()
	if flags&stTotal != 0 {
		t := r.tokens()
		st.Total = &t
	}
	st.Provider = r.name()
	if flags&stClaude != 0 {
		r.json(&st.Claude)
	}
	if flags&stCodexIdentity != 0 {
		r.json(&st.CodexIdentity)
	}
	if flags&stMore != 0 {
		r.json(&st.StateMore)
	}
	if flags >= stMore<<1 || len(r.b) > 0 {
		r.fail()
	}
	if r.bad {
		return nil
	}
	st.share()
	return st
}

// decodeCacheBinary is decodeCache of a file in the binary form.
func decodeCacheBinary(b []byte, zone string) (map[string]*state, bool) {
	c, ok := readCacheBinary(b)
	if !ok || c.Version != cacheVersion || c.Zone != zone || c.Files == nil {
		return nil, false
	}
	return keepCurrent(c.Files, c.Parsers), true
}

// readCacheBinary is the kept file b in the binary form as it was written,
// its states read on a few goroutines; false when it doesn't read whole.
func readCacheBinary(b []byte) (cacheFile, bool) {
	var c cacheFile
	r := reader{b: b[len(cacheMagic):]}
	c.Version = int(r.uvarint())
	c.Zone = r.str()
	c.Parsers = map[string]int{}
	for range r.count() {
		k := r.str()
		c.Parsers[k] = int(r.varint())
	}
	switch r.byte() {
	case 0:
		return c, !r.bad && len(r.b) == 0
	case 1:
	default:
		return c, false
	}
	n := r.count()
	keys := make([]string, 0, n)
	recs := make([][]byte, 0, n)
	for range n {
		keys = append(keys, r.str())
		size := r.uvarint()
		if size == 0 {
			recs = append(recs, nil)
		} else {
			recs = append(recs, r.take(size-1))
		}
		if r.bad {
			return c, false
		}
	}
	if r.bad || len(r.b) > 0 {
		return c, false
	}
	parsed := make([]*state, len(recs))
	var bad atomic.Bool
	one := func(i int) {
		if recs[i] == nil {
			return
		}
		rr := reader{b: recs[i]}
		if parsed[i] = rr.state(); parsed[i] == nil {
			bad.Store(true)
		}
	}
	if workers := min(runtime.GOMAXPROCS(0), 8, len(recs)/1024); workers < 2 {
		for i := range recs {
			one(i)
		}
	} else {
		var next atomic.Int64
		var wg sync.WaitGroup
		for range workers {
			wg.Go(func() {
				for {
					i := int(next.Add(256)) - 256
					if i >= len(recs) || bad.Load() {
						return
					}
					for j := i; j < min(i+256, len(recs)); j++ {
						one(j)
					}
				}
			})
		}
		wg.Wait()
	}
	if bad.Load() {
		return c, false
	}
	c.Files = make(map[string]*state, len(keys))
	for i, k := range keys {
		c.Files[k] = parsed[i]
	}
	return c, true
}

// isBinaryCache: b is a kept file in the binary form.
func isBinaryCache(b []byte) bool { return bytes.HasPrefix(b, []byte(cacheMagic)) }
