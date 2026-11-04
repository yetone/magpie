package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

// lists is a relay's models for each key, changed between fetches.
type relayLists struct {
	mu sync.Mutex
	by map[string][]string
}

func (l *relayLists) set(key string, ids ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.by[key] = ids
}

func (l *relayLists) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		l.mu.Lock()
		ids, ok := l.by[key]
		l.mu.Unlock()
		if !ok {
			http.Error(rw, `{"error":{"message":"invalid key"}}`, http.StatusUnauthorized)
			return
		}
		var b strings.Builder
		b.WriteString(`{"data":[`)
		for i, id := range ids {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(`{"id":"` + id + `"}`)
		}
		b.WriteString(`]}`)
		_, _ = rw.Write([]byte(b.String()))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// young02036 on Discord: a provider's keys each serve the models of their
// own group, and each key's list was read only when it was added. Fetch
// models now carries what each key's list gained and lost into that key's
// models, and leaves what the user un-ticked, or ticked, as they set it.
func TestFetchRefreshesEachKeysModels(t *testing.T) {
	prefillHome(t)
	l := &relayLists{by: map[string][]string{
		"sk-a": {"gpt-5.5", "deepseek-v4", "glm-5"},
		"sk-b": {"kimi-k3"},
	}}
	srv := l.serve(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Key: "sk-a",
		Keys: []KeyAccount{{Key: "sk-b"}}}); err != nil {
		t.Fatal(err)
	}
	fetch := func() {
		t.Helper()
		p, _ := Find("relay")
		if _, err := p.Fetch(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	models := func(key string) []string {
		p, _ := Find("relay")
		return p.AccountModels[keyID(key)]
	}
	fetch()

	// a key added takes its own list, as the GUI's add does it
	l.set("sk-c", "deepseek-v4", "qwen3-max")
	had := KeysHeld("relay")
	if err := AddKey("relay", "C group", "sk-c", ""); err != nil {
		t.Fatal(err)
	}
	PrefillNewKeys(context.Background(), had, "relay")
	fetch()
	if got := models("sk-c"); !slices.Equal(got, []string{"deepseek-v4", "qwen3-max"}) {
		t.Fatalf("the added key's models %v", got)
	}
	// the user narrows sk-a by hand: glm-5 un-ticked
	if err := SetAccountModels("relay", keyID("sk-a"), []string{"gpt-5.5", "deepseek-v4"}); err != nil {
		t.Fatal(err)
	}

	// the groups change: sk-a's gives up deepseek-v4 and takes kimi-k3,
	// sk-c's takes glm-5
	l.set("sk-a", "gpt-5.5", "glm-5", "kimi-k3")
	l.set("sk-c", "deepseek-v4", "qwen3-max", "glm-5")
	fetch()
	if got := models("sk-a"); !slices.Equal(got, []string{"gpt-5.5", "kimi-k3"}) {
		t.Errorf("sk-a after a fetch: %v, want [gpt-5.5 kimi-k3] (deepseek-v4 gone, glm-5 still un-ticked)", got)
	}
	if got := models("sk-c"); !slices.Equal(got, []string{"deepseek-v4", "qwen3-max", "glm-5"}) {
		t.Errorf("sk-c after a fetch: %v, want glm-5 ticked", got)
	}
	if got := models("sk-b"); got != nil {
		t.Errorf("sk-b serves every model and was given %v", got)
	}
	// the gateway sends sk-c the model its group took on
	if p, _ := Find("relay"); !p.AccountServes(keyID("sk-c"), "glm-5") || p.AccountServes(keyID("sk-a"), "deepseek-v4") {
		t.Error("the keys' models don't hold the gateway's requests")
	}

	// the user un-ticks glm-5 for sk-c and ticks deepseek-v4 for sk-a,
	// which its list hasn't; fetched again, both stay as they set them
	if err := SetAccountModels("relay", keyID("sk-c"), []string{"deepseek-v4", "qwen3-max"}); err != nil {
		t.Fatal(err)
	}
	if err := SetAccountModels("relay", keyID("sk-a"), []string{"gpt-5.5", "kimi-k3", "deepseek-v4"}); err != nil {
		t.Fatal(err)
	}
	fetch()
	fetch()
	if got := models("sk-c"); !slices.Equal(got, []string{"deepseek-v4", "qwen3-max"}) {
		t.Errorf("sk-c's un-tick undone by a fetch: %v", got)
	}
	if got := models("sk-a"); !slices.Equal(got, []string{"gpt-5.5", "kimi-k3", "deepseek-v4"}) {
		t.Errorf("sk-a's tick undone by a fetch: %v", got)
	}

	// a key whose list can't be read now keeps its models
	l.mu.Lock()
	delete(l.by, "sk-c")
	l.mu.Unlock()
	l.set("sk-a", "gpt-5.5")
	fetch()
	if got := models("sk-c"); !slices.Equal(got, []string{"deepseek-v4", "qwen3-max"}) {
		t.Errorf("sk-c's models changed while its list failed: %v", got)
	}
	// the provider's own picks, what agents get, stay the user's
	if p, _ := Find("relay"); len(p.Models) != 0 {
		t.Errorf("the provider's picks were set: %v", p.Models)
	}
}
