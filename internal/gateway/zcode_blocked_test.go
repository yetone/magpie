package gateway

import (
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

// countAsked is an upstream that counts tokens, noting each time it's asked.
type countAsked struct {
	mu    sync.Mutex
	paths []string
}

func (c *countAsked) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	c.paths = append(c.paths, r.URL.Path)
	c.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"input_tokens":999}`)
}

func (c *countAsked) asked() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.paths...)
}

func estimated(t *testing.T, rec *httptest.ResponseRecorder) int {
	t.Helper()
	var got struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); rec.Code != 200 || err != nil || got.InputTokens == 0 {
		t.Fatalf("count_tokens: %d %s", rec.Code, rec.Body)
	}
	return got.InputTokens
}

// Claude Code asks count_tokens before each turn; ZCode itself never does,
// and zcode.z.ai answers it with an error every time. A ZCode account's
// count is estimated, the vendor not asked.
func TestZCodeCountTokensEstimated(t *testing.T) {
	fresh(t)
	up := &countAsked{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	p := provider.Provider{ID: "zcode", Name: "ZCode", Anthropic: srv.URL + "/api/v1/zcode-plan/anthropic", Models: []string{"glm-5.1"},
		Account: &provider.Account{Agent: "zcode", User: "trial@example.com", Plan: "Start Plan"}}
	body := []byte(`{"model":"glm-5.1","messages":[{"role":"user","content":"hello there"}]}`)
	rec := httptest.NewRecorder()
	New().countOn(rec, httptest.NewRequest("POST", "/v1/messages/count_tokens", nil), p, "glm-5.1", true, body)
	if n := estimated(t, rec); n == 999 || len(up.asked()) > 0 {
		t.Fatalf("zcode.z.ai was asked to count: %v, %d", up.asked(), n)
	}
}

// ZCode moved onto its plugin estimates as the built-in does.
func TestMovedZCodeCountTokensEstimated(t *testing.T) {
	up := &countAsked{}
	movedFake(t, "zcode", up)
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages/count_tokens",
		strings.NewReader(`{"model":"zcode/fake-claude","messages":[{"role":"user","content":"hello there"}]}`)))
	if n := estimated(t, rec); n == 999 || len(up.asked()) > 0 {
		t.Fatalf("the vendor was asked to count: %v, %d", up.asked(), n)
	}
}

// edgeBlockPage is Alibaba Cloud's ESA block page, as zcode.z.ai served it.
const edgeBlockPage = `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>405</title></head>
<body><div class="main"><h1>Sorry, your request has been blocked due to unusual activity.</h1>
<p>Request ID: 0bd6a2b417592xxxx</p>
<a href="https://errors.aliyun.com/error/405?code=blocked">errors.aliyun.com</a></div></body></html>`

// A vendor's edge firewall blocking this address answers an HTML page: the
// agent and the trace are told so in plain words, not the page, and the
// group's next member is asked, as for another provider's failure.
func TestEdgeBlockedSaidPlainly(t *testing.T) {
	fresh(t)
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusMethodNotAllowed)
		io.WriteString(w, edgeBlockPage)
	}))
	t.Cleanup(blocked.Close)
	var other int
	fine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		other++
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"ok","choices":[{"message":{"role":"assistant","content":"from b"}}]}`)
	}))
	t.Cleanup(fine.Close)
	for _, p := range []provider.Provider{
		{ID: "za", Name: "ZA", Key: "k", Models: []string{"m"}, Chat: blocked.URL + "/v1"},
		{ID: "zb", Name: "ZB", Key: "k", Models: []string{"m"}, Chat: fine.URL + "/v1"},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}

	// alone: the agent is told what happened
	s := New()
	code, reply := postAs(t, s, "", `{"model":"za/m","messages":[{"role":"user","content":"hi"}]}`)
	if code != http.StatusMethodNotAllowed || !strings.Contains(reply, provider.BlockedHint) || strings.Contains(reply, "<") {
		t.Fatalf("blocked reply: %d %s", code, reply)
	}
	if tr := s.trace.routes[len(s.trace.routes)-1].Tries[0]; !strings.HasSuffix(tr.Error, " — "+provider.BlockedHint) || strings.Contains(tr.Error, "<") {
		t.Fatalf("trace: %+v", tr)
	}

	// in a group: the next member answers
	if err := provider.SaveGroup(provider.Group{Name: "Z", Members: []string{"za/m", "zb/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s = New()
	code, reply = postAs(t, s, "", `{"model":"group/z","messages":[{"role":"user","content":"hi"}]}`)
	if code != http.StatusOK || !strings.Contains(reply, "from b") || other != 1 {
		t.Fatalf("group reply: %d %s (next asked %d)", code, reply, other)
	}
	tries := s.trace.routes[len(s.trace.routes)-1].Tries
	if len(tries) != 2 || tries[0].Rest == nil || !strings.Contains(tries[0].Error, provider.BlockedHint) {
		t.Fatalf("tries: %+v", tries)
	}
}

// A custom Responses relay refusing with 403 and no message of its own
// tells the agent what it was, not a bare "403 Forbidden" (maralica on
// Discord): Cloudflare's block page as a firewall's, another page by its
// title, an empty body as a refusal with no details. Its JSON error still
// reads as its message.
func TestRelayRefusalSaysWhatItWas(t *testing.T) {
	cf := `<!DOCTYPE html>
<html class="no-js" lang="en-US"><head><title>Attention Required! | Cloudflare</title></head>
<body><div id="cf-error-details"><h1 data-translate="block_headline">Sorry, you have been blocked</h1>
<span class="cf-footer-item">Cloudflare Ray ID: <strong>8d1c2f3a4b5c6d7e</strong></span></div></body></html>`
	for _, c := range []struct {
		name, ct, body string
		want           []string
	}{
		{"cloudflare", "text/html; charset=UTF-8", cf, []string{"403 Forbidden: a web page, not an API reply: “Attention Required! | Cloudflare”", provider.BlockedHint}},
		{"page", "text/html", "<html><head><title>403 Forbidden</title></head><body><center><h1>403 Forbidden</h1></center><hr><center>nginx</center></body></html>", []string{"403 Forbidden: a web page, not an API reply: “403 Forbidden”"}},
		{"empty", "", "", []string{"403 Forbidden: " + provider.NoDetails}},
		{"json", "application/json", `{"error":{"message":"this key may not use gpt-5.5","type":"forbidden"}}`, []string{"this key may not use gpt-5.5"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			fresh(t)
			relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if c.ct != "" {
					w.Header().Set("Content-Type", c.ct)
				}
				w.WriteHeader(http.StatusForbidden)
				io.WriteString(w, c.body)
			}))
			t.Cleanup(relay.Close)
			if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Models: []string{"gpt-5.5"}, Responses: relay.URL + "/v1"}); err != nil {
				t.Fatal(err)
			}
			code, reply := postTo(t, New(), "/v1/responses", `{"model":"relay/gpt-5.5","input":"hi"}`)
			if code != http.StatusForbidden || strings.Contains(reply, "<") || strings.Contains(reply, "&lt;") {
				t.Fatalf("%d %s", code, reply)
			}
			var v struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(reply), &v); err != nil {
				t.Fatalf("%v: %s", err, reply)
			}
			if strings.Contains(v.Error.Message, "<") {
				t.Fatalf("markup in %q", v.Error.Message)
			}
			for _, w := range c.want {
				if !strings.Contains(v.Error.Message, w) {
					t.Errorf("missing %q in %q", w, v.Error.Message)
				}
			}
		})
	}
}

