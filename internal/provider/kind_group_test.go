package provider

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

func memberIDs(ms []Member) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}

// A group of one kind (#5339) has the providers that serve its model under
// exactly its id, in the order they were added, none switched off and none
// the model is disabled on; a model of another name, or of another case,
// is no member. It is in no list of groups and offered to no agent.
func TestKindGroupHoldsTheExactModel(t *testing.T) {
	autolistHome(t)
	lists := map[string][]string{
		"one":   {"chat-x", "embed-1"},
		"two":   {"chat-x", "Embed-1", "embed-1-v2"},
		"three": {"chat-x", "embed-1"},
		"four":  {"chat-x", "embed-1"},
		"five":  {"chat-x", "embed-1"},
	}
	for _, id := range []string{"one", "two", "three", "four", "five"} {
		r := newRelayList(t, lists[id]...)
		p := Provider{ID: id, Name: id, Chat: r.srv.URL + "/v1", Key: "k", Models: []string{"chat-x"}}
		if id == "four" {
			p.Off = true
		}
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
		found, err := Find(id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := found.Fetch(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if err := DisableModels("five/embed-1"); err != nil {
		t.Fatal(err)
	}
	serves := func(p Provider, model string) bool {
		for _, m := range catalog.LiveServing(p.ID, catalog.ServesEmbedding) {
			if m.ID == model {
				return true
			}
		}
		return false
	}
	for _, ref := range []string{"embed-1", "group/" + KindGroupID(catalog.ServesEmbedding, "embed-1")} {
		g, ms, ok := KindGroup(catalog.ServesEmbedding, ref, serves)
		if !ok || g.ID != "auto-embedding:embed-1" || !g.Auto || !slices.Equal(memberIDs(ms), []string{"one/embed-1", "three/embed-1"}) || !slices.Equal(g.Members, memberIDs(ms)) {
			t.Fatalf("%s: %v %v %v", ref, ok, g, memberIDs(ms))
		}
	}
	// a group of another kind's id is not this kind's
	if _, _, ok := KindGroup(catalog.ServesRerank, "group/auto-embedding:embed-1", serves); ok {
		t.Fatal("an embedding group's id made a rerank group")
	}
	if _, ms, ok := KindGroup(catalog.ServesEmbedding, "embed-2", serves); ok {
		t.Fatalf("a model no one serves made a group of %v", memberIDs(ms))
	}
	for _, g := range Groups() {
		if strings.Contains(g.ID, "embed") || strings.Contains(g.ID, ":") {
			t.Fatalf("Groups has %s", g.ID)
		}
	}
	for _, e := range Catalog() {
		if strings.Contains(strings.ToLower(e.ID+e.Model), "embed") {
			t.Fatalf("agents are offered %s", e.ID)
		}
	}
}

// A list saved before its models made for other APIs were kept is fetched
// again once a run, with no schedule set, so embeddings and the rest find
// them without the user pressing Refresh (#5339); a list saved with them,
// and a provider never fetched, are left alone.
func TestKindlessListFetchedOnce(t *testing.T) {
	autolistHome(t)
	t.Cleanup(func() {
		listsTried.Lock()
		clear(listsTried.m)
		listsTried.Unlock()
	})
	old := newRelayList(t, "chat-x", "embed-1")
	kinded := newRelayList(t, "chat-x", "embed-1")
	never := newRelayList(t, "chat-x")
	for id, r := range map[string]*relayList{"old": old, "kinded": kinded, "never": never} {
		if err := Save(Provider{ID: id, Name: id, Chat: r.srv.URL + "/v1", Key: "k", Models: []string{"chat-x"}}); err != nil {
			t.Fatal(err)
		}
		if id == "never" {
			continue
		}
		found, _ := Find(id)
		if _, err := found.Fetch(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	// "old"'s list as a magpie before this one saved it: no kinded mark,
	// embed-1 dropped
	path := catalog.LivePath("old")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f map[string]any
	json.Unmarshal(b, &f)
	delete(f, "kinded")
	var kept []any
	for _, m := range f["models"].([]any) {
		if m.(map[string]any)["ID"] != "embed-1" {
			kept = append(kept, m)
		}
	}
	f["models"] = kept
	b, _ = json.Marshal(f)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if catalog.LiveKinded("old") || !catalog.LiveKinded("kinded") || len(catalog.LiveServing("old", "")) != 0 {
		t.Fatal("the old list's setup")
	}
	o, k, n := old.asked.Load(), kinded.asked.Load(), never.asked.Load()
	now := time.Now()
	if got := refreshDueLists(context.Background(), now); !slices.Equal(got, []string{"old"}) {
		t.Fatalf("asked %v, want [old]", got)
	}
	if old.asked.Load() != o+1 || kinded.asked.Load() != k || never.asked.Load() != n {
		t.Fatalf("lists asked: old %d, kinded %d, never %d", old.asked.Load()-o, kinded.asked.Load()-k, never.asked.Load()-n)
	}
	if ms := catalog.LiveServing("old", catalog.ServesEmbedding); len(ms) != 1 || ms[0].ID != "embed-1" || !catalog.LiveKinded("old") {
		t.Fatalf("after the fetch: %v", ms)
	}
	if got := refreshDueLists(context.Background(), now.Add(time.Hour)); len(got) != 0 {
		t.Fatalf("asked %v again", got)
	}
}
