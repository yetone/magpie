package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// asked is what a vendor was asked for, in order, from the several
// goroutines a model test runs at once.
type asked struct {
	mu     sync.Mutex
	models []string
}

func (a *asked) add(model string) {
	a.mu.Lock()
	a.models = append(a.models, model)
	a.mu.Unlock()
}

func (a *asked) all() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.models)
}

// wire takes what the vendor was asked for since the last call and says
// whether every one of those requests named a model the way a turn does:
// by the name on the wire, and nothing else. Taking it empties the record,
// so a probe that went out under the name magpie knows can't be vouched
// for by a later probe that went out under the right one.
func (a *asked) wire(t *testing.T, what string, want []string) []string {
	t.Helper()
	a.mu.Lock()
	got := slices.Clone(a.models)
	a.models = nil
	a.mu.Unlock()
	if len(got) == 0 {
		t.Errorf("%s asked the vendor for no model at all", what)
	}
	for _, m := range got {
		if !slices.Contains(want, m) {
			t.Errorf("%s asked the vendor for %q; a turn only ever carries %v", what, m, want)
		}
	}
	return got
}

// asking is a vendor that answers with the model it was asked for, as a
// relay does when it serves models under ids of its own, and records what it
// was asked for.
func asking(t *testing.T, a *asked) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"data":[{"id":"model-2"},{"id":"model-3"}]}`)
			return
		}
		var b struct {
			Model string `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&b)
		a.add(b.Model)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"id":"x","model":`+strconv.Quote(b.Model)+
			`,"choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":5,"completion_tokens":3}}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(s.Close)
	return s
}

