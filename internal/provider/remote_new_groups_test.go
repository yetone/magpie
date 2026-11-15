package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/agentenv"
)

// A remote magpie whose picks leave one of its models out: a routing group
// its user makes there later is offered here at once, with no tick in the
// editor, while a new model of its providers still waits for one, and a
// group the user saw and unticked stays out (KoiLx0 on X: office/group/opus
// missing from the agents' picker until picked by hand).
func TestRemoteMagpieNewGroupServedBesidePicks(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}

	var list atomic.Value
	list.Store(`{"data":[{"id":"group/smart"},{"id":"fake/claude-opus-9"},{"id":"fake/gpt-x"}]}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(list.Load().(string)))
	}))
	defer srv.Close()

	if err := Save(Provider{ID: "office", Name: "Office", Preset: RemoteMagpiePreset, Chat: srv.URL, Key: "sk-office"}); err != nil {
		t.Fatal(err)
	}
	fetch := func() *Provider {
		p, err := Find("office")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Fetch(context.Background()); err != nil {
			t.Fatal(err)
		}
		p, _ = Find("office")
		return p
	}
	exposed := func(p *Provider) []string {
		var ids []string
		for _, m := range p.Exposed() {
			ids = append(ids, m.ID)
		}
		return ids
	}
	p := fetch()
	p.Models = []string{"group/smart", "fake/claude-opus-9"} // fake/gpt-x left out
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}

	// the remote's user makes group/opus, and a provider there lists a new model
	list.Store(`{"data":[{"id":"group/smart"},{"id":"group/opus"},{"id":"fake/claude-opus-9"},{"id":"fake/gpt-x"},{"id":"fake/new-1"}]}`)
	p = fetch()
	got := exposed(p)
	if !slices.Contains(got, "group/opus") {
		t.Fatalf("the remote's new group isn't offered: %v", got)
	}
	for _, id := range []string{"fake/gpt-x", "fake/new-1"} {
		if slices.Contains(got, id) {
			t.Fatalf("%s is offered beside picks that left a model out: %v", id, got)
		}
	}

	// the editor starts from Picks: saved as it is, the group stays
	p.Models = p.Picks()
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("office")
	if got := exposed(p); !slices.Contains(got, "group/opus") {
		t.Fatalf("the group is gone after a Save from the editor: %v", got)
	}

	// unticked in the editor, it stays out, and so after the next list
	p.Models = slices.DeleteFunc(p.Picks(), func(id string) bool { return id == "group/opus" })
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	p = fetch()
	if got := exposed(p); slices.Contains(got, "group/opus") {
		t.Fatalf("a group the user unticked is offered again: %v", got)
	}
}
