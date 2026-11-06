package gui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/sessions"
)

// #5491 (Flandern1211): a dsh fork's requests were listed under the session
// it was forked from. Here the requests are real dsh 0.2.0-rc.2's, captured
// on its chat-completions route (sessions' testdata/dsh-gateway), sent
// through the gateway twice, byte for byte the same as a fork's and its
// source's are. The vendor answers on the messages protocol and streams, so
// the id dsh reads is magpie's translation of the vendor's. The first reply
// is written into the source's file, the second into a fork of it made as
// dsh's buildForkSeed makes one: the source's events up to the boundary, a
// session/end-seed, then its own turn, under a header naming parentSession.
func TestDshForkNamedByTheReplyTheGatewayGave(t *testing.T) {
	sandboxHome(t)
	fixture := filepath.Join("..", "sessions", "testdata", "dsh-gateway")
	const source = "session-508e7b0a-37c0-41f7-ac62-e48ab3146111"
	const fork = "session-5e1c0f3a-8d2b-4c6e-9a71-2f4b6d8e0c13"

	var n atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		if !strings.HasSuffix(r.URL.Path, "/messages") {
			http.Error(w, "messages only", 404)
			return
		}
		id := "msg_up" + string(rune('0'+n.Add(1)))
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range []string{
			`{"type":"message_start","message":{"id":"` + id + `","type":"message","role":"assistant","model":"m1","content":[],"usage":{"input_tokens":9,"output_tokens":0}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Done."}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
			`{"type":"message_stop"}`,
		} {
			var typ struct{ Type string }
			json.Unmarshal([]byte(ev), &typ)
			io.WriteString(w, "event: "+typ.Type+"\ndata: "+ev+"\n\n")
		}
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "fake", Name: "FAKE", Key: "k", Models: []string{"m1"}, Anthropic: up.URL}); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(filepath.Join(fixture, "requests", "completions-turn.json"))
	if err != nil {
		t.Fatal(err)
	}
	head, body, _ := bytes.Cut(b, []byte("\n"))
	var captured struct {
		Path    string            `json:"path"`
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(head, &captured); err != nil {
		t.Fatal(err)
	}
	// only the model is changed, to one this gateway has
	body = bytes.Replace(body, []byte(`"model":"group/flash"`), []byte(`"model":"fake/m1"`), 1)
	s := gateway.New()
	ask := func() string {
		t.Helper()
		r := httptest.NewRequest("POST", captured.Path, bytes.NewReader(body))
		for k, v := range captured.Headers {
			r.Header.Set(k, v)
		}
		r.Header.Del("Content-Length")
		r.Header.Del("Accept-Encoding")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%d %.300s", w.Code, w.Body)
		}
		// the id dsh reads: its chunks' (pi-ai's responseId)
		sc := bufio.NewScanner(w.Body)
		for sc.Scan() {
			var c struct{ ID string }
			if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok && json.Unmarshal([]byte(d), &c) == nil && c.ID != "" {
				return c.ID
			}
		}
		t.Fatal("no reply id in the stream")
		return ""
	}
	sourceReply, forkReply := ask(), ask()
	if sourceReply == forkReply || strings.HasPrefix(sourceReply, "msg_") {
		t.Fatalf("client ids %q, %q: not two chat-completions ids", sourceReply, forkReply)
	}
	routes := s.Trace(context.Background(), 0, 0).Routes
	if len(routes) != 2 || routes[0].Conv == "" || routes[0].Conv != routes[1].Conv {
		t.Fatalf("routes %+v: want two of one conversation", routes)
	}
	for _, r := range routes {
		if r.Agent != "dsh" || r.Session != "" {
			t.Fatalf("route %d: agent %q session %q", r.ID, r.Agent, r.Session)
		}
		if r.ResponseID != sourceReply && r.ResponseID != forkReply {
			t.Fatalf("route %d keeps %q, the client read %q or %q", r.ID, r.ResponseID, sourceReply, forkReply)
		}
	}

	// dsh writes the replies down: the source's own, then the fork's
	home := t.TempDir()
	if err := os.CopyFS(filepath.Join(home, "sessions"), os.DirFS(filepath.Join(fixture, "sessions"))); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DSH_HOME", home)
	srcPath := filepath.Join(home, "sessions", "--work-proj--", source, "session.v4.jsonl")
	src, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	src = bytes.Replace(src, []byte(`"responseId":"c1"`), []byte(`"responseId":"`+sourceReply+`"`), 1)
	if err := os.WriteFile(srcPath, src, 0o600); err != nil {
		t.Fatal(err)
	}
	writeDshFork(t, filepath.Join(home, "sessions", "--work-proj--", fork), fork, source, src, forkReply)
	sessions.Reset()
	t.Cleanup(sessions.Reset)
	resetRouteConvs(t)

	// old history: a route from before reply ids were kept
	old := routes[0]
	old.ID, old.ResponseID = 99, ""
	want := map[string]string{sourceReply: source, forkReply: fork, "": source}
	priced := pricedRoutes(append(routes, old))
	for _, r := range priced {
		if w := want[r.ResponseID]; r.Session != w {
			t.Errorf("Routing: route %d (reply %q) under %q, want %q", r.ID, r.ResponseID, r.Session, w)
		}
	}
	out := contextOf(append(routes, old), 7)
	keys := map[string]bool{}
	for _, s := range out.Sessions {
		keys[s.Key] = true
	}
	if !keys[source] || !keys[fork] || len(keys) != 2 {
		t.Errorf("Context: sessions %v, want %s and %s", keys, source, fork)
	}
}

// A reply id is the vendor's, and some vendors give one out again (Ollama's
// chatcmpl-<0-998>): a route that opens another session's conversation
// stays that session's though its reply's id is one a fork kept.
func TestDshReplyIDOfAnotherConversationNamesNoOne(t *testing.T) {
	sandboxHome(t)
	of := routeOwners([]sessions.Session{
		{Agent: "dsh", ID: "source", Convs: []string{"magpie-open"}, ResponseIDs: []string{"chatcmpl-1"}},
		{Agent: "dsh", ID: "fork", ForkOf: "source", ResponseIDs: []string{"chatcmpl-217"}},
		{Agent: "dsh", ID: "fork2", ForkOf: "fork", ResponseIDs: []string{"chatcmpl-5"}},
		{Agent: "dsh", ID: "other", Convs: []string{"magpie-other"}, ResponseIDs: []string{"chatcmpl-9"}},
	})
	for _, c := range []struct{ reply, conv, want string }{
		{"chatcmpl-217", "magpie-open", "fork"},   // the fork's own turn
		{"chatcmpl-5", "magpie-open", "fork2"},    // a fork of the fork
		{"chatcmpl-217", "magpie-other", "other"}, // another conversation's, the same id
		{"chatcmpl-9", "magpie-open", "source"},   // another's id on the source's opening
		{"chatcmpl-217", "magpie-unknown", "fork"},
		{"chatcmpl-unknown", "magpie-open", "source"},
		{"", "magpie-open", "source"},
	} {
		if got := ownerOf("dsh", c.reply, c.conv, of); got != c.want {
			t.Errorf("reply %s, conv %s: %q, want %q", c.reply, c.conv, got, c.want)
		}
	}
}

// The Routing page polls every second or two. A dsh reply whose id dsh never
// writes down (a title call's, one that failed) is listed nowhere, and the
// sessions were listed again every convsFresh for as long as such a route
// was on the page; a route's reply is waited for only convsKept after it
// ended.
func TestRoutingWaitsForAReplyIDOnlyAWhile(t *testing.T) {
	sandboxHome(t)
	t.Setenv("DSH_HOME", t.TempDir())
	sessions.Reset()
	t.Cleanup(sessions.Reset)
	resetRouteConvs(t)
	now := time.Now()
	conv := ownerKey("dsh", ownerConv, "magpie-open")
	built := now.Add(-3 * convsFresh)
	routeConvs.Lock()
	routeConvs.of, routeConvs.at = map[string]string{conv: "source"}, built
	routeConvs.Unlock()
	listed := func(r gateway.Route) bool {
		routeSessions(true, looseKeys(r, now))
		routeConvs.Lock()
		defer routeConvs.Unlock()
		again := !routeConvs.at.Equal(built)
		routeConvs.of, routeConvs.at = map[string]string{conv: "source"}, built
		return again
	}
	old := gateway.Route{Agent: "dsh", Conv: "magpie-open", ResponseID: "chatcmpl-title", Done: true, Time: now.Add(-2 * convsKept), Millis: 900}
	if listed(old) {
		t.Fatal("a route ended long ago, whose reply id no session has, listed the sessions again")
	}
	recent := old
	recent.Time = now.Add(-5 * time.Second)
	if !listed(recent) {
		t.Fatal("a route just ended, its reply not yet listed, did not list the sessions again")
	}
}

func resetRouteConvs(t *testing.T) {
	reset := func() {
		routeConvs.Lock()
		routeConvs.of, routeConvs.at = nil, time.Time{}
		routeConvs.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

// writeDshFork writes a fork of the session file src as dsh does
// (buildForkSeed, api-session-controller's fork): every event of src, a
// session/end-seed after them at the last one's time, then a turn of its
// own whose reply has the id given, copied from src's own reply.
func writeDshFork(t *testing.T, dir, id, parent string, src []byte, reply string) {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(src), []byte("\n"))
	var out bytes.Buffer
	out.WriteString(`{"type":"session","version":4,"id":"` + id + `","createdAt":1792664880000,"cwd":"/work/proj","parentSession":"` + parent + `","isSeeded":true}` + "\n")
	var last struct {
		Seq  int   `json:"seq"`
		Time int64 `json:"time"`
	}
	var answer []byte
	for _, l := range lines[1:] {
		out.Write(l)
		out.WriteByte('\n')
		json.Unmarshal(l, &last)
		if bytes.Contains(l, []byte(`"type":"assistant/message"`)) {
			answer = l
		}
	}
	if answer == nil {
		t.Fatal("the source has no reply to copy")
	}
	seq, n := last.Seq+1, strconv.Itoa
	out.WriteString(`{"type":"session/end-seed","seq":` + n(seq) + `,"time":` + strconv.FormatInt(last.Time, 10) + `,"data":{"inherited":true}}` + "\n")
	seq++
	out.WriteString(`{"type":"user/message","seq":` + n(seq) + `,"time":` + strconv.FormatInt(last.Time+int64(seq), 10) + `,"data":{"content":[{"type":"text","text":"Now the tests"}],"source":{"kind":"user"},"role":"user","id":"0b7c1d2e-3f40-4a51-8b62-7c83d94ea5f6"},"surfaceOp":"append"}` + "\n")
	seq++
	var at struct {
		Seq  int   `json:"seq"`
		Time int64 `json:"time"`
	}
	json.Unmarshal(answer, &at)
	was := `"seq":` + n(at.Seq) + `,"time":` + strconv.FormatInt(at.Time, 10)
	if !bytes.Contains(answer, []byte(was)) {
		t.Fatalf("the source's reply has no %s", was)
	}
	own := bytes.Replace(answer, []byte(was), []byte(`"seq":`+n(seq)+`,"time":`+strconv.FormatInt(last.Time+int64(seq), 10)), 1)
	// the reply's own id (replayState.response.responseId), wherever it sits
	var ids struct {
		Data struct {
			Message struct {
				Source struct {
					ReplayState struct {
						Response struct {
							ResponseID string `json:"responseId"`
						} `json:"response"`
					} `json:"replayState"`
				} `json:"source"`
			} `json:"message"`
		} `json:"data"`
	}
	json.Unmarshal(answer, &ids)
	from := ids.Data.Message.Source.ReplayState.Response.ResponseID
	if from == "" {
		t.Fatal("the source's reply has no responseId")
	}
	own = bytes.ReplaceAll(own, []byte(`"responseId":"`+from+`"`), []byte(`"responseId":"`+reply+`"`))
	out.Write(own)
	out.WriteByte('\n')
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.v4.jsonl"), out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}
