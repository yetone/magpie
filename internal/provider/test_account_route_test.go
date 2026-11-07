package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/testenv"
)

// A model's test asks the account a request for it goes to (#4992,
// scutken): a newapi pool's accounts list different models, and its
// grok-4.7, served by one account alone, was asked of the first and
// failed 503 "no available channel" while the gateway answered real
// requests on the account that serves it.

// twoAccountPlugin is the fake plugin signed in twice, a@fake first:
// first, the first sign-in (an OAuth one with access a@fake unless
// given), and b@fake an OAuth one. The fake vendor answers b@fake
// alone and says what else asked it, as newapi tells an account without
// the model it isn't there.
func twoAccountPlugin(t *testing.T, ctx context.Context, first map[string]any) (Provider, func() []string) {
	t.Helper()
	bun := testenv.Bun(t)
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Setenv("FAKE_GROK", "1")
	t.Cleanup(plugin.Settle)
	var mu sync.Mutex
	var wrong []string
	testenv.FakeVendor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if auth := r.Header.Get("Authorization"); auth != "Bearer b@fake" {
			mu.Lock()
			wrong = append(wrong, auth+" "+r.URL.Path)
			mu.Unlock()
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"error":{"message":"no available channel for model","type":"new_api_error"}}`)
			return
		}
		fmt.Fprint(w, `{"id":"x","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if first == nil {
		first = map[string]any{"type": "oauth", "refresh": "r-a", "access": "a@fake", "expires": 9e15, "accountId": "a@fake"}
	}
	var ls []savedLogin
	for _, u := range []struct {
		user string
		auth map[string]any
	}{
		{"a@fake", first},
		{"b@fake", map[string]any{"type": "oauth", "refresh": "r-b", "access": "b@fake", "expires": 9e15, "accountId": "b@fake"}},
	} {
		k, err := plugin.Import(ctx, "fakeco", u.auth)
		if err != nil {
			t.Fatal(err)
		}
		ls = append(ls, savedLogin{Agent: "plugin:fakeco", User: u.user, Home: k, On: true})
	}
	saveLogins(t, ls...)
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := Find(PluginID("fakeco"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Account == nil || p.Account.User == "b@fake" || len(p.AlsoOn()) != 1 {
		t.Fatalf("the pool: first %+v, %d more", p.Account, len(p.AlsoOn()))
	}
	return *p, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), wrong...)
	}
}

// A model the user lets one account serve (AccountModels, #474), asked
// by "测试模型" and the right-click test (TestModels), goes to that
// account, and its row in the request log names it.
func TestModelTestAsksTheAccountThatServesIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	p, wrong := twoAccountPlugin(t, ctx, nil)
	rows := probeRows(t)
	p.AccountModels = map[string][]string{"a@fake": {"fake-1"}, "b@fake": {"grok-4.7"}}
	rs := p.TestModels(ctx, []string{"grok-4.7"})
	if len(rs) != 1 || !rs[0].OK {
		t.Fatalf("grok-4.7's test: %+v (others asked: %v)", rs, wrong())
	}
	if w := wrong(); len(w) != 0 {
		t.Errorf("asked an account the user kept off grok-4.7: %v", w)
	}
	for _, r := range rows() {
		if r.Model == "grok-4.7" && r.Account != "b@fake" {
			t.Errorf("the test's row names %q, not the account asked: %+v", r.Account, r)
		}
	}
}

// An account whose own list (its plan, as the plugin lists it) leaves a
// model out is passed over for one whose list has it, as the gateway
// passes it over (Account.Lists): the fake plugin lists fake-1 alone to
// an API key "few".
func TestModelTestPassesAnAccountWhoseListLacksIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	p, wrong := twoAccountPlugin(t, ctx, map[string]any{"type": "api", "key": "few"})
	if p.Account.Lists("grok-4.7") || !p.Account.Lists("fake-1") {
		t.Fatal("the first account's own list is not fake-1 alone")
	}
	rs := p.TestModels(ctx, []string{"grok-4.7"})
	if len(rs) != 1 || !rs[0].OK {
		t.Fatalf("grok-4.7's test: %+v (others asked: %v)", rs, wrong())
	}
	if w := wrong(); len(w) != 0 {
		t.Errorf("asked the account whose list lacks grok-4.7: %v", w)
	}
}

