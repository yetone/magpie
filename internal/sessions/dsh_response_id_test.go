package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// dshReply is an assistant/message line holding a reply's replay state, as
// dsh writes it: the id is the one the gateway put on the reply.
func dshReplyWithID(seq int, id string) string {
	return `{"type":"assistant/message","seq":` + strconv.Itoa(seq) + `,"time":1000,"data":{"message":{"source":{"provider":"magpie","model":"group/flash","replayState":{"response":{"kind":"pi-ai","version":2,"api":"openai-completions","provider":"magpie","model":"group/flash","responseId":"` + id + `","stopReason":"stop"}}}}}}`
}

// dshSessionFile writes a session file with the lines given and parses it.
func dshSessionFile(t *testing.T, lines []string) *state {
	t.Helper()
	p := filepath.Join(t.TempDir(), "session.jsonl")
	b := ""
	for _, l := range lines {
		b += l + "\n"
	}
	if err := os.WriteFile(p, []byte(b), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return parseDsh(file{agent: "dsh", path: p, main: true, size: st.Size(), mod: modOf(st.ModTime())}, nil)
}

func TestDshResponseIDsSkipTheSeed(t *testing.T) {
	head := `{"type":"session","version":4,"id":"session-child","cwd":"/w","createdAt":1000,"delegationDepth":0,"isSeeded":true}`
	s := dshSessionFile(t, []string{
		head,
		dshReplyWithID(1, "inherited-1"),
		dshReplyWithID(2, "inherited-2"),
		`{"type":"session/end-seed","seq":3,"time":1000,"data":{"inherited":true}}`,
		dshReplyWithID(4, "own-1"),
		dshReplyWithID(5, "own-2"),
	})
	if want := []string{"own-1", "own-2"}; !slices.Equal(s.more().ResponseIDs, want) {
		t.Fatalf("fork's own replies: %v, want %v", s.more().ResponseIDs, want)
	}
}

// A file in the format before session/end-seed marks its inherited events by
// seq: those are its source's, not its own.
func TestDshResponseIDsSkipTheSeedLength(t *testing.T) {
	head := `{"type":"session","version":1,"id":"session-old","cwd":"/w","createdAt":1000,"seedLength":2,"isSeeded":true}`
	s := dshSessionFile(t, []string{
		head,
		dshReplyWithID(0, "inherited"),
		dshReplyWithID(1, "inherited-2"),
		dshReplyWithID(2, "own"),
	})
	if want := []string{"own"}; !slices.Equal(s.more().ResponseIDs, want) {
		t.Fatalf("fork's own replies: %v, want %v", s.more().ResponseIDs, want)
	}
}

// A session that is no fork owns every reply of its own.
func TestDshResponseIDsOfASession(t *testing.T) {
	head := `{"type":"session","version":4,"id":"session-plain","cwd":"/w","createdAt":1000,"delegationDepth":0,"isSeeded":false}`
	s := dshSessionFile(t, []string{
		head,
		dshReplyWithID(1, "a"),
		dshReplyWithID(2, "b"),
	})
	if want := []string{"a", "b"}; !slices.Equal(s.more().ResponseIDs, want) {
		t.Fatalf("replies: %v, want %v", s.more().ResponseIDs, want)
	}
}

// A reply dsh did not get from magpie's gateway is no route's to name: its id
// would match nothing, and is not kept.
func TestDshResponseIDsOnlyFromTheGateway(t *testing.T) {
	head := `{"type":"session","version":4,"id":"session-plain","cwd":"/w","createdAt":1000,"delegationDepth":0,"isSeeded":false}`
	s := dshSessionFile(t, []string{
		head,
		`{"type":"assistant/message","seq":1,"time":1000,"data":{"message":{"source":{"provider":"deepseek","model":"deepseek-v4","replayState":{"response":{"provider":"deepseek","responseId":"vendor-1"}}}}}}`,
		dshReplyWithID(2, "gw-1"),
	})
	if want := []string{"gw-1"}; !slices.Equal(s.more().ResponseIDs, want) {
		t.Fatalf("replies: %v, want %v", s.more().ResponseIDs, want)
	}
}

// A fork read before its seed's end lets its source's replies stand for it
// for that read; the marker, arriving later, takes them back (TestDshCalls-
// EndSeedAcrossReads reads the calls the same way).
func TestDshResponseIDsEndSeedAcrossReads(t *testing.T) {
	head := `{"type":"session","version":4,"id":"session-child","cwd":"/w","createdAt":1000,"delegationDepth":0,"isSeeded":true}`
	dir := t.TempDir()
	p := filepath.Join(dir, "session.jsonl")
	write := func(lines ...string) {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		for _, l := range lines {
			if _, err := f.WriteString(l + "\n"); err != nil {
				t.Fatal(err)
			}
		}
	}
	f := func() file {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		return file{agent: "dsh", path: p, main: true, size: st.Size(), mod: modOf(st.ModTime())}
	}
	write(head, dshReplyWithID(1, "inherited"))
	first := parseDsh(f(), nil)
	if len(first.more().ResponseIDs) != 1 {
		t.Fatalf("before the marker: %v", first.more().ResponseIDs)
	}
	write(`{"type":"session/end-seed","seq":2,"time":1000,"data":{"inherited":true}}`, dshReplyWithID(3, "own"))
	next := parseDsh(f(), first)
	if want := []string{"own"}; !slices.Equal(next.more().ResponseIDs, want) {
		t.Fatalf("after the marker: %v, want %v", next.more().ResponseIDs, want)
	}
}

// A fork of a real dsh 0.2.0-rc.2 session (testdata/dsh-gateway), made as
// dsh's buildForkSeed makes one: the source's events, a session/end-seed,
// then its own turn, under a header naming the source (parentSession). The
// listing gives the fork its own reply only and the source its own, and
// names the source the fork was made from (ForkOf).
func TestDshForkOfARealSessionListsItsOwnReplies(t *testing.T) {
	const source, fork = "session-508e7b0a-37c0-41f7-ac62-e48ab3146111", "session-5e1c0f3a-8d2b-4c6e-9a71-2f4b6d8e0c13"
	manageSetup(t)
	home := filepath.Join(t.TempDir(), "dsh")
	if err := os.CopyFS(filepath.Join(home, "sessions"), os.DirFS(filepath.Join("testdata", "dsh-gateway", "sessions"))); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join(home, "sessions", "--work-proj--", source, "session.v4.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(src)), "\n")
	answer := ""
	for _, l := range lines {
		if strings.Contains(l, `"type":"assistant/message"`) {
			answer = l
		}
	}
	// the source's last event is seq 17 at 1792664874100
	b := `{"type":"session","version":4,"id":"` + fork + `","createdAt":1792664880000,"cwd":"/work/proj","parentSession":"` + source + `","isSeeded":true}` + "\n" +
		strings.Join(lines[1:], "\n") + "\n" +
		`{"type":"session/end-seed","seq":18,"time":1792664874100,"data":{"inherited":true}}` + "\n" +
		`{"type":"user/message","seq":19,"time":1792664880100,"data":{"content":[{"type":"text","text":"Now the tests"}],"source":{"kind":"user"},"role":"user","id":"0b7c1d2e-3f40-4a51-8b62-7c83d94ea5f6"},"surfaceOp":"append"}` + "\n" +
		strings.NewReplacer(`"seq":14,"time":1792664874092`, `"seq":20,"time":1792664880200`, `"responseId":"c1"`, `"responseId":"chatcmpl-fork1"`).Replace(answer) + "\n"
	if !strings.Contains(b, `"seq":20,`) || !strings.Contains(b, "chatcmpl-fork1") {
		t.Fatal("the fixture's reply is not the one this fork copies")
	}
	dir := filepath.Join(home, "sessions", "--work-proj--", fork)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.v4.jsonl"), []byte(b), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DSH_HOME", home)
	ageDsh(home)
	Reset()
	byID := map[string]Session{}
	for _, s := range List(0) {
		if s.Agent == "dsh" {
			byID[s.ID] = s
		}
	}
	if got := byID[source].ResponseIDs; !slices.Equal(got, []string{"c1"}) {
		t.Errorf("source's replies %v, want [c1]", got)
	}
	f := byID[fork]
	if !slices.Equal(f.ResponseIDs, []string{"chatcmpl-fork1"}) || f.ForkOf != source || len(f.Convs) != 0 {
		t.Errorf("fork's replies %v, forked from %q, convs %v; want its own reply, from %s, no opening of its own", f.ResponseIDs, f.ForkOf, f.Convs, source)
	}
	if byID[source].ForkOf != "" {
		t.Errorf("the source is forked from %q", byID[source].ForkOf)
	}
}

// dsh writes no parentSession for a fork it makes today (0.2.2): the
// openings its seed carried are the only record that it came from the
// session it copied, and are kept when the seed's end takes them off Convs.
func TestDshForkWithoutAParentKeepsItsSeedOpenings(t *testing.T) {
	head := `{"type":"session","version":4,"id":"session-child","cwd":"/w","createdAt":1000,"delegationDepth":0,"isSeeded":true}`
	s := dshSessionFile(t, []string{
		head,
		`{"type":"user/message","seq":1,"time":1000,"data":{"source":{"kind":"user"},"content":[{"type":"text","text":"same first prompt"}]}}`,
		`{"type":"session/end-seed","seq":2,"time":1000,"data":{"inherited":true}}`,
		dshReplyWithID(3, "own"),
	})
	want := dshConvs(json.RawMessage(`[{"type":"text","text":"same first prompt"}]`))
	if len(want) == 0 {
		t.Fatal("the fixture opens no conversation")
	}
	if !slices.Equal(s.more().SeedConvs, want) {
		t.Fatalf("seed openings %v, want %v", s.more().SeedConvs, want)
	}
	if len(s.more().Convs) != 0 {
		t.Fatalf("the fork keeps its source's opening as its own: %v", s.more().Convs)
	}
	if !slices.Equal(s.more().ResponseIDs, []string{"own"}) {
		t.Fatalf("replies %v, want [own]", s.more().ResponseIDs)
	}
}
