package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// toldAsks are an error's ask in each protocol the gateway answers, whole
// and streamed.
var toldAsks = []struct{ name, path, body string }{
	{"messages", "/v1/messages", `{"model":"f/m","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`},
	{"messages stream", "/v1/messages", `{"model":"f/m","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`},
	{"chat", "/v1/chat/completions", `{"model":"f/m","messages":[{"role":"user","content":"hi"}]}`},
	{"chat stream", "/v1/chat/completions", `{"model":"f/m","stream":true,"messages":[{"role":"user","content":"hi"}]}`},
	{"responses", "/v1/responses", `{"model":"f/m","input":"hi"}`},
	{"responses stream", "/v1/responses", `{"model":"f/m","stream":true,"input":"hi"}`},
	{"gemini", "/v1beta/models/f/m:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`},
	{"gemini stream", "/v1beta/models/f/m:streamGenerateContent?alt=sse", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`},
}

// inJSON is s as it reads inside a JSON string.
func inJSON(s string) string {
	b, _ := json.Marshal(s)
	return strings.ReplaceAll(strings.Trim(string(b), `"`), `\u0026`, "&")
}

// @gptchan404 on X: a vendor's refusal reached the agent wrapped in
// magpie's words ("claude-opus-5-5 refused this request (safety filter:
// content_policy_rejected — <Factory's words>) via Factory"), so the user
// had no error of the vendor's own to take back to it. Every protocol,
// whole and streamed, is now told the vendor's words first, with its
// status, on a line of their own, and magpie's after them.
func TestRefusalToldWithTheVendorsWordsFirst(t *testing.T) {
	for _, up := range []provider.Protocol{provider.Anthropic, provider.Chat} {
		for _, ask := range toldAsks {
			t.Run(string(up)+"/"+ask.name, func(t *testing.T) {
				fresh(t)
				f := &scripted{replies: []reply{{403, "application/json", factoryPolicy403(up == provider.Anthropic)}}}
				scriptedOn(t, "f", up, f)
				_, out := sendTo(New(), ask.path, ask.body)
				first := "F: " + factoryPolicyMsg + " (HTTP 403)"
				if !strings.Contains(out, inJSON(first+"\nmagpie: m refused this request (safety filter: content_policy_rejected) via F")) {
					t.Errorf("not the vendor's words first, then magpie's:\n%s", out)
				}
			})
		}
	}
}

// Any other error of a vendor's is told the same way: its words, status,
// code and request id first, as it said them; and a client of OpenAI's
// shape keeps the vendor's code on the error.
func TestVendorErrorToldWithItsOwnWordsFirst(t *testing.T) {
	for _, up := range []provider.Protocol{provider.Anthropic, provider.Chat} {
		for _, ask := range toldAsks {
			t.Run(string(up)+"/"+ask.name, func(t *testing.T) {
				fresh(t)
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					io.ReadAll(r.Body)
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Request-Id", "req_7Hq2")
					w.WriteHeader(http.StatusBadRequest)
					if up == provider.Anthropic {
						io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"messages.0.content: Input should be a valid list"}}`)
						return
					}
					io.WriteString(w, `{"error":{"message":"messages.0.content: Input should be a valid list","type":"invalid_request_error","code":"invalid_value"}}`)
				}))
				t.Cleanup(srv.Close)
				p := provider.Provider{ID: "f", Name: "F", Key: "k", Models: []string{"m"}}
				if up == provider.Anthropic {
					p.Anthropic = srv.URL
				} else {
					p.Chat = srv.URL + "/v1"
				}
				if err := provider.Save(p); err != nil {
					t.Fatal(err)
				}
				_, out := sendTo(New(), ask.path, ask.body)
				code := "invalid_value"
				if up == provider.Anthropic {
					code = "invalid_request_error"
				}
				first := "F: messages.0.content: Input should be a valid list (HTTP 400, " + code + ", request id req_7Hq2)"
				if !strings.Contains(out, inJSON(first)) {
					t.Errorf("not the vendor's words, status, code and request id first:\n%s", out)
				}
				oai := strings.HasPrefix(ask.path, "/v1/chat") || strings.HasPrefix(ask.path, "/v1/responses")
				if oai && up == provider.Chat && !strings.Contains(out, `"invalid_value"`) {
					t.Errorf("the vendor's code left out of the error: %s", out)
				}
			})
		}
	}
}

func TestUpstreamTold(t *testing.T) {
	b := []byte(`{"error":{"message":"Your credit balance is too low","type":"invalid_request_error"}}`)
	h := http.Header{"X-Request-Id": {"r1"}}
	for _, c := range []struct{ msg, want string }{
		{"P: Your credit balance is too low", "P: Your credit balance is too low (HTTP 400, invalid_request_error, request id r1)"},
		{"P: Your credit balance is too low — top up at example.com", "P: Your credit balance is too low (HTTP 400, invalid_request_error, request id r1)\nmagpie: top up at example.com"},
		{"", "P: Your credit balance is too low (HTTP 400, invalid_request_error, request id r1)"},
	} {
		if got := upstreamTold("P", 400, h, b, c.msg, false); got != c.want {
			t.Errorf("%q:\n got %q\nwant %q", c.msg, got, c.want)
		}
	}
	// an empty body: who, and its status, first; magpie's reading after
	if got := upstreamTold("P", 403, http.Header{}, nil, "P: 403 Forbidden: "+provider.NoDetails, false); got != "P: HTTP 403\nmagpie: "+provider.NoDetails {
		t.Errorf("empty body: %q", got)
	}
}

// The Factory plugin's refusal (packages/factory, @gptchan404's case):
// Factory's words and its request id and code on the first line, the
// plugin's advice on magpie's, after the refusal.
func TestPluginAdviceToldOnMagpiesLine(t *testing.T) {
	advice := factoryPolicyMsg[strings.Index(factoryPolicyMsg, " — ")+len(" — "):]
	factory := "This request was blocked by Factory's usage policy check. (upstream request id 8d1f0c2a3b4e5f60::LAX, code content_policy_rejected)"
	p := provider.Provider{ID: "factory", Name: "Factory"}
	got := refusalTold(p, "claude-opus-5-5", 400, "", "safety filter: content_policy_rejected — "+factoryPolicyMsg, true)
	want := "Factory: " + factory + " (HTTP 400)\nmagpie: claude-opus-5-5 refused this request (safety filter: content_policy_rejected) via Factory — " + advice
	if got != want {
		t.Errorf("refusal:\n got %q\nwant %q", got, want)
	}
	b := []byte(`{"error":{"type":"permission_error","message":` + strconv.Quote(factoryPolicyMsg) + `}}`)
	got = upstreamTold("Factory", 403, http.Header{}, b, "Factory: "+factoryPolicyMsg, true)
	if want := "Factory: " + factory + " (HTTP 403, permission_error)\nmagpie: " + advice; got != want {
		t.Errorf("error:\n got %q\nwant %q", got, want)
	}
	// a vendor's own words keep their dash when no plugin wrote them
	got = upstreamTold("Factory", 403, http.Header{}, b, "Factory: "+factoryPolicyMsg, false)
	if want := "Factory: " + factoryPolicyMsg + " (HTTP 403, permission_error)"; got != want {
		t.Errorf("not a plugin's:\n got %q\nwant %q", got, want)
	}
}
