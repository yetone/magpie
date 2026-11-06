package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// passOn sets a saved provider's errors to the client's (errors=pass).
func passOn(t *testing.T, id string) {
	t.Helper()
	p, err := provider.Find(id)
	if err != nil {
		t.Fatal(err)
	}
	p.PassErrors = true
	if !p.PassesErrors() {
		t.Fatalf("%s takes no errors=pass", id)
	}
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
}

// restedAny is whether any rest is on id or one of its models.
func restedAny(id string) bool {
	restingUntil.Lock()
	defer restingUntil.Unlock()
	for k := range restingUntil.m {
		if k == id || strings.HasPrefix(k, id+"/") || strings.HasPrefix(k, id+"@") {
			return true
		}
	}
	return false
}

// ne9ko9 on Discord: a provider whose errors the user leaves to the client
// is asked once. Its error reaches the agent as it came: not waited out,
// not handed to the group's next member, nothing rested for it.
func TestPassErrorsAskedOnce(t *testing.T) {
	for _, c := range []struct {
		name, body string
		status     int
	}{
		{"busy", `{"error":{"message":"upstream overloaded","type":"server_error","code":50301}}`, 503},
		{"rate limit", `{"error":{"message":"Rate limit exceeded, please try again later","type":"rate_limit","code":1302}}`, 429},
		{"unserved model", `{"error":{"message":"The model m does not exist","code":"model_not_found"}}`, 404},
	} {
		t.Run(c.name, func(t *testing.T) {
			fresh(t)
			a := &scripted{replies: []reply{{c.status, "", c.body}, {c.status, "", c.body}, {200, "", chatOK}}}
			b := &scripted{replies: []reply{{200, "", chatOK}}}
			scriptedOn(t, "a", provider.Chat, a)
			scriptedOn(t, "b", provider.Chat, b)
			passOn(t, "a")
			if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
				t.Fatal(err)
			}
			// alone, and as a group's first member
			for i, model := range []string{"a/m", "group/g"} {
				code, body := postAs(t, New(), "", `{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)
				if code != c.status || body != c.body || a.n != i+1 || b.n != 0 {
					t.Fatalf("%s: got %d %s (a %d, b %d), want %d %s asked once", model, code, body, a.n, b.n, c.status, c.body)
				}
				if restedAny("a") {
					t.Fatalf("%s: a provider passing its errors was rested", model)
				}
			}
		})
	}
}

// Nothing is inferred from a pass provider's error: a prompt too long is
// the vendor's own body, not said as context_length_exceeded.
func TestPassErrorsNotInferred(t *testing.T) {
	fresh(t)
	said := `{"error":{"message":"This model's maximum context length is 128000 tokens","code":1261,"type":"zhipu_error"},"request_id":"r1"}`
	a := &scripted{replies: []reply{{400, "", said}}}
	scriptedOn(t, "a", provider.Chat, a)
	passOn(t, "a")
	code, body := postAs(t, New(), "", `{"model":"a/m","messages":[{"role":"user","content":"hi"}]}`)
	if code != 400 || body != said || a.n != 1 {
		t.Fatalf("got %d %s after %d", code, body, a.n)
	}
}

// Another API's error is written in the client's shape with the vendor's
// type, numeric code and words, and nothing of magpie's in it, streamed or
// not.
func TestPassErrorsOtherAPI(t *testing.T) {
	for _, stream := range []bool{false, true} {
		fresh(t)
		said := `{"type":"error","error":{"type":"upstream_rule","message":"Request rejected by gateway rule","code":20031}}`
		a := &scripted{replies: []reply{{403, "", said}, {200, "", "{}"}}}
		scriptedOn(t, "a", provider.Anthropic, a)
		passOn(t, "a")
		req := `{"model":"a/m","messages":[{"role":"user","content":"hi"}]}`
		if stream {
			req = `{"model":"a/m","stream":true,"messages":[{"role":"user","content":"hi"}]}`
		}
		w := httptest.NewRecorder()
		New().Handler().ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(req)))
		b := w.Body.String()
		if w.Code != 403 || a.n != 1 || gjson.Get(b, "error.type").String() != "upstream_rule" ||
			gjson.Get(b, "error.code").Raw != "20031" || gjson.Get(b, "error.message").String() != "Request rejected by gateway rule" {
			t.Fatalf("stream %v: got %d %s after %d", stream, w.Code, b, a.n)
		}
	}
}
