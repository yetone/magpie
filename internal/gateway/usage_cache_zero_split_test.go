package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

const (
	zeroSplitBefore = `"cache_creation_input_tokens":30,"cache_creation":{"ephemeral_5m_input_tokens":20,"ephemeral_1h_input_tokens":10}`
	zeroSplitStart  = `"cache_creation_input_tokens":20,"cache_creation":{"ephemeral_5m_input_tokens":15,"ephemeral_1h_input_tokens":5}`
	// zeroSplitBefore1h is the first request's 1h writes in zeroSplitBefore
	zeroSplitBefore1h = 10
	zeroSplitDelta    = `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7,"cache_creation_input_tokens":20,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0}}}`
)

func cacheSplitRoundStart(fields string) string {
	return `{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"m","content":[],"usage":{` + fields + `}}}`
}

func wantCacheLedger(t *testing.T, hour int) {
	t.Helper()
	records := usage.Load(time.Time{})
	if len(records) != 1 {
		t.Fatalf("ledger records = %d, want 1", len(records))
	}
	if u := records[0]; u.CacheWrite != 50 || u.CacheWrite1h != hour {
		t.Errorf("ledger writes: %d/hour%d, want 50/hour%d", u.CacheWrite, u.CacheWrite1h, hour)
	}
}

func TestProbe1717ContinuationTotalZeroSplit(t *testing.T) {
	checkCacheContinuation(t, zeroSplitDelta, 15)
}

func checkCacheContinuation(t *testing.T, delta string, hour int) {
	t.Helper()
	f := &cutAnthropic{
		cuts:  map[int][]string{0: {cacheSplitRoundStart(zeroSplitBefore), anthText("hel")}},
		whole: []string{cacheSplitRoundStart(zeroSplitStart), anthText("lo"), delta, `{"type":"message_stop"}`},
	}
	body := continuationChatServer(t, "m", f)
	if f.calls() != 2 || chatTextOf(body) != "hello" {
		t.Fatalf("continuation failed: calls%d body%s", f.calls(), body)
	}
	if prefill, ok := f.lastAssistantText(t, 1); !ok || prefill != "hel" {
		t.Fatalf("retry did not continue cut reply: %q %t", prefill, ok)
	}
	u := chatUsageOf(body)
	details, _ := u["prompt_tokens_details"].(map[string]any)
	// the client is told the last try's writes, the ledger every try's
	if details["cache_write_tokens"] != 20.0 {
		t.Errorf("client cache writes=%v, want the last try's 20", details["cache_write_tokens"])
	}
	wantCacheLedger(t, hour)
}

func TestProbe1717SearchTotalZeroSplit(t *testing.T) {
	checkCacheSearch(t, zeroSplitDelta, 15)
}

func checkCacheSearch(t *testing.T, delta string, hour int) {
	t.Helper()
	for _, stream := range []bool{true, false} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			fresh(t)
			apis := newSearchAPIs(t)
			if err := provider.SetSearchAPI(apis.api("tavily", "tvly-k")); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var asked []string
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				mu.Lock()
				asked = append(asked, string(b))
				n := len(asked)
				mu.Unlock()
				if n == 1 {
					writeStream(w, []string{
						cacheSplitRoundStart(zeroSplitBefore),
						`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_1","name":"web_search","input":{}}}`,
						`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"latest go\"}"}}`,
						`{"type":"content_block_stop","index":0}`,
						`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`,
						`{"type":"message_stop"}`,
					}, false, "")
					return
				}
				writeStream(w, []string{cacheSplitRoundStart(zeroSplitStart), anthText("answer"), delta, `{"type":"message_stop"}`}, false, "")
			}))
			t.Cleanup(up.Close)
			if err := provider.Save(provider.Provider{ID: "up", Name: "UP", Key: "k", Models: []string{"m"}, Anthropic: up.URL}); err != nil {
				t.Fatal(err)
			}
			body := fmt.Sprintf(`{"model":"up/m","max_tokens":100,"stream":%t,"messages":[{"role":"user","content":"latest go"}],"tools":[{"type":"web_search_20250305","name":"web_search"}]}`, stream)
			r := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
			r.Header.Set("x-api-key", Token)
			r.RemoteAddr = "127.0.0.1:12345"
			w := httptest.NewRecorder()
			lanGuard(New().Handler()).ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("search reply=%d %s", w.Code, w.Body)
			}
			mu.Lock()
			secondRound := len(asked) == 2 && strings.Contains(asked[1], `"tool_result"`) && strings.Contains(asked[1], "https://go.dev/dl/")
			mu.Unlock()
			if !secondRound {
				t.Fatal("model did not receive search results in a second round")
			}
			client := newSniffer(provider.Anthropic, w.Header().Get("Content-Type"))
			client.write(w.Body.Bytes())
			u := client.usage()
			if u.CacheWrite != 20 || u.CacheWrite1h != hour-zeroSplitBefore1h {
				t.Errorf("client writes: %d/hour%d, want the last round's 20/hour%d", u.CacheWrite, u.CacheWrite1h, hour-zeroSplitBefore1h)
			}
			wantCacheLedger(t, hour)
		})
	}
}

func TestProbe1717BridgeTotalZeroSplit(t *testing.T) {
	ev := func(e string) string { return `{"type":"stream_event","event":` + e + `}` }
	before := `"cache_creation_input_tokens":30,"cache_creation":{"ephemeral_5m_input_tokens":20,"ephemeral_1h_input_tokens":10}`
	start := `"cache_creation_input_tokens":20,"cache_creation":{"ephemeral_5m_input_tokens":15,"ephemeral_1h_input_tokens":5}`
	delta := `"cache_creation_input_tokens":20,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0}`
	lines := []string{
		ev(`{"type":"message_start","message":{"id":"m1","model":"x","usage":{` + before + `}}}`),
		ev(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t1","name":"WebSearch"}}`),
		ev(`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`),
		ev(`{"type":"message_stop"}`),
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"results"}]}}`,
		ev(`{"type":"message_start","message":{"id":"m2","model":"x","usage":{` + start + `}}}`),
		ev(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"answer"}}`),
		ev(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7,` + delta + `}}`),
		ev(`{"type":"message_stop"}`),
	}
	run := &subscriptionRun{}
	seg := run.attach()
	done := make(chan struct{})
	go func() { defer close(done); run.readOutput(strings.NewReader(strings.Join(lines, "\n") + "\n")) }()
	var client Usage
	for e := range seg {
		if e.Kind == KStart || e.Kind == KUsage {
			client.add(e.Usage)
		}
	}
	<-done
	// told the second message's, billed both: 10 from the first + 5 from
	// the second's message_start
	if client.CacheWrite1h != 5 || client.billed().CacheWrite1h != 15 {
		t.Errorf("1h writes = %d told, %d billed; want 5 and 15", client.CacheWrite1h, client.billed().CacheWrite1h)
	}
}
