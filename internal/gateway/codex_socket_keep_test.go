package gateway

import (
	"fmt"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/usage"
)

// keptReply answers a turn as the backend does: one assistant message,
// then response.completed with an id of the turn's count.
func keptReply(f *fakeSocketBackend) func(socketTurn) []string {
	return func(turn socketTurn) []string {
		f.mu.Lock()
		n := len(f.sockets)
		f.mu.Unlock()
		return []string{
			fmt.Sprintf(`{"type":"response.created","response":{"id":"resp-%d"}}`, n),
			`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_a","type":"message","status":"in_progress","role":"assistant","content":[]}}`,
			`{"type":"response.output_text.delta","delta":"pong"}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_a","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":"pong"}]}}`,
			fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp-%d","usage":{"input_tokens":7,"output_tokens":1}}}`, n),
		}
	}
}

const (
	userPing  = `{"type":"message","role":"user","content":[{"type":"input_text","text":"ping"}]}`
	asstPong  = `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"pong"}]}`
	userAgain = `{"type":"message","role":"user","content":[{"type":"input_text","text":"again"}]}`
)

func keptTurn(conv, model string, items ...string) string {
	return `{"model":"` + model + `","stream":true,"store":false,"prompt_cache_key":"` + conv +
		`","instructions":"be brief","input":[` + strings.Join(items, ",") + `]}`
}

func keptPost(t *testing.T, acct, body string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer chatgpt-token-"+acct)
	req.Header.Set("chatgpt-account-id", acct)
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "pong") ||
		!strings.Contains(rec.Body.String(), "event: response.completed\n") {
		t.Errorf("%d, a reply without its end: %s", rec.Code, rec.Body.String())
	}
}

func (f *fakeSocketBackend) opened() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.handshakes
}

func (f *fakeSocketBackend) turn(i int) socketTurn {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sockets[i]
}

// rem_thank on X: Codex sends a turn on its kept socket as only its new
// input after previous_response_id, while magpie sent the whole
// conversation on a new socket every turn, and a long one was closed 1009
// as too big. A conversation's next turn now goes on its kept socket as a
// delta, and one that doesn't carry on from the last goes whole.
func TestCodexKeptSocketSendsTheDelta(t *testing.T) {
	socketOn(t)
	f := &fakeSocketBackend{keep: true}
	f.frames = keptReply(f)
	chatgpt(t, f.ServeHTTP)

	keptPost(t, "acct-1", keptTurn("conv-1", "gpt-5.5", userPing))
	keptPost(t, "acct-1", keptTurn("conv-1", "gpt-5.5", userPing, asstPong, userAgain))

	if posts, sockets := f.counts(); posts != 0 || sockets != 2 || f.opened() != 1 {
		t.Fatalf("%d posts, %d turns on %d sockets; want both turns on one socket", posts, sockets, f.opened())
	}
	first, second := gjson.ParseBytes(f.turn(0).msg), gjson.ParseBytes(f.turn(1).msg)
	if first.Get("previous_response_id").Exists() || len(first.Get("input").Array()) != 1 {
		t.Errorf("first turn: %s", f.turn(0).msg)
	}
	if got := second.Get("previous_response_id").String(); got != "resp-1" {
		t.Errorf("previous_response_id %q, want resp-1: %s", got, f.turn(1).msg)
	}
	in := second.Get("input").Array()
	if len(in) != 1 || in[0].Get("content.0.text").String() != "again" {
		t.Errorf("delta input %s, want only the new user message", second.Get("input").Raw)
	}
	if !strings.HasPrefix(string(f.turn(1).msg), `{"type":"response.create",`) ||
		second.Get("model").String() != "gpt-5.5" || second.Get("instructions").String() != "be brief" {
		t.Errorf("delta lost the turn's fields: %s", f.turn(1).msg)
	}

	// an edited history goes whole, on the same socket
	edited := strings.Replace(userPing, "ping", "PING", 1)
	keptPost(t, "acct-1", keptTurn("conv-1", "gpt-5.5", edited, asstPong, userAgain, asstPong, userAgain))
	third := gjson.ParseBytes(f.turn(2).msg)
	if third.Get("previous_response_id").Exists() || len(third.Get("input").Array()) != 5 {
		t.Errorf("edited history went as a delta: %s", f.turn(2).msg)
	}
	// another model goes whole too
	keptPost(t, "acct-1", keptTurn("conv-1", "gpt-5.4", edited, asstPong, userAgain, asstPong, userAgain, asstPong, userAgain))
	fourth := gjson.ParseBytes(f.turn(3).msg)
	if fourth.Get("previous_response_id").Exists() || len(fourth.Get("input").Array()) != 7 {
		t.Errorf("another model went as a delta: %s", f.turn(3).msg)
	}
	if f.turn(3).socket != 1 {
		t.Errorf("turn on socket %d, want the kept one", f.turn(3).socket)
	}
}

