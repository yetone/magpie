package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// advisorKey is a gateway key whose models are models, and its secret.
func advisorKey(t *testing.T, models ...string) string {
	t.Helper()
	keys, secrets := newCaller(t, "Held")
	if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: models}); err != nil {
		t.Fatal(err)
	}
	return secrets[0]
}

func sendAs(t *testing.T, s *Server, secret, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/v1/messages?beta=true", strings.NewReader(body))
	req.Header.Set("User-Agent", "claude-cli/2.1.316 (external, cli)")
	req.Header.Set("Authorization", "Bearer "+secret)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// Claude Code's advisor names a second model inside the request. A key
// held to some models may not reach another through it: magpie asks the
// advisor model as the caller (not as a model magpie chose), so the key's
// models hold it, and the advisor fails as an advisor does.
func TestAdvisorHeldToTheKeysModels(t *testing.T) {
	u := advisedSetup(t)
	secret := advisorKey(t, "v/*")
	body := strings.Replace(advisorTurn("v/glm-5.1", "adv/adv-pro"), `"stream":true`, `"stream":false`, 1)
	rec := sendAs(t, New(), secret, body)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	u.mu.Lock()
	adv := len(u.advisor)
	u.mu.Unlock()
	if adv != 0 {
		t.Fatalf("the advisor model the key may not use was asked %d times", adv)
	}
	r := gjson.Parse(rec.Body.String())
	var result gjson.Result
	r.Get("content").ForEach(func(_, b gjson.Result) bool {
		if b.Get("type").String() == "advisor_tool_result" {
			result = b
		}
		return true
	})
	if result.Get("content.type").String() != "advisor_tool_result_error" {
		t.Fatalf("advisor result %s", rec.Body)
	}
}

// The same on a Claude provider whose upstream runs the advisor itself: a
// held key's advisor is sent upstream only when the key may use its model.
func TestNativeAdvisorHeldToTheKeysModels(t *testing.T) {
	fresh(t)
	u, url := newBillingUpstream(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Anthropic: url, Models: []string{"claude-opus-5-5", "claude-sonnet-5-5"}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		models  []string
		advisor string
		sent    bool
	}{
		{[]string{"relay/claude-sonnet-5-5"}, "relay/claude-opus-5-5", false},
		{[]string{"relay/claude-sonnet-5-5"}, "claude-opus-5-5", false},
		{[]string{"relay/claude-sonnet-5-5"}, "relay/claude-opus-5-5[1m]", false},
		{[]string{"relay/*"}, "relay/claude-opus-5-5", true},
	} {
		secret := advisorKey(t, c.models...)
		rec := sendAs(t, New(), secret, advisorTurn("relay/claude-sonnet-5-5", c.advisor))
		if rec.Code != 200 {
			t.Fatalf("%v %s: %d %s", c.models, c.advisor, rec.Code, rec.Body)
		}
		b := u.last(t)
		sent := false
		for _, tl := range gjson.Get(b, "tools").Array() {
			sent = sent || strings.HasPrefix(tl.Get("type").String(), "advisor_")
		}
		if sent != c.sent {
			t.Errorf("key %v, advisor %s: sent upstream %t, want %t: %s", c.models, c.advisor, sent, c.sent, b)
		}
	}
}
