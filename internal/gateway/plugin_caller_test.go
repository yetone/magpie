package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// A plugin is told the agent its request came from (@febsincos on X): its
// fetch by magpie's x-magpie-caller-* headers, its chat.headers hook as
// input.caller, with OpenCode's own input.agent left as it was. A request
// another magpie passed on (a Remote magpie provider) names the agent on
// the first computer and the computers it came through, never the magpie
// that passed it on. The vendor is told none of it, and its request is
// the same whoever asked.
func TestPluginToldTheCaller(t *testing.T) {
	type sent struct {
		h    http.Header
		body string
	}
	var mu sync.Mutex
	var got []sent
	pid := besideFake(t, "fakeco", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, sent{r.Header.Clone(), string(b)})
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"id":"c","object":"chat.completion.chunk","model":"fake-1","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}`+"\n\n")
		io.WriteString(w, `data: {"id":"c","object":"chat.completion.chunk","model":"fake-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	was := usage.PluginAgents
	usage.PluginAgents = func() []usage.Known {
		return []usage.Known{{ID: "pi", Name: "Pi Agent", Names: []string{"pi"}}, {ID: "alma", Name: "Alma", Names: []string{"alma"}}}
	}
	t.Cleanup(func() { usage.PluginAgents = was })

	// this computer's magpie is also the other computer's Remote magpie:
	// a request to office/… is passed on to it, as magpie's own
	remote := httptest.NewServer(New().Handler())
	t.Cleanup(remote.Close)
	oid, err := provider.Add(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Chat: strings.TrimPrefix(remote.URL, "http://")})
	if err != nil {
		t.Fatal(err)
	}
	office, _ := provider.Find(oid)
	if _, err := office.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	here := "studio-ü"
	was2 := hostName
	hostName = func() string { return here }
	t.Cleanup(func() { hostName = was2 })

	type caller struct {
		Agent string   `json:"agent"`
		Name  string   `json:"name"`
		Via   []string `json:"via"`
	}
	const body = `{"model":"%s","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	cases := []struct {
		name, model string
		headers     map[string]string
		want        caller
	}{
		{"an agent here, by its token", pid + "/fake-1", map[string]string{"Authorization": "Bearer " + TokenFor("alma")}, caller{"alma", "Alma", nil}},
		{"an agent here, by its User-Agent", pid + "/fake-1", map[string]string{"User-Agent": "pi/0.9"}, caller{"pi", "Pi Agent", nil}},
		{"passed on by this computer's Remote magpie", oid + "/" + pid + "/fake-1", map[string]string{"User-Agent": "pi/0.9"}, caller{"pi", "Pi Agent", []string{here}}},
		{"passed on through two magpies", pid + "/fake-1", map[string]string{"User-Agent": "magpie/0.1.0", AgentHeader: "pi", ViaHeader: "MacBook-Pro", ViaChainHeader: "MacBook-Pro,office-mini", HopsHeader: "2"}, caller{"pi", "Pi Agent", []string{"MacBook-Pro", "office-mini"}}},
		{"passed on by an older magpie", pid + "/fake-1", map[string]string{"User-Agent": "magpie/0.1.0", AgentHeader: "pi", ViaHeader: "笔记本", HopsHeader: "1"}, caller{"pi", "Pi Agent", []string{"笔记本"}}},
		{"a client's own caller headers, not a magpie", pid + "/fake-1", map[string]string{"User-Agent": "pi/0.9", AgentHeader: "codex", ViaHeader: "elsewhere", provider.CallerAgentHeader: "codex", provider.CallerViaHeader: "elsewhere"}, caller{"pi", "Pi Agent", nil}},
	}
	var first sent
	for i, tc := range cases {
		mu.Lock()
		got = nil
		mu.Unlock()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(strings.Replace(body, "%s", tc.model, 1)))
		for k, v := range tc.headers {
			req.Header.Set(k, v)
		}
		New().Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", tc.name, rec.Code, rec.Body)
		}
		mu.Lock()
		s := slices.Clone(got)
		mu.Unlock()
		if len(s) != 1 {
			t.Fatalf("%s: the vendor got %d requests", tc.name, len(s))
		}
		h := s[0].h
		// the fetch, by its headers
		dec := func(v string) string { d, _ := url.PathUnescape(v); return d }
		var via []string
		for v := range strings.SplitSeq(h.Get("X-Saw-Caller-Via"), ",") {
			if v != "" {
				via = append(via, dec(v))
			}
		}
		if a, n := dec(h.Get("X-Saw-Caller-Agent")), dec(h.Get("X-Saw-Caller-Name")); a != tc.want.Agent || n != tc.want.Name || !slices.Equal(via, tc.want.Via) {
			t.Errorf("%s: the plugin's fetch was told %q (%q) via %q; want %+v", tc.name, a, n, via, tc.want)
		}
		// chat.headers, by input.caller
		raw, _ := url.QueryUnescape(h.Get("X-Saw-Hook-Caller"))
		var hook struct {
			Agent  string `json:"agent"`
			Caller caller `json:"caller"`
		}
		if err := json.Unmarshal([]byte(raw), &hook); err != nil {
			t.Errorf("%s: chat.headers was told no caller: %q", tc.name, raw)
		} else if hook.Agent != "build" || hook.Caller.Agent != tc.want.Agent || hook.Caller.Name != tc.want.Name || !slices.Equal(hook.Caller.Via, append([]string{}, tc.want.Via...)) {
			t.Errorf("%s: chat.headers was told agent %q, caller %+v; want build, %+v", tc.name, hook.Agent, hook.Caller, tc.want)
		}
		// the vendor: none of it, and the same request whoever asked
		for k := range h {
			if strings.HasPrefix(strings.ToLower(k), "x-magpie-") {
				t.Errorf("%s: the vendor got %s: %q", tc.name, k, h.Values(k))
			}
		}
		for k := range h {
			if strings.HasPrefix(k, "X-Saw-") {
				h.Del(k)
			}
		}
		if i == 0 {
			first = s[0]
			continue
		}
		if s[0].body != first.body {
			t.Errorf("%s: the vendor got\n%s\nwhere the first request's got\n%s", tc.name, s[0].body, first.body)
		}
		for k := range first.h {
			if _, ok := h[k]; !ok {
				t.Errorf("%s: the vendor got no %s, which the first request's had", tc.name, k)
			}
		}
		for k, v := range h {
			if b := first.h.Values(k); !slices.Equal(v, b) {
				t.Errorf("%s: the vendor got %s %q where the first request's got %q", tc.name, k, v, b)
			}
		}
	}
}