// putProvider adds a provider to the file the configured and the signed-in
// ones are merged from, for a provider whose id Save reserves and a test
// still needs magpie to be able to find.
func putProvider(t *testing.T, p map[string]any) {
	t.Helper()
	b, err := json.Marshal(map[string]any{"providers": []map[string]any{p}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(provider.Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(provider.Path(), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A relay that serves a model under an id of its own is asked for it by that
// id, and by nothing else: the catalog agents see, the routing groups and the
// usage records keep the name magpie knows the model by, so a rename upstream
// is not a rename here.
func TestUpstreamNameReachesTheVendorOnly(t *testing.T) {
	fresh(t)

	var seen asked
	relay, other := asking(t, &seen), asking(t, &seen)

	for _, p := range []provider.Provider{
		{ID: "relay-b", Name: "Relay B", Key: "k", Models: []string{"model-2"}, Chat: relay.URL},
		{ID: "other", Name: "Other", Key: "k", Models: []string{"model-2"}, Chat: other.URL},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SetUpstreamName("relay-b/model-2", "vendor-c/model-2-preview"); err != nil {
		t.Fatal(err)
	}

	s := New()
	ask := func(model string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(
			`{"model":"`+model+`","max_tokens":1024,"stream":true,`+
				`"messages":[{"role":"user","content":"hi"}]}`))
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: status %d: %s", model, rec.Code, rec.Body.String())
		}
	}

	// the relay is asked for the model by the name it serves it under
	ask("relay-b/model-2")
	if got := seen.all(); len(got) != 1 || got[0] != "vendor-c/model-2-preview" {
		t.Fatalf("the relay was asked for %v; want the upstream name", got)
	}

	// another provider serving the same model id is left alone: an upstream
	// name is one provider's, not the model's
	ask("other/model-2")
	if got := seen.all(); len(got) != 2 || got[1] != "model-2" {
		t.Fatalf("the other provider was asked for %v", got[1:])
	}

	// the catalog still knows the model by magpie's name for it
	found := false
	for _, e := range provider.Catalog() {
		if e.ID == "relay-b/model-2" {
			found = true
			if e.Model != "model-2" {
				t.Errorf("the catalog shows the model as %q; the canonical name is what agents see", e.Model)
			}
		}
	}
	if !found {
		t.Fatal("relay-b/model-2 is not in the catalog")
	}
	if _, model, ok := provider.Resolve("relay-b/model-2"); !ok || model != "model-2" {
		t.Errorf("Resolve gave %q; the wire name is not the model's identity", model)
	}

	// a call is recorded under the canonical name; what the vendor's own
	// answer said it was is kept beside it, which is what that field is for
	rows := usage.Load(time.Time{})
	if len(rows) == 0 {
		t.Fatal("no call was recorded")
	}
	for _, r := range rows {
		if r.Model != "model-2" {
			t.Errorf("a call was recorded as %q; the vendor's name is not the model's", r.Model)
		}
	}
	if rows[0].Served != "vendor-c/model-2-preview" {
		t.Errorf("the record says the vendor served %q", rows[0].Served)
	}

	// taking the user's name away asks for the model's own again
	if err := provider.SetUpstreamName("relay-b/model-2", ""); err != nil {
		t.Fatal(err)
	}
	ask("relay-b/model-2")
	if got := seen.all(); got[len(got)-1] != "model-2" {
		t.Fatalf("after --reset the relay was asked for %q", got[len(got)-1])
	}
}

// A name given for a provider outlives that provider, and it outlives it by
// the id: the provider can be deleted and another take the same id, and the
// name goes on asking that one's models for theirs. So a removal has to work
// off the key the name is stored at — which is all `magpie model wire
// relay-b/model-2 --reset` has left to work off once the provider is gone —
// and here it is: with the name off, the relay that took the id is asked for
// its own model by its own id.
func TestUpstreamNameDroppedWithItsProvider(t *testing.T) {
	fresh(t)

	var gone, here asked
	was, now := asking(t, &gone), asking(t, &here)

	for _, p := range []provider.Provider{
		{ID: "relay-b", Name: "Relay B", Key: "k", Models: []string{"model-2"}, Chat: was.URL},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SetUpstreamName("relay-b/model-2", "vendor-c/model-2-preview"); err != nil {
		t.Fatal(err)
	}
	if err := provider.Delete("relay-b"); err != nil {
		t.Fatal(err)
	}
	// the name outlived the provider that had it, and the id is free: a
	// relay taking it is asked for the model under the deleted one's
	// vendor's name, which is the hazard the removal is for
	if err := provider.Save(provider.Provider{ID: "relay-b", Name: "Relay B", Key: "k",
		Models: []string{"model-2"}, Chat: now.URL}); err != nil {
		t.Fatal(err)
	}

	s := New()
	ask := func(model string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(
			`{"model":"`+model+`","max_tokens":1024,"stream":true,`+
				`"messages":[{"role":"user","content":"hi"}]}`))
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: status %d: %s", model, rec.Code, rec.Body.String())
		}
	}

	ask("relay-b/model-2")
	if got := here.all(); len(got) != 1 || got[0] != "vendor-c/model-2-preview" {
		t.Fatalf("the relay that took the id was asked for %v; want the name the deleted one had", got)
	}

	if dropped, err := provider.DropUpstreamName("relay-b/model-2"); err != nil || !dropped {
		t.Fatalf("DropUpstreamName = %v, %v; want the name taken off by its key", dropped, err)
	}
	ask("relay-b/model-2")
	if got := here.all(); len(got) != 2 || got[1] != "model-2" {
		t.Fatalf("with the name off the relay that took the id was asked for %v; want model-2", got[1:])
	}
	if got := gone.all(); len(got) != 0 {
		t.Errorf("the deleted relay's was asked for %v; nothing sends to it now", got)
	}
}

// The same removal, with a provider shown by the id whose name is gone: the
// name a provider is shown by is a spelling any other provider answers to as
// well, so a ref spelled with the id b's provider had names that provider
// rather than the one the name under it is for. Taken through that name, the
// removal would take away the other provider's own name — a name that is
// really in force — and leave the one that has this relay's models going out
// renamed to a vendor that never heard of them, for whoever takes the id.
func TestUpstreamNameDroppedUnderAProviderShownAsTheGoneId(t *testing.T) {
	fresh(t)

	var here, that asked
	now, other := asking(t, &here), asking(t, &that)

	if err := provider.Save(provider.Provider{ID: "b", Name: "B", Key: "k",
		Models: []string{"model-2"}, Chat: now.URL}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetUpstreamName("b/model-2", "vendor-c/model-2-preview"); err != nil {
		t.Fatal(err)
	}
	if err := provider.Delete("b"); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "b", Name: "B Two", Key: "k",
		Models: []string{"model-2"}, Chat: now.URL}); err != nil {
		t.Fatal(err)
	}

	s := New()
	ask := func(model string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(
			`{"model":"`+model+`","max_tokens":1024,"stream":true,`+
				`"messages":[{"role":"user","content":"hi"}]}`))
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: status %d: %s", model, rec.Code, rec.Body.String())
		}
	}

	// the id b is free again, and the relay that took it is asked for its
	// model under the deleted one's vendor's name
	ask("b/model-2")
	if got := here.all(); len(got) != 1 || got[0] != "vendor-c/model-2-preview" {
		t.Fatalf("the relay that took the id was asked for %v; want the name the deleted one had", got)
	}

	// that relay goes too, leaving the name in force for the id b still, and
	// a relay of an id of its own is now shown by the id b — with a name of
	// its own, which is the one a removal taken through that name would take
	// away instead
	if err := provider.Delete("b"); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "c", Name: "B", Key: "k",
		Models: []string{"model-2"}, Chat: other.URL}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetUpstreamName("B/model-2", "vendor-c/model-2"); err != nil {
		t.Fatal(err)
	}
	ask("c/model-2")
	if got := that.all(); len(got) != 1 || got[0] != "vendor-c/model-2" {
		t.Fatalf("the relay shown as B was asked for %v; want the name given for it", got)
	}

	if dropped, err := provider.DropUpstreamName("b/model-2"); err != nil || !dropped {
		t.Fatalf("DropUpstreamName = %v, %v; want the name taken off by its key", dropped, err)
	}
	if got := settings.Load().ModelWires; len(got) != 1 || got["c/model-2"] != "vendor-c/model-2" {
		t.Fatalf("the settings hold %v; want the name the deleted relay had gone, and the other left in force", got)
	}

	// which is all taking that name off does to what goes out: the relay
	// shown as B is still asked for its model by the name given for it, and
	// the one that took the freed id b is asked for its own
	ask("c/model-2")
	if got := that.all(); len(got) != 2 || got[1] != "vendor-c/model-2" {
		t.Fatalf("with the other name off the relay shown as B was asked for %v; want the name given for it", got[1:])
	}
	if got := here.all(); len(got) != 1 {
		t.Errorf("the relay that took the id was asked for %v again; nothing sends to it now", got)
	}
	if err := provider.Save(provider.Provider{ID: "b", Name: "B Two", Key: "k",
		Models: []string{"model-2"}, Chat: now.URL}); err != nil {
		t.Fatal(err)
	}
	ask("b/model-2")
	if got := here.all(); len(got) != 2 || got[1] != "model-2" {
		t.Fatalf("with the name off the relay that took the id was asked for %v; want model-2", got[1:])
	}
}

