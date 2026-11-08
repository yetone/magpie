package gui

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/sessions"
)

// A request still answering has no reply id: the messages of its turn
// name its branch on the Routing page, as that branch's own file has
// them, and only where one session has the message and agrees with the
// opening. The runtime context dsh sends after the typed message is every
// session's of the folder, so it names no one; a message no session has,
// or another conversation's newest turn's, leaves the opening's session.
func TestDshLiveRequestIsNamedByItsTurn(t *testing.T) {
	list := []sessions.Session{
		{Agent: "dsh", ID: "source", Convs: []string{"magpie-open"}, TurnConvs: []string{"magpie-msg-source", "magpie-context"}},
		{Agent: "dsh", ID: "fork", SeedConvs: []string{"magpie-open"}, TurnConvs: []string{"magpie-msg-fork", "magpie-context"}},
		{Agent: "dsh", ID: "named-fork", ForkOf: "source", TurnConvs: []string{"magpie-msg-named"}},
		{Agent: "dsh", ID: "other", Convs: []string{"magpie-other"}, TurnConvs: []string{"magpie-continue"}},
	}
	of := routeOwners(list)
	for _, c := range []struct {
		last []string
		want string
	}{
		{[]string{"magpie-context", "magpie-msg-fork"}, "fork"},     // in flight in the fork
		{[]string{"magpie-context", "magpie-msg-source"}, "source"}, // in flight in the source
		{[]string{"magpie-msg-named"}, "named-fork"},                // a fork that names its parent
		{[]string{"magpie-context"}, "source"},                      // every session's message: the opening's
		{[]string{"magpie-msg-else"}, "source"},                     // a message no session has
		{[]string{"magpie-continue"}, "source"},                     // another conversation's newest turn
		{nil, "source"},
	} {
		r := gateway.Route{Agent: "dsh", Conv: "magpie-open", LastConvs: c.last}
		if got := ownerOfRoute(r, of); got != c.want {
			t.Errorf("turn %v: %q, want %q", c.last, got, c.want)
		}
	}
	// a reply id still comes first: once the fork's reply is read, it names
	// the fork whatever the turn says
	r := gateway.Route{Agent: "dsh", Conv: "magpie-open", ResponseID: "chatcmpl-fork", LastConvs: []string{"magpie-msg-source"}}
	list[1].ResponseIDs = []string{"chatcmpl-fork"}
	if got := ownerOfRoute(r, routeOwners(list)); got != "fork" {
		t.Errorf("reply and turn: %q, want the reply's, the fork", got)
	}
}

// A request's turn is a reason to read the sessions again only while it is
// answering, and no longer than a reply waits: a message no file keeps as
// sent would list every agent's sessions again each convsFresh for good.
func TestLooseKeysAskForATurnOnlyWhileItAnswers(t *testing.T) {
	now := time.Now()
	turn := ownerKey("dsh", ownerLast, "magpie-msg")
	for _, c := range []struct {
		name string
		r    gateway.Route
		want bool
	}{
		{"answering", gateway.Route{Agent: "dsh", Conv: "magpie-open", LastConvs: []string{"magpie-msg"}, Time: now.Add(-10 * time.Second)}, true},
		{"done", gateway.Route{Agent: "dsh", Conv: "magpie-open", LastConvs: []string{"magpie-msg"}, Time: now.Add(-10 * time.Second), Done: true}, false},
		{"answering past the window", gateway.Route{Agent: "dsh", Conv: "magpie-open", LastConvs: []string{"magpie-msg"}, Time: now.Add(-2 * convsKept)}, false},
	} {
		if got := slices.Contains(looseKeys(c.r, now), turn); got != c.want {
			t.Errorf("%s: asks for its turn %v, want %v", c.name, got, c.want)
		}
	}
}

// The Routing page end to end, on files as dsh writes them: a fork with no
// parentSession, its source, and the runtime context both send after the
// typed message. A request of the fork's still answering is the source's
// until the fork's file has its turn, and the fork's as soon as it does —
// the page reads the sessions again for it, though the opening is known.
func TestRoutingNamesAForkRequestStillAnswering(t *testing.T) {
	sandboxHome(t)
	home := t.TempDir()
	t.Setenv("DSH_HOME", home)
	sessions.Reset()
	t.Cleanup(sessions.Reset)
	resetRouteConvs := func() {
		routeConvs.Lock()
		routeConvs.of, routeConvs.at = nil, time.Time{}
		routeConvs.Unlock()
	}
	resetRouteConvs()
	t.Cleanup(resetRouteConvs)
	defer func(f time.Duration) { convsFresh = f }(convsFresh)
	convsFresh = 0

	user := func(seq, text, kind string) string {
		return `{"type":"user/message","seq":` + seq + `,"time":1000,"data":{"source":{"kind":"` + kind + `"},"content":[{"type":"text","text":"` + text + `"}]}}`
	}
	turn := func(seq, n string) string {
		return `{"type":"turn/start","seq":` + seq + `,"time":1000,"data":{"turn":` + n + `}}`
	}
	id := func(text string) string {
		sum := sha256.Sum256([]byte(`{"role":"user","content":"` + text + `"}`))
		return "magpie-" + hex.EncodeToString(sum[:12])
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
	const endSeed = `{"type":"session/end-seed","seq":4,"time":1000,"data":{"inherited":true}}`
	source := []string{turn("1", "1"), user("2", "same first prompt", "user"), user("3", "runtime context", "runtime-context")}
	write("session-source", `{"type":"session","version":4,"id":"session-source","cwd":"/w","createdAt":1000,"delegationDepth":0,"isSeeded":false}`, source...)
	forkHead := `{"type":"session","version":4,"id":"session-fork","cwd":"/w","createdAt":1000,"delegationDepth":0,"isSeeded":true}`
	write("session-fork", forkHead, append(slices.Clone(source), endSeed)...)

	route := gateway.Route{ID: 1, Agent: "dsh", Conv: id("same first prompt"), Time: time.Now(),
		LastConvs: []string{id("runtime context"), id("the fork's own question")}}
	named := func() string {
		out := pricedRoutes([]gateway.Route{route})[0]
		if out.LastConvs != nil {
			t.Errorf("the page is sent the turn's ids: %v", out.LastConvs)
		}
		return out.Session
	}
	if got := named(); got != "session-source" {
		t.Fatalf("before the fork's file has its turn: %q, want the opening's, the source", got)
	}
	write("session-fork", forkHead, append(slices.Clone(source), endSeed, turn("5", "2"),
		user("6", "the fork's own question", "user"), user("7", "runtime context", "runtime-context"))...)
	if got := named(); got != "session-fork" {
		t.Fatalf("once it has: %q, want the fork", got)
	}
}
