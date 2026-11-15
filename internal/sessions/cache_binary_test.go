package sessions

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// richCache is a kept index with every field of a state, a day and Tokens
// set somewhere, and the rare parts (Claude Code's window, a Codex
// identity, StateMore) on some files.
func richCache(t *testing.T) cacheFile {
	t.Helper()
	var claude *claudeUsageState
	if err := json.Unmarshal([]byte(`[{"key":"message:1","m":{"request":"r1","usage":{"at":"2026-10-24T01:02:03Z","model":"m","input":3},"call":2}}]`), &claude); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 24, 1, 2, 3, 4, time.UTC)
	east := time.Date(2026, 10, 24, 9, 0, 0, 0, time.FixedZone("", 8*3600))
	tok := Tokens{Input: 1, Output: -2, CacheRead: 3, CacheWrite: 1 << 40, CacheWrite1h: 5}
	full := &state{
		Agent: "claude", Claude: claude, Head: digest{1, 2, 3}, HeadSize: 4096, CdRead: true, ContentHash: digest{9: 7},
		Size: 1 << 33, Mod: -5, Off: 77, ID: "id-1", CodexIdentity: &CodexIdentity{ID: "c", Role: "subagent", ParentSession: "p", RootSession: "r"},
		Cwd: "/w/é", Title: "a <b> & \"c\" \x00", Start: at, Last: east,
		Models: named[Tokens]{{"a", tok}, {"b", Tokens{}}},
		Days: named[*day]{
			{"2026-10-23", nil},
			{"2026-10-24", &day{Models: named[Tokens]{{"a", tok}}, Active: 99, Hours: hours{hourEntry(3, 5), hourEntry(23, 1)}, Prompts: 2, Replies: 3,
				Tools: named[int]{{"Bash", 4}, {"Read", 1}}, Skills: named[int]{{"pdf", 1}}}},
			{"2026-10-25", &day{}},
		},
		Entry: "claude-desktop", Msg: "msg_1", MsgModel: "m", MsgUse: tok, MsgDay: "2026-10-24", Model: "gpt-5.5", Total: &Tokens{Input: 8},
		Provider:  "openai",
		StateMore: &StateMore{DBRevision: "rev", Named: "n", Convs: []string{"c1"}, Since: at, CodexSpawnNames: map[string]string{"a": "b"}},
	}
	files := map[string]*state{"/full": full, "/nil": nil, "/empty": {}, "/k\x00e\"y": {Agent: "codex", Size: 1}}
	for i := range 3000 {
		files[fmt.Sprintf("/s/%04d.jsonl", i)] = &state{Size: int64(i), Cwd: strings.Repeat("d", i%300), Title: "t", Start: at, Last: at.Add(time.Duration(i) * time.Second),
			Models: named[Tokens]{{"m", Tokens{Input: i}}}, Days: named[*day]{{"2026-10-24", &day{Models: named[Tokens]{{"m", Tokens{Input: i}}}}}}}
	}
	return cacheFile{Version: cacheVersion, Zone: "z", Parsers: map[string]int{}, Files: files}
}

// What the binary form reads back is what the JSON form it replaced read
// back, field for field.
func TestCacheBinaryIsJSON(t *testing.T) {
	unraised(t)
	for _, c := range []cacheFile{
		richCache(t),
		{Version: cacheVersion, Zone: "z", Files: map[string]*state{}},
		{Version: cacheVersion, Zone: "z"},
	} {
		j, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		want, wantOK := decodeCache(j, "z")
		var b bytes.Buffer
		if err := encodeCache(&b, c); err != nil {
			t.Fatal(err)
		}
		if !isBinaryCache(b.Bytes()) || b.Len() >= len(j) {
			t.Fatalf("binary %d bytes, JSON %d", b.Len(), len(j))
		}
		got, ok := decodeCache(b.Bytes(), "z")
		if ok != wantOK || !reflect.DeepEqual(got, want) {
			gj, _ := json.Marshal(got)
			wj, _ := json.Marshal(want)
			t.Fatalf("%d files: got %v %.400s, want %v %.400s", len(c.Files), ok, gj, wantOK, wj)
		}
		if f := got["/full"]; f != nil && (f.Last.Location().String() != want["/full"].Last.Location().String() || !f.Last.Equal(want["/full"].Last)) {
			t.Fatalf("last %v, want %v", f.Last, want["/full"].Last)
		}
	}
}

