package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// Anthropic's refusal of a request on a disabled organization, as Claude
// Code's errors page quotes it ("API Error: 400 ... This organization has
// been disabled.") in the API's own error body.
const orgDisabled = `{"type":"error","error":{"type":"invalid_request_error","message":"This organization has been disabled."},"request_id":"req_011CVs3dD7kFz8Rkq2a1bXyZ"}`

// A flagged account's refusal (RiverOnVenus on X got 403s; Claude's apps
// show 「您的账户暂时无法接收 Claude 的回复」).
const cantReceive = `{"type":"error","error":{"type":"permission_error","message":"Your account can't receive replies from Claude right now."}}`

// claudeOffResults are Claude Code's result lines for an account
// Anthropic turned off: its organization disabled (a 400), the claude.ai
// account put on hold (account_on_hold, no status of the API's), and an
// organization that turned off subscription sign-in for Claude Code
// (oauth_org_not_allowed, the API's 403), and a flagged account that
// "can’t receive replies from Claude" (RiverOnVenus on X: a plain 403,
// the apps' 「您的账户暂时无法接收 Claude 的回复」).
var claudeOffResults = map[string]string{
	"disabled": `    echo '{"type":"assistant","message":{"id":"x","model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"API Error: 400 {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"This organization has been disabled.\"}}"}]},"error":"invalid_request","request_id":"req_011CVs3dD7kFz8Rkq2a1bXyZ"}'
    echo '{"type":"result","subtype":"success","is_error":true,"result":"API Error: 400 {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"This organization has been disabled.\"},\"request_id\":\"req_011CVs3dD7kFz8Rkq2a1bXyZ\"}","api_error_status":400}'
`,
	"on hold": `    echo '{"type":"result","subtype":"success","is_error":true,"result":"Your account is on hold and can'"'"'t use Claude Code. View details or appeal: https://claude.ai/restricted"}'
`,
	"org off": `    echo '{"type":"result","subtype":"success","is_error":true,"result":"Your organization has disabled Claude subscription access for Claude Code · Use an Anthropic API key instead, or ask your admin to enable access","api_error_status":403}'
`,
	"can't receive": `    echo '{"type":"result","subtype":"success","is_error":true,"result":"API Error: 403 {\"type\":\"error\",\"error\":{\"type\":\"permission_error\",\"message\":\"Your account can’t receive replies from Claude right now.\"}}","api_error_status":403}'
`,
}

// A Claude subscription account Anthropic turned off mid-use (Ramon317317
// on X: a Claude account flagged, and the group didn't move on): the
// request is answered by the spare account, on every route a client
// takes, streamed and not; the account rests as one its vendor
// restricted, and the requests after it go to the spare alone. Before, the
// disabled organization's 400 ended the request at the first account, and
// it was asked again by the next.
func TestClaudeAccountOffMovesToNextAccount(t *testing.T) {
	asks := []struct{ path, body string }{
		{"/v1/messages", `{"model":"claude/claude-sonnet-5","max_tokens":50,"stream":true,"messages":[{"role":"user","content":"ping"}]}`},
		{"/v1/messages", `{"model":"claude/claude-sonnet-5","max_tokens":50,"messages":[{"role":"user","content":"ping"}]}`},
		{"/v1/chat/completions", `{"model":"claude/claude-sonnet-5","stream":true,"messages":[{"role":"user","content":"ping"}]}`},
		{"/v1/chat/completions", `{"model":"claude/claude-sonnet-5","messages":[{"role":"user","content":"ping"}]}`},
		{"/v1/responses", `{"model":"claude/claude-sonnet-5","stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`},
		{"/v1/responses", `{"model":"claude/claude-sonnet-5","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`},
	}
	for name, first := range claudeOffResults {
		for i, ask := range asks {
			t.Run(name+"/"+ask.path+map[bool]string{true: "/stream", false: "/whole"}[strings.Contains(ask.body, `"stream":true`)], func(t *testing.T) {
				log := claudeSubscriptionFirstSays(t, first)
				s := New()
				code, body := sendTo(s, ask.path, ask.body)
				b, _ := os.ReadFile(log)
				if runs := strings.Join(strings.Fields(string(b)), ","); code != 200 || !strings.Contains(body, "pong") || runs != "own,tok-spare" {
					t.Fatalf("ask %d: status %d: %.400s (runs %q)", i, code, body, runs)
				}
				r := lastRoute(s)
				if len(r.Tries) != 2 || r.Tries[0].Fail != failRestricted || r.Tries[0].Rest == nil || r.Tries[1].Status != 200 {
					t.Fatalf("tries: %+v", r.Tries)
				}
				os.Remove(log)
				code, body = sendTo(s, ask.path, ask.body)
				b, _ = os.ReadFile(log)
				if runs := strings.Join(strings.Fields(string(b)), ","); code != 200 || runs != "tok-spare" {
					t.Fatalf("the next request: status %d: %.300s (runs %q, want the spare's alone)", code, body, runs)
				}
			})
		}
	}
}