// A name given for a provider's every model has the model in it, so each
// model is asked for by its own: one name covers a relay that namespaces its
// models, and none of them collapses into another's — which is what the
// record, and the bill read from it, would otherwise say answered.
func TestUpstreamWildcardNamesEachModel(t *testing.T) {
	fresh(t)

	var seen asked
	relay := asking(t, &seen)
	if err := provider.Save(provider.Provider{ID: "relay-b", Name: "Relay B", Key: "k",
		Models: []string{"model-2", "model-3"}, Chat: relay.URL}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetUpstreamName("relay-b/*", "vendor-c/sol-*"); err != nil {
		t.Fatal(err)
	}

	s := New()
	for _, model := range []string{"model-2", "model-3"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(
			`{"model":"relay-b/`+model+`","max_tokens":1024,"stream":true,`+
				`"messages":[{"role":"user","content":"hi"}]}`))
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: status %d: %s", model, rec.Code, rec.Body.String())
		}
	}
	want := []string{"vendor-c/sol-model-2", "vendor-c/sol-model-3"}
	if got := seen.all(); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("the relay was asked for %v; want %v — each model by its own name", got, want)
	}

	// the reply naming the model it was asked for is that model answering:
	// "vendor-c/sol-model-3" is not another model than the one asked for
	r := lastRoute(s)
	if r.Tries[0].Model != "model-3" {
		t.Errorf("the try is for %q; a wire name is not the model's identity", r.Tries[0].Model)
	}
	if r.Served != "vendor-c/sol-model-3" {
		t.Errorf("the route says %q served", r.Served)
	}
	if r.Swapped || r.Tries[0].Swapped {
		t.Errorf("the model answered under the relay's own id is marked swapped: %+v", r.Tries[0])
	}
	// and the ledger, which reads the records back, marks it the same way
	rows, _, _ := usage.Ledger(usage.Month, usage.Filter{})
	if len(rows) == 0 {
		t.Fatal("the ledger has no rows")
	}
	for _, row := range rows {
		if row.Model != "model-2" && row.Model != "model-3" {
			t.Errorf("a row is for %q; a wire name is not the model's", row.Model)
		}
		if row.Swapped {
			t.Errorf("a row is marked swapped though the vendor served what it was asked for: %+v", row)
		}
	}
}

// The first test drives a request that has to be translated, because the
// relay only speaks Chat Completions. These two cover the paths that test
// cannot reach: a request the provider understands as it came, which magpie
// relays without rebuilding, and the token count asked of the vendor.
func TestUpstreamNameOnNativeAndCountPaths(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
	}{
		{"a request relayed as it came", "/v1/messages"},
		{"a token count asked of the vendor", "/v1/messages/count_tokens"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			var seen asked
			// the vendor's own reply, naming the model it was asked for
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var b struct {
					Model string `json:"model"`
				}
				json.NewDecoder(r.Body).Decode(&b)
				seen.add(b.Model)
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "count_tokens") {
					io.WriteString(w, `{"input_tokens":7}`)
					return
				}
				io.WriteString(w, `{"id":"x","model":`+strconv.Quote(b.Model)+
					`,"content":[{"type":"text","text":"hi"}],`+
					`"usage":{"input_tokens":5,"output_tokens":3}}`)
			}))
			t.Cleanup(up.Close)
			// an Anthropic base, so /v1/messages is the provider's own
			// protocol and the request is relayed rather than rebuilt
			if err := provider.Save(provider.Provider{
				ID: "relay", Name: "Relay", Key: "k",
				Models:    []string{"sol"},
				Anthropic: up.URL,
			}); err != nil {
				t.Fatal(err)
			}
			if err := provider.SetUpstreamName("relay/sol", "vendor-c/sol-2"); err != nil {
				t.Fatal(err)
			}

			s := New()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", tc.path, strings.NewReader(
				`{"model":"relay/sol","max_tokens":64,`+
					`"messages":[{"role":"user","content":"hi"}]}`))
			s.Handler().ServeHTTP(rec, req)
			if rec.Code != 200 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			if got := seen.all(); len(got) != 1 || got[0] != "vendor-c/sol-2" {
				t.Fatalf("the vendor was asked for %v; want the upstream name", got)
			}
			// "vendor-c/sol-2" is the same model as "sol", and a name that
			// is not a prefix or a dated version of it is exactly what a
			// comparison with magpie's own name would get wrong
			if tc.path == "/v1/messages" {
				if r := lastRoute(s); r.Served != "vendor-c/sol-2" || r.Swapped {
					t.Errorf("the model answered under the relay's own id is marked swapped: %+v", r)
				}
			}
		})
	}
}

// The lookup is the model's own name, then the provider's "*", then the
// canonical id — and --reset removes only the first, leaving a provider-wide
// name in force. The CLI says so; this is where it is true.
func TestUpstreamNamePrecedence(t *testing.T) {
	fresh(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k",
		Chat: "https://relay.example/v1", Models: []string{"sol", "other"}}); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}

	// with nothing set, the vendor is asked for the model magpie knows
	if got := provider.UpstreamName(*p, "sol"); got != "sol" {
		t.Errorf("unset: %q; want the canonical id", got)
	}

	// a name given for every model of the provider covers a model that has
	// none of its own, with each model in it
	if err := provider.SetUpstreamName("relay/*", "vendor-c/*"); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"sol", "other"} {
		if got := provider.UpstreamName(*p, m); got != "vendor-c/"+m {
			t.Errorf("%s under the provider's *: %q; want vendor-c/%s", m, got, m)
		}
	}

	// a name with no model in it is every model's own name under that one
	if err := provider.SetUpstreamName("relay/*", "vendor-c/one"); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"sol", "other"} {
		if got := provider.UpstreamName(*p, m); got != "vendor-c/one" {
			t.Errorf("%s under a name with no *: %q; want vendor-c/one", m, got)
		}
	}

	// the model's own wins
	if err := provider.SetUpstreamName("relay/sol", "vendor-c/sol-exact"); err != nil {
		t.Fatal(err)
	}
	if got := provider.UpstreamName(*p, "sol"); got != "vendor-c/sol-exact" {
		t.Errorf("exact: %q; want vendor-c/sol-exact", got)
	}
	if got := provider.UpstreamName(*p, "other"); got != "vendor-c/one" {
		t.Errorf("other: %q; want the provider's vendor-c/one", got)
	}

	// taking the model's away leaves the provider's in force
	if err := provider.SetUpstreamName("relay/sol", ""); err != nil {
		t.Fatal(err)
	}
	if got := provider.UpstreamName(*p, "sol"); got != "vendor-c/one" {
		t.Errorf("after --reset: %q; want the provider's vendor-c/one still in force", got)
	}
	if err := provider.SetUpstreamName("relay/*", ""); err != nil {
		t.Fatal(err)
	}
	if got := provider.UpstreamName(*p, "sol"); got != "sol" {
		t.Errorf("after both are reset: %q; want the canonical id", got)
	}
	// what was given is kept as it was given, the "*" and all, for a list of
	// them to show
	if err := provider.SetUpstreamName("relay/*", "vendor-c/*"); err != nil {
		t.Fatal(err)
	}
	if got := p.UpstreamNames()["*"]; got != "vendor-c/*" {
		t.Errorf("the name given is %q; want it as it was given", got)
	}
}