// A magpie passing on a request it was passed adds itself to the chain
// (passOnCaller), so the magpie after it names every computer on the way.
func TestPassOnCallerAddsThisComputer(t *testing.T) {
	const here = "studio"
	was := hostName
	hostName = func() string { return here }
	t.Cleanup(func() { hostName = was })
	for _, tc := range []struct {
		name      string
		c         caller
		via, want string
	}{
		{"an agent here", caller{agent: "pi"}, here, here},
		{"passed on once before", caller{agent: "pi", via: "laptop", chain: []string{"laptop"}}, "laptop", "laptop," + here},
		{"passed on twice before", caller{agent: "pi", via: "laptop", chain: []string{"laptop", "mini"}}, "laptop", "laptop,mini," + here},
	} {
		req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		passOnCaller(context.WithValue(context.Background(), callerCtx{}, tc.c), req)
		if v, ch := req.Header.Get(ViaHeader), req.Header.Get(ViaChainHeader); v != tc.via || ch != tc.want {
			t.Errorf("%s: sent via %q, chain %q; want %q, %q", tc.name, v, ch, tc.via, tc.want)
		}
		// and the magpie after reads it back
		in := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		in.Header = req.Header.Clone()
		in.Header.Set("User-Agent", "magpie/0.1.0")
		if got := callerFrom(in).chain; strings.Join(got, ",") != tc.want {
			t.Errorf("%s: the next magpie read the chain %q; want %q", tc.name, got, tc.want)
		}
	}
	// a chain that doesn't begin at the agent's computer, or has a part
	// that isn't a name, is the computer alone
	for _, ch := range []string{"elsewhere,mini", "laptop,,mini", "laptop,a b", "laptop" + strings.Repeat(",x", catalog.MaxVia)} {
		in := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		in.Header.Set("User-Agent", "magpie/0.1.0")
		in.Header.Set(AgentHeader, "pi")
		in.Header.Set(ViaHeader, "laptop")
		in.Header.Set(ViaChainHeader, ch)
		if got := callerFrom(in).chain; !slices.Equal(got, []string{"laptop"}) {
			t.Errorf("chain %q was read as %q; want [laptop]", ch, got)
		}
	}
}
