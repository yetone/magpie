package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// listedIDs is /v1/models' ids, in order, as the request r is answered.
func listedIDs(t *testing.T, r *http.Request) []string {
	t.Helper()
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, r)
	var l struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &l); err != nil {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var ids []string
	for _, m := range l.Data {
		ids = append(ids, m.ID)
	}
	return ids
}

// love1sbug on X, moving from CLIProxyAPI: the old clients ask for
// gpt-5.6-sol and pick from /v1/models, and magpie listed only
// codex/gpt-5.6-sol, whatever Names in agents' lists said. With Model ids
// without the provider on, the list has gpt-5.6-sol too, after the
// provider/model ids it had, and a request for it on each API reaches the
// Codex account as gpt-5.6-sol. Off, as by default, the list is as it was.
func TestBareModelIDsReporterCase(t *testing.T) {
	codexSignedIn(t)
	var mu sync.Mutex
	var asked []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		asked = append(asked, modelOf(b))
		mu.Unlock()
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.6-sol"}}`,
			`data: {"type":"response.output_text.delta","delta":"pong"}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`))
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	if err := provider.Save(provider.Provider{ID: "codex", Models: []string{"gpt-5.6-sol"}}); err != nil {
		t.Fatal(err)
	}

	list := func() []string { return listedIDs(t, httptest.NewRequest("GET", "/v1/models", nil)) }
	before := list()
	if !slices.Contains(before, "codex/gpt-5.6-sol") || slices.Contains(before, "gpt-5.6-sol") {
		t.Fatalf("off, as by default: %v", before)
	}
	if err := provider.SetBareModelIDs(true); err != nil {
		t.Fatal(err)
	}
	after := list()
	if !slices.Equal(after[:len(before)], before) || !slices.Contains(after[len(before):], "gpt-5.6-sol") {
		t.Fatalf("on: %v, want %v and then gpt-5.6-sol", after, before)
	}
	// the text list a client takes its models from has it too
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models?format=text", nil))
	if !slices.Contains(strings.Split(rec.Body.String(), "\n"), "gpt-5.6-sol") {
		t.Fatalf("text list %q", rec.Body.String())
	}
	// and one model looked up by it
	rec = httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models/gpt-5.6-sol", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"id":"gpt-5.6-sol"`) {
		t.Fatalf("GET /v1/models/gpt-5.6-sol: %d %s", rec.Code, rec.Body.String())
	}

	for _, c := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"hi"}]}`},
		{"/v1/responses", `{"model":"gpt-5.6-sol","input":"hi"}`},
		{"/v1/messages", `{"model":"gpt-5.6-sol","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`},
	} {
		asked = nil
		req := httptest.NewRequest("POST", c.path, strings.NewReader(c.body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "python-requests/2.31") // a client magpie doesn't know, as CLIProxyAPI's are
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, req)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "pong") || rec.Header().Get("X-Magpie-Provider") != "codex" {
			t.Fatalf("%s: %d by %q %s", c.path, rec.Code, rec.Header().Get("X-Magpie-Provider"), rec.Body.String())
		}
		if !slices.Equal(asked, []string{"gpt-5.6-sol"}) {
			t.Fatalf("%s asked the backend for %v", c.path, asked)
		}
	}

	if err := provider.SetBareModelIDs(false); err != nil {
		t.Fatal(err)
	}
	if got := list(); !slices.Equal(got, before) {
		t.Fatalf("off again: %v, want %v", got, before)
	}
}