// A wire name keyed by something no lookup will match is refused where it is
// given rather than saved as a silent no-op, and one that is only whitespace
// is the user taking their name away, not a stored empty one.
func TestUpstreamNameRejectsBadKeys(t *testing.T) {
	fresh(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k",
		Chat: "https://relay.example/v1", Models: []string{"sol"}}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"noslash", "/sol", "Not A Provider/sol", "relay/"} {
		if err := provider.SetUpstreamName(key, "vendor-c/sol"); err == nil {
			t.Errorf("the key %q was accepted", key)
		}
	}
	// a key hand-edited into the file is that entry's own no-op, and must
	// not keep every other setting from being saved
	s := settings.Load()
	if s.ModelWires == nil {
		s.ModelWires = map[string]string{}
	}
	s.ModelWires["noslash"] = "vendor-c/sol"
	if err := settings.Save(s); err != nil {
		t.Fatalf("a wire name keyed badly by hand stopped the settings being saved: %v", err)
	}
	if err := provider.SetUpstreamName("relay/sol", "   "); err != nil {
		t.Errorf("a whitespace-only name should remove the override, not fail: %v", err)
	}
}

// A provider given another id takes the names the user gave its models with
// it: a model asked for by the relay's own id would be asked for by magpie's
// again, and the relay would be asked for a model it does not have.
func TestUpstreamNamesFollowTheProviderRename(t *testing.T) {
	fresh(t)
	if err := provider.Save(provider.Provider{ID: "relay-b", Name: "Relay B", Key: "k",
		Chat: "https://relay.example/v1", Models: []string{"model-2", "model-3"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetUpstreamName("relay-b/model-2", "vendor-c/model-2-preview"); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetUpstreamName("relay-b/*", "vendor-c/sol-*"); err != nil {
		t.Fatal(err)
	}
	if err := provider.Rename("relay-b", "relay-c"); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("relay-c")
	if err != nil {
		t.Fatal(err)
	}
	if got := provider.UpstreamName(*p, "model-2"); got != "vendor-c/model-2-preview" {
		t.Errorf("the model's own name is %q after the rename", got)
	}
	if got := provider.UpstreamName(*p, "model-3"); got != "vendor-c/sol-model-3" {
		t.Errorf("the provider's name is %q after the rename; it follows the id", got)
	}
}

// A relay that only knows its own ids would fail a test naming the model
// magpie knows it by, though every real turn reaches it: the provider's own
// tests ask for the model the way a request does. Each of the two probes is
// held to that on its own, on what it asked the relay for while it was the
// only one running — a provider test asking for magpie's name and a model
// test asking for the relay's, read together, both look right.
func TestProviderTestsAskTheWireName(t *testing.T) {
	fresh(t)
	var seen asked
	relay := asking(t, &seen)
	if err := provider.Save(provider.Provider{ID: "relay-b", Name: "Relay B", Key: "k",
		Models: []string{"model-2", "model-3"}, Chat: relay.URL}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetUpstreamName("relay-b/*", "vendor-c/sol-*"); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("relay-b")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// the two names the relay serves these models by
	wire := []string{"vendor-c/sol-model-2", "vendor-c/sol-model-3"}

	results := p.Test(ctx)
	if len(results) == 0 {
		t.Fatal("the provider was not tested at all")
	}
	for _, r := range results {
		if !r.OK {
			t.Errorf("the provider's own test failed: %+v", r)
		}
	}
	seen.wire(t, "the provider's own test", wire)

	for _, r := range p.TestModels(ctx, []string{"model-2", "model-3"}) {
		if !r.OK {
			t.Errorf("testing %q failed: %+v", r.Model, r)
		}
	}
	// each model by its own wire name, and both of them asked for
	if got := seen.wire(t, "testing the models", wire); len(got) != len(wire) {
		t.Errorf("testing the models asked the relay for %v; want one request per model", got)
	}
	// the model reported is the one magpie knows, not the relay's own id
	for _, r := range p.TestModels(ctx, []string{"model-2"}) {
		if r.Model != "model-2" {
			t.Errorf("a test reports the model as %q; the canonical id is what the user picked", r.Model)
		}
	}
}

// A drawing is the one request that keeps the name magpie knows the model by:
// the gateway builds those bodies itself, on the images API or on chat with
// modalities, and no upstream name is written into one. A provider test that
// asked for the vendor's own name would be testing a request no turn ever
// makes, and would report a working drawing model as broken.
func TestProviderDrawingTestsAskTheCanonicalName(t *testing.T) {
	fresh(t)
	var seen asked
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Model string `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&b)
		seen.add(b.Model)
		if strings.HasSuffix(r.URL.Path, "/images/generations") {
			// not served here, so the chat fallback is taken as the
			// gateway takes it
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"id":"x","model":`+strconv.Quote(b.Model)+
			`,"choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":5,"completion_tokens":3}}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(relay.Close)
	if err := provider.Save(provider.Provider{ID: "relay-b", Name: "Relay B", Key: "k",
		Models: []string{"imagine-2"}, Chat: relay.URL}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetUpstreamName("relay-b/*", "vendor-c/*"); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("relay-b")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range p.TestModels(context.Background(), []string{"imagine-2"}) {
		if !r.OK {
			t.Fatalf("testing the drawing model failed: %+v", r)
		}
	}
	// the images request and the chat one it fell back to, neither carrying
	// the upstream name
	if got := seen.all(); len(got) != 2 || got[0] != "imagine-2" || got[1] != "imagine-2" {
		t.Fatalf("the relay was asked for %v; a drawing goes out under the name magpie knows the model by", got)
	}
}

// A name that is only whitespace is the user taking their name away, not one
// to be kept: it is a legal removal, so it must not fail, and it must leave
// nothing behind — no key in the settings, and the model asked for by the
// name magpie knows it by. A wire name that is a space in the file would be
// sent to the vendor as the model, and a list of them would show it as one
// the user gave.
func TestUpstreamNameBlankIsAReset(t *testing.T) {
	fresh(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k",
		Chat: "https://relay.example/v1", Models: []string{"sol", "other"}}); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}

	if err := provider.SetUpstreamName("relay/sol", "vendor-c/sol"); err != nil {
		t.Fatal(err)
	}
	if got := provider.UpstreamName(*p, "sol"); got != "vendor-c/sol" {
		t.Fatalf("before the reset: %q; want vendor-c/sol", got)
	}

	// blank takes a name that was given away, and is not an error
	if err := provider.SetUpstreamName("relay/sol", "   "); err != nil {
		t.Fatalf("a whitespace-only name is the user taking theirs away, not a bad input: %v", err)
	}
	if n, ok := settings.Load().ModelWires["relay/sol"]; ok {
		t.Errorf("the settings kept a wire name of %q for relay/sol", n)
	}
	if got := provider.UpstreamName(*p, "sol"); got != "sol" {
		t.Errorf("after a blank name: %q; want the canonical id", got)
	}
	if _, ok := p.UpstreamNames()["sol"]; ok {
		t.Error("a name taken away is still listed as one the user gave")
	}

	// and blank for a key there is nothing under writes no key at all
	if err := provider.SetUpstreamName("relay/other", "\t\n "); err != nil {
		t.Fatalf("a whitespace-only name is the user taking theirs away, not a bad input: %v", err)
	}
	if n, ok := settings.Load().ModelWires["relay/other"]; ok {
		t.Errorf("a blank name wrote %q for relay/other", n)
	}
	if got := provider.UpstreamName(*p, "other"); got != "other" {
		t.Errorf("after a blank name: %q; want the canonical id", got)
	}
	if got := p.UpstreamNames(); len(got) != 0 {
		t.Errorf("the names given are %v; a blank one is none", got)
	}

	// a provider-wide name in force is what a model's own blank name falls
	// back to, not a blank name of its own
	if err := provider.SetUpstreamName("relay/*", "vendor-c/*"); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetUpstreamName("relay/sol", "  "); err != nil {
		t.Fatalf("a whitespace-only name is the user taking theirs away, not a bad input: %v", err)
	}
	if got := provider.UpstreamName(*p, "sol"); got != "vendor-c/sol" {
		t.Errorf("with the provider's name in force: %q; want vendor-c/sol", got)
	}
}

// A name left blank in the file — by an older magpie, or by hand — is no
// name: the model is asked for by the name magpie knows it by, and a list of
// them does not show a blank as one the user gave. It is the same thing a
// blank name is where it is set, and the file is no more exempt.
func TestUpstreamNameBlankInTheFileIsNoName(t *testing.T) {
	fresh(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k",
		Chat: "https://relay.example/v1", Models: []string{"sol", "other"}}); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}

	s := settings.Load()
	s.ModelWires = map[string]string{
		"relay/sol":   "   ",
		"relay/other": "vendor-c/other",
	}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}

	if got := provider.UpstreamName(*p, "sol"); got != "sol" {
		t.Errorf("a blank name in the file: %q; want the canonical id", got)
	}
	if got := provider.UpstreamName(*p, "other"); got != "vendor-c/other" {
		t.Errorf("a name beside a blank one: %q; want vendor-c/other", got)
	}
	names := p.UpstreamNames()
	if _, ok := names["sol"]; ok {
		t.Errorf("a blank name in the file is listed as one given: %v", names)
	}
	if names["other"] != "vendor-c/other" {
		t.Errorf("the names given are %v; a blank one must not hide the rest", names)
	}

	// the same for the provider-wide key, which covers the models a blank
	// model's own name does not reach
	s = settings.Load()
	s.ModelWires["relay/*"] = "  "
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if got := provider.UpstreamName(*p, "sol"); got != "sol" {
		t.Errorf("a blank name in the file: %q; want the canonical id", got)
	}
	if got := p.UpstreamNames(); len(got) != 1 || got["other"] != "vendor-c/other" {
		t.Errorf("the names given are %v; a blank one is none", got)
	}
}

// Every "*" in a wire name is the model, wherever it stands and however many
// there are — the rule the settings' own comment, the CLI help and the README
// all give, and what the lookup does. A name with none is that one name for
// every model of the key, which is what a query lists back as it was given,
// "*" and all, rather than a name with one filled in.
func TestUpstreamNameWildcardStandsWhereverItStands(t *testing.T) {
	fresh(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k",
		Chat: "https://relay.example/v1", Models: []string{"sol", "other"}}); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		wires map[string]string
		model string
		want  string
		why   string
	}{
		{map[string]string{"relay/sol": "vendor-c/*"}, "sol", "vendor-c/sol", "a trailing *"},
		{map[string]string{"relay/sol": "vendor/*/pro"}, "sol", "vendor/sol/pro", "a * in the middle"},
		{map[string]string{"relay/sol": "*-pro"}, "sol", "sol-pro", "a leading *"},
		{map[string]string{"relay/sol": "a/*-b/*"}, "sol", "a/sol-b/sol", "two *"},
		{map[string]string{"relay/sol": "*"}, "sol", "sol", "nothing but a *"},
		{map[string]string{"relay/*": "vendor/*/pro"}, "sol", "vendor/sol/pro", "the provider's, mid-name"},
		{map[string]string{"relay/*": "vendor-c/one"}, "sol", "vendor-c/one", "no * at all"},
		{map[string]string{"relay/*": "vendor-c/one"}, "other", "vendor-c/one", "no *, other model"},
		{map[string]string{"relay/sol": "vendor-c/sol"}, "other", "other", "another provider's model"},
	} {
		if got := provider.UpstreamNameIn(tc.wires, "relay", tc.model); got != tc.want {
			t.Errorf("%s: %q; want %q", tc.why, got, tc.want)
		}
	}

	// what a query shows is what was given, "*" and all, so a name given for
	// every model can be read as what it sends rather than as one model's
	if err := provider.SetUpstreamName("relay/*", "vendor/*/pro"); err != nil {
		t.Fatal(err)
	}
	if got := p.UpstreamNames()["*"]; got != "vendor/*/pro" {
		t.Errorf("the name given for every model is listed as %q; want it as it was given", got)
	}
	for _, m := range []string{"sol", "other"} {
		if got := provider.UpstreamName(*p, m); got != "vendor/"+m+"/pro" {
			t.Errorf("%s under the provider's name: %q; want vendor/%s/pro", m, got, m)
		}
	}

	// a name with no "*" in it is that one name for every model of the key,
	// and is listed as it was given too
	if err := provider.SetUpstreamName("relay/*", "vendor-c/one"); err != nil {
		t.Fatal(err)
	}
	if got := p.UpstreamNames()["*"]; got != "vendor-c/one" {
		t.Errorf("the name given for every model is listed as %q; want vendor-c/one", got)
	}
	for _, m := range []string{"sol", "other"} {
		if got := provider.UpstreamName(*p, m); got != "vendor-c/one" {
			t.Errorf("%s under a name with no *: %q; want vendor-c/one", m, got)
		}
	}

	// and a single model's own name, wildcard and all, beside the
	// provider's
	if err := provider.SetUpstreamName("relay/sol", "vendor-c/sol-*"); err != nil {
		t.Fatal(err)
	}
	names := p.UpstreamNames()
	if names["sol"] != "vendor-c/sol-*" || names["*"] != "vendor-c/one" {
		t.Errorf("the names given are %v; each is listed as it was given", names)
	}
	if got := provider.UpstreamName(*p, "sol"); got != "vendor-c/sol-sol" {
		t.Errorf("the model's own: %q; want vendor-c/sol-sol", got)
	}
	if got := provider.UpstreamName(*p, "other"); got != "vendor-c/one" {
		t.Errorf("the other model: %q; want the provider's vendor-c/one", got)
	}
}

// A name given for a provider's every model is not one id for all of them: an
// Antigravity account is asked for the variant its effort picks, and the name
// goes with that variant. Filling the "*" in with the model magpie knows
// would send one id whatever level is asked for, so the effort stops choosing
// and a relay that serves gemini-3.7-flash-high is asked for the base id it
// has no answer for.
func TestUpstreamNameFollowsTheAntigravityVariant(t *testing.T) {
	fresh(t)
	var raw []catalog.Model
	for _, l := range []string{"gemini-3.7-flash-high - Gemini 3.7 Flash (High)", "gemini-3.7-flash-low - Gemini 3.7 Flash (Low)",
		"gemini-3.7-flash-medium - Gemini 3.7 Flash (Medium)"} {
		id, name, _ := strings.Cut(l, " - ")
		raw = append(raw, catalog.Model{ID: id, Name: name})
	}
	if err := catalog.SaveLive("antigravity", "", raw); err != nil {
		t.Fatal(err)
	}
	// a wire name is given for a model the provider really serves, so
	// magpie must be able to find the provider that serves it. The account
	// is built at runtime and its id is reserved (Save refuses it), so the
	// test registers the provider under that id in the file directly: the
	// model a name is keyed by is the family the live list stands for.
	putProvider(t, map[string]any{
		"id": "antigravity", "name": "Antigravity", "key": "k",
		"chat": "https://antigravity.test/v1",
	})
	if err := provider.SetUpstreamName("antigravity/*", "vendor-c/*"); err != nil {
		t.Fatal(err)
	}
	ask := func(agent, model, effort string) string {
		t.Helper()
		p := provider.Provider{ID: "antigravity", Name: "Antigravity", Account: &provider.Account{Agent: agent}}
		r := codeAssistRequest(t)
		r.Thinking, r.Effort = false, effort
		return modelOf(codeAssistBody(p, r, model, settings.Load().ModelWires))
	}

	// the level the effort picked is in the name it is asked for by
	for _, c := range []struct{ effort, want string }{
		{"high", "vendor-c/gemini-3.7-flash-high"},
		{"medium", "vendor-c/gemini-3.7-flash-medium"},
		{"low", "vendor-c/gemini-3.7-flash-low"},
		{"", "vendor-c/gemini-3.7-flash-high"},
	} {
		if got := ask("antigravity", "gemini-3.7-flash", c.effort); got != c.want {
			t.Errorf("at %q the vendor is asked for %q; want %q — each level by its own name", c.effort, got, c.want)
		}
	}

	// a name given for the model itself is that model's, whichever variant
	// answers for it: the exact key still wins over the provider's
	if err := provider.SetUpstreamName("antigravity/gemini-3.7-flash", "vendor-c/flash"); err != nil {
		t.Fatal(err)
	}
	for _, effort := range []string{"high", "low"} {
		if got := ask("antigravity", "gemini-3.7-flash", effort); got != "vendor-c/flash" {
			t.Errorf("the model's own name at %q: %q; want vendor-c/flash", effort, got)
		}
	}

	// a Gemini CLI account has no variants, so the model magpie knows is
	// what is asked for, under the provider's name
	if err := provider.SetUpstreamName("antigravity/gemini-3.7-flash", ""); err != nil {
		t.Fatal(err)
	}
	if got := ask("gemini", "gemini-3.7-flash", "high"); got != "vendor-c/gemini-3.7-flash" {
		t.Errorf("a Gemini CLI account: %q; want vendor-c/gemini-3.7-flash", got)
	}

	// and with no name given, the envelope goes out as it was built
	if err := provider.SetUpstreamName("antigravity/*", ""); err != nil {
		t.Fatal(err)
	}
	if got := ask("antigravity", "gemini-3.7-flash", "low"); got != "gemini-3.7-flash-low" {
		t.Errorf("with nothing given: %q; want the variant as it is", got)
	}
}

// A turn's route and its ledger row are two views of the one call, so they
// have to judge it against one name: on an Antigravity account the call
// goes out under the variant of the model the effort picks, so a relay that
// answers with that very id has not swapped anything — and one that answers
// with another level's id has, whichever of the two the reader is looking
// at. The route read the model magpie knows where the ledger read the id
// that went out, and told the user two different things about the same
// reply.
//
// The provider here is one of the user's own under an id of its own, which is
// the shape the two views agree on by rule and not by coincidence: a relay's
// id names no account, so neither view reaches for a variant, and both read
// the reply back by the one name that went out. An Antigravity account's
// provider is not a shape a test can build here — it speaks Code Assist,
// which magpie cannot point at a relay — and the rule keyed by the account is
// read in internal/provider, where the account is what a caller hands in.
func TestUpstreamNameReadTheSameByTheRouteAndTheLedger(t *testing.T) {
	fresh(t)
	var raw []catalog.Model
	for _, l := range []string{"gemini-3.7-flash-high - High", "gemini-3.7-flash-low - Low", "gemini-3.7-flash-medium - Medium"} {
		id, name, _ := strings.Cut(l, " - ")
		raw = append(raw, catalog.Model{ID: id, Name: name})
	}
	if err := catalog.SaveLive("antigravity", "", raw); err != nil {
		t.Fatal(err)
	}
	// a relay that answers with an id of its own choosing, the way a vendor
	// serves a model under a name of its own and says which answered
	answers := ""
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Model string `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&b)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"id":"x","model":`+strconv.Quote(answers)+
			`,"choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":5,"completion_tokens":3}}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(relay.Close)
	putProvider(t, map[string]any{
		"id": "ag-relay", "name": "AG Relay", "key": "k", "chat": relay.URL,
		"models": []string{"gemini-3.7-flash"},
	})
	if err := provider.SetUpstreamName("ag-relay/*", "vendor-c/*"); err != nil {
		t.Fatal(err)
	}
	s := New()
	turn := func() (Route, usage.Row) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(
			`{"model":"ag-relay/gemini-3.7-flash","max_tokens":1024,"stream":true,`+
				`"thinking":{"type":"enabled","budget_tokens":24000},`+
				`"messages":[{"role":"user","content":"hi"}]}`))
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		rows, _, _ := usage.Ledger(usage.All, usage.Filter{Query: "gemini-3.7-flash"})
		if len(rows) == 0 {
			t.Fatal("the ledger holds no row for the turn")
		}
		return lastRoute(s), rows[0] // newest first: the turn just made
	}

	// the id the call went out under is the one the wire gave it, so a relay
	// answering with that very id has swapped nothing — in the route and in
	// the ledger alike
	answers = "vendor-c/gemini-3.7-flash"
	route, row := turn()
	if route.Swapped || row.Swapped {
		t.Errorf("the relay answered with the id the call went out under, which is no swap: the route says %v, the ledger %v", route.Swapped, row.Swapped)
	}

	// another model's id is another model, and both views say so
	answers = "vendor-c/gemini-3.7-flash-high"
	route, row = turn()
	if !route.Swapped || !row.Swapped {
		t.Errorf("the relay answered with another level's id: route swapped %v, ledger %v", route.Swapped, row.Swapped)
	}
}

