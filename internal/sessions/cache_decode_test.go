package sessions

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unsafe"
)

// unraised holds parseVersions at none raised for a test of the kept
// file's form, whatever the agents' versions are now: a file with no
// versions in it is then as current as its parses.
func unraised(t *testing.T) {
	kept := parseVersions
	parseVersions = map[string]int{}
	t.Cleanup(func() { parseVersions = kept })
}

// The kept file decoded on a few goroutines is what one json.Unmarshal of
// it was, and is refused where that was.
func TestDecodeCacheIsUnmarshal(t *testing.T) {
	unraised(t)
	unmarshal := func(b []byte, zone string) (map[string]*state, bool) {
		var c cacheFile
		if json.Unmarshal(b, &c) != nil || c.Version != cacheVersion || c.Zone != zone || c.Files == nil {
			return nil, false
		}
		return c.Files, true
	}
	var many strings.Builder
	fmt.Fprintf(&many, `{"version":%d,"zone":"z","files":{`, cacheVersion)
	for i := range 3000 {
		if i > 0 {
			many.WriteString(",")
		}
		fmt.Fprintf(&many, `"/s/%d.jsonl":{"size":%d,"mod":%d,"title":"té %d","models":{"m":{"input":%d}},"claude":[{"key":"message:%d","m":{"request":"r%d"}}],"days":{"2026-10-%02d":{"models":{"m":{"output":%d}}}}}`, i, i, i*7, i, i, i, i, 1+i%28, i)
	}
	many.WriteString(`}}`)
	v := cacheVersion
	cases := map[string]string{
		"many":         many.String(),
		"escaped keys": fmt.Sprintf(`{"version":%d,"zone":"z","files":{"/a\"b\\cé.jsonl":{"size":1},"/n":null}}`, v),
		"twice":        fmt.Sprintf(`{"zone":"z","files":{"/a":{"size":1},"/a":{"size":2}},"version":%d}`, v),
		"empty":        fmt.Sprintf(`{"version":%d,"zone":"z","files":{}}`, v),
		"no files":     fmt.Sprintf(`{"version":%d,"zone":"z","files":null}`, v),
		"old version":  fmt.Sprintf(`{"version":%d,"zone":"z","files":{"/a":{"size":1}}}`, v-1),
		"other zone":   fmt.Sprintf(`{"version":%d,"zone":"y","files":{"/a":{"size":1}}}`, v),
		"bad file":     fmt.Sprintf(`{"version":%d,"zone":"z","files":{"/a":{"size":"one"}}}`, v),
		"bad late":     strings.Replace(many.String(), `"size":2999`, `"size":"x"`, 1),
		"cut":          many.String()[:many.Len()-5],
		"trailing":     many.String() + " x",
		"no comma":     fmt.Sprintf(`{"version":%d,"zone":"z","files":{"/a":{"size":1} "/b":{"size":2}}}`, v),
		"garbage":      "garbage",
		"empty bytes":  "",
	}
	for name, in := range cases {
		want, wantOK := unmarshal([]byte(in), "z")
		got, ok := decodeCache([]byte(in), "z")
		if ok != wantOK || !reflect.DeepEqual(got, want) {
			wj, _ := json.Marshal(want)
			gj, _ := json.Marshal(got)
			t.Errorf("%s: got %v %.300s, want %v %.300s", name, ok, gj, wantOK, wj)
		}
	}
}