// A custom Chat relay that serves only some agents' clients, sent
// WorkBuddy's request with the agent's own User-Agent passed on
// (maralica on Discord, "magpie-grok"): it is asked for its own model id,
// never one magpie named, with WorkBuddy's User-Agent as sent, and
// whatever reason its 403 gives reaches the agent: plain text, JSON, a
// page with no title, a page's fragment, an XML error.
func TestChatRelayRefusalSaysWhy(t *testing.T) {
	const ua = "WorkBuddy/5.9.2 WorkBuddy/5.9.2 CLI/2.165.0"
	for _, c := range []struct {
		name, ct, body, want string
	}{
		{"text", "text/plain", "only Claude Code and Codex CLI are allowed", "magpie-grok: 403 Forbidden: only Claude Code and Codex CLI are allowed"},
		{"json", "application/json", `{"error":{"message":"client WorkBuddy is not allowed","type":"forbidden"}}`, "magpie-grok: client WorkBuddy is not allowed"},
		{"untitled page", "text/html", "<html><body><h1>403</h1><p>only Claude Code &amp; Codex CLI are allowed</p></body></html>", "magpie-grok: 403 Forbidden: a web page, not an API reply: “403 only Claude Code & Codex CLI are allowed”"},
		{"fragment", "text/html", "<h1>Forbidden</h1>\n<p>client not allowed</p>", "magpie-grok: 403 Forbidden: Forbidden client not allowed"},
		{"xml", "application/xml", `<?xml version="1.0"?><Error><Code>ClientNotAllowed</Code><Message>client not allowed</Message></Error>`, "magpie-grok: 403 Forbidden: ClientNotAllowed client not allowed"},
		{"empty", "", "", "magpie-grok: 403 Forbidden: " + provider.NoDetails},
	} {
		for _, model := range []string{"magpie-grok/grok-4.7", "group/g"} {
			t.Run(c.name+" "+model, func(t *testing.T) {
				fresh(t)
				var asked, sentUA string
				relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					b, _ := io.ReadAll(r.Body)
					var v struct {
						Model string `json:"model"`
					}
					json.Unmarshal(b, &v)
					asked, sentUA = v.Model, r.Header.Get("User-Agent")
					if c.ct != "" {
						w.Header().Set("Content-Type", c.ct)
					}
					w.WriteHeader(http.StatusForbidden)
					io.WriteString(w, c.body)
				}))
				t.Cleanup(relay.Close)
				if err := provider.Save(provider.Provider{ID: "magpie-grok", Name: "magpie-grok", Key: "k", Models: []string{"grok-4.7"}, Chat: relay.URL + "/v1"}); err != nil {
					t.Fatal(err)
				}
				if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"magpie-grok/grok-4.7"}, Routing: provider.Ordered}); err != nil {
					t.Fatal(err)
				}
				rec := httptest.NewRecorder()
				req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"`+model+`","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
				req.Header.Set("User-Agent", ua)
				New().Handler().ServeHTTP(rec, req)
				if asked != "grok-4.7" || sentUA != ua {
					t.Fatalf("relay asked for %q as %q", asked, sentUA)
				}
				var v struct {
					Error struct {
						Message string `json:"message"`
					} `json:"error"`
				}
				if rec.Code != http.StatusForbidden || json.Unmarshal(rec.Body.Bytes(), &v) != nil {
					t.Fatalf("%d %s", rec.Code, rec.Body)
				}
				if v.Error.Message != c.want {
					t.Fatalf("said %q, want %q", v.Error.Message, c.want)
				}
			})
		}
	}
}

