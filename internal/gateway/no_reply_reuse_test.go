package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// RookieRicardoR on X: the same prompt sent to DeepSeek from Claude Code,
// then from Pi, came back with the same words, and the reply was taken for
// one magpie kept and gave again. magpie keeps no reply to give a later
// request: each request, the same bytes from the same agent too, or from
// another agent and protocol, is asked of the vendor, and answered with
// what the vendor said to it.
func TestSameRequestIsAskedEveryTime(t *testing.T) {
	fresh(t)
	var mu sync.Mutex
	asked := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		mu.Lock()
		asked++
		n := asked
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		text := fmt.Sprintf("answer number %d", n)
		if strings.HasSuffix(r.URL.Path, "/messages") {
			fmt.Fprintf(w, `{"id":"msg_%d","type":"message","role":"assistant","model":"deepseek-v4","content":[{"type":"text","text":%q}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":3}}`, n, text)
			return
		}
		fmt.Fprintf(w, `{"id":"c%d","object":"chat.completion","model":"deepseek-v4","choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":3}}`, n, text)
	}))
	defer up.Close()
	if err := provider.Save(provider.Provider{ID: "ds", Name: "DS", Key: "k", Models: []string{"deepseek-v4"},
		Chat: up.URL + "/v1", Anthropic: up.URL + "/anthropic"}); err != nil {
		t.Fatal(err)
	}
	const prompt = "write a haiku about magpies"
	claude := `{"model":"ds/deepseek-v4","max_tokens":64,"messages":[{"role":"user","content":"` + prompt + `"}]}`
	chat := `{"model":"ds/deepseek-v4","messages":[{"role":"user","content":"` + prompt + `"}]}`
	var replies []string
	for i, q := range []struct{ path, body string }{
		{"/v1/messages", claude}, {"/v1/messages", claude}, {"/v1/chat/completions", chat}, {"/v1/chat/completions", chat},
	} {
		code, body := post(t, q.path, q.body)
		if code != http.StatusOK {
			t.Fatalf("request %d (%s): %d %s", i, q.path, code, body)
		}
		replies = append(replies, body)
	}
	if asked != len(replies) {
		t.Fatalf("the vendor was asked %d times for %d requests", asked, len(replies))
	}
	for i, body := range replies {
		if want := fmt.Sprintf("answer number %d", i+1); !strings.Contains(body, want) {
			t.Errorf("request %d answered %s, want the vendor's own answer to it (%q)", i, body, want)
		}
	}
}
