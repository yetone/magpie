package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
)

// ourFree is a provider listing ids as Our Free Model's plugin does: its
// free pools' models, and its account channels' as "channel/model".
func ourFree(ids ...string) []plugin.Provider {
	p := plugin.Provider{ID: "our-free-model", Spec: "magpie-our-free-model", Name: "Our Free Model",
		NPM: "@ai-sdk/openai-compatible"}
	for _, id := range ids {
		p.Models = append(p.Models, plugin.Model{ID: id, Name: id, NPM: "@ai-sdk/openai-compatible"})
	}
	return []plugin.Provider{p}
}

// Our Free Model's service, started again with magpie after an update,
// lists its free pools at once and its account channels' models only once
// it has read them, so the list the gateway's magpie read a minute after
// it started was short. The picks it didn't have were dropped for good,
// and its user picked every model again after each update (多喝热水 on
// Discord). They are set aside now, not served while the list leaves them
// out, and picked again when it lists them; the loop asks again soon after
// a list left a pick out, not an hour later.
func TestPluginPicksComeBackWithTheirList(t *testing.T) {
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", "")
	write := func(name string, v any) {
		t.Helper()
		b, _ := json.Marshal(v)
		if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(settings.Dir(), name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("plugins.json", map[string]any{"plugins": []map[string]any{{"spec": "magpie-our-free-model"}}})
	write("plugin-auth.json", map[string]any{"our-free-model": map[string]any{"type": "api", "key": "managed"}})
	full := ourFree("pool-a", "pool-b", "qoder/claude-x", "kiro/claude-y", "kiro/claude-z")
	plugin.UseCached(full)
	t.Cleanup(func() { plugin.UseCached(nil) })
	// picked in the editor from the full list: kiro/claude-z left unpicked
	if err := Save(Provider{ID: "our-free-model", Models: []string{"pool-a", "qoder/claude-x", "kiro/claude-y"}}); err != nil {
		t.Fatal(err)
	}

	answers := make(chan []plugin.Provider)
	asked := make(chan struct{})
	oldList, oldFirst, oldEvery, oldSoon := relistPlugins, relistFirst, relistEvery, relistSoon
	relistPlugins = func(ctx context.Context) ([]plugin.Provider, error) {
		select {
		case asked <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		select {
		case ps := <-answers:
			plugin.UseCached(ps) // as plugin.Providers keeps it
			return ps, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	// an hour between lists, unless one left a pick out
	relistFirst, relistEvery, relistSoon = time.Millisecond, time.Hour, time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		<-done
		relistPlugins, relistFirst, relistEvery, relistSoon = oldList, oldFirst, oldEvery, oldSoon
	})
	go func() { defer close(done); KeepPluginsListed(ctx) }()
	wait := func(what string) {
		t.Helper()
		select {
		case <-asked:
		case <-time.After(10 * time.Second):
			t.Fatal(what)
		}
	}
	wait("the plugins weren't asked")
	answers <- ourFree("pool-a", "pool-b") // the channels not read yet
	// asked again soon: the hour's wait would fail this
	wait("a list that left picks out wasn't asked for again soon")

	p, err := Find("our-free-model")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.Models, []string{"pool-a"}) || !slices.Equal(p.Delisted, []string{"qoder/claude-x", "kiro/claude-y"}) {
		t.Fatalf("short list: picks %v, set aside %v", p.Models, p.Delisted)
	}
	if got := servedIDs(t, "our-free-model"); !slices.Equal(got, []string{"pool-a"}) {
		t.Fatalf("served while the list is short: %v", got)
	}
	// the editor saves something else, sending no Delisted: kept
	p.Models = []string{"pool-a", "pool-b"}
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}

	answers <- full
	// the loop takes the full list in its own time (a fixed 200ms read
	// the picks before it had, under load: #3051), so wait for its state
	want := []string{"pool-a", "pool-b", "qoder/claude-x", "kiro/claude-y"}
	for deadline := time.Now().Add(10 * time.Second); ; {
		select {
		case <-asked:
			t.Fatal("asked again soon after a list that left nothing out")
		case <-time.After(10 * time.Millisecond):
		}
		if p, err = Find("our-free-model"); err != nil {
			t.Fatal(err)
		}
		if slices.Equal(p.Models, want) && len(p.Delisted) == 0 || time.Now().After(deadline) {
			break
		}
	}
	// and with the list taken, it isn't asked for again soon
	select {
	case <-asked:
		t.Fatal("asked again soon after a list that left nothing out")
	case <-time.After(100 * time.Millisecond):
	}
	if !slices.Equal(p.Models, []string{"pool-a", "pool-b", "qoder/claude-x", "kiro/claude-y"}) || len(p.Delisted) != 0 {
		t.Fatalf("full list again: picks %v, set aside %v", p.Models, p.Delisted)
	}
	if got := servedIDs(t, "our-free-model"); slices.Contains(got, "kiro/claude-z") {
		t.Fatalf("a model left unpicked is served: %v", got)
	}
}

// Every pick set aside: none of the plugin's models is served, not the
// whole list as for a provider nothing was ever picked of; Select none
// forgets what was set aside.
func TestEveryPickDelistedServesNone(t *testing.T) {
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", "")
	if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]any{
		"plugins.json":     map[string]any{"plugins": []map[string]any{{"spec": "magpie-our-free-model"}}},
		"plugin-auth.json": map[string]any{"our-free-model": map[string]any{"type": "api", "key": "managed"}},
	} {
		b, _ := json.Marshal(v)
		if err := os.WriteFile(filepath.Join(settings.Dir(), name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	before := ourFree("pool-a", "kiro/claude-y")
	plugin.UseCached(before)
	t.Cleanup(func() { plugin.UseCached(nil) })
	if err := Save(Provider{ID: "our-free-model", Models: []string{"kiro/claude-y"}}); err != nil {
		t.Fatal(err)
	}
	now := ourFree("pool-a")
	plugin.UseCached(now)
	if got := dropGonePluginPicks(before, now)["our-free-model"]; !slices.Equal(got, []string{"kiro/claude-y"}) {
		t.Fatalf("dropped %v", got)
	}
	if got := servedIDs(t, "our-free-model"); len(got) != 0 {
		t.Fatalf("served with every pick set aside: %v", got)
	}
	// another setting saved: still waiting for its picks
	p, _ := Find("our-free-model")
	p.Off = false
	p.MaxRPM = 10
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	if got := servedIDs(t, "our-free-model"); len(got) != 0 {
		t.Fatalf("served after another setting was saved: %v", got)
	}
	p, _ = Find("our-free-model")
	p.Models, p.NoModels = nil, true
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	if p, _ = Find("our-free-model"); len(p.Delisted) != 0 {
		t.Fatalf("Select none kept %v set aside", p.Delisted)
	}
}

// The same for a provider of the user's own whose list is fetched again,
// by Fetch models or by itself (RefreshEvery): a relay that leaves a model
// out of one list gives the pick back with the list that has it.
func TestRelayPicksComeBackWithTheirList(t *testing.T) {
	oneHome(t)
	var short atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models") {
			http.NotFound(w, r)
			return
		}
		ids := []string{"m-a", "m-b", "m-c"}
		if short.Load() {
			ids = []string{"m-a", "m-c"}
		}
		var data []map[string]string
		for _, id := range ids {
			data = append(data, map[string]string{"id": id, "object": "model"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
	}))
	t.Cleanup(srv.Close)
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "k", Chat: srv.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	refetch := func() []string {
		t.Helper()
		p, err := Find("relay")
		if err != nil {
			t.Fatal(err)
		}
		_, dropped, err := p.Refetch(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return dropped
	}
	refetch()
	p, _ := Find("relay")
	p.Models = []string{"m-a", "m-b"}
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	short.Store(true)
	if got := refetch(); !slices.Equal(got, []string{"m-b"}) {
		t.Fatalf("dropped %v", got)
	}
	if got := servedIDs(t, "relay"); !slices.Equal(got, []string{"m-a"}) {
		t.Fatalf("served while m-b isn't listed: %v", got)
	}
	short.Store(false)
	refetch()
	if p, _ = Find("relay"); !slices.Equal(p.Models, []string{"m-a", "m-b"}) || len(p.Delisted) != 0 {
		t.Fatalf("listed again: picks %v, set aside %v", p.Models, p.Delisted)
	}
}

// A model its vendor takes off for good leaves the list for good: WorkBuddy
// AI's /v3/config of 2026-11-03 lists hy3 at x0.00 and no longer lists
// deepseek-v4.1-flash, its free model before. A Free-plan account whose
// one pick was that model had every pick set aside and was served none, so
// the agents' model picker had none of its models and its Providers row
// read "1 image model", its plan's drawer (maxleebeta on Discord). The
// picks are waited for a while (pickWait), as a list short for a restart
// gives them back within minutes; after that the list is served as if
// none were picked, and the picks still come back with a list that has
// them.
func TestPicksGoneForGoodServeTheList(t *testing.T) {
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", "")
	if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]any{
		"plugins.json":     map[string]any{"plugins": []map[string]any{{"spec": "magpie-our-free-model"}}},
		"plugin-auth.json": map[string]any{"our-free-model": map[string]any{"type": "api", "key": "managed"}},
	} {
		b, _ := json.Marshal(v)
		if err := os.WriteFile(filepath.Join(settings.Dir(), name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	before := ourFree("deepseek-v4.1-flash", "deepseek-v4.1-flash-sg", "glm-5.3-flash")
	plugin.UseCached(before)
	t.Cleanup(func() { plugin.UseCached(nil) })
	if err := Save(Provider{ID: "our-free-model", Models: []string{"deepseek-v4.1-flash"}}); err != nil {
		t.Fatal(err)
	}
	now := ourFree("hy3", "deepseek-v4.1-flash-sg", "glm-5.3-flash")
	plugin.UseCached(now)
	if got := dropGonePluginPicks(before, now)["our-free-model"]; !slices.Equal(got, []string{"deepseek-v4.1-flash"}) {
		t.Fatalf("dropped %v", got)
	}
	p, _ := Find("our-free-model")
	if since := time.Since(p.DelistedAt); since < 0 || since > time.Minute {
		t.Fatalf("every pick set aside at %v, not now", p.DelistedAt)
	}
	if got := servedIDs(t, "our-free-model"); len(got) != 0 {
		t.Fatalf("served at once with every pick set aside: %v", got)
	}
	whole := []string{"hy3", "deepseek-v4.1-flash-sg", "glm-5.3-flash"}
	// waited past pickWait, another setting saved on the way
	p.DelistedAt = time.Now().Add(-pickWait - time.Minute)
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	p.MaxRPM = 10
	p.DelistedAt = time.Time{}
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	if got := servedIDs(t, "our-free-model"); !slices.Equal(got, whole) {
		t.Fatalf("served %v after pickWait, want the list %v", got, whole)
	}
	// set aside by a magpie before this one, with no time kept
	f, err := read()
	if err != nil {
		t.Fatal(err)
	}
	for i := range f.Providers {
		f.Providers[i].DelistedAt = time.Time{}
	}
	if err := store(f); err != nil {
		t.Fatal(err)
	}
	if got := servedIDs(t, "our-free-model"); !slices.Equal(got, whole) {
		t.Fatalf("served %v with the picks set aside before, want the list %v", got, whole)
	}
	// the list has the pick again: it is the pick again, the wait forgotten
	again := ourFree("hy3", "deepseek-v4.1-flash", "glm-5.3-flash")
	plugin.UseCached(again)
	dropGonePluginPicks(now, again)
	p, _ = Find("our-free-model")
	if !slices.Equal(p.Models, []string{"deepseek-v4.1-flash"}) || len(p.Delisted) != 0 || !p.DelistedAt.IsZero() {
		t.Fatalf("listed again: picks %v, set aside %v at %v", p.Models, p.Delisted, p.DelistedAt)
	}
	if got := servedIDs(t, "our-free-model"); !slices.Equal(got, []string{"deepseek-v4.1-flash"}) {
		t.Fatalf("served %v once the pick is listed again", got)
	}
}