// The ZCode plugin's answer to the Start Plan's 405 code 3012 (0.1.27 on,
// plugins#348) is an account ZCode's risk control blocked: it rests two
// hours as restricted, the account rather than one model, and is lifted
// by hand like WorkBuddy's. The words 0.1.26 gave the same 405 read as any
// failure does.
func TestZCodeStartPlanBlockRestsRestricted(t *testing.T) {
	fresh(t)
	s := New()
	p := provider.Provider{ID: "zcode", Name: "ZCode", Account: &provider.Account{Agent: "plugin", User: "u1"}}
	c := candidate{p: p, model: "GLM-5.3-Flash", rest: p.ID + "#u1"}
	body := []byte(`{"type":"error","error":{"type":"permission_error","message":"request has been blocked due to unusual activity. (405, code 3012) — ` + provider.ZCodeRestrictedHint + `"}}`)
	now := time.Now()
	rest := s.restAfter(c, 405, nil, body)
	if rest.Why != failRestricted || rest.By != "restricted" || rest.Key != c.restKey() {
		t.Fatalf("rests %+v", rest)
	}
	if d := restedFor(rest, now); d < restrictedRest-time.Minute || d > restrictedRest+time.Minute {
		t.Fatalf("rests %v", d)
	}
	if !s.Unrest(rest.Key) {
		t.Fatal("its rest can't be lifted")
	}
	old := []byte(`{"type":"error","error":{"type":"permission_error","message":"405 Method Not Allowed — ZCode's Start Plan still turned this request away, though magpie sends it as the ZCode app does; it can be a network block of this IP, or ZCode checking for something new. Use an account with a GLM Coding Plan, or add another provider to this group"}}`)
	if f := failureOf(c, 405, old); f == failRestricted {
		t.Fatal("0.1.26's words read as restricted")
	}
	if f := failureOf(c, 200, body); f == failRestricted {
		t.Fatal("a 200 read as restricted")
	}
}

