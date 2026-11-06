package gui

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

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
	// the fork's own turn is the fork; the source's is the source; the
	// opening alone is the source's, whose it is
	for _, k := range []string{"session-fork", "session-source"} {
		if _, ok := keys[k]; !ok {
			t.Fatalf("no %s among %v", k, keys)
		}
	}
	if len(keys) != 2 {
		t.Fatalf("sessions %v, want the fork and its source", keys)
	}
}
