package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// listedBefore is keyAllowed as it was before #1762, every group read
// and every provider's accounts listed again for each entry: what the
// lists must still show.
func listedBefore(who access.Identity, es []provider.Entry) []string {
	var out []string
	for _, e := range es {
		ok := !who.Restricted()
		if !ok && e.Group != "" {
			if g, ms, found := provider.FindGroup(e.ID); found && groupAllowed(who, g, ms) {
				ok = slices.ContainsFunc(ms, func(m provider.Member) bool { return accountServes(who, m.Provider, m.Model) })
			}
		} else if !ok {
			ok = modelAllowed(who, e.Provider, e.Model) && accountServes(who, e.Provider, e.Model)
		}
		if ok {
			out = append(out, e.ID)
		}
	}
	return out
}

func listedNow(who access.Identity, es []provider.Entry) []string {
	r := httptest.NewRequest("GET", "/v1/models", nil)
	r = r.WithContext(access.WithIdentity(r.Context(), who))
	var out []string
	for _, e := range keyAllowed(r, es) {
		out = append(out, e.ID)
	}
	return out
}

// A key's model lists are read once a list since #1762, and show what
// they did when every entry read them again: by a model's id in any
// case, a pattern, a group named, every group, a group inside one named,
// a found group, the key's accounts and an account's own models (#474).
func TestKeyListShowsWhatItDid(t *testing.T) {
	fresh(t)
	var planMs, spareMs []string
	for i := range 40 {
		planMs = append(planMs, fmt.Sprintf("m%03d", i))
		spareMs = append(spareMs, fmt.Sprintf("m%03d", i+20)) // m020–m039 are both's: found groups
	}
	for _, p := range []provider.Provider{
		{ID: "plan", Name: "Plan", Chat: "https://plan.invalid/v1", Key: "k-plan", Keys: []provider.KeyAccount{{Key: "k-plan-2"}}, Models: planMs},
		{ID: "spare", Name: "Spare", Chat: "https://spare.invalid/v1", Key: "k-spare", Models: spareMs},
		{ID: "local", Name: "Local", Chat: "http://127.0.0.1:11434/v1", Models: []string{"tiny"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := catalog.SaveLive("local", "http://127.0.0.1:11434/v1", []catalog.Model{{ID: "tiny"}}); err != nil {
		t.Fatal(err)
	}
	for _, g := range []provider.Group{
		{Name: "Both", Members: []string{"plan/m001", "spare/m050"}, Routing: provider.Ordered},
		{Name: "Outer", Members: []string{"group/both", "plan/m002"}, Routing: provider.Ordered},
		{Name: "Solo", Members: []string{"plan/m003"}, Routing: provider.Ordered},
		{Name: "Narrow", Members: []string{"plan/m004", "plan/m000"}, Routing: provider.Ordered},
	} {
		if err := provider.SaveGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	// plan's first key serves m000 and m004 only, its second every model
	if err := provider.SetAccountModels("plan", provider.KeyID("k-plan"), []string{"m000", "m004"}); err != nil {
		t.Fatal(err)
	}
	es := provider.Catalog()
	groups := 0
	for _, e := range es {
		if e.Group != "" {
			groups++
		}
	}
	if groups < 20 || !slices.ContainsFunc(es, func(e provider.Entry) bool { return e.ID == "local/tiny" }) {
		t.Fatalf("the catalog has %d groups: %v", groups, listedBefore(access.Identity{}, es))
	}
	planKey, plan2Key, spareKey := "plan/"+provider.KeyID("k-plan"), "plan/"+provider.KeyID("k-plan-2"), "spare/"+provider.KeyID("k-spare")
	for _, who := range []access.Identity{
		{},
		{Models: []string{"plan/*"}},
		{Models: []string{"PLAN/M00*", "spare/m05?", "Spare/M021"}},
		{Models: []string{"group/both"}},
		{Models: []string{"group/*"}},
		{Models: []string{"group/outer", "spare/m021"}},
		{Models: []string{"group/auto-m02*", "*/m03*"}},
		{Models: []string{"plan/m001", "spare/m050"}},
		{Accounts: []string{planKey}},
		{Accounts: []string{plan2Key, "spare/gone"}},
		{Accounts: []string{planKey, spareKey}},
		{Models: []string{"plan/*", "group/*"}, Accounts: []string{planKey}},
		{Models: []string{"group/narrow", "group/auto-m03*"}, Accounts: []string{planKey, "local/gone"}},
		{Accounts: []string{"local/gone"}},
	} {
		want, got := listedBefore(who, es), listedNow(who, es)
		if !slices.Equal(got, want) {
			t.Errorf("models %v accounts %v:\n got %v\nwant %v", who.Models, who.Accounts, got, want)
		}
		if who.Restricted() && len(want) == len(es) {
			t.Errorf("models %v accounts %v hold nothing back: %v", who.Models, who.Accounts, want)
		}
	}
}

// A key's lists are read again each request (#1762): a change to the
// key's models, a group's members or an account's own models shows on
// the next list.
func TestKeyListSeesChangesNextRequest(t *testing.T) {
	fresh(t)
	plan := &fake{t: t, ctype: "application/json", reply: `{}`}
	spare := &fake{t: t, ctype: "application/json", reply: `{}`}
	twoProviders(t, plan, spare)
	keys, secrets := newCaller(t, "Held")
	if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: []string{"group/g"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"plan/m1"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	listed := func() []string {
		t.Helper()
		var ids []string
		for _, path := range []string{"/v1/models", "/v1beta/models"} {
			r := httptest.NewRequest("GET", path, nil)
			r.Header.Set("Authorization", "Bearer "+secrets[0])
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			var l struct {
				Data   []struct{ ID string }
				Models []struct{ Name string }
			}
			if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &l) != nil {
				t.Fatal(path, w.Code, w.Body.String())
			}
			for _, m := range l.Data {
				ids = append(ids, m.ID)
			}
			for _, m := range l.Models {
				ids = append(ids, "gemini:"+strings.TrimPrefix(m.Name, "models/"))
			}
		}
		slices.Sort(ids)
		return ids
	}
	if got := listed(); !slices.Equal(got, []string{"gemini:group/g", "group/g"}) {
		t.Fatalf("held to group g: %v", got)
	}
	// the group takes a member the key may not use alone: still listed,
	// the key names the group
	if err := provider.SaveGroup(provider.Group{ID: "g", Name: "G", Members: []string{"plan/m1", "spare/m2"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	if got := listed(); !slices.Equal(got, []string{"gemini:group/g", "group/g"}) {
		t.Fatalf("group g grown: %v", got)
	}
	// the key now names plan's models: g has spare's too, and goes
	if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: []string{"plan/*"}}); err != nil {
		t.Fatal(err)
	}
	if got := listed(); !slices.Equal(got, []string{"gemini:plan/m1", "plan/m1"}) {
		t.Fatalf("held to plan/*: %v", got)
	}
	// g loses spare's member: every member is the key's, but the key was
	// never given g (kautism), so it stays out
	if err := provider.SaveGroup(provider.Group{ID: "g", Name: "G", Members: []string{"plan/m1"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	if got := listed(); !slices.Equal(got, []string{"gemini:plan/m1", "plan/m1"}) {
		t.Fatalf("group g narrowed: %v", got)
	}
	// held to plan's key, which is then set to serve another model only
	if _, err := access.Update("accounts-key", access.Change{Key: keys[0].ID, Accounts: []string{"plan/" + provider.KeyID("k")}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetAccountModels("plan", provider.KeyID("k"), []string{"other"}); err != nil {
		t.Fatal(err)
	}
	if got := listed(); len(got) != 0 {
		t.Fatalf("plan's key serves m1 no more: %v", got)
	}
	if err := provider.SetAccountModels("plan", provider.KeyID("k"), nil); err != nil {
		t.Fatal(err)
	}
	if got := listed(); !slices.Equal(got, []string{"gemini:plan/m1", "plan/m1"}) {
		t.Fatalf("plan's key serves m1 again: %v", got)
	}
}

// BenchmarkHeldKeyListsModels is #1762's catalog: two providers of 500
// models each serving the same ids, so magpie finds 500 groups for them,
// listed for a key held to some models, one held to some accounts, and
// one held to none.
func BenchmarkHeldKeyListsModels(b *testing.B) {
	b.Setenv("XDG_CONFIG_HOME", b.TempDir())
	b.Setenv("XDG_CACHE_HOME", b.TempDir())
	var ms []string
	for i := range 500 {
		ms = append(ms, fmt.Sprintf("model-%03d", i))
	}
	for _, id := range []string{"plan", "spare"} {
		if err := provider.Save(provider.Provider{ID: id, Name: id, Key: "k-" + id, Chat: "https://" + id + ".invalid/v1", Models: ms}); err != nil {
			b.Fatal(err)
		}
	}
	s := settings.Load()
	s.LAN = true
	if err := settings.Save(s); err != nil {
		b.Fatal(err)
	}
	var secrets []string
	for _, name := range []string{"Models", "Accounts", "Free"} {
		secret, err := access.Update("add-key", access.Change{Name: name})
		if err != nil {
			b.Fatal(err)
		}
		secrets = append(secrets, secret)
	}
	keys, _ := access.List()
	keys = keys[len(keys)-3:]
	if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: []string{
		"plan/model-00*", "spare/MODEL-1*", "plan/model-250", "group/auto-model-3*", "group/auto-model-499",
	}}); err != nil {
		b.Fatal(err)
	}
	if _, err := access.Update("accounts-key", access.Change{Key: keys[1].ID, Accounts: []string{"plan/" + provider.KeyID("k-plan")}}); err != nil {
		b.Fatal(err)
	}
	h := New().Handler()
	for i, name := range []string{"models", "accounts", "free"} {
		b.Run(name, func(b *testing.B) {
			for b.Loop() {
				r := httptest.NewRequest("GET", "/v1/models", nil)
				r.Header.Set("Authorization", "Bearer "+secrets[i])
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"group/auto-model-499"`) {
					b.Fatal(w.Code, w.Body.String())
				}
			}
		})
	}
}