// The ZCode plugin's 3012 answer keeps ZCode's own words, "request has
// been blocked due to unusual activity", in front of ZCodeRestrictedHint.
// Served through the plugin, it reaches the agent and the trace with the
// hint, never as a firewall blocking the address (BlockedHint), and the
// account rests as restricted. Before, the gateway read ZCode's words as
// Alibaba Cloud's block page, told the agent to change networks and
// rested the account as any failure (windy_66454 on Discord, 10-31). The
// plugin's words for the page itself are still the firewall's.
func TestZCodeRiskControlNotToldAsFirewall(t *testing.T) {
	for _, tc := range []struct {
		name, body, says, notSays, why string
	}{
		{"risk control", `{"type":"error","error":{"type":"permission_error","message":"request has been blocked due to unusual activity. (405, code 3012) — ` + provider.ZCodeRestrictedHint + `"}}`, provider.ZCodeRestrictedHint, provider.BlockedHint, failRestricted},
		{"block page", `{"type":"error","error":{"type":"permission_error","message":"405 Method Not Allowed — ` + provider.BlockedHint + `"}}`, provider.BlockedHint, provider.ZCodeRestrictedHint, failOther},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FAKE_ZCODE", "1")
			movedFake(t, "zcode", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusMethodNotAllowed)
				io.WriteString(w, tc.body)
			}))
			p, err := provider.Find("zcode")
			if err != nil {
				t.Fatal(err)
			}
			c := candidate{p: *p, rest: "zcode", model: "GLM-5.3"}
			b := &scripted{replies: []reply{{200, "", chatOK}}}
			scriptedOn(t, "b", provider.Chat, b)
			if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"zcode/GLM-5.3", "b/m"}, Routing: provider.Ordered}); err != nil {
				t.Fatal(err)
			}
			s := New()
			if code, body := postAs(t, s, "", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`); code != 200 {
				t.Fatalf("group request: %d %s", code, body)
			}
			tries := s.trace.routes[len(s.trace.routes)-1].Tries
			if len(tries) != 2 || tries[0].Rest == nil {
				t.Fatalf("tries: %+v", tries)
			}
			tr := tries[0]
			if !strings.Contains(tr.Error, tc.says) || strings.Contains(tr.Error, tc.notSays) {
				t.Fatalf("told %q", tr.Error)
			}
			if tr.Fail != tc.why || tr.Rest.Why != tc.why || tr.Rest.Key != c.restKey() {
				t.Fatalf("failed %q, rested %+v", tr.Fail, *tr.Rest)
			}
		})
	}
}