// A group whose first member, a key on Anthropic's API, answers that its
// organization is disabled, as a 400, a 403 or an error event before
// anything is said, has the request answered by the next member on every
// route, streamed and not. Before, the 400 and the event were the
// agent's, and the next member was never asked.
func TestClaudeOrgDisabledGroupMovesOn(t *testing.T) {
	refusals := map[string]func(w http.ResponseWriter, stream bool){
		"400": func(w http.ResponseWriter, _ bool) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			io.WriteString(w, orgDisabled)
		},
		"403": func(w http.ResponseWriter, _ bool) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(403)
			io.WriteString(w, `{"type":"error","error":{"type":"permission_error","message":"This organization has been disabled."}}`)
		},
		"403 can't receive": func(w http.ResponseWriter, _ bool) {
			w.WriteHeader(403)
			io.WriteString(w, cantReceive)
		},
		"event": func(w http.ResponseWriter, stream bool) {
			if !stream {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(400)
				io.WriteString(w, orgDisabled)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m1","role":"assistant","model":"m","content":[],"usage":{"input_tokens":3}}}`,
				`event: error`+"\n"+`data: `+orgDisabled))
		},
	}
	asks := []struct{ path, body string }{
		{"/v1/messages", `{"model":"group/g","max_tokens":50,"stream":true,"messages":[{"role":"user","content":"ping"}]}`},
		{"/v1/messages", `{"model":"group/g","max_tokens":50,"messages":[{"role":"user","content":"ping"}]}`},
		{"/v1/chat/completions", `{"model":"group/g","stream":true,"messages":[{"role":"user","content":"ping"}]}`},
		{"/v1/chat/completions", `{"model":"group/g","messages":[{"role":"user","content":"ping"}]}`},
		{"/v1/responses", `{"model":"group/g","stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`},
		{"/v1/responses", `{"model":"group/g","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`},
	}
	for name, refuse := range refusals {
		for _, ask := range asks {
			t.Run(name+ask.path+map[bool]string{true: "/stream", false: "/whole"}[strings.Contains(ask.body, `"stream":true`)], func(t *testing.T) {
				fresh(t)
				var asked []string
				up := func(id string) string {
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						b, _ := io.ReadAll(r.Body)
						asked = append(asked, id)
						stream := gjson.GetBytes(b, "stream").Bool()
						if id == "a" {
							refuse(w, stream)
							return
						}
						if stream {
							w.Header().Set("Content-Type", "text/event-stream")
							io.WriteString(w, anthropicAnswer)
							return
						}
						w.Header().Set("Content-Type", "application/json")
						io.WriteString(w, `{"id":"m2","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"from b"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2}}`)
					}))
					t.Cleanup(srv.Close)
					return srv.URL
				}
				for _, id := range []string{"a", "b"} {
					if err := provider.Save(provider.Provider{ID: id, Name: strings.ToUpper(id), Key: "k-" + id, Models: []string{"m"}, Anthropic: up(id)}); err != nil {
						t.Fatal(err)
					}
				}
				if err := provider.SaveGroup(provider.Group{Name: "g", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
					t.Fatal(err)
				}
				s := New()
				code, body := sendTo(s, ask.path, ask.body)
				if code != 200 || !strings.Contains(body, "from b") || strings.Join(asked, ",") != "a,b" {
					t.Fatalf("status %d: %.400s (asked %v)", code, body, asked)
				}
				// these members are relays (not api.anthropic.com): the
				// refusal is about the relay's account behind it, so the
				// member rests as any failure, not as restricted
				if r := lastRoute(s); len(r.Tries) != 2 || r.Tries[0].Fail == failRestricted || r.Tries[0].Rest == nil {
					t.Fatalf("the refusing relay member: %+v", r.Tries)
				}
			})
		}
	}
}

