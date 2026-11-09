package davsync

import (
	"context"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/library"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// A computer with no setup of its own that writes the folder's file first
// must not let its empty parts clear what a computer joining after it has:
// a part with nothing in it is the folder never having had one, not a
// removal (#5642, tan-yong-sheng: three computers syncing the library
// alone, and the main one's providers and routing groups were gone a few
// minutes later). Providers, groups, settings and the library all stay,
// and go up for the first computer to take.
func TestSyncEmptyFirstKeepsLaterSetups(t *testing.T) {
	fake := &fakeDAV{files: map[string][]byte{}, etags: map[string]string{}, dirs: map[string]bool{"/dav": true}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ctx := context.Background()
	lib := true // the library alone, as the reporter had it
	cfg := Config{URL: srv.URL + "/dav/", User: "me", Password: "pw", Passphrase: "correct horse", Library: &lib}
	now := func(t *testing.T) {
		t.Helper()
		if err := Now(ctx); err != nil {
			t.Fatal(err)
		}
	}
	mcp := func() []string {
		var out []string
		v, err := library.Read(nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range v.Servers {
			out = append(out, s.Name)
		}
		return out
	}
	check := func(t *testing.T, who string, key string) {
		t.Helper()
		if got := ids(); !slices.Equal(got, []string{"deepseek=" + key}) {
			t.Fatalf("%s's providers: %v", who, got)
		}
		gs, err := provider.StoredGroups()
		if err != nil || len(gs) != 1 || gs[0].ID != "fast" {
			t.Fatalf("%s's routing groups: %v %v", who, gs, err)
		}
		if s := settings.Load(); s.Theme != "dark" {
			t.Fatalf("%s's settings: theme %q", who, s.Theme)
		}
		if got := mcp(); !slices.Equal(got, []string{"github"}) {
			t.Fatalf("%s's library MCP servers: %v", who, got)
		}
	}

	// b writes the folder's file first, with nothing of its own
	b := newComputer(t)
	b.use(t)
	Configure(cfg)
	now(t)

	// a joins after it, with providers, a routing group, settings and a
	// library of its own: none of it goes
	a := newComputer(t)
	a.use(t)
	provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k1"})
	if err := provider.SaveGroup(provider.Group{ID: "fast", Name: "Fast", Members: []string{"deepseek/deepseek-chat"}}); err != nil {
		t.Fatal(err)
	}
	if err := settings.Save(settings.Settings{Theme: "dark"}); err != nil {
		t.Fatal(err)
	}
	if _, err := library.SaveServer("", library.Server{Name: "github", Transport: "stdio", Command: "fixture-mcp"}); err != nil {
		t.Fatal(err)
	}
	Configure(cfg)
	now(t)
	check(t, "a after joining", "k1")

	// what a has went up: b gets it (without the key, which stays a's),
	// and a keeps it when b's sync comes back
	b.use(t)
	now(t)
	check(t, "b after a joined", "")
	a.use(t)
	now(t)
	check(t, "a after b's sync came back", "k1")

	// after the join an emptied part is a removal, and spreads as before
	if err := provider.Delete("deepseek"); err != nil {
		t.Fatal(err)
	}
	now(t)
	b.use(t)
	now(t)
	if got := ids(); len(got) != 0 {
		t.Fatalf("a removal after the join didn't reach b: %v", got)
	}
}
