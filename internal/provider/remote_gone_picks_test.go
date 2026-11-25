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

// A routing group another magpie removed leaves this magpie's list for
// good, though an editor opened before the background follow dropped it
// still held it among its picks and saved it back. Before, that Save
// picked it again, no later fetch could tell it from one typed by hand,
// and it stayed in the list after every Refresh (CryptoSarom on X). One
// typed by hand, never listed, still stays.
func TestRemoteMagpieDropsPicksItNoLongerServes(t *testing.T) {
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
	list.Store(`{"data":[{"id":"fake/m-alpha"},{"id":"fake/m-gamma"},{"id":"group/smart"}]}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(list.Load().(string)))
	}))
	defer srv.Close()

	find := func() *Provider {
		t.Helper()
		p, err := Find("office")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	refetch := func() []string {
		t.Helper()
		_, dropped, err := find().Refetch(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return dropped
	}
	exposed := func() []string {
		var ids []string
		for _, m := range find().Exposed() {
			ids = append(ids, m.ID)
		}
		return ids
	}

	if err := Save(Provider{ID: "office", Name: "Office", Preset: RemoteMagpiePreset, Chat: srv.URL, Key: "sk-office"}); err != nil {
		t.Fatal(err)
	}
	refetch()
	p := find()
	p.Models = []string{"fake/m-alpha", "group/smart", "mine/typed"}
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	// the editor opens with these picks; group Smart is removed there,
	// and the follow fetches the list in the background
	editor := slices.Clone(find().Models)
	list.Store(`{"data":[{"id":"fake/m-alpha"},{"id":"fake/m-gamma"}]}`)
	if got := refetch(); !slices.Equal(got, []string{"group/smart"}) {
		t.Fatalf("the follow dropped %v, want group/smart", got)
	}

	// the editor's Save
	p = find()
	p.Models = editor
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	p = find()
	if !slices.Equal(p.Models, []string{"fake/m-alpha", "mine/typed"}) || !slices.Equal(p.Delisted, []string{"group/smart"}) {
		t.Fatalf("after the editor's Save: picks %v, set aside %v", p.Models, p.Delisted)
	}
	if got := exposed(); slices.Contains(got, "group/smart") || !slices.Contains(got, "mine/typed") {
		t.Fatalf("offered %v", got)
	}
	// and after the Refresh the reporter pressed
	refetch()
	if got := exposed(); slices.Contains(got, "group/smart") || !slices.Contains(got, "mine/typed") {
		t.Fatalf("offered after a Refresh %v", got)
	}

	// made there again, it is picked here again
	list.Store(`{"data":[{"id":"fake/m-alpha"},{"id":"fake/m-gamma"},{"id":"group/smart"}]}`)
	refetch()
	if p = find(); !slices.Contains(p.Models, "group/smart") || len(p.Delisted) > 0 {
		t.Fatalf("group/smart, listed again, is not picked again: picks %v, set aside %v", p.Models, p.Delisted)
	}
}