// Which accounts are read as turned off by Anthropic: a Claude
// subscription's and a key on Anthropic's own API, not a relay's in front
// of it, nor another subscription's; and not the refusals that are about
// what was asked, which every account gives alike.
func TestClaudeAccountOffScoped(t *testing.T) {
	claude := provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: "me@example.com"}}
	direct := provider.Provider{ID: "k", Key: "sk", Anthropic: "https://api.anthropic.com"}
	relay := provider.Provider{ID: "r", Key: "sk", Anthropic: "https://relay.example.com"}
	codex := provider.Provider{ID: "codex", Account: &provider.Account{Agent: "codex", User: "me@example.com"}}
	for _, c := range []struct {
		p      provider.Provider
		status int
		body   string
		want   bool
	}{
		{claude, 400, orgDisabled, true},
		{direct, 400, orgDisabled, true},
		{direct, 403, `{"type":"error","error":{"type":"permission_error","message":"OAuth authentication is currently not allowed for this organization."}}`, true},
		{claude, 502, `{"error":{"message":"Your account is on hold and can't use Claude Code. View details or appeal: https://claude.ai/restricted"}}`, false},
		{claude, 400, `{"error":{"message":"Your account is on hold and can't use Claude Code. View details or appeal: https://claude.ai/restricted"}}`, true},
		{claude, 403, cantReceive, true},
		{direct, 403, cantReceive, true},
		{claude, 403, strings.ReplaceAll(cantReceive, "can't", "can’t"), true},
		{claude, 403, strings.ReplaceAll(cantReceive, "can't", "cannot"), true},
		{relay, 403, cantReceive, false},
		{relay, 400, orgDisabled, false},
		{codex, 400, orgDisabled, false},
		{claude, 400, `{"type":"error","error":{"type":"invalid_request_error","message":"Third-party apps now draw from your extra usage, not your plan limits."}}`, false},
		{claude, 400, `{"type":"error","error":{"type":"invalid_request_error","message":"Claude Code is unable to respond to this request, which appears to violate our Usage Policy"}}`, false},
	} {
		if got := claudeAccountOff(c.p, c.status, []byte(c.body)); got != c.want {
			t.Errorf("%s %d %.80s: %v, want %v", c.p.ID, c.status, c.body, got, c.want)
		}
		if got := failureOf(candidate{p: c.p}, c.status, []byte(c.body)) == failRestricted; got != c.want {
			t.Errorf("%s %d %.80s: restricted %v, want %v", c.p.ID, c.status, c.body, got, c.want)
		}
		if c.want && riskRefused(candidate{p: c.p}, c.status, nil, []byte(c.body)) {
			t.Errorf("%s %d: the provider's other accounts are left out", c.p.ID, c.status)
		}
	}
	// the request isn't at fault: another member may serve it; the
	// third-party refusal stays the agent's (#4503)
	if !retryable(403, []byte(cantReceive)) {
		t.Error("a flagged account's 403 isn't asked of the next member")
	}
	if !retryable(400, []byte(orgDisabled)) {
		t.Error("a disabled organization's 400 isn't asked of the next member")
	}
	if retryable(400, []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"Third-party apps now draw from your extra usage, not your plan limits."}}`)) {
		t.Error("the third-party refusal is asked of the next member")
	}
}