// A backend that lost the response (previous_response_not_found) is
// asked again whole, on a new socket, and the turn ends as it should.
func TestCodexKeptSocketLostResponseGoesWhole(t *testing.T) {
	socketOn(t)
	f := &fakeSocketBackend{keep: true}
	reply := keptReply(f)
	f.frames = func(turn socketTurn) []string {
		if gjson.GetBytes(turn.msg, "previous_response_id").Exists() {
			return []string{`{"type":"error","status":400,"error":{"type":"invalid_request_error","code":"previous_response_not_found","message":"Previous response with id 'resp-1' not found."}}`}
		}
		return reply(turn)
	}
	chatgpt(t, f.ServeHTTP)

	keptPost(t, "acct-1", keptTurn("conv-1", "gpt-5.5", userPing))
	keptPost(t, "acct-1", keptTurn("conv-1", "gpt-5.5", userPing, asstPong, userAgain))
	if posts, sockets := f.counts(); posts != 0 || sockets != 3 || f.opened() != 2 {
		t.Fatalf("%d posts, %d turns on %d sockets; want the refused delta again whole on a second socket", posts, sockets, f.opened())
	}
	last := f.turn(2)
	if last.socket != 2 || gjson.GetBytes(last.msg, "previous_response_id").Exists() ||
		len(gjson.GetBytes(last.msg, "input").Array()) != 3 {
		t.Errorf("retry on socket %d: %s", last.socket, last.msg)
	}
}

// A kept socket the backend closed meanwhile is found closed on the next
// turn, which goes whole on a new socket.
func TestCodexKeptSocketClosedMeanwhileGoesWhole(t *testing.T) {
	socketOn(t)
	f := &fakeSocketBackend{} // closes after each turn
	f.frames = keptReply(f)
	chatgpt(t, f.ServeHTTP)

	keptPost(t, "acct-1", keptTurn("conv-1", "gpt-5.5", userPing))
	keptPost(t, "acct-1", keptTurn("conv-1", "gpt-5.5", userPing, asstPong, userAgain))
	if posts, sockets := f.counts(); posts != 0 || sockets != 2 || f.opened() != 2 {
		t.Fatalf("%d posts, %d turns on %d sockets", posts, sockets, f.opened())
	}
	if m := f.turn(1).msg; gjson.GetBytes(m, "previous_response_id").Exists() {
		t.Errorf("a new socket was sent the delta: %s", m)
	}
}

// Another account's turn of the same conversation never goes on the
// first account's socket, nor names its response.
func TestCodexKeptSocketIsTheAccounts(t *testing.T) {
	socketOn(t)
	f := &fakeSocketBackend{keep: true}
	f.frames = keptReply(f)
	chatgpt(t, f.ServeHTTP)

	keptPost(t, "acct-1", keptTurn("conv-1", "gpt-5.5", userPing))
	keptPost(t, "acct-2", keptTurn("conv-1", "gpt-5.5", userPing, asstPong, userAgain))
	if _, sockets := f.counts(); sockets != 2 || f.opened() != 2 {
		t.Fatalf("%d turns on %d sockets; want each account its own", sockets, f.opened())
	}
	second := f.turn(1)
	if second.header.Get("chatgpt-account-id") != "acct-2" || second.socket != 2 ||
		gjson.GetBytes(second.msg, "previous_response_id").Exists() {
		t.Errorf("acct-2's turn on socket %d as %s: %s", second.socket, second.header.Get("chatgpt-account-id"), second.msg)
	}
}

