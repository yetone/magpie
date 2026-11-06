package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// A gateway key held to some models (#882) is refused a Claude Desktop
// check on the model or group the check was served on — its default, the
// first entry of its list, a group among them — not the Claude model the
// probe named: the 403 said "may not use <that model>", one the key was
// never asked about, and the same model on any other agent went on
// (#4175).
func TestClaudeDesktopProbeRefusalNamesTheModelChecked(t *testing.T) {
	fresh(t)
	a := &fake{t: t, reply: deepseekReply}
	b := &fake{t: t, reply: deepseekReply}
	pa := httptest.NewServer(a)
	t.Cleanup(pa.Close)
	pb := httptest.NewServer(b)
	t.Cleanup(pb.Close)
	for _, p := range []provider.Provider{
		{ID: "prova", Name: "Prova", Key: "k", Chat: pa.URL + "/v1", Models: []string{"glm-5-turbo"}},
		{ID: "provb", Name: "Provb", Key: "k", Chat: pb.URL + "/v1", Models: []string{"glm-5-turbo"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	shown, _ := provider.CatalogFor("claude-desktop")
	if len(shown) == 0 || shown[0].Group == "" {
		t.Fatalf("want the found group first on Desktop's list, got %v", shown)
	}
	first := shown[0].ID
	if err := provider.SetModelOrder("claude-desktop", []string{first}); err != nil {
		t.Fatal(err)
	}
	keys, secrets := newCaller(t, "Held", "Free")
	if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: []string{"prova/*"}}); err != nil {
		t.Fatal(err)
	}
	srv := New()
	send := func(secret, model string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"`+model+`","max_tokens":1,"messages":[{"role":"user","content":"."}]}`))
		req.Header.Set("Authorization", "Bearer "+secret)
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh) Claude/2.7032.0 Chrome/140.0 Electron/38.0")
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		return w
	}
	w := send(secrets[0], "claude-opus-4-6")
	if w.Code != 403 {
		t.Fatalf("the probe on the group the key may not use went as %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "may not use "+first) {
		t.Fatalf("the 403 does not name the group the key was checked against: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "claude-opus-4-6") {
		t.Fatalf("the 403 names the model the probe sent, not the one checked: %s", w.Body.String())
	}
	// the same key on the model it holds goes on another agent (Desktop's
	// lists alone stand a request on the group): the refusal was the
	// group's, not its own model's
	plain := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"prova/glm-5-turbo","messages":[{"role":"user","content":"hi"}]}`))
	plain.Header.Set("Authorization", "Bearer "+secrets[0])
	w2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w2, plain)
	if w2.Code != 200 {
		t.Fatalf("the key's own model refused on another agent: %d %s", w2.Code, w2.Body.String())
	}
	// a key given the group's every member is answered on it
	if w := send(secrets[1], "claude-opus-4-6"); w.Code != 200 {
		t.Fatalf("an unrestricted key refused the check: %d %s", w.Code, w.Body.String())
	}
}

// A Desktop check moved onto a model (no group among the lists) a key is
// held away from is refused in that model's name, as the key's lists show
// it (#4175).
func TestClaudeDesktopProbeRefusalNamesTheModelNotTheProbe(t *testing.T) {
	fresh(t)
	served := &fake{t: t, reply: deepseekReply}
	up := httptest.NewServer(served)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "solo", Name: "Solo", Key: "k", Chat: up.URL + "/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelOrder("claude-desktop", []string{"solo/m1"}); err != nil {
		t.Fatal(err)
	}
	keys, secrets := newCaller(t, "Held")
	if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: []string{"other/*"}}); err != nil {
		t.Fatal(err)
	}
	srv := New()
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"claude-opus-4-6","max_tokens":1,"messages":[{"role":"user","content":"."}]}`))
	req.Header.Set("Authorization", "Bearer "+secrets[0])
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh) Claude/2.7032.0 Chrome/140.0 Electron/38.0")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatalf("the probe on the model the key may not use went as %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "may not use solo/m1") {
		t.Fatalf("the 403 does not name the model the key was checked against: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "claude-opus-4-6") {
		t.Fatalf("the 403 names the model the probe sent, not the one checked: %s", w.Body.String())
	}
}
