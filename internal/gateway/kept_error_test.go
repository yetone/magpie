package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// ne9ko9 on Discord: a vendor that speaks the client's own API has its
// error reach the client as it came: its status, its own type, a numeric
// code and the fields magpie knows nothing of, every byte but the message,
// which names the provider. It was rebuilt from the status, with only a
// string code kept.
func TestSameAPIErrorKeptAsSent(t *testing.T) {
	for _, c := range []struct {
		name, path, req string
		proto           provider.Protocol
		status          int
		body, want      string
	}{
		{"chat", "/v1/chat/completions", `{"model":"a/m","messages":[{"role":"user","content":"hi"}]}`, provider.Chat, 400,
			`{"error":{"message":"Request rejected by gateway rule (FX-1214)","type":"upstream_rule","code":1214,"param":null,"extra":{"trace":"t-9"}},"request_id":"req-77"}`,
			`{"error":{"message":"A: Request rejected by gateway rule (FX-1214)","type":"upstream_rule","code":1214,"param":null,"extra":{"trace":"t-9"}},"request_id":"req-77"}`},
		{"anthropic", "/v1/messages", `{"model":"a/m","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`, provider.Anthropic, 403,
			`{"type":"error","error":{"type":"upstream_rule","message":"Request rejected by gateway rule","code":20031,"details":["region"]},"request_id":"req_01"}`,
			`{"type":"error","error":{"type":"upstream_rule","message":"A: Request rejected by gateway rule","code":20031,"details":["region"]},"request_id":"req_01"}`},
		{"responses", "/v1/responses", `{"model":"a/m","input":"hi"}`, provider.Responses, 400,
			`{"error":{"message":"Request rejected by gateway rule","type":"upstream_rule","code":1214,"param":"input","extra":true}}`,
			`{"error":{"message":"A: Request rejected by gateway rule","type":"upstream_rule","code":1214,"param":"input","extra":true}}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			fresh(t)
			a := &scripted{replies: []reply{{c.status, "", c.body}}}
			scriptedOn(t, "a", c.proto, a)
			w := httptest.NewRecorder()
			New().Handler().ServeHTTP(w, httptest.NewRequest("POST", c.path, strings.NewReader(c.req)))
			if w.Code != c.status || w.Body.String() != c.want {
				t.Fatalf("the vendor's error was rewritten:\n got %d %s\nwant %d %s", w.Code, w.Body.String(), c.status, c.want)
			}
		})
	}
}

// A prompt too long for the model is still said the way the client's API
// says it, so the agent compacts: on the vendor's own body, with only the
// fields that say so changed and every other one kept.
func TestSameAPIOverflowKeepsTheRest(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{{400, "", `{"error":{"message":"Prompt exceeds max length","code":1261,"type":"zhipu_error","extra":"x"},"request_id":"r1"}`}}}
	scriptedOn(t, "a", provider.Chat, a)
	w := httptest.NewRecorder()
	New().Handler().ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"a/m","messages":[{"role":"user","content":"hi"}]}`)))
	b := w.Body.String()
	if w.Code != 400 || gjson.Get(b, "error.code").String() != "context_length_exceeded" || gjson.Get(b, "error.type").String() != "invalid_request_error" {
		t.Fatalf("overflow not said as Chat says it: %d %s", w.Code, b)
	}
	if gjson.Get(b, "error.extra").String() != "x" || gjson.Get(b, "request_id").String() != "r1" || gjson.Get(b, "error.message").String() != "A: Prompt exceeds max length" {
		t.Fatalf("the vendor's other fields were lost: %s", b)
	}
}

// Another API's error is rebuilt in the client's shape, as before.
func TestOtherAPIErrorRebuilt(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{{400, "", `{"error":{"message":"Request rejected by gateway rule","type":"upstream_rule","code":1214,"extra":true}}`}}}
	scriptedOn(t, "a", provider.Chat, a)
	w := httptest.NewRecorder()
	New().Handler().ServeHTTP(w, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"a/m","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)))
	b := w.Body.String()
	if w.Code != 400 || gjson.Get(b, "type").String() != "error" || gjson.Get(b, "error.type").String() != "invalid_request_error" || !strings.Contains(gjson.Get(b, "error.message").String(), "Request rejected by gateway rule") {
		t.Fatalf("not rebuilt in Anthropic's shape: %d %s", w.Code, b)
	}
}

func TestSetErrorField(t *testing.T) {
	for _, c := range []struct {
		in, name string
		v        any
		want     string
	}{
		{`{"error":{"message":"a","code":7}}`, "code", "x", `{"error":{"message":"a","code":"x"}}`},
		{`{"error":{"message":"a"}}`, "type", "t", `{"error":{"type":"t","message":"a"}}`},
		{`{"error":{}}`, "type", "t", `{"error":{"type":"t"}}`},
		{`{"error": { "message":"a<b"} }`, "message", "a<b — c&d", `{"error": { "message":"a<b — c&d"} }`},
	} {
		if got := string(setErrorField([]byte(c.in), c.name, c.v)); got != c.want {
			t.Errorf("setErrorField(%s, %s) = %s, want %s", c.in, c.name, got, c.want)
		}
	}
}