// A kept file cut anywhere, with a byte added, of another version or zone
// is refused, never half read and never a panic.
func TestCacheBinaryRefusesBadFiles(t *testing.T) {
	unraised(t)
	small := cacheFile{Version: cacheVersion, Zone: "z", Files: map[string]*state{}}
	for k, v := range richCache(t).Files {
		if !strings.HasPrefix(k, "/s/") {
			small.Files[k] = v
		}
	}
	var b bytes.Buffer
	if err := encodeCache(&b, small); err != nil {
		t.Fatal(err)
	}
	all := b.Bytes()
	if files, ok := decodeCache(all, "z"); !ok || len(files) != len(small.Files) {
		t.Fatalf("whole file: %d files, %v", len(files), ok)
	}
	for n := len(cacheMagic); n < len(all); n++ {
		if files, ok := decodeCache(all[:n], "z"); ok {
			t.Fatalf("cut at %d of %d: read %d files", n, len(all), len(files))
		}
	}
	if _, ok := decodeCache(append(slices.Clip(all), 0), "z"); ok {
		t.Fatal("a byte added was read")
	}
	if _, ok := decodeCache(all, "y"); ok {
		t.Fatal("another zone was read")
	}
	small.Version--
	b.Reset()
	encodeCache(&b, small)
	if _, ok := decodeCache(b.Bytes(), "z"); ok {
		t.Fatal("another version was read")
	}
	// every byte flipped in turn: refused or read, never a panic
	for i := len(cacheMagic); i < len(all); i++ {
		bad := slices.Clone(all)
		bad[i] ^= 0xff
		decodeCache(bad, "z")
	}
}

// Every field of a state the JSON form kept is in the binary form: a new
// field fails here until appendBinary and reader.state carry it.
func TestCacheBinaryKnowsEveryField(t *testing.T) {
	fields := func(v any) []string {
		var out []string
		ty := reflect.TypeOf(v)
		for i := range ty.NumField() {
			out = append(out, ty.Field(i).Name)
		}
		return out
	}
	for _, c := range []struct {
		v    any
		want []string
	}{
		{state{}, []string{"Agent", "Codex", "parsePath", "Claude", "Head", "HeadSize", "CdRead", "ContentHash", "Size", "Mod", "Off", "ID", "CodexIdentity", "Cwd", "Title", "Start", "Last", "Models", "Days", "Entry", "Msg", "MsgModel", "MsgUse", "MsgDay", "Model", "Total", "Provider", "StateMore"}},
		{day{}, []string{"Models", "Active", "Hours", "Prompts", "Replies", "Tools", "Skills"}},
		{Tokens{}, []string{"Input", "Output", "CacheRead", "CacheWrite", "CacheWrite1h"}},
	} {
		if got := fields(c.v); !slices.Equal(got, c.want) {
			t.Errorf("%T has fields %v; the binary cache (cache_binary.go) carries %v: add the new one there and here", c.v, got, c.want)
		}
	}
}

// A JSON index an older magpie kept is read, and the next save writes it
// in the binary form.
func TestCacheJSONReadThenBinary(t *testing.T) {
	unraised(t)
	c := richCache(t)
	j, _ := json.Marshal(c)
	files, ok := decodeCache(j, "z")
	if !ok || len(files) != len(c.Files) {
		t.Fatalf("JSON: %d files, %v", len(files), ok)
	}
	var b bytes.Buffer
	if err := encodeCache(&b, cacheFile{Version: cacheVersion, Zone: "z", Files: files}); err != nil {
		t.Fatal(err)
	}
	again, ok := decodeCache(b.Bytes(), "z")
	if !ok || !reflect.DeepEqual(again, files) {
		t.Fatal("the JSON index written in binary reads back otherwise")
	}
}

// savedIndex is the index on disk as it was saved, every file in it.
func savedIndex(t *testing.T) cacheFile {
	t.Helper()
	b, err := os.ReadFile(CachePath())
	if err != nil {
		t.Fatal(err)
	}
	c, ok := readCacheBinary(b)
	if !isBinaryCache(b) || !ok {
		t.Fatalf("cache file: %.200q", b)
	}
	return c
}

// An older magpie's JSON index, with no session changed since, is written
// again in the binary form on the first start: a save comes only when a
// file changes, so it stayed JSON, read the slow way on every start.
func TestCacheJSONRewrittenOnStart(t *testing.T) {
	setup(t)
	unraised(t)
	List(0)
	Saved()
	c := savedIndex(t)
	if len(c.Files) == 0 {
		t.Fatal("the fixture keeps no parse: it tests nothing")
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(CachePath(), b, 0o644); err != nil {
		t.Fatal(err)
	}
	Reset()
	List(0)
	Saved()
	if again := savedIndex(t); len(again.Files) != len(c.Files) {
		t.Fatalf("rewritten with %d files, want %d", len(again.Files), len(c.Files))
	}
}