// A route reads a turn's reply against the name the call really went out
// under, and on an Antigravity account that name is the variant of the model
// the effort picked — so the account is the question, not the provider's id.
// The two are one id today because an account provider is built with its
// account's own id, and a test that builds a provider of an account's own id
// with no account behind it says nothing about which of the two the rule is
// keyed by: it is the shape where they come apart, and the relay here is
// asked for the model magpie knows, so no level of the family is what the
// route has to read the reply back by.
//
// Only the route is asserted: a record keeps the provider id and no account,
// and reads itself back by that id (SentNameIn), which is the whole of what
// the ledger has to work from.
func TestRouteJudgesTheUpstreamNameByTheAccountAndNotByTheProvidersId(t *testing.T) {
	fresh(t)
	var raw []catalog.Model
	for _, l := range []string{"gemini-3.7-flash-high - High", "gemini-3.7-flash-low - Low", "gemini-3.7-flash-medium - Medium"} {
		id, name, _ := strings.Cut(l, " - ")
		raw = append(raw, catalog.Model{ID: id, Name: name})
	}
	if err := catalog.SaveLive("antigravity", "", raw); err != nil {
		t.Fatal(err)
	}
	answers := ""
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"id":"x","model":`+strconv.Quote(answers)+
			`,"choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":5,"completion_tokens":3}}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(relay.Close)
	putProvider(t, map[string]any{
		"id": "antigravity", "name": "Antigravity", "key": "k", "chat": relay.URL,
		"models": []string{"gemini-3.7-flash"},
	})
	if err := provider.SetUpstreamName("antigravity/*", "vendor-c/*"); err != nil {
		t.Fatal(err)
	}
	s := New()
	turn := func() Route {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(
			`{"model":"antigravity/gemini-3.7-flash","max_tokens":1024,"stream":true,`+
				`"thinking":{"type":"enabled","budget_tokens":24000},`+
				`"messages":[{"role":"user","content":"hi"}]}`))
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		return lastRoute(s)
	}

	// the relay was asked for the model under its provider's own name, and
	// answered with that id: nothing swapped
	answers = "vendor-c/gemini-3.7-flash"
	if r := turn(); r.Swapped {
		t.Errorf("the relay answered with the id it was asked for, which is no swap: the route says %v", r.Swapped)
	}

	// a level of the family is another model, and the route reads it as one
	answers = "vendor-c/gemini-3.7-flash-high"
	if r := turn(); !r.Swapped {
		t.Errorf("the relay answered with a level's id: the route says swapped %v", r.Swapped)
	}
}

