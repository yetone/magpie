package provider

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// signed is the headers Sign puts on a chat request of p under ctx.
func signed(t *testing.T, ctx context.Context, p Provider) http.Header {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://ark.example/api/v3/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Sign(ctx, req, Chat, nil); err != nil {
		t.Fatal(err)
	}
	return req.Header
}

// once is h's value under name in any case, and how many names of it h
// has.
func once(h http.Header, name string) (string, int) {
	var v string
	n := 0
	for k, vs := range h {
		if strings.EqualFold(k, name) {
			n++
			v = strings.Join(vs, ",")
		}
	}
	return v, n
}

// axelburks on Discord: a model's own headers go after the provider's, a
// name of the model's replacing the provider's in any case, on that
// provider's requests for that model alone; magpie's own auth never
// stands over one, and the agent's header the provider passes on does.
func TestModelHeadersGoAfterTheProvidersOnTheirModelAlone(t *testing.T) {
	prefillHome(t)
	p := Provider{ID: "ark", Name: "Ark", Chat: "https://ark.example/api/v3", Key: "ark-k", Models: []string{"ep-a", "ep-b"},
		Headers: map[string]string{"X-Route": "provider", "X-Keep": "kept"}}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	if err := SetModelHeaders("ark/ep-a", map[string]string{" x-route ": " model ", "X-New": "new", "authorization": "Bearer mine", "X-Thread": "${threadId}"}); err != nil {
		t.Fatal(err)
	}
	q, err := Find("ark")
	if err != nil {
		t.Fatal(err)
	}
	bg := context.Background()
	h := signed(t, WithModel(bg, "ark", "ep-a"), *q)
	for name, want := range map[string]string{"X-Route": "model", "X-Keep": "kept", "X-New": "new", "Authorization": "Bearer mine"} {
		if v, n := once(h, name); v != want || n != 1 {
			t.Errorf("ep-a: %s = %q in %d names, want %q once", name, v, n, want)
		}
	}
	if !q.AsksThread(WithModel(bg, "ark", "ep-a")) || q.AsksThread(WithModel(bg, "ark", "ep-b")) {
		t.Error("a model's ${threadId} is asked for that model alone")
	}
	// another model, none, and another provider's under the same ctx: the
	// provider's own
	for name, ctx := range map[string]context.Context{"ep-b": WithModel(bg, "ark", "ep-b"), "no model": bg, "another provider's": WithModel(bg, "other", "ep-a")} {
		h := signed(t, ctx, *q)
		if v, _ := once(h, "X-Route"); v != "provider" {
			t.Errorf("%s: X-Route %q", name, v)
		}
		if v, _ := once(h, "X-New"); v != "" {
			t.Errorf("%s: sent ep-a's X-New", name)
		}
		if v, _ := once(h, "Authorization"); v != "Bearer ark-k" {
			t.Errorf("%s: Authorization %q", name, v)
		}
	}
	// the agent's own header the provider passes on stands over the model's
	q.PassHeaders = []string{"X-Route"}
	ctx := WithClientHeaders(WithModel(bg, "ark", "ep-a"), "ark", http.Header{"X-Route": {"agent"}})
	if v, n := once(signed(t, ctx, *q), "X-Route"); v != "agent" || n != 1 {
		t.Errorf("passed: X-Route %q in %d", v, n)
	}

	// none takes them away
	if err := SetModelHeaders("ark/ep-a", map[string]string{"": "x"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := settings.Load().ModelHeaders["ark/ep-a"]; ok {
		t.Fatal("headers kept after none were given")
	}
}

func TestSetModelHeadersRefuses(t *testing.T) {
	prefillHome(t)
	if err := Save(Provider{ID: "ark", Name: "Ark", Chat: "https://ark.example/api/v3", Key: "ark-k", Models: []string{"ep-a"}}); err != nil {
		t.Fatal(err)
	}
	for why, h := range map[string]map[string]string{
		"isn't a header name": {"X Bad": "v"},
		"is one line":         {"X-A": "v\r\nX-Injected: 1"},
		"are one header":      {"X-A": "1", "x-a": "2"},
	} {
		if err := SetModelHeaders("ark/ep-a", h); err == nil || !strings.Contains(err.Error(), why) {
			t.Errorf("%v: %v, want %q", h, err, why)
		}
	}
	if err := SetModelHeaders("ark/nope", map[string]string{"X-A": "1"}); err == nil || !strings.Contains(err.Error(), "has no model") {
		t.Errorf("a model it doesn't serve: %v", err)
	}
	if len(settings.Load().ModelHeaders) != 0 {
		t.Fatalf("a refusal wrote %v", settings.Load().ModelHeaders)
	}
	// a signed-in account is sent its sign-in's headers alone, even with
	// some in the file
	s := settings.Load()
	s.ModelHeaders = map[string]map[string]string{"codex/gpt-5.5": {"X-A": "1"}}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if h := (Provider{ID: "codex", Account: &Account{}}).headersFor("gpt-5.5"); h != nil {
		t.Fatalf("an account's model headers: %v", h)
	}
}
