package gateway

import (
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// 小八 on Discord: "路由组的外显模型名可以选择去掉 group/ 这个前缀吗". With
// Model ids without the provider on, a routing group the user made is
// listed by its own id too (fast beside group/fast), and with Only in
// place of group/fast; GET /v1/models/fast answers for it, and a request
// for fast reaches the group. Not listed so: a group whose id a provider
// serves a model by (that bare id is the model row's, which reaches the
// group as the gateway routes it, listed once), a group magpie found
// (auto/glm-5.3, listed as glm-5.3 already), and a group a key may not
// use. Off, the list is as it was.
func TestBareIDsListUserGroups(t *testing.T) {
	fresh(t)
	for _, p := range []provider.Provider{
		{ID: "a", Name: "A", Key: "k", Models: []string{"glm-5.3", "only-a"}},
		{ID: "b", Name: "B", Key: "k", Models: []string{"glm-5.3", "only-b"}},
	} {
		f := &fake{t: t, ctype: "application/json", reply: `{"id":"from-` + p.ID + `","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`}
		up := httptest.NewServer(f)
		t.Cleanup(up.Close)
		p.Chat = up.URL + "/v1"
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	for _, g := range []provider.Group{
		{ID: "fast", Name: "Fast", Members: []string{"b/only-b", "a/only-a"}, Routing: provider.Ordered},
		{ID: "only-a", Name: "Only A", Members: []string{"a/only-a"}, Routing: provider.Ordered},
	} {
		if err := provider.SaveGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SetAutoGroups(true); err != nil {
		t.Fatal(err)
	}
	keys, secrets := newCaller(t, "Held")
	if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: []string{"a/*"}}); err != nil {
		t.Fatal(err)
	}
	get := func(secret string) []string {
		r := httptest.NewRequest("GET", "/v1/models", nil)
		if secret != "" {
			r.Header.Set("Authorization", "Bearer "+secret)
		}
		return listedIDs(t, r)
	}
	count := func(ids []string, id string) int {
		n := 0
		for _, x := range ids {
			if x == id {
				n++
			}
		}
		return n
	}
	chat := func(path, body string) (int, string) {
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		return rec.Code, rec.Header().Get("X-Magpie-Provider")
	}

	off := get("")
	if !slices.Contains(off, "group/fast") || slices.Contains(off, "fast") {
		t.Fatalf("off: %v", off)
	}

	if err := provider.SetBareIDs(true, false); err != nil {
		t.Fatal(err)
	}
	on := get("")
	if !slices.Equal(on[:len(off)], off) || count(on, "fast") != 1 || count(on, "group/fast") != 1 {
		t.Fatalf("on: %v, want %v and then fast once", on, off)
	}
	if count(on, "only-a") != 1 || slices.Contains(on, "auto-glm-5-3") || count(on, "glm-5.3") != 1 {
		t.Fatalf("on: only-a and glm-5.3 once each, no auto-glm-5-3: %v", on)
	}
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models/fast", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"id":"fast"`) {
		t.Fatalf("GET /v1/models/fast: %d %s", rec.Code, rec.Body.String())
	}
	for _, c := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"fast","messages":[{"role":"user","content":"hi"}]}`},
	} {
		if code, by := chat(c.path, c.body); code != 200 || by != "b" {
			t.Fatalf("%s fast: %d by %q, want the group's first member, b", c.path, code, by)
		}
	}
	// a key held to a/* isn't shown the group, by either id
	if held := get(secrets[0]); slices.Contains(held, "fast") || slices.Contains(held, "group/fast") {
		t.Fatalf("held key: %v", held)
	}

	if err := provider.SetBareIDs(true, true); err != nil {
		t.Fatal(err)
	}
	only := get("")
	if slices.Contains(only, "group/fast") || count(only, "fast") != 1 {
		t.Fatalf("Only: %v", only)
	}
	// in group/fast's place
	if i, j := slices.Index(off, "group/fast"), slices.Index(only, "fast"); i != j {
		t.Fatalf("Only put fast at %d of %v, group/fast was at %d of %v", j, only, i, off)
	}
	rec = httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models/fast", nil))
	if rec.Code != 200 {
		t.Fatalf("Only, GET /v1/models/fast: %d %s", rec.Code, rec.Body.String())
	}
	// an agent configured with group/fast is still served
	if code, by := chat("/v1/chat/completions", `{"model":"group/fast","messages":[{"role":"user","content":"hi"}]}`); code != 200 || by != "b" {
		t.Fatalf("group/fast under Only: %d by %q", code, by)
	}
}

// 小八 on Discord, v0.1.4377: with Only on, group/glm-5.2 stayed while
// other groups lost the prefix. Each such group is named for a model a
// provider serves, and its members are listed through the group alone
// (Group.Unlists), so no provider's glm-5.2 row is shown to carry the
// bare id. A request for glm-5.2 reaches the group all the same (GroupFor
// before Resolve), so the group is listed as glm-5.2, in its place.
func TestBareIDsListGroupNamedForAModel(t *testing.T) {
	fresh(t)
	for _, p := range []provider.Provider{
		{ID: "a", Name: "A", Key: "k", Models: []string{"glm-5.2", "other-a"}},
		{ID: "b", Name: "B", Key: "k", Models: []string{"glm-5.2"}},
	} {
		f := &fake{t: t, ctype: "application/json", reply: `{"id":"from-` + p.ID + `","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`}
		up := httptest.NewServer(f)
		t.Cleanup(up.Close)
		p.Chat = up.URL + "/v1"
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	// b first: a request that went by Resolve instead would reach a
	g := provider.Group{ID: "glm-5.2", Name: "GLM 5.2", Members: []string{"b/glm-5.2", "a/glm-5.2"}, Routing: provider.Ordered, Unlists: true}
	if err := provider.SaveGroup(g); err != nil {
		t.Fatal(err)
	}
	get := func() []string { return listedIDs(t, httptest.NewRequest("GET", "/v1/models", nil)) }
	count := func(ids []string, id string) int {
		n := 0
		for _, x := range ids {
			if x == id {
				n++
			}
		}
		return n
	}
	off := get()
	if !slices.Contains(off, "group/glm-5.2") || slices.Contains(off, "a/glm-5.2") || slices.Contains(off, "b/glm-5.2") {
		t.Fatalf("off: %v, want group/glm-5.2 and no member listed", off)
	}

	if err := provider.SetBareIDs(true, false); err != nil {
		t.Fatal(err)
	}
	if on := get(); count(on, "glm-5.2") != 1 {
		t.Fatalf("on: %v, want glm-5.2 once", on)
	}

	if err := provider.SetBareIDs(true, true); err != nil {
		t.Fatal(err)
	}
	only := get()
	if slices.Contains(only, "group/glm-5.2") {
		t.Fatalf("Only: %v still lists group/glm-5.2", only)
	}
	if i, j := slices.Index(off, "group/glm-5.2"), slices.Index(only, "glm-5.2"); i != j || count(only, "glm-5.2") != 1 {
		t.Fatalf("Only: glm-5.2 at %d of %v, want once where group/glm-5.2 was, %d of %v", j, only, i, off)
	}
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models/glm-5.2", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"id":"glm-5.2"`) {
		t.Fatalf("GET /v1/models/glm-5.2: %d %s", rec.Code, rec.Body.String())
	}
	for _, m := range []string{"glm-5.2", "group/glm-5.2"} {
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"`+m+`","messages":[{"role":"user","content":"hi"}]}`)))
		if by := rec.Header().Get("X-Magpie-Provider"); rec.Code != 200 || by != "b" {
			t.Fatalf("%s: %d by %q, want the group's first member, b: %s", m, rec.Code, by, rec.Body.String())
		}
	}
}
