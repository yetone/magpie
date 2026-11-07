package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The Cline plugin's provider has ClinePass's Upstream tick too (ARNO on
// Discord). Since 2026-11-06 Cline answers providerOptions with 403
// (#5442), so with the tick saved on, a DeepSeek or GLM model's request
// reaches the plugin as sent, on Chat and Anthropic Messages alike, and
// only glm-5.3-flash carries OpenRouter's provider.only, which Cline
// still takes; its other models' requests go as sent too.
func TestClinePluginTickSendsNoProviderOptions(t *testing.T) {
	t.Setenv("FAKE_DEEPSEEK", "1")
	t.Setenv("FAKE_GLM", "1")
	var mu sync.Mutex
	var sent []map[string]any
	pid := besideFake(t, "cline", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q map[string]any
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &q)
		mu.Lock()
		sent = append(sent, q)
		mu.Unlock()
		if q["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`, `data: [DONE]`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
	}))
	p, err := provider.Find(pid)
	if err != nil {
		t.Fatal(err)
	}
	ds, _ := p.ClinePin("deepseek-v4-pro")
	glm, _ := p.ClinePin("glm-5.3")
	if !p.ClinePinnable() || ds != "" || glm != "" {
		t.Fatalf("the Cline plugin's provider: pinnable %v, pinned unasked %q %q", p.ClinePinnable(), ds, glm)
	}
	ask := func() []map[string]any {
		mu.Lock()
		sent = nil
		mu.Unlock()
		tries, last := 0, []int{}
		for _, req := range []struct{ path, body string }{
			{"/v1/chat/completions", `{"model":"` + pid + `/deepseek-v4-pro","messages":[{"role":"user","content":"hi"}]}`},
			{"/v1/messages", `{"model":"` + pid + `/deepseek-v4-pro","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`},
			{"/v1/chat/completions", `{"model":"` + pid + `/glm-5.3","messages":[{"role":"user","content":"hi"}]}`},
			{"/v1/messages", `{"model":"` + pid + `/glm-5.3","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`},
			{"/v1/chat/completions", `{"model":"` + pid + `/fake-1","messages":[{"role":"user","content":"hi"}]}`},
			{"/v1/chat/completions", `{"model":"` + pid + `/glm-5.3-flash","messages":[{"role":"user","content":"hi"}]}`},
		} {
			// each ask is one request, unless magpie tried it again: then
			// it says why, and a try the vendor answered is never sent twice
			rt := postProto(t, New(), req.path, req.body)
			tries += len(rt.Tries)
			mu.Lock()
			last = append(last, len(sent)-1)
			mu.Unlock()
			for i, try := range rt.Tries {
				if i > 0 {
					t.Logf("%s %s: tried again after %d %q", req.path, req.body, rt.Tries[i-1].Status, rt.Tries[i-1].Error)
				}
				if try.Status == 200 && i < len(rt.Tries)-1 {
					t.Fatalf("%s %s: a try the vendor answered 200 was sent again: %+v", req.path, req.body, rt.Tries)
				}
			}
		}
		mu.Lock()
		defer mu.Unlock()
		if len(sent) != tries {
			t.Fatalf("the plugin sent %d requests for 6 asks tried %d times", len(sent), tries)
		}
		// each ask's request as it last went
		var got []map[string]any
		for n, i := range last {
			if i < 0 || (n > 0 && i == last[n-1]) {
				t.Fatalf("ask %d reached the plugin's vendor with no request", n)
			}
			got = append(got, sent[i])
		}
		return got
	}
	for i, q := range ask() {
		if q["providerOptions"] != nil {
			t.Fatalf("request %d pinned with the tick off: %v", i, q["providerOptions"])
		}
		if q["provider"] != nil {
			t.Fatalf("request %d pinned with the tick off: provider %v", i, q["provider"])
		}
	}

	// the tick, saved as the editor saves it, is kept on the account
	p.PinUpstream = true
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	if p, err = provider.Find(pid); err != nil || !p.PinUpstream {
		t.Fatalf("the tick wasn't kept: %+v, %v", p, err)
	}
	for _, m := range []string{"deepseek-v4-pro", "glm-5.3", "cline-pass/deepseek-v4-pro", "cline-pass/deepseek-v4.1-flash-1017", "cline-pass/glm-5.3"} {
		if up, _ := p.ClinePin(m); up != "" {
			t.Errorf("%s: pinned to %q, which only providerOptions could say", m, up)
		}
	}
	got := ask()
	for i, q := range got[:4] {
		if q["providerOptions"] != nil || q["provider"] != nil {
			t.Fatalf("request %d reached the plugin with providerOptions %v provider %v, want neither", i, q["providerOptions"], q["provider"])
		}
	}
	if got[4]["providerOptions"] != nil || got[4]["provider"] != nil {
		t.Fatalf("fake-1 pinned: %v %v", got[4]["providerOptions"], got[4]["provider"])
	}
	if po, _ := json.Marshal(got[5]["provider"]); string(po) != `{"only":["z-ai"]}` || got[5]["providerOptions"] != nil {
		t.Fatalf("glm-5.3-flash reached the plugin with provider %s and providerOptions %v, want provider.only [z-ai] alone", po, got[5]["providerOptions"])
	}
}