// A title, cwd or model that many kept files repeat is held once, not
// once a file: 23.7k sessions a script ran with one prompt held 11 MB of
// copies of it.
func TestDecodeCacheSharesRepeats(t *testing.T) {
	unraised(t)
	var b strings.Builder
	fmt.Fprintf(&b, `{"version":%d,"zone":"z","files":{`, cacheVersion)
	for i := range 2000 {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"/s/%d.jsonl":{"size":%d,"title":"重要执行方式 %d","cwd":"/w/p","model":"gpt-5.5","codex_identity":{"id":"c%d","role":"subagent"}}`, i, i, i%2, i)
	}
	b.WriteString(`}}`)
	files, ok := decodeCache([]byte(b.String()), "z")
	if !ok || len(files) != 2000 {
		t.Fatalf("decoded %d files, %v", len(files), ok)
	}
	held := map[string]map[*byte]bool{}
	for _, st := range files {
		for name, s := range map[string]string{"title": st.Title, "cwd": st.Cwd, "model": st.Model, "role": st.CodexIdentity.Role} {
			if held[name] == nil {
				held[name] = map[*byte]bool{}
			}
			held[name][unsafe.StringData(s)] = true
		}
	}
	for name, want := range map[string]int{"title": 2, "cwd": 1, "model": 1, "role": 1} {
		if len(held[name]) != want {
			t.Errorf("%s: %d copies held, want %d", name, len(held[name]), want)
		}
	}
	if st := files["/s/3.jsonl"]; st.Title != "重要执行方式 1" || st.CodexIdentity.ID != "c3" {
		t.Fatalf("file 3: %+v", st)
	}
}

// A file of one day holds that day's models once, and a parse of it, from
// a clone, counts a call once in each: the kept state is as it was read.
func TestDecodeCacheSharesOneDaysModels(t *testing.T) {
	unraised(t)
	m := `{"gpt-5.5":{"input":10,"output":2,"cache_read":0,"cache_write":0}}`
	b := fmt.Sprintf(`{"version":%d,"zone":"z","files":{"/one":{"size":1,"models":%s,"days":{"2026-10-24":{"m":%s,"a":5}}},"/other":{"size":1,"models":%s,"days":{"2026-10-24":{"m":{"o3":{"input":1,"output":0,"cache_read":0,"cache_write":0}}}}},"/two":{"size":1,"models":%s,"days":{"2026-10-23":{"m":%s},"2026-10-24":{"a":1}}}}}`, cacheVersion, m, m, m, m, m)
	files, ok := decodeCache([]byte(b), "z")
	if !ok || len(files) != 3 {
		t.Fatalf("decoded %d files, %v", len(files), ok)
	}
	same := func(a, b named[Tokens]) bool {
		return unsafe.SliceData(a) == unsafe.SliceData(b)
	}
	one, other, two := files["/one"], files["/other"], files["/two"]
	if !same(one.Models, one.Days.of("2026-10-24").Models) {
		t.Errorf("a file of one day holds its models twice")
	}
	if same(other.Models, other.Days.of("2026-10-24").Models) || same(two.Models, two.Days.of("2026-10-23").Models) {
		t.Errorf("models held once that aren't the same, or of a file of two days")
	}
	c := one.clone()
	c.use("2026-10-24", "gpt-5.5", Tokens{Input: 1})
	if c.Models.of("gpt-5.5").Input != 11 || c.Days.of("2026-10-24").Models.of("gpt-5.5").Input != 11 {
		t.Fatalf("a parse counted %d and %d, want 11 in each", c.Models.of("gpt-5.5").Input, c.Days.of("2026-10-24").Models.of("gpt-5.5").Input)
	}
	c.dropUse("2026-10-24", "gpt-5.5", Tokens{Input: 11, Output: 2})
	if len(c.Models) != 0 || c.Days.of("2026-10-24") == nil || len(c.Days.of("2026-10-24").Models) != 0 {
		t.Fatalf("a parse took back to %v and %+v", c.Models, c.Days.of("2026-10-24"))
	}
	if one.Models.of("gpt-5.5").Input != 10 || one.Days.of("2026-10-24").Models.of("gpt-5.5").Input != 10 {
		t.Fatalf("the kept state was written: %v", one.Models)
	}
}

// A save writes the file json.Marshal would, byte for byte, a file's parse
// at a time: keys in order and escaped as Marshal escapes them, a file of
// no parse, an empty index and none at all.
func TestEncodeCacheIsMarshal(t *testing.T) {
	at := time.Date(2026, 10, 25, 7, 0, 0, 0, time.UTC)
	files := map[string]*state{"/b/<a&b>.jsonl": {Size: 3, Mod: 9, Title: "a <b> & \"c\"  ", Start: at, Last: at, Models: named[Tokens]{{"m", Tokens{Input: 1}}}}, "/a/x.jsonl": nil}
	for i := range 3000 {
		files[fmt.Sprintf("/s/%04d.jsonl", i)] = &state{Size: int64(i), Cwd: strings.Repeat("d", i%300), Start: at, Last: at.Add(time.Duration(i) * time.Second)}
	}
	for _, c := range []cacheFile{
		{Version: cacheVersion, Zone: "28800/28800", Files: files},
		{Version: cacheVersion, Zone: "<z>", Parsers: map[string]int{"codex": 2, "claude": 1}, Files: map[string]*state{"/c": {Agent: "codex", Size: 1}}},
		{Version: cacheVersion, Zone: "0/0"},
	} {
		want, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		var got bytes.Buffer
		if err := encodeCache(&got, c); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got.Bytes(), want) {
			n := 0
			for n < min(got.Len(), len(want)) && got.Bytes()[n] == want[n] {
				n++
			}
			t.Fatalf("%d files: %d bytes, want %d; apart at %d: %.80q", len(c.Files), got.Len(), len(want), n, got.Bytes()[n:])
		}
	}
}

// A raise of one agent's parseVersions reads that agent's files again and
// keeps every other agent's parses: a raise of cacheVersion read a long
// history whole, minutes of CPU after an update (#2278).
func TestParseVersionsReadOneAgentAgain(t *testing.T) {
	defer func(v map[string]int) { parseVersions = v }(parseVersions)
	kept := func(c cacheFile, now map[string]int) []string {
		var b bytes.Buffer
		if err := encodeCache(&b, c); err != nil {
			t.Fatal(err)
		}
		parseVersions = now
		files, ok := decodeCache(b.Bytes(), "z")
		if !ok {
			t.Fatal("refused")
		}
		return slices.Sorted(maps.Keys(files))
	}
	files := map[string]*state{"/codex": {Agent: "codex", Size: 1}, "/claude": {Agent: "claude", Size: 2}, "/old": {Size: 3}}
	c := cacheFile{Version: cacheVersion, Zone: "z", Files: files}
	if got := kept(c, map[string]int{}); !slices.Equal(got, []string{"/claude", "/codex", "/old"}) {
		t.Errorf("nothing raised: kept %v", got)
	}
	if got := kept(c, map[string]int{"claude": 1}); !slices.Equal(got, []string{"/codex"}) {
		t.Errorf("claude raised: kept %v, want Codex's alone", got)
	}
	c.Parsers = map[string]int{"claude": 1}
	if got := kept(c, map[string]int{"claude": 1}); !slices.Equal(got, []string{"/claude", "/codex", "/old"}) {
		t.Errorf("saved at the same versions: kept %v", got)
	}
	if got := kept(c, map[string]int{"claude": 1, "codex": 1}); !slices.Equal(got, []string{"/claude"}) {
		t.Errorf("codex raised: kept %v, want Claude Code's alone", got)
	}
}