// Two providers serving one bare id list it once, and a request for it
// goes where magpie sends a bare id: the group it found for it, first
// member first, or with found groups off the first provider in the
// Providers order; the listed row is where that is. A gateway key is
// listed a bare id only where its request would be served, another magpie
// none (its provider would list each model twice), and an id that names a
// provider of the user's ("vendor/m9") isn't listed bare.
func TestBareModelIDsShared(t *testing.T) {
	fresh(t)
	fakes := map[string]*fake{}
	for _, p := range []provider.Provider{
		{ID: "a", Name: "A", Key: "k", Models: []string{"glm-5.3", "only-a"}},
		{ID: "b", Name: "B", Key: "k", Models: []string{"glm-5.3", "only-b", "vendor/m9"}},
		{ID: "vendor", Name: "Vendor", Key: "k", Models: []string{"m1"}},
	} {
		f := &fake{t: t, ctype: "application/json", reply: `{"id":"from-` + p.ID + `","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`}
		fakes[p.ID] = f
		up := httptest.NewServer(f)
		t.Cleanup(up.Close)
		p.Chat = up.URL + "/v1"
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SetBareModelIDs(true); err != nil {
		t.Fatal(err)
	}
	keys, secrets := newCaller(t, "Held")
	if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: []string{"b/*"}}); err != nil {
		t.Fatal(err)
	}
	get := func(secret string, magpie bool) []string {
		r := httptest.NewRequest("GET", "/v1/models", nil)
		if secret != "" {
			r.Header.Set("Authorization", "Bearer "+secret)
		}
		if magpie {
			r.Header.Set(provider.DrawersHeader, "1")
		}
		return listedIDs(t, r)
	}
	bare := func(ids []string) []string {
		var out []string
		for _, id := range ids {
			if !strings.Contains(id, "/") || id == "vendor/m9" {
				out = append(out, id)
			}
		}
		slices.Sort(out)
		return out
	}
	chat := func(model string) string {
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
			strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)))
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", model, rec.Code, rec.Body.String())
		}
		return rec.Header().Get("X-Magpie-Provider")
	}

	for _, found := range []bool{true, false} {
		if err := provider.SetAutoGroups(found); err != nil {
			t.Fatal(err)
		}
		if got, want := bare(get("", false)), []string{"glm-5.3", "m1", "only-a", "only-b"}; !slices.Equal(got, want) {
			t.Fatalf("found groups %v: bare ids %v, want %v", found, got, want)
		}
		if by := chat("glm-5.3"); by != "a" {
			t.Fatalf("found groups %v: glm-5.3 answered by %q, want a, first in the Providers order", found, by)
		}
		if by := chat("only-b"); by != "b" {
			t.Fatalf("only-b answered by %q", by)
		}
	}

	if got := bare(get("", true)); len(got) != 0 {
		t.Fatalf("another magpie was listed bare ids %v", got)
	}
	// held to b/*: glm-5.3 bare reaches a first, which the key may not use
	if got, want := bare(get(secrets[0], false)), []string{"only-b"}; !slices.Equal(got, want) {
		t.Fatalf("held key's bare ids %v, want %v", got, want)
	}
}