// The id an Antigravity envelope is named with is the id a ledger reads the
// vendor's reply back against, so it is one rule and not two: a model of the
// family and one of the family's own variant ids — an id a pick or an
// agent's model list kept from before the families were one model — each go
// out at, and are judged against, the same id at every level.
func TestAntigravityEnvelopeIsNamedAsTheLedgerJudgesIt(t *testing.T) {
	fresh(t)
	var raw []catalog.Model
	for _, l := range []string{"gemini-3.7-flash-high - High", "gemini-3.7-flash-low - Low", "gemini-3.7-flash-medium - Medium"} {
		id, name, _ := strings.Cut(l, " - ")
		raw = append(raw, catalog.Model{ID: id, Name: name})
	}
	if err := catalog.SaveLive("antigravity", "", raw); err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{"gemini-3.7-flash", "gemini-3.7-flash-high", "gemini-3.7-flash-low"} {
		for _, effort := range []string{"high", "medium", "low", "minimal", "xhigh", ""} {
			sent := codeAssistID(model, effort, "antigravity")
			judged := provider.SentNameIn(nil, "antigravity", model, effort)
			if sent != judged {
				t.Errorf("%s at %q goes out as %q and is judged against %q", model, effort, sent, judged)
			}
			if usage.Swapped(sent, sent) {
				t.Errorf("%s at %q: the id the call goes out under is read as another model answering", model, effort)
			}
		}
	}
	// and a Gemini CLI account is asked for the model magpie knows, which is
	// what its ledger row reads back too
	if got := codeAssistID("gemini-3.7-flash", "high", "gemini"); got != "gemini-3.7-flash" {
		t.Errorf("a Gemini CLI account is asked for %q; want the model magpie knows", got)
	}
}

