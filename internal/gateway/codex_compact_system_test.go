package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// chatgptSystem400 is what the ChatGPT backend answers a request carrying
// the system role with, passed through the relay above it unchanged
// (jian-cui's sub2api error log, #5067).
const chatgptSystem400 = `{"detail":"System messages are not allowed"}`

// sub2apiSystem400 is the same refusal as a current sub2api words it to
// its client (writeOpenAIUpstreamClientError: the upstream's detail as the
// message, the type invalid_request_error), and sub2apiOld400 as the
// reporter's sub2api did, every refusal "Upstream request failed"
// (app.log: "answered 400 (sub2api: Upstream request failed)").
const (
	sub2apiSystem400 = `{"error":{"type":"invalid_request_error","message":"System messages are not allowed"}}`
	sub2apiOld400    = `{"error":{"type":"upstream_error","message":"Upstream request failed"}}`
)

// systemCompaction is a Codex compaction whose history begins with Codex's
// developer message, as every one of Codex's does.
const systemCompaction = `{"model":"fake/m1","stream":true,"store":false,"prompt_cache_key":"thread-5067",
  "input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"You are Codex, a coding agent."}]},
  {"type":"message","role":"user","content":[{"type":"input_text","text":"fix the login bug"}]},
  {"type":"message","role":"assistant","content":[{"type":"output_text","text":"I fixed main.go"}]},
  {"type":"compaction_trigger"}]}`

var summaryReply = sse(
	`data: {"type":"response.created","response":{"id":"resp_v1","status":"in_progress","output":[]}}`,
	`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"SUMMARY"}]}}`,
	`data: {"type":"response.completed","response":{"id":"resp_v1","status":"completed","output":[]}}`)

// codexRelay puts the fake provider on a relay of a ChatGPT account: a
// body carrying the system role is answered with refusal, anything else
// is summarised. It records each body it was sent.
func codexRelay(t *testing.T, refusal func(w http.ResponseWriter)) *[]string {
	t.Helper()
	setup(t, provider.Responses, &fake{t: t})
	var mu sync.Mutex
	got := &[]string{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		*got = append(*got, string(b))
		mu.Unlock()
		if strings.Contains(string(b), `"role":"system"`) {
			refusal(w)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, summaryReply)
	}))
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Responses: up.URL + "/v1"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	return got
}

func refuseWith(status int, body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}
}

func errorEvent(typ, msg string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\""+typ+"\",\"message\":\""+msg+"\"}}\n\n")
	}
}

// #5067: Codex on a plugin provider whose Responses endpoint is a relay
// of a ChatGPT account: the summary ask went out with its developer
// messages as system ones (forVendor, #2955) and the backend behind the
// relay refused every one, 400 "System messages are not allowed", three
// of the failures holding the conversation (compactFails) — while the
// turns around it, developer as they came, are taken. The summary is
// asked again with the roles as the client sent them, and the backend
// takes it the way it takes the turns: the model's summary of the
// conversation's items, not of it as text. Each wording a relay passes
// the refusal on in, and an error event after a 200, which had skipped
// every retry and counted toward the guard.
func TestCodexCompactSystemRefusedAsksAgainDeveloper(t *testing.T) {
	for name, refusal := range map[string]func(http.ResponseWriter){
		"passed through":              refuseWith(400, chatgptSystem400),
		"sub2api":                     refuseWith(400, sub2apiSystem400),
		"older sub2api":               refuseWith(400, sub2apiOld400),
		"in the stream":               errorEvent("invalid_request_error", "System messages are not allowed"),
		"older sub2api in the stream": errorEvent("upstream_error", "Upstream request failed"),
	} {
		t.Run(name, func(t *testing.T) {
			got := codexRelay(t, refusal)
			code, body := codexPost(t, systemCompaction)
			if code != 200 || !strings.Contains(body, `"compaction"`) {
				t.Fatalf("%d %s\nupstream got %q", code, body, *got)
			}
			if sum := compactedText(t, body); sum != "SUMMARY" {
				t.Errorf("summary %q", sum)
			}
			// serve asks again itself after an "Upstream request failed"
			// in a stream, as after any reply broken off: the last ask is
			// the one taken
			if len(*got) < 2 || !strings.Contains(name, "older sub2api in") && len(*got) != 2 {
				t.Fatalf("asks: %d %q", len(*got), *got)
			}
			last := (*got)[len(*got)-1]
			if strings.Contains(last, `"role":"system"`) || !strings.Contains(last, `"role":"developer"`) ||
				strings.Contains(last, "This is the conversation so far") {
				t.Errorf("asked again with: %s", last)
			}
		})
	}
}

// Refused as sent too, the summary is asked as text, and made by magpie
// when that is refused as well: compaction after compaction goes on, and
// the conversation is never held (compactFails). An error event after a
// 200 the same.
func TestCodexCompactSystemRefusedEverywhereNeverHolds(t *testing.T) {
	for name, refusal := range map[string]func(http.ResponseWriter){
		"status":    refuseWith(400, sub2apiSystem400),
		"in stream": errorEvent("upstream_error", "Upstream request failed"),
	} {
		t.Run(name, func(t *testing.T) {
			setup(t, provider.Responses, &fake{t: t})
			var mu sync.Mutex
			calls := 0
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				mu.Lock()
				calls++
				mu.Unlock()
				refusal(w)
			}))
			t.Cleanup(up.Close)
			if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Responses: up.URL + "/v1"}); err != nil {
				t.Fatal(err)
			}
			s := New()
			for i := range compactFailsMost + 1 {
				mu.Lock()
				calls = 0
				mu.Unlock()
				rec := codexPostTo(s, systemCompaction)
				if rec.Code != 200 {
					t.Fatalf("compaction %d: %d %s", i+1, rec.Code, rec.Body)
				}
				if sum := compactedText(t, rec.Body.String()); !strings.Contains(sum, "fix the login bug") {
					t.Errorf("compaction %d: local summary %s", i+1, sum)
				}
				// as it came (system), as sent (developer), as text; serve
				// asks each again itself after a stream broken off
				mu.Lock()
				n := calls
				mu.Unlock()
				if name == "status" && n != 3 || n < 3 {
					t.Errorf("compaction %d: asks %d", i+1, n)
				}
			}
		})
	}
}