// qQianye on X, a Docker magpie with several WorkBuddy accounts: the
// client on their own computer listed every model as provider/model, and
// a gateway has no Agents page to pick its models on. With Model ids
// without the provider on Only, the list gives each model its bare id in
// place of the provider/model rows it stands for, where the first of them
// was; a row the bare id doesn't reach (b/vendor/m9, whose bare id names
// the provider vendor) keeps its id, the routing group magpie found for
// glm-5.3 (auto/glm-5.3) is listed by glm-5.3 alone, a key held to b/* keeps b/glm-5.3
// (its bare id would reach a), another magpie is listed as before, and a
// request by a provider/model id is still served by that provider.
func TestBareModelIDsOnly(t *testing.T) {
	fresh(t)
	for _, p := range []provider.Provider{
		{ID: "a", Name: "A", Key: "k", Models: []string{"glm-5.3", "only-a"}},
		{ID: "b", Name: "B", Key: "k", Models: []string{"glm-5.3", "only-b", "vendor/m9"}},
		{ID: "vendor", Name: "Vendor", Key: "k", Models: []string{"m1"}},
	} {
		f := &fake{t: t, ctype: "application/json", reply: `{"id":"from-` + p.ID + `","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`}
		up := httptest.NewServer(f)
		t.Cleanup(up.Close)
		p.Chat = up.URL + "/v1"
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	keys, secrets := newCaller(t, "Held")
	if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: []string{"b/*"}}); err != nil {
		t.Fatal(err)
	}
	get := func(secret string, magpie bool) []string {
		r := httptest.NewRequest("GET", "/v1/models", nil)
		if secret != "" {
			r.Header.Set("Authorization", "Bearer "+secret)
		}
		if magpie {
			r.Header.Set(provider.DrawersHeader, "1")
		}
		return listedIDs(t, r)
	}
	chat := func(model string) string {
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
			strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)))
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", model, rec.Code, rec.Body.String())
		}
		return rec.Header().Get("X-Magpie-Provider")
	}
	// the provider's rows of each model, in the list's order
	provided := func(ids []string) []string {
		var out []string
		for _, id := range ids {
			if slices.Contains([]string{"auto/glm-5.3", "a/glm-5.3", "a/only-a", "b/glm-5.3", "b/only-b", "b/vendor/m9", "vendor/m1"}, id) {
				out = append(out, id)
			}
		}
		return out
	}

	for _, found := range []bool{true, false} {
		if err := provider.SetAutoGroups(found); err != nil {
			t.Fatal(err)
		}
		if err := provider.SetBareModelIDs(false); err != nil {
			t.Fatal(err)
		}
		before := get("", false)
		if err := provider.SetBareIDs(true, true); err != nil {
			t.Fatal(err)
		}
		got := get("", false)
		if p := provided(got); !slices.Equal(p, []string{"b/vendor/m9"}) {
			t.Fatalf("found groups %v: Only listed the provider's rows %v in %v", found, p, got)
		}
		// each bare id once, where its first provider/model row was
		var want []string
		seen := map[string]bool{}
		for _, id := range before {
			if bare, ok := map[string]string{"auto/glm-5.3": "glm-5.3", "a/glm-5.3": "glm-5.3", "b/glm-5.3": "glm-5.3", "a/only-a": "only-a", "b/only-b": "only-b", "vendor/m1": "m1"}[id]; ok {
				if !seen[bare] {
					seen[bare] = true
					want = append(want, bare)
				}
				continue
			}
			want = append(want, id)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("found groups %v: Only listed %v, want %v (from %v)", found, got, want, before)
		}
		if by := chat("glm-5.3"); by != "a" {
			t.Fatalf("glm-5.3 answered by %q", by)
		}
		if by := chat("b/glm-5.3"); by != "b" {
			t.Fatalf("b/glm-5.3, no longer listed, answered by %q", by)
		}
	}
	// Claude Code's discovery, which leaves out a bare row it wouldn't keep
	// when the model's full row is listed: here that row is gone, so each
	// model is listed by its alias, none lost
	r := httptest.NewRequest("GET", "/v1/models?limit=1000", nil)
	r.Header.Set("User-Agent", "claude-code/2.1.316")
	r.Header.Set("anthropic-version", "2023-06-01")
	if got, all := listedIDs(t, r), get("", false); len(got) != len(all) {
		t.Fatalf("Claude Code's discovery listed %v of %v", got, all)
	}
	if got := get("", true); !slices.Contains(got, "a/glm-5.3") || slices.Contains(got, "glm-5.3") {
		t.Fatalf("another magpie was listed %v", got)
	}
	if got := provided(get(secrets[0], false)); !slices.Equal(got, []string{"b/glm-5.3", "b/vendor/m9"}) {
		t.Fatalf("held key's provider rows %v", got)
	}
	if err := provider.SetBareModelIDs(true); err != nil {
		t.Fatal(err)
	}
	if got := provided(get("", false)); !slices.Contains(got, "a/glm-5.3") || !slices.Contains(got, "vendor/m1") {
		t.Fatalf("On after Only lists the provider's rows %v", got)
	}
}
