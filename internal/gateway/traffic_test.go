package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// The gateway's running byte counts (Server.Traffic, which Usage's live
// rate reads) take what each request's row takes, a refused try's bytes
// too, whole or streamed, and nothing twice (songlairui on X: how much
// bandwidth the gateway is using).
func TestTrafficCountsWhatRowsCount(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "whole", true: "stream"}[stream], func(t *testing.T) {
			fresh(t)
			overloaded := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				io.WriteString(w, `{"error":{"message":"overloaded"}}`)
			}))
			t.Cleanup(overloaded.Close)
			ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, sse(`data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"}}]}`, `data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`, `data: [DONE]`))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, chatOK)
			}))
			t.Cleanup(ok.Close)
			for _, p := range []provider.Provider{
				{ID: "a", Name: "A", Key: "k", Models: []string{"m"}, Chat: overloaded.URL + "/v1"},
				{ID: "b", Name: "B", Key: "k", Models: []string{"m"}, Chat: ok.URL + "/v1"},
			} {
				if err := provider.Save(p); err != nil {
					t.Fatal(err)
				}
			}
			if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
				t.Fatal(err)
			}
			s := New()
			if got := s.Traffic(); got != (Traffic{}) {
				t.Fatalf("a new gateway has counted %+v", got)
			}
			code, body := postAs(t, s, "", fmt.Sprintf(`{"model":"group/g","stream":%v,"messages":[{"role":"user","content":"hi"}]}`, stream))
			if code != 200 || !strings.Contains(body, "hello") {
				t.Fatalf("%d %s", code, body)
			}
			route := lastRoute(s).ID
			row := rowOf(t, func(u usage.Record) bool { return u.RouteID == route })
			if row.UpBytes == 0 || row.DownBytes == 0 {
				t.Fatalf("row: up %d, down %d", row.UpBytes, row.DownBytes)
			}
			if got := s.Traffic(); got.Up != int64(row.UpBytes) || got.Down != int64(row.DownBytes) {
				t.Fatalf("traffic %+v; the row: up %d, down %d", got, row.UpBytes, row.DownBytes)
			}
		})
	}
}

// A reply is counted as it streams in, not when it ends: while the
// vendor holds its stream open after the first event, the gateway's down
// count already has that event's bytes.
func TestTrafficCountsAStreamAsItFlows(t *testing.T) {
	fresh(t)
	first := `data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"}}]}` + "\n\n"
	rest := sse(`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`, `data: [DONE]`)
	release := make(chan struct{})
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, first)
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		io.WriteString(w, rest)
	}))
	t.Cleanup(vendor.Close)
	var once bool
	free := func() {
		if !once {
			once = true
			close(release)
		}
	}
	t.Cleanup(free)
	if err := provider.Save(provider.Provider{ID: "b", Name: "B", Key: "k", Models: []string{"m"}, Chat: vendor.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	s := New()
	done := make(chan struct{})
	go func() {
		defer close(done)
		postAs(t, s, "", `{"model":"b/m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for s.Traffic().Down < int64(len(first)) {
		if time.Now().After(deadline) {
			free()
			<-done
			t.Fatalf("mid-stream traffic %+v; the vendor had sent %d bytes", s.Traffic(), len(first))
		}
		time.Sleep(5 * time.Millisecond)
	}
	mid := s.Traffic()
	if mid.Up == 0 {
		t.Fatalf("mid-stream traffic %+v: nothing counted up", mid)
	}
	free()
	<-done
	if end := s.Traffic(); end.Down != mid.Down+int64(len(rest)) {
		t.Fatalf("traffic mid-stream %+v, at the end %+v; the rest was %d bytes", mid, end, len(rest))
	}
}

// A Codex turn on the Responses WebSocket is in the gateway's count as
// what the socket was written, as its row is, not the HTTP body built
// before the transport chose the socket; one that went over HTTP after a
// refused handshake is in it as that body (#5781's 13 MB rows, live).
func TestTrafficCountsWhatTheSocketSent(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    *fakeSocketBackend
	}{
		{"kept socket", &fakeSocketBackend{keep: true}},
		{"refused, over HTTP", &fakeSocketBackend{refuse: http.StatusForbidden, whole: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			socketOn(t)
			f := tc.f
			f.frames = keptReply(f)
			chatgpt(t, f.ServeHTTP)
			s := New()
			start := time.Now()
			big := strings.Replace(userPing, "ping", strings.Repeat("x", 50_000), 1)
			for _, body := range []string{
				keptTurn("conv-1", "gpt-5.5-ws", big),
				keptTurn("conv-1", "gpt-5.5-ws", big, asstPong, userAgain),
			} {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(body))
				req.Header.Set("Authorization", "Bearer chatgpt-token-acct-1")
				req.Header.Set("chatgpt-account-id", "acct-1")
				s.Handler().ServeHTTP(rec, req)
				if rec.Code != 200 || !strings.Contains(rec.Body.String(), "pong") {
					t.Fatalf("%d %s", rec.Code, rec.Body.String())
				}
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
			up := 0
			for _, r := range rows {
				up += r.UpBytes
			}
			posts, sockets := f.counts()
			if f.refuse == 0 && (posts != 0 || sockets != 2) || f.refuse != 0 && posts != 2 {
				t.Fatalf("%d posts, %d socket turns", posts, sockets)
			}
			if got := s.Traffic().Up; got != int64(up) || up == 0 {
				t.Fatalf("traffic up %d; the rows: %d", got, up)
			}
			if f.refuse == 0 {
				if want := len(f.turn(0).msg) + len(f.turn(1).msg); up != want {
					t.Fatalf("rows and traffic up %d; the socket was written %d", up, want)
				}
			} else if up < 100_000 {
				t.Fatalf("rows and traffic up %d; two HTTP bodies of over 50,000 went", up)
			}
		})
	}
}
