package sessions

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSaidWords(t *testing.T) {
	var got []string
	saidWords("Fix the OAuth-login, 修复登录问题 ok", func(w string) { got = append(got, w) })
	want := []string{"fix", "the", "oauth", "login", "修复", "复登", "登录", "录问", "问题", "题", "ok"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("words %q, want %q", got, want)
	}
	got = nil
	saidWords(strings.Repeat("a", 100), func(w string) { got = append(got, w) })
	if len(got) != 1 || len(got[0]) != saidWordMax {
		t.Fatalf("a long run: %q", got)
	}
}

func TestSaidTerms(t *testing.T) {
	for needle, want := range map[string][]string{
		"auth bug":  {`"auth"*`, `"bug"*`},
		"登录问题":      {`"登录"`, `"录问"`, `"问题"`},
		"修":         {`"修"*`},
		"!!! ...":   nil,
		"bug bug":   {`"bug"*`},
		`say "hi"*`: {`"say"*`, `"hi"*`},
	} {
		if got := saidTerms(NeedleOf(needle)); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: terms %q, want %q", needle, got, want)
		}
	}
}

// saidTestIndex is an index in a test's folder over the sessions list
// gives.
func saidTestIndex(t *testing.T, list func() []saidSession) *saidIndex {
	t.Helper()
	file := filepath.Join(t.TempDir(), "said.db")
	x := &saidIndex{where: func() string { return file }, list: list}
	t.Cleanup(func() {
		x.mu.Lock()
		run := x.running
		x.mu.Unlock()
		if run != nil {
			<-run
		}
		if x.db != nil {
			x.db.Close()
		}
	})
	return x
}

// stale has the next search bring the index up to date.
func (x *saidIndex) stale() {
	x.mu.Lock()
	x.at = time.Time{}
	x.mu.Unlock()
}

// saidFile writes lines to a session file of agent and lists it as the
// index would.
func saidFile(t *testing.T, dir, agent, id string, last time.Time, lines ...string) saidSession {
	t.Helper()
	path := filepath.Join(dir, id+".jsonl")
	writeLines(t, path, lines...)
	return saidOf(t, Session{Agent: agent, ID: id, Path: path, Last: last})
}