// A turn that names no conversation leaves no socket kept, and a kept
// one is let go once it has waited codexKeepIdle.
func TestCodexKeptSocketLetGo(t *testing.T) {
	socketOn(t)
	was := codexKeepIdle
	codexKeepIdle = 20 * time.Millisecond
	t.Cleanup(func() { codexKeepIdle = was })
	f := &fakeSocketBackend{keep: true}
	f.frames = keptReply(f)
	chatgpt(t, f.ServeHTTP)

	keptPost(t, "acct-1", `{"model":"gpt-5.5","stream":true,"input":[`+userPing+`]}`)
	if n := codexKeptCount(); n != 0 {
		t.Fatalf("%d sockets kept for a turn with no conversation", n)
	}
	keptPost(t, "acct-1", keptTurn("conv-1", "gpt-5.5", userPing))
	for deadline := time.Now().Add(10 * time.Second); codexKeptCount() != 0; {
		if time.Now().After(deadline) {
			t.Fatal("the kept socket was never let go")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Two turns of one conversation at once each have a socket of their own:
// the kept one is taken by one turn only.
func TestCodexKeptSocketTakenOnce(t *testing.T) {
	socketOn(t)
	f := &fakeSocketBackend{keep: true}
	reply := keptReply(f)
	both := make(chan struct{})
	var once sync.Once
	f.frames = func(turn socketTurn) []string {
		f.mu.Lock()
		n := len(f.sockets)
		f.mu.Unlock()
		if n == 3 {
			once.Do(func() { close(both) })
		}
		if n > 1 {
			// neither turn ends before both have begun
			select {
			case <-both:
			case <-time.After(10 * time.Second):
			}
		}
		return reply(turn)
	}
	chatgpt(t, f.ServeHTTP)

	keptPost(t, "acct-1", keptTurn("conv-1", "gpt-5.5", userPing))
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			keptPost(t, "acct-1", keptTurn("conv-1", "gpt-5.5", userPing, asstPong, userAgain))
		}()
	}
	wg.Wait()
	if _, sockets := f.counts(); sockets != 3 || f.opened() != 2 {
		t.Fatalf("%d turns on %d sockets; want the second turn at once on a socket of its own", sockets, f.opened())
	}
	deltas := 0
	for i := 1; i < 3; i++ {
		if gjson.GetBytes(f.turn(i).msg, "previous_response_id").Exists() {
			deltas++
			if f.turn(i).socket != 1 {
				t.Errorf("a delta went on socket %d", f.turn(i).socket)
			}
		}
	}
	if deltas != 1 {
		t.Errorf("%d deltas, want the one turn on the kept socket", deltas)
	}
	if n := codexKeptCount(); n != 1 {
		t.Errorf("%d sockets kept, want the last to end", n)
	}
}

// A turn whose input has other than the last reply's output after the
// last input goes whole: the backend would put its own output there.
func TestCodexKeptSocketOtherReplyGoesWhole(t *testing.T) {
	socketOn(t)
	f := &fakeSocketBackend{keep: true}
	f.frames = keptReply(f)
	chatgpt(t, f.ServeHTTP)

	keptPost(t, "acct-1", keptTurn("conv-1", "gpt-5.5", userPing))
	other := strings.Replace(asstPong, "pong", "something else", 1)
	keptPost(t, "acct-1", keptTurn("conv-1", "gpt-5.5", userPing, other, userAgain))
	if _, sockets := f.counts(); sockets != 2 || f.opened() != 1 {
		t.Fatalf("%d turns on %d sockets; want both on the kept one", sockets, f.opened())
	}
	second := gjson.ParseBytes(f.turn(1).msg)
	if second.Get("previous_response_id").Exists() || len(second.Get("input").Array()) != 3 {
		t.Errorf("a turn after another reply went as a delta: %s", f.turn(1).msg)
	}
}

// matrix0487 on Discord: a request's row said 13 MB went up while its
// kept socket was sent the new input alone. A turn that went over the
// socket counts what the socket was sent, the delta on a kept one, and
// says it went over the socket; one over HTTP says neither.
func TestCodexSocketRowCountsWhatTheSocketSent(t *testing.T) {
	socketOn(t)
	f := &fakeSocketBackend{keep: true}
	f.frames = keptReply(f)
	chatgpt(t, f.ServeHTTP)

	start := time.Now()
	long := strings.Repeat("x", 50_000)
	big := strings.Replace(userPing, "ping", long, 1)
	keptPost(t, "acct-1", keptTurn("conv-1", "gpt-5.5-ws", big))
	keptPost(t, "acct-1", keptTurn("conv-1", "gpt-5.5-ws", big, asstPong, userAgain))
	if posts, sockets := f.counts(); posts != 0 || sockets != 2 || f.opened() != 1 {
		t.Fatalf("%d posts, %d turns on %d sockets; want both turns on one socket", posts, sockets, f.opened())
	}
	var rows []usage.Record
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		rows = rows[:0]
		for _, r := range usage.Load(start) {
			if r.Time.After(start) && r.Model == "gpt-5.5-ws" {
				rows = append(rows, r)
			}
		}
		if len(rows) >= 2 || time.Now().After(deadline) {
			break
		}
	}
	if len(rows) != 2 {
		t.Fatalf("%d rows, want 2", len(rows))
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Time.Before(rows[j].Time) })
	for i, r := range rows {
		if want := len(f.turn(i).msg); !r.Socket || r.UpBytes != want {
			t.Errorf("turn %d: socket %v, %d bytes up; want the socket's %d", i, r.Socket, r.UpBytes, want)
		}
	}
	if rows[1].UpBytes > 1000 {
		t.Errorf("the delta turn counted %d bytes up, the whole conversation", rows[1].UpBytes)
	}
}