// A turn reads the upstream names once, wherever in it they are looked up:
// the id a try goes out under, the body the vendor is sent, the token count
// asked of it. settings.Load reads and parses the whole file every call, so
// a name looked up per place made every turn pay for the settings once per
// place — a turn that named no model at all paying just the same. Reading
// them once also keeps a turn to the names that stood when it came in, so a
// name given while it is on its way is for the turns after it and not for
// the one in the air.
func TestUpstreamNamesAreReadOnceInATurn(t *testing.T) {
	fresh(t)
	var seen []string
	named := false
	// a relay that is given a name for the model it serves while a turn is
	// on its way, and answers with the model it was asked for, as a relay
	// serving a model under an id of its own does
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Model string `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&b)
		seen = append(seen, b.Model)
		if !named {
			named = true
			st := settings.Load()
			st.ModelWires = map[string]string{"relay/model-2": "vendor-c/model-2-renamed"}
			if err := settings.Save(st); err != nil {
				t.Error(err)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"id":"x","model":`+strconv.Quote(b.Model)+
			`,"choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":5,"completion_tokens":3}}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(relay.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k",
		Models: []string{"model-2"}, Anthropic: relay.URL}); err != nil {
		t.Fatal(err)
	}
	s := New()
	turn := func() Route {
		t.Helper()
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(
			`{"model":"relay/model-2","max_tokens":1024,"stream":true,`+
				`"messages":[{"role":"user","content":"hi"}]}`)))
		if rec.Code != 200 {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		return lastRoute(s)
	}

	// the first turn went out under the names that stood when it came in, and
	// is judged against those and not the ones given since: read afresh at
	// every place it looks, its own record of the reply would name the model
	// the relay never answered for
	route := turn()
	if got := seen; len(got) != 1 || got[0] != "model-2" {
		t.Fatalf("the relay was asked for %v; the first turn goes out under the names it came in with", got)
	}
	if route.Served != "model-2" || route.Swapped {
		t.Errorf("the relay answered with the model the turn went out under, which is no swap: %+v", route)
	}
	// and the next turn is the one the name given mid-flight is for
	route = turn()
	if got := seen; len(got) != 2 || got[1] != "vendor-c/model-2-renamed" {
		t.Fatalf("the relay was asked for %v; the second turn goes out under the name given", got)
	}
	if route.Served != "vendor-c/model-2-renamed" || route.Swapped {
		t.Errorf("the relay answered with the name the turn went out under, which is no swap: %+v", route)
	}
}
