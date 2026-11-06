package budget

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// scopedKey is a key in a sandbox HOME with limits ls, and the provider
// "plan" serving m1 and m2.
func scopedKey(t *testing.T, ls ...access.Limit) access.Identity {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	Forget()
	t.Cleanup(Forget)
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: "k", Chat: "https://plan.example/v1", Models: []string{"m1", "m2"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := access.Update("add-key", access.Change{Name: "Shared"}); err != nil {
		t.Fatal(err)
	}
	ks, _ := access.List()
	for i := range ls {
		n := i
		if _, err := access.Update("limit-key", access.Change{Key: ks[0].ID, Limit: &ls[i], Index: &n}); err != nil {
			t.Fatal(err)
		}
	}
	ks, _ = access.List()
	k := ks[0]
	return access.Identity{KeyID: k.ID, KeyName: k.Name, Limit: k.Limit, More: k.More}
}

// A limit of some models counts only their calls, from the ledger after a
// restart too, and holds only a request for one of them: the key's other
// models are still served, and Spent names the spent ones for a group.
func TestScopedLimitCountsItsModels(t *testing.T) {
	who := scopedKey(t, access.Limit{Period: "day", Tokens: 1000, Models: []string{"plan/m1"}})
	now := time.Now()
	call := func(model string, n int) {
		t.Helper()
		Append(usage.Record{Time: now, Provider: "plan", Model: model, Input: n, Status: 200, CallerKeyID: who.KeyID})
	}
	refused := func(model string) *Refusal {
		t.Helper()
		release, r := Reserve(who, -1, model, now)
		if release != nil {
			release()
		}
		return r
	}
	// the key's limit is read once before the calls, as the gateway does
	if r := refused("plan/m1"); r != nil {
		t.Fatal(r)
	}
	call("m2", 5000)
	if r := refused("plan/m1"); r != nil {
		t.Fatal("another model's call spent it:", r)
	}
	call("m1", 1200)
	for _, restart := range []bool{false, true} {
		if restart {
			Forget()
		}
		r := refused("plan/m1")
		if r == nil || !strings.Contains(r.Error(), "plan/m1") {
			t.Fatalf("restart %v: plan/m1 refused as %v", restart, r)
		}
		if r := refused("plan/m2"); r != nil {
			t.Fatalf("restart %v: plan/m2 refused: %v", restart, r)
		}
		sp := Spent(who, now)
		if len(sp) != 1 || !access.ModelAllowed(sp[0].Models, "plan/m1") || access.ModelAllowed(sp[0].Models, "plan/m2") || sp[0].Reset.IsZero() {
			t.Fatalf("restart %v: spent %+v", restart, sp)
		}
		all := All(access.Key{ID: who.KeyID, Limit: who.Limit, More: who.More}, now)
		if len(all) != 1 || all[0].Tokens != 1200 || !all[0].Spent || all[0].Models[0] != "plan/m1" {
			t.Fatalf("restart %v: status %+v", restart, all)
		}
	}
	// a routing group's request isn't held by it: the gateway leaves
	// plan/m1 out of the group's members
	if r := refused("group/any"); r != nil {
		t.Fatal("a group's request was refused:", r)
	}
}

// A key with a limit of the whole key and one of a provider: the whole
// key's counts every call, the provider's only its own, and a request of
// another model doesn't let go of the provider's sums, which would read
// the ledger again on every request.
func TestScopedBesideTheWholeKey(t *testing.T) {
	who := scopedKey(t,
		access.Limit{Period: "day", Tokens: 10_000},
		access.Limit{Period: "day", Tokens: 1000, Models: []string{"plan/m1"}})
	now := time.Now()
	if _, r := Reserve(who, -1, "plan/m2", now); r != nil {
		t.Fatal(r)
	}
	Append(usage.Record{Time: now, Provider: "plan", Model: "m1", Input: 800, Status: 200, CallerKeyID: who.KeyID})
	Append(usage.Record{Time: now, Provider: "plan", Model: "m2", Input: 800, Status: 200, CallerKeyID: who.KeyID})
	if sp := Spent(who, now); len(sp) != 0 {
		t.Fatal("spent before its cap:", sp)
	}
	release, r := Reserve(who, -1, "plan/m2", now)
	if r != nil {
		t.Fatal(r)
	}
	release()
	state.Lock()
	kept := len(state.sums[slot(who.KeyID)])
	state.Unlock()
	if kept != 2 {
		t.Fatalf("%d windows kept after another model's request, want 2", kept)
	}
	all := All(access.Key{ID: who.KeyID, Limit: who.Limit, More: who.More}, now)
	if len(all) != 2 || all[0].Tokens != 1600 || all[1].Tokens != 800 {
		t.Fatalf("status %+v", all)
	}
	// the binding one is the whole key's, though the provider's is fuller
	if b := Binding(all); b == nil || len(b.Models) != 0 {
		t.Fatalf("binding %+v", b)
	}
	Append(usage.Record{Time: now, Provider: "plan", Model: "m1", Input: 300, Status: 200, CallerKeyID: who.KeyID})
	if _, r := Reserve(who, -1, "plan/m1", now); r == nil {
		t.Fatal("plan/m1 past its cap was let in")
	}
	if release, r := Reserve(who, -1, "plan/m2", now); r != nil {
		t.Fatal("plan/m2 refused for plan/m1's cap:", r)
	} else {
		release()
	}
}
