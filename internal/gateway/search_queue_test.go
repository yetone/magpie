package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// #2316 (clxsh, their test as filed): Codex includes web_search even when
// it asks for no search. That selects searchReply: queue progress must survive translation before any content,
// including the fallback writer's initial hold. Release the fake queue only
// after the client receives progress, so content cannot hide a lost heartbeat.
func TestSearchQueueKeepsCodexAlive(t *testing.T) {
	keepFast(t, 10*time.Millisecond, 20*time.Millisecond, 100*time.Millisecond)
	held, quiet, watch, silent := keepHeldAfter, agentQuietMost, watchEvery, translatedQuietLongest
	keepHeldAfter, agentQuietMost, watchEvery, translatedQuietLongest = 10*time.Millisecond, 50*time.Millisecond, 5*time.Millisecond, 40*time.Millisecond
	t.Cleanup(func() { keepHeldAfter, agentQuietMost, watchEvery, translatedQuietLongest = held, quiet, watch, silent })
	for _, mode := range []string{"comments", "progress", "cancel", "silent"} {
		t.Run(mode, func(t *testing.T) {
			fresh(t)
			release, stopped := make(chan struct{}), make(chan struct{})
			proceed := make(chan struct{})
			var once, onceGo sync.Once
			finish := func() { once.Do(func() { close(release) }) }
			// #5666: a client that fails before go (t.Fatal on a loaded
			// runner) must not leave the handler on proceed, or up.Close
			// waits for it until the package alarm.
			goOn := func() { onceGo.Do(func() { close(proceed) }) }
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(stopped)
				var q struct{ Tools []struct{ Type, Name string } }
				if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
					t.Error(err)
					return
				}
				search, client := false, false
				for _, tool := range q.Tools {
					search = search || tool.Type == "function" && tool.Name == "web_search"
					client = client || tool.Type == "function" && tool.Name == "get_value"
				}
				if !search || !client {
					t.Error("search translation lost the search or client tool")
					http.Error(w, "missing tools", 400)
					return
				}
				select {
				case <-proceed:
				case <-r.Context().Done():
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "data: "+`{"type":"response.created","response":{"id":"queued","model":"m1","status":"in_progress"}}`+"\n\n")
				w.(http.Flusher).Flush()
				tick := time.NewTicker(20 * time.Millisecond)
				defer tick.Stop()
				pings := 0
				for {
					select {
					case <-r.Context().Done():
						return
					case <-tick.C:
						if mode == "silent" && pings > 0 {
							continue
						}
						pings++
						if mode == "comments" {
							io.WriteString(w, ": keepalive\n\n")
						} else {
							io.WriteString(w, "data: "+`{"type":"response.in_progress","response":{"id":"queued","status":"in_progress"}}`+"\n\n")
						}
						w.(http.Flusher).Flush()
					case <-release:
						io.WriteString(w, "data: "+`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"get_value","arguments":""}}`+"\n\n")
						io.WriteString(w, "data: "+`{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"fc_1","delta":"{}"}`+"\n\n")
						io.WriteString(w, "data: "+`{"type":"response.completed","response":{"id":"queued","status":"completed","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"get_value","arguments":"{}"}],"usage":{"input_tokens":10,"output_tokens":2}}}`+"\n\n")
						w.(http.Flusher).Flush()
						return
					}
				}
			}))
			defer up.Close()
			defer finish()
			defer goOn()
			if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "fixture", Models: []string{"m1"}, Responses: up.URL + "/v1"}); err != nil {
				t.Fatal(err)
			}
			if err := provider.SetSearchAPI(provider.SearchAPI{Vendor: "searxng", URL: up.URL + "/unused-search"}); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(New().Handler())
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, "POST", server.URL+CodexPath+"/responses", strings.NewReader(`{"model":"fake/m1","stream":true,"input":"Use get_value","tools":[{"type":"web_search"},{"type":"function","name":"get_value","parameters":{"type":"object","properties":{}}}]}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer magpie")
			// The idle deadline is much longer than the simulated queue tick;
			// comments cannot reset it. A hard cap also catches runaway pings.
			hard := time.AfterFunc(5*time.Second, cancel)
			defer hard.Stop()
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			// #2547: the idle window starts at the first data line, not at
			// the headers: a loaded machine's path to the first translated
			// event is not the client's idle, and the hard cap still ends a
			// stream that never speaks.
			var idle *time.Timer
			defer func() {
				if idle != nil {
					idle.Stop()
				}
			}()
			// Hold the queue's first event back past the fixed window the
			// idle once started at headers with, so silent keeps a red if
			// the window ever starts ahead of the state again.
			if mode == "silent" {
				time.Sleep(500 * time.Millisecond)
			}
			goOn()
			var body strings.Builder
			pings, completed := 0, false
			sc := bufio.NewScanner(res.Body)
			for sc.Scan() {
				line := sc.Text()
				body.WriteString(line + "\n")
				if strings.HasPrefix(line, "data:") {
					if idle == nil {
						idle = time.AfterFunc(300*time.Millisecond, cancel)
					} else {
						idle.Reset(300 * time.Millisecond)
					}
				}
				if strings.Contains(line, `"type":"response.in_progress"`) {
					pings++
					if pings >= 3 {
						if mode == "cancel" {
							cancel()
						}
						if mode == "comments" || mode == "progress" {
							finish()
						}
					}
				}
				if strings.Contains(line, `"type":"response.completed"`) {
					completed = true
					break
				}
			}
			cancel()
			select {
			case <-stopped:
			case <-time.After(2 * time.Second):
				t.Fatal("client cancellation did not close upstream")
			}
			if mode == "silent" {
				if completed || pings == 0 || pings > 12 {
					t.Fatalf("silent upstream covered indefinitely: pings=%d complete=%v", pings, completed)
				}
			} else if mode == "cancel" {
				if pings < 3 || completed {
					t.Fatalf("queue cancellation: pings=%d complete=%v", pings, completed)
				}
			} else if pings < 3 || !completed || !strings.Contains(body.String(), `"name":"get_value"`) || !strings.Contains(body.String(), `"call_id":"call_1"`) {
				t.Fatalf("queue progress or native tool call lost: pings=%d complete=%v\n%s", pings, completed, body.String())
			}
		})
	}
}
