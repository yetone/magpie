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
	// sk-b was on All models and its list is short of the provider's: the
	// first fetch ticked its own list, which hasn't changed since
	if got := models("sk-b"); !slices.Equal(got, []string{"kimi-k3"}) {
		t.Errorf("sk-b's models %v, want its own list [kimi-k3]", got)
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

// young02036 on Discord: the editor's Select all available ticks every
// model a key's own list has, un-ticked ones too. KeyListed is that list,
// by the ids the provider lists them by; a key no list marks is unknown.
func TestKeyListedIsTheKeysOwnList(t *testing.T) {
	prefillHome(t)
	l := &relayLists{by: map[string][]string{
		"sk-a": {"gpt-5.5", "glm-5"},
		"sk-b": {"kimi-k3", "glm-5"},
	}}
	srv := l.serve(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Key: "sk-a",
		Keys: []KeyAccount{{Key: "sk-b"}}}); err != nil {
		t.Fatal(err)
	}
	if got := KeyListed("relay", keyID("sk-b")); got != nil {
		t.Fatalf("before any fetch: %v, want nil", got)
	}
	p, _ := Find("relay")
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	// the user un-ticks glm-5 for sk-b: its list still has it
	if err := SetAccountModels("relay", keyID("sk-b"), []string{"kimi-k3"}); err != nil {
		t.Fatal(err)
	}
	if got := KeyListed("relay", strings.ToUpper(keyID("sk-b"))); !slices.Equal(got, []string{"glm-5", "kimi-k3"}) {
		t.Errorf("sk-b's list: %v, want [glm-5 kimi-k3]", got)
	}
	if got := KeyListed("relay", keyID("sk-a")); !slices.Equal(got, []string{"glm-5", "gpt-5.5"}) {
		t.Errorf("sk-a's list: %v, want [glm-5 gpt-5.5]", got)
	}
	if got := KeyListed("relay", keyID("sk-none")); got != nil {
		t.Errorf("a key no list marks: %v, want nil", got)
	}
	if got := KeyListed("nope", keyID("sk-a")); got != nil {
		t.Errorf("no such provider: %v", got)
	}
}

// young02036 on Discord (v0.1.2965): Fetch models ticked each key's
// available models only for keys that already had a list of their own, so
// a provider's first key, keys added before per-key lists and keys whose
// list was read late all stayed on All models. Fetch now ticks each key on
// All models with its own list when that list is short of the provider's;
// a key the user set to All models, or narrowed, stays as they set it.
func TestFetchTicksEachKeysListOnAllModels(t *testing.T) {
	prefillHome(t)
	l := &relayLists{by: map[string][]string{
		"sk-a": {"m1", "m2", "m3", "m4"},
		"sk-b": {"m1", "m2"},
		"sk-c": {"m3", "m5"},
		"sk-d": {"m4"},
	}}
	srv := l.serve(t)
	// every key saved without a list, as keys added before per-key models
	// and a key whose read failed when it was added are
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Key: "sk-a",
		Keys: []KeyAccount{{Key: "sk-b"}, {Key: "sk-c"}, {Key: "sk-d"}}}); err != nil {
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
	// the user sets sk-d to All models before any fetch
	if err := SetAccountModels("relay", keyID("sk-d"), nil); err != nil {
		t.Fatal(err)
	}
	fetch()
	if got := models("sk-b"); !slices.Equal(got, []string{"m1", "m2"}) {
		t.Errorf("sk-b after a fetch: %v, want its list [m1 m2] ticked", got)
	}
	if got := models("sk-c"); !slices.Equal(got, []string{"m3", "m5"}) {
		t.Errorf("sk-c after a fetch: %v, want its list [m3 m5] ticked", got)
	}
	if got := models("sk-a"); !slices.Equal(got, []string{"m1", "m2", "m3", "m4"}) {
		t.Errorf("sk-a after a fetch: %v, want its list [m1 m2 m3 m4] ticked", got)
	}
	if got := models("sk-d"); got != nil {
		t.Errorf("sk-d was set to All models by the user and was given %v", got)
	}
	if p, _ := Find("relay"); p.AccountServes(keyID("sk-b"), "m5") || !p.AccountServes(keyID("sk-c"), "m5") || !p.AccountServes(keyID("sk-d"), "m5") {
		t.Error("the gateway would send m5 to a key whose list hasn't it")
	}

	// the user un-ticks m2 for sk-b, and sets sk-c to All models
	if err := SetAccountModels("relay", keyID("sk-b"), []string{"m1"}); err != nil {
		t.Fatal(err)
	}
	if err := SetAccountModels("relay", keyID("sk-c"), []string{}); err != nil {
		t.Fatal(err)
	}
	fetch()
	fetch()
	if got := models("sk-b"); !slices.Equal(got, []string{"m1"}) {
		t.Errorf("sk-b's un-tick undone by a fetch: %v", got)
	}
	if got := models("sk-c"); got != nil {
		t.Errorf("sk-c set to All models was narrowed by a fetch: %v", got)
	}
	if got := models("sk-d"); got != nil {
		t.Errorf("sk-d set to All models was narrowed by a fetch: %v", got)
	}

	// a key whose list can't be read stays on All models until it can
	if err := AddKey("relay", "E", "sk-e", ""); err != nil {
		t.Fatal(err)
	}
	fetch()
	if got := models("sk-e"); got != nil {
		t.Errorf("sk-e's list failed and it was given %v", got)
	}
	l.set("sk-e", "m2", "m5")
	fetch()
	if got := models("sk-e"); !slices.Equal(got, []string{"m2", "m5"}) {
		t.Errorf("sk-e once its list reads: %v, want [m2 m5]", got)
	}
	// the provider's own picks, what agents get, stay the user's
	if p, _ := Find("relay"); len(p.Models) != 0 {
		t.Errorf("the provider's picks were set: %v", p.Models)
	}
}

// One key alone has nothing to be short of: it stays on All models.
func TestFetchLeavesALoneKeyOnAllModels(t *testing.T) {
	prefillHome(t)
	l := &relayLists{by: map[string][]string{"sk-a": {"m1", "m2"}}}
	srv := l.serve(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Key: "sk-a"}); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("relay"); len(p.AccountModels) != 0 {
		t.Errorf("a lone key was given %v", p.AccountModels)
	}
}