func saidOf(t *testing.T, s Session) saidSession {
	t.Helper()
	fi, err := os.Stat(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	return saidSession{s: s, size: fi.Size(), sig: fi.ModTime().String(), grow: s.Agent == "claude" || s.Agent == "codex"}
}

func cxSaid(role, text string) string {
	kind := "input_text"
	if role == "assistant" {
		kind = "output_text"
	}
	return `{"type":"response_item","payload":{"type":"message","role":"` + role + `","content":[{"type":"` + kind + `","text":"` + text + `"}]}}`
}

func saidAgents(f SaidFound) []string {
	var out []string
	for _, h := range f.Hits {
		out = append(out, h.Session.Agent+":"+h.Session.ID)
	}
	return out
}

// cryingLiu on X: what was said in any agent's session is found from one
// search, the newest session first, with the words round it.
func TestSearchSaidAcrossAgents(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	list := []saidSession{
		saidFile(t, dir, "claude", "c1", now.Add(-time.Hour), claudeAsked, claudeTool, claudeSaid),
		saidFile(t, dir, "codex", "x1", now,
			`{"type":"session_meta","payload":{"id":"x1"}}`,
			cxSaid("user", "Why does the rainbow build fail?"),
			`{"type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"command\":[\"kubectl\"]}","call_id":"c1"}}`,
			cxSaid("assistant", "修复登录问题之后就好了")),
	}
	x := saidTestIndex(t, func() []saidSession { return list })
	ctx := context.Background()

	f, err := x.search(ctx, NeedleOf("rainbow"), "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := saidAgents(f); !reflect.DeepEqual(got, []string{"codex:x1", "claude:c1"}) {
		t.Fatalf("rainbow found in %v", got)
	}
	if f.Indexed != 2 || f.Total != 2 {
		t.Fatalf("indexed %d of %d", f.Indexed, f.Total)
	}
	if h := f.Hits[1].Hit; h.Match != "rainbow" || h.Role != "assistant" {
		t.Fatalf("claude's hit: %+v", h)
	}

	// Chinese, by its characters side by side, and a word by its start
	if f, _ := x.search(ctx, NeedleOf("登录问题"), "", 10); !reflect.DeepEqual(saidAgents(f), []string{"codex:x1"}) {
		t.Fatalf("登录问题 found in %v", saidAgents(f))
	}
	if f, _ := x.search(ctx, NeedleOf("salt"), "", 10); !reflect.DeepEqual(saidAgents(f), []string{"claude:c1"}) {
		t.Fatalf("salt found in %v", saidAgents(f))
	}
	// one agent's alone
	if f, _ := x.search(ctx, NeedleOf("rainbow"), "claude", 10); !reflect.DeepEqual(saidAgents(f), []string{"claude:c1"}) {
		t.Fatalf("claude's rainbow: %v", saidAgents(f))
	}
	// a word said in no session, and one only a tool call has
	for _, w := range []string{"kubernetes", "kubectl"} {
		if f, _ := x.search(ctx, NeedleOf(w), "", 10); len(f.Hits) != 0 {
			t.Fatalf("%s found in %v", w, saidAgents(f))
		}
	}
	// the limit
	if f, _ := x.search(ctx, NeedleOf("rainbow"), "", 1); len(f.Hits) != 1 || !f.Cut {
		t.Fatalf("limit 1: %v cut %v", saidAgents(f), f.Cut)
	}
	if _, err := x.search(ctx, NeedleOf("?!"), "", 10); err != ErrNoWords {
		t.Fatalf("no words: %v", err)
	}
}

// The index tells which files to read: a session it doesn't name is not
// read for a search.
func TestSearchSaidReadsOnlyTheSessionsWithTheWords(t *testing.T) {
	dir := t.TempDir()
	list := []saidSession{
		saidFile(t, dir, "claude", "c1", time.Now(), claudeAsked, claudeSaid),
		saidFile(t, dir, "claude", "c2", time.Now(), `{"type":"user","message":{"role":"user","content":"nothing here"},"uuid":"u1"}`),
	}
	x := saidTestIndex(t, func() []saidSession { return list })
	if _, err := x.search(context.Background(), NeedleOf("salt"), "", 10); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	read := map[string]int{}
	scanHook = func(p string, _ int64) {
		mu.Lock()
		read[filepath.Base(p)]++
		mu.Unlock()
	}
	t.Cleanup(func() { scanHook = nil })
	f, err := x.search(context.Background(), NeedleOf("salt"), "", 10)
	if err != nil || len(f.Hits) != 1 {
		t.Fatalf("salt: %v %v", saidAgents(f), err)
	}
	if read["c2.jsonl"] != 0 || read["c1.jsonl"] == 0 {
		t.Fatalf("files read: %v", read)
	}
}

// A Claude Code or Codex file that grew is read from where the last read
// ended, and a line still being written waits until it is whole.
func TestSearchSaidReadsAGrownFileFromWhereItEnded(t *testing.T) {
	dir := t.TempDir()
	ss := saidFile(t, dir, "claude", "c1", time.Now(), claudeAsked, claudeSaid)
	var mu sync.Mutex
	list := []saidSession{ss}
	x := saidTestIndex(t, func() []saidSession { mu.Lock(); defer mu.Unlock(); return list })
	ctx := context.Background()
	if f, _ := x.search(ctx, NeedleOf("salt"), "", 10); len(f.Hits) != 1 {
		t.Fatal("salt not found")
	}
	size := ss.size

	grow := func(text string) {
		fh, err := os.OpenFile(ss.s.Path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		fh.WriteString(text)
		fh.Close()
		mu.Lock()
		list = []saidSession{saidOf(t, ss.s)}
		mu.Unlock()
		x.stale()
	}
	var from []int64
	scanHook = func(p string, off int64) {
		if p == ss.s.Path {
			mu.Lock()
			from = append(from, off)
			mu.Unlock()
		}
	}
	t.Cleanup(func() { scanHook = nil })

	// half a line: not read yet
	line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Zanzibar holds the keys."}]},"uuid":"a3"}`
	grow(line[:40])
	if f, _ := x.search(ctx, NeedleOf("zanzibar"), "", 10); len(f.Hits) != 0 {
		t.Fatalf("half a line indexed: %v", saidAgents(f))
	}
	grow(line[40:] + "\n")
	f, _ := x.search(ctx, NeedleOf("zanzibar"), "", 10)
	if len(f.Hits) != 1 {
		t.Fatalf("zanzibar not found after the file grew")
	}
	mu.Lock()
	first := append([]int64(nil), from...)
	mu.Unlock()
	if len(first) == 0 || first[0] != size {
		t.Fatalf("the grown file was read from %v, want %d first", first, size)
	}
	// what was there before is still found
	if f, _ := x.search(ctx, NeedleOf("salt"), "", 10); len(f.Hits) != 1 {
		t.Fatal("salt lost after the file grew")
	}
}

// A session gone from the list is gone from the index; one rewritten is
// read again whole, its old words gone.
func TestSearchSaidForgetsGoneAndRewrittenSessions(t *testing.T) {
	dir := t.TempDir()
	a := saidFile(t, dir, "gemini", "g1", time.Now(), claudeAsked)
	b := saidFile(t, dir, "claude", "c1", time.Now(), claudeSaid)
	a.grow = false
	var mu sync.Mutex
	list := []saidSession{a, b}
	x := saidTestIndex(t, func() []saidSession { mu.Lock(); defer mu.Unlock(); return list })
	ctx := context.Background()
	if _, err := x.search(ctx, NeedleOf("salt"), "", 10); err != nil {
		t.Fatal(err)
	}
	count := func(key string) (n int) {
		x.db.QueryRow(`SELECT count(*) FROM chunks WHERE key = ?`, key).Scan(&n)
		return
	}
	if count("claude:c1") == 0 {
		t.Fatal("claude:c1 not indexed")
	}
	mu.Lock()
	list = []saidSession{a}
	mu.Unlock()
	x.stale()
	x.search(ctx, NeedleOf("salt"), "", 10)
	if n := count("claude:c1"); n != 0 {
		t.Fatalf("a gone session keeps %d rows", n)
	}

	// a session that isn't only added to, changed: read again whole
	a.sig = "changed"
	mu.Lock()
	list = []saidSession{a}
	mu.Unlock()
	x.stale()
	x.search(ctx, NeedleOf("login"), "", 10)
	if n := count("gemini:g1"); n != 1 {
		t.Fatalf("a rewritten session has %d rows, want 1", n)
	}
}

// An agent magpie may not read is in no answer, even while the index
// still holds it.
func TestSearchSaidLeavesOutAgentsNotRead(t *testing.T) {
	dir := t.TempDir()
	list := []saidSession{saidFile(t, dir, "claude", "c1", time.Now(), claudeSaid)}
	x := saidTestIndex(t, func() []saidSession { return list })
	ctx := context.Background()
	if f, _ := x.search(ctx, NeedleOf("salt"), "", 10); len(f.Hits) != 1 {
		t.Fatal("salt not found")
	}
	off := func() bool { return true }
	OwnOff.Store(&off)
	t.Cleanup(func() { OwnOff.Store(nil) })
	if f, _ := x.search(ctx, NeedleOf("salt"), "", 10); len(f.Hits) != 0 {
		t.Fatalf("found in an agent not read: %v", saidAgents(f))
	}
}

// The real list: every agent's sessions, of the agents magpie may read.
func TestSaidSessionsListsTheAgentsRead(t *testing.T) {
	d := consentHome(t)
	writeTwoAgents(t, d)
	keys := func() map[string]bool {
		out := map[string]bool{}
		for _, ss := range saidSessions() {
			out[ss.s.Agent] = true
			if ss.size == 0 || ss.sig == "" || !ss.grow {
				t.Errorf("%s: %+v", ss.s.Agent, ss)
			}
		}
		return out
	}
	if got := keys(); len(got) != 0 {
		t.Fatalf("nothing allowed lists %v", got)
	}
	if err := SetReads(new(bool), map[string]bool{"claude": true}); err != nil {
		t.Fatal(err)
	}
	Reset()
	if got := keys(); !got["claude"] || got["codex"] {
		t.Fatalf("claude allowed lists %v", got)
	}
	on := true
	if err := SetReads(&on, nil); err != nil {
		t.Fatal(err)
	}
	Reset()
	if got := keys(); !got["claude"] || !got["codex"] {
		t.Fatalf("all allowed lists %v", got)
	}
}

// A hit past what a transcript keeps is shown: the transcript starts a
// few parts before it.
func TestTranscriptAtStartsBeforeTheHit(t *testing.T) {
	filler := strings.Repeat("lorem ipsum ", 600) // ~7,200 characters
	var lines []string
	for i := range 80 {
		lines = append(lines, `{"type":"user","message":{"role":"user","content":"`+filler+`"},"uuid":"u`+strconv.Itoa(i)+`"}`)
	}
	lines = append(lines, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"the zanzibar answer"}]},"uuid":"z"}`)
	s := claudeFile(t, lines...)

	whole, err := TranscriptOf(s)
	if err != nil || !whole.Cut {
		t.Fatalf("the fixture should be cut: %v %v", whole.Cut, err)
	}
	at, err := TranscriptAt(s, NeedleOf("Zanzibar"))
	if err != nil {
		t.Fatal(err)
	}
	if at.From != 80-transcriptLead {
		t.Fatalf("from %d, want %d", at.From, 80-transcriptLead)
	}
	if n := len(at.Parts); n == 0 || !strings.Contains(at.Parts[n-1].Text, "zanzibar") {
		t.Fatalf("the hit isn't in the transcript (%d parts)", n)
	}
	// a hit TranscriptOf shows: it as it is
	near, _ := TranscriptAt(s, NeedleOf("lorem"))
	if near.From != 0 || len(near.Parts) != len(whole.Parts) {
		t.Fatalf("a near hit moved the transcript: from %d", near.From)
	}
}

// The first listing of a long history can outlast a search's wait: the
// answer then says the sessions are still being found, not that none says
// the words (the real 29G Claude and 60G Codex history took ~50s to list).
func TestSearchSaidWhileTheSessionsAreFound(t *testing.T) {
	dir := t.TempDir()
	at := time.Now().Add(-time.Hour)
	ss := saidFile(t, dir, "claude", "c1", at, `{"type":"user","message":{"role":"user","content":"the Zanzibar key"}}`)
	hold := make(chan struct{})
	x := saidTestIndex(t, func() []saidSession { <-hold; return []saidSession{ss} })
	var let sync.Once
	letGo := func() { let.Do(func() { close(hold) }) }
	t.Cleanup(letGo) // before the index's own, which waits for the listing
	// the search waits saidWait for a listing held until it is let go
	f, err := x.search(context.Background(), "zanzibar", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !f.Listing || len(f.Hits) != 0 {
		t.Fatalf("while listing: %+v", f)
	}
	letGo()
	x.mu.Lock()
	run := x.running
	x.mu.Unlock()
	<-run
	f, err = x.search(context.Background(), "zanzibar", "", 10)
	if err != nil || f.Listing || f.Indexed != 1 || f.Total != 1 || len(f.Hits) != 1 {
		t.Fatalf("listed: %+v %v", f, err)
	}
}