// The provider's Test picks each endpoint's model from every account's,
// and asks it of an account that serves it: a first account kept for a
// model the fake plugin doesn't have is never asked.
func TestProviderTestAsksAnAccountThatServesItsModel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	p, wrong := twoAccountPlugin(t, ctx, nil)
	p.AccountModels = map[string][]string{"a@fake": {"none-of-these"}}
	rs := p.Test(ctx)
	if len(rs) == 0 {
		t.Fatal("no endpoint tested")
	}
	for _, r := range rs {
		if !r.OK {
			t.Errorf("%s with %s: %+v (others asked: %v)", r.Protocol, r.Model, r, wrong())
		}
	}
	if w := wrong(); len(w) != 0 {
		t.Errorf("asked the account kept off every model: %v", w)
	}
}

// askingRelay is a relay that answers one model only with one key, as a
// site whose groups serve different models does. It keeps what was
// asked, as key:model.
func askingRelay(t *testing.T, good, model string) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	asked := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Model string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		asked = append(asked, key+":"+in.Model)
		mu.Unlock()
		if key != good || in.Model != model {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, `{"error":{"message":"no available channel for model %s under key %s"}}`, in.Model, key)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"hi"}}]}`)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), asked...)
	}
}

// A key the user lets serve a model (AccountModels by KeyID, #474) is the
// one its test goes with, as routing's, for a model's test and for the
// provider's Test, not the provider's first key.
func TestModelTestUsesTheKeyServingIt(t *testing.T) {
	probeHome(t)
	srv, asked := askingRelay(t, "sk-b", "m-b")
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1",
		Key: "sk-a", Keys: []KeyAccount{{Name: "b", Key: "sk-b"}}, Models: []string{"m-b"}}); err != nil {
		t.Fatal(err)
	}
	if err := SetAccountModels("relay", keyID("sk-a"), []string{"m-a"}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if rs := p.TestModels(context.Background(), []string{"m-b"}); len(rs) != 1 || !rs[0].OK {
		t.Fatalf("m-b's test: %+v (asked %v)", rs, asked())
	}
	if rs := p.Test(context.Background()); len(rs) == 0 || !rs[0].OK || rs[0].Model != "m-b" {
		t.Fatalf("the provider's test: %+v (asked %v)", rs, asked())
	}
	for _, a := range asked() {
		// "sk-a:" alone is the model list the Test reads first, a GET
		if strings.HasPrefix(a, "sk-a:") && a != "sk-a:" {
			t.Errorf("asked the key the user kept off m-b: %v", asked())
		}
	}
}

// A model the user barred every account on from is not asked, as the
// gateway never routes it, rather than asked of the first account and
// failed for a reason the user already set.
func TestModelTestBarredModelIsNotAsked(t *testing.T) {
	probeHome(t)
	srv, asked := askingRelay(t, "sk-a", "m-a")
	p := Provider{ID: "pool", Name: "Pool", Chat: srv.URL + "/v1",
		Account:       &Account{Agent: "pool", User: "first"},
		Models:        []string{"m-a", "m-b"},
		AccountModels: map[string][]string{"first": {"m-a"}}}
	rs := p.TestModels(context.Background(), []string{"m-b"})
	if len(rs) != 1 || rs[0].OK || !strings.Contains(rs[0].Error, "set not to serve") {
		t.Fatalf("m-b's test: %+v", rs)
	}
	if got := asked(); len(got) != 0 {
		t.Errorf("asked anyway: %v", got)
	}
}

// Detect APIs asks each model with a key a request for it goes to, as a
// model's test does: a relay's key kept for other models is not asked.
func TestDetectModelsUsesTheKeyServingIt(t *testing.T) {
	probeHome(t)
	srv, asked := askingRelay(t, "sk-b", "m-b")
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1",
		Key: "sk-a", Keys: []KeyAccount{{Name: "b", Key: "sk-b"}}, Models: []string{"m-b"}}); err != nil {
		t.Fatal(err)
	}
	if err := SetAccountModels("relay", keyID("sk-a"), []string{"m-a"}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	ms, _, err := p.DetectModels(context.Background(), srv.URL+"/v1", []string{"m-b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || len(ms[0].Results) == 0 || !ms[0].Results[0].OK {
		t.Fatalf("m-b's detection: %+v (asked %v)", ms, asked())
	}
	for _, a := range asked() {
		if strings.HasPrefix(a, "sk-a:") {
			t.Errorf("asked the key the user kept off m-b: %v", asked())
		}
	}
}
