package gateway

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// A routing group listing alone (Group.Unlists) stands in for its
// members in the lists, so its members are left out of them. A gateway
// key that may use those members and not the group is given neither:
// luckymanjc on Discord (11-05) held his Remote magpie's key to every
// Codex model ("codex/*", his three accounts), and his other computer
// was offered GPT-6-Luna alone, the one Codex model no group of his
// listed alone; the key's picker offered it alone too. The group stands
// in only for a caller that may use it: for the rest the members it may
// use are listed, on every route a list is read from, the remote
// magpie's included.
func TestKeyNotGivenTheGroupIsListedItsMembers(t *testing.T) {
	codexFreeBesidePro(t)
	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "mygpt", Name: "MyGPT", Members: []string{"codex/gpt-6.1-sol", "codex/gpt-6-astra", "codex/gpt-5.6-terra"}, Unlists: true}); err != nil {
		t.Fatal(err)
	}
	keys, secrets := newCaller(t, "Magpie")
	var accts []string
	for _, r := range p.AccountIDs() {
		accts = append(accts, "codex/"+r.ID)
	}
	hold := func(models, accounts []string) {
		t.Helper()
		if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: models}); err != nil {
			t.Fatal(err)
		}
		if _, err := access.Update("accounts-key", access.Change{Key: keys[0].ID, Accounts: accounts}); err != nil {
			t.Fatal(err)
		}
	}
	list := func(secret string) []string {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/v1/models", nil)
		if secret != "" {
			req.Header.Set("Authorization", "Bearer "+secret)
		}
		New().Handler().ServeHTTP(rec, req)
		var out struct {
			Data []struct{ ID string } `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		var ids []string
		for _, d := range out.Data {
			ids = append(ids, d.ID)
		}
		return ids
	}
	has := func(ids []string, want ...string) bool {
		for _, w := range want {
			if !slices.Contains(ids, w) {
				return false
			}
		}
		return true
	}
	members := []string{"codex/gpt-6.1-sol", "codex/gpt-6-astra", "codex/gpt-5.6-terra"}

	// the user's own list: the group in place of its members, as the
	// option says
	if ids := list(""); !slices.Contains(ids, "group/mygpt") || slices.ContainsFunc(members, func(m string) bool { return slices.Contains(ids, m) }) {
		t.Fatalf("unrestricted: %v", ids)
	}
	// the reporter's key: every Codex model, Luna named too, his accounts
	hold([]string{"codex/*", "codex/gpt-6-luna"}, accts)
	if ids := list(secrets[0]); !has(ids, append(members, "codex/gpt-6-luna", "codex/gpt-6-sol")...) || slices.Contains(ids, "group/mygpt") {
		t.Fatalf("key held to codex/*: %v", ids)
	}
	// a key given one member only is listed that one, not the others
	hold([]string{"codex/gpt-6-astra"}, nil)
	if ids := list(secrets[0]); !slices.Equal(ids, []string{"codex/gpt-6-astra"}) {
		t.Fatalf("key held to one member: %v", ids)
	}
	// a key given the group is given it in place of its members, as the
	// user's list is
	hold([]string{"codex/*", "group/mygpt"}, nil)
	if ids := list(secrets[0]); !slices.Contains(ids, "group/mygpt") || slices.ContainsFunc(members, func(m string) bool { return slices.Contains(ids, m) }) {
		t.Fatalf("key given the group: %v", ids)
	}

	// the other computer: a Remote magpie on the reporter's key is
	// offered every Codex model
	hold([]string{"codex/*", "codex/gpt-6-luna"}, accts)
	remote := httptest.NewServer(New().Handler())
	t.Cleanup(remote.Close)
	id, err := provider.Add(provider.Provider{ID: "office", Name: "Office", Key: secrets[0], Preset: provider.RemoteMagpiePreset, Chat: strings.TrimPrefix(remote.URL, "http://")})
	if err != nil {
		t.Fatal(err)
	}
	office, _ := provider.Find(id)
	ms, err := office.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range ms {
		got = append(got, m.ID)
	}
	if !has(got, append(members, "codex/gpt-6-luna")...) {
		t.Fatalf("remote magpie: %v", got)
	}
}
