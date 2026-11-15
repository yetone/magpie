package sessions

import (
	"encoding/json"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"unique"
	"unsafe"

	"github.com/tidwall/gjson"
)

// decodeCache is the kept file b, in the binary form (cache_binary.go) or
// as json.Unmarshal reads the JSON one an older magpie wrote, with the files
// decoded on a few goroutines: one Unmarshal of a long history's 40MB took
// 320 to 440ms of every start (#3493). False when b isn't valid JSON, is of
// another version or zone, or a file's parse doesn't decode.
func decodeCache(b []byte, zone string) (map[string]*state, bool) {
	if isBinaryCache(b) {
		return decodeCacheBinary(b, zone)
	}
	if !gjson.ValidBytes(b) {
		return nil, false
	}
	// b is never changed, and nothing read from it is kept but cloned keys
	s := unsafe.String(unsafe.SliceData(b), len(b))
	head := gjson.GetMany(s, "version", "zone", "files", "parsers")
	if head[0].Type != gjson.Number || head[0].Int() != cacheVersion || head[1].String() != zone || !head[2].IsObject() {
		return nil, false
	}
	was := map[string]int{}
	head[3].ForEach(func(k, v gjson.Result) bool {
		was[k.String()] = int(v.Int())
		return true
	})
	var keys, raws []string
	head[2].ForEach(func(k, v gjson.Result) bool {
		keys = append(keys, strings.Clone(k.String()))
		raws = append(raws, v.Raw)
		return true
	})
	parsed := make([]*state, len(raws))
	var bad atomic.Bool
	one := func(i int) {
		if json.Unmarshal(unsafe.Slice(unsafe.StringData(raws[i]), len(raws[i])), &parsed[i]) != nil {
			bad.Store(true)
		} else if parsed[i] != nil {
			parsed[i].share()
		}
	}
	if workers := min(runtime.GOMAXPROCS(0), 8, len(raws)/256); workers < 2 {
		for i := range raws {
			one(i)
		}
	} else {
		var next atomic.Int64
		var wg sync.WaitGroup
		for range workers {
			wg.Go(func() {
				for {
					i := int(next.Add(64)) - 64
					if i >= len(raws) || bad.Load() {
						return
					}
					for j := i; j < min(i+64, len(raws)); j++ {
						one(j)
					}
				}
			})
		}
		wg.Wait()
	}
	if bad.Load() {
		return nil, false
	}
	files := make(map[string]*state, len(keys))
	for i, k := range keys {
		files[k] = parsed[i] // a key given twice is its last, as Unmarshal has it
	}
	return keepCurrent(files, was), true
}

// keepCurrent is files without the parses made by another version of
// their agent's parser than parseVersions, which are read again; was is
// parseVersions as files were kept.
func keepCurrent(files map[string]*state, was map[string]int) map[string]*state {
	// a parse of no named agent stands only when no agent's version moved
	unnamed := true
	for a, v := range was {
		unnamed = unnamed && parseVersions[a] == v
	}
	for a, v := range parseVersions {
		unnamed = unnamed && was[a] == v
	}
	for k, p := range files {
		if p != nil && (p.Agent == "" && !unnamed || p.Agent != "" && was[p.Agent] != parseVersions[p.Agent]) {
			delete(files, k)
		}
	}
	return files
}

// share has st's strings that many files repeat held once: an agent run
// by a script gives each of its sessions the same prompt, and a long
// history's 23.7k copies of one ~420-byte title, with its cwds and model
// names, were 11 of the 54 MB its kept parses held.
func (st *state) share() {
	if st.StateMore.zero() {
		st.StateMore = nil // an old cache's, or one Unmarshal made for an empty value
	}
	for _, s := range []*string{&st.Title, &st.Cwd, &st.Model, &st.Provider, &st.MsgModel, &st.MsgDay} {
		if *s != "" {
			*s = unique.Make(*s).Value()
		}
	}
	if m := st.StateMore; m != nil {
		for _, s := range []*string{&m.First, &m.Named, &m.CodexNickname} {
			if *s != "" {
				*s = unique.Make(*s).Value()
			}
		}
	}
	if id := st.CodexIdentity; id != nil {
		for _, s := range []*string{&id.Role, &id.ParentSession, &id.RootSession} {
			if *s != "" {
				*s = unique.Make(*s).Value()
			}
		}
	}
	// a file of one day (15,345 of a long history's 15,624) has that day's
	// models as its own: held once, they were ~10 of the 28MB kept. A kept
	// state is never written: a parse writes a clone, which holds them apart.
	if len(st.Days) == 1 {
		if d := st.Days[0].v; d != nil && slices.Equal(d.Models, st.Models) {
			d.Models = st.Models
		}
	}
}
