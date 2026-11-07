package gui

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/sessions"
)

// The Context page names a dsh fork's requests as Routing does: by the reply
// each got (Route.ResponseID), where the conversation's opening is the
// source's. A request of the source's own still opens under the source.
func TestContextTellsAForkByItsReply(t *testing.T) {
	sandboxHome(t)
	home := t.TempDir()
	t.Setenv("DSH_HOME", home)
	sessions.Reset()
	t.Cleanup(sessions.Reset)

	const opening = `{"type":"user/message","seq":1,"time":1000,"data":{"source":{"kind":"user"},"content":[{"type":"text","text":"same first prompt"}]}}`
	const endSeed = `{"type":"session/end-seed","seq":2,"time":1000,"data":{"inherited":true}}`
	reply := func(seq int, id string) string {
		return `{"type":"assistant/message","seq":` + strconv.Itoa(seq) + `,"time":1000,"data":{"message":{"source":{"provider":"magpie","model":"group/flash","replayState":{"response":{"provider":"magpie","responseId":"` + id + `"}}}}}}`
	}
	write := func(name, header string, lines ...string) {
		dir := filepath.Join(home, "sessions", "proj", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		b := header + "\n"
		for _, l := range lines {
			b += l + "\n"
		}
		if err := os.WriteFile(filepath.Join(dir, "session.v4.jsonl"), []byte(b), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("session-source", `{"type":"session","version":4,"id":"session-source","cwd":"/w","createdAt":1000,"delegationDepth":0,"isSeeded":false}`,
		opening, reply(2, "reply-source"))
	write("session-fork", `{"type":"session","version":4,"id":"session-fork","cwd":"/w","createdAt":1000,"delegationDepth":0,"parentSession":"session-source","isSeeded":true}`,
		opening, reply(2, "reply-source"), endSeed, reply(4, "reply-fork"))

	var openingHash string
	for _, s := range sessions.List(0) {
		if s.ID == "session-source" && len(s.Convs) > 0 {
			openingHash = s.Convs[0]
		}
	}
	if openingHash == "" {
		t.Fatal("the fixture session names no conversation id")
	}
	conv := func(id int64, c string) gateway.Route {
		r := ctxRoute(id, "dsh", "", 20000, int(1000*id), 128000, 0)
		r.Conv = c
		return r
	}
	fork := conv(1, openingHash)
	fork.ResponseID = "reply-fork"
	source := conv(2, openingHash)
	source.ResponseID = "reply-source"
	loose := conv(3, openingHash)
	out := contextOf([]gateway.Route{fork, source, loose}, 7)
	keys := map[string]string{}
	for _, s := range out.Sessions {
		keys[s.Key] = s.Title
	}
	// the fork's own turn is the fork, the source's is the source, and the
	// opening alone has no reply id: it goes by its opening, the source's,
	// as it did before reply ids were read
	for _, k := range []string{"session-fork", "session-source"} {
		if _, ok := keys[k]; !ok {
			t.Fatalf("no %s among %v", k, keys)
		}
	}
	if len(keys) != 2 {
		t.Fatalf("sessions %v, want the fork and its source", keys)
	}
}

// A fork's request counts under the source only while its reply is unread:
// the opening they share is the source's, so the source's card climbed with
// every turn of the fork. Until the reply names one the tab counts the request
// in neither, and the read after dsh writes it down counts it in the fork.
func TestContextKeepsAForkRequestOffItsSource(t *testing.T) {
	sandboxHome(t)
	home := t.TempDir()
	t.Setenv("DSH_HOME", home)
	sessions.Reset()
	t.Cleanup(sessions.Reset)

	const opening = `{"type":"user/message","seq":1,"time":1000,"data":{"source":{"kind":"user"},"content":[{"type":"text","text":"same first prompt"}]}}`
	const endSeed = `{"type":"session/end-seed","seq":2,"time":1000,"data":{"inherited":true}}`
	reply := func(seq int, id string) string {
		return `{"type":"assistant/message","seq":` + strconv.Itoa(seq) + `,"time":1000,"data":{"message":{"source":{"provider":"magpie","model":"group/flash","replayState":{"response":{"provider":"magpie","responseId":"` + id + `"}}}}}}`
	}
	write := func(name, header string, lines ...string) {
		dir := filepath.Join(home, "sessions", "proj", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		b := header + "\n"
		for _, l := range lines {
			b += l + "\n"
		}
		if err := os.WriteFile(filepath.Join(dir, "session.v4.jsonl"), []byte(b), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	const sourceHead = `{"type":"session","version":4,"id":"session-source","cwd":"/w","createdAt":1000,"delegationDepth":0,"isSeeded":false}`
	const forkHead = `{"type":"session","version":4,"id":"session-fork","cwd":"/w","createdAt":2000,"delegationDepth":0,"isSeeded":true}`
	write("session-source", sourceHead, opening, reply(2, "reply-source"))
	write("session-fork", forkHead, opening, reply(2, "reply-source"), endSeed)

	var openingHash string
	for _, s := range sessions.List(0) {
		if s.ID == "session-source" && len(s.Convs) > 0 {
			openingHash = s.Convs[0]
		}
	}
	if openingHash == "" {
		t.Fatal("the fixture session names no conversation id")
	}
	conv := func(id int64, tokens int) gateway.Route {
		r := ctxRoute(id, "dsh", "", tokens, 0, 128000, 0)
		r.Conv = openingHash
		r.Time, r.Millis = time.Now(), 1000
		return r
	}
	source := conv(1, 20000)
	source.ResponseID = "reply-source"
	// dsh's history before reply ids were kept: no id to wait for, and it
	// must stay on the source's card
	old := conv(2, 18000)
	old.Time, old.Millis = time.Now().Add(-48*time.Hour), 1000
	forked := conv(3, 30000)
	forked.ResponseID = "reply-fork"
	// the fork's reply is the gateway's, not in its file yet: the source's
	// card keeps its own call and its history, and no row of the opening's
	// hash appears
	out := contextOf([]gateway.Route{source, old, forked}, 7)
	if len(out.Sessions) != 1 || out.Sessions[0].Key != "session-source" {
		t.Fatalf("sessions %v, want the source alone", ctxKeys(out))
	}
	if s := out.Sessions[0]; s.Requests != 2 || s.Peak != 20000 {
		t.Fatalf("the source's %d requests, peak %d; want its own 2, 20000", s.Requests, s.Peak)
	}
	if !out.held {
		t.Fatal("an unread reply is waited for, and the answer says so")
	}
	// dsh writes the reply down; the next read names the fork
	write("session-fork", forkHead, opening, reply(2, "reply-source"), endSeed, reply(4, "reply-fork"))
	out = contextOf([]gateway.Route{source, old, forked}, 7)
	peaks := map[string]int{}
	for _, s := range out.Sessions {
		peaks[s.Key] = s.Peak
	}
	if peaks["session-source"] != 20000 || peaks["session-fork"] != 30000 {
		t.Fatalf("peaks %v, want the source's 20000 and the fork's 30000", peaks)
	}
	if out.held {
		t.Fatal("nothing is left out once the reply names the fork")
	}
}

func ctxKeys(out ctxJSON) []string {
	keys := make([]string, 0, len(out.Sessions))
	for _, s := range out.Sessions {
		keys = append(keys, s.Key)
	}
	return keys
}
