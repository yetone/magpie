package agent

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// KoiLx0 on X: every agent on a client magpie had to be switched on by
// hand before it took the remote's models. With the Remote magpie's
// switch on (off by default), an agent installed here that the remote has
// connected is connected on the follow loop, on the remote's pick; one
// the remote hasn't connected is left alone; and one the user
// disconnected here, by the Agents page or by hand in its file, is never
// connected again, until the user connects it themselves.
func TestRemoteConnectsNewAgents(t *testing.T) {
	home, _ := codexHome(t, "", "")
	resetRemoteCache()
	t.Cleanup(resetRemoteCache)
	fake := &remotePicksFake{picks: map[string]map[string]string{}}
	fake.set("claude", "model", "group/fast")
	srv := httptest.NewServer(fake)
	defer srv.Close()
	if err := provider.Save(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Chat: srv.URL, Key: "secret",
		Models: []string{"group/smart", "group/fast"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	own := `{"model": "opus"}`
	writeFile(t, path, own)
	c := claude(home)
	ctx := context.Background()
	tick := func() { connectFromRemote([]*Agent{c}, remotePicks(ctx, 0)) }

	// off by default: nothing connected
	tick()
	if c.Wired() || readFile(path) != own {
		t.Fatalf("connected with the switch off:\n%s", readFile(path))
	}
	if ConnectsAgents()["office"] {
		t.Fatal("the switch is on by default")
	}

	// on: connected on the remote's pick
	SetConnectAgents("office", true)
	if !ConnectsAgents()["office"] {
		t.Fatal("the switch didn't turn on")
	}
	tick()
	if !c.Wired() || writtenModel(path) != "office/group/fast" {
		t.Fatalf("not connected on the remote's group/fast: on %q\n%s", writtenModel(path), readFile(path))
	}

	// disconnected on the Agents page: never again
	if err := c.Disconnect(); err != nil {
		t.Fatal(err)
	}
	tick()
	tick()
	if c.Wired() {
		t.Fatal("an agent the user disconnected was connected again")
	}

	// connected by hand: the switch follows it again; then taken out by
	// hand in its file (no Disconnect mark): never again either
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	tick()
	if got := stashShared()[autoKey(c.ID)]; got != "on" {
		t.Fatalf("connected by hand, recorded %q", got)
	}
	writeFile(t, path, own)
	if c.Wired() || c.Disconnected() {
		t.Fatal("the file written by hand should leave it unconnected, unmarked")
	}
	tick()
	tick()
	if c.Wired() {
		t.Fatal("an agent the user disconnected by hand in its file was connected again")
	}

	// an agent the remote hasn't connected stays as it is
	forget(autoKey(c.ID), c.ID+disconnectedKey)
	fake.mu.Lock()
	delete(fake.picks, "claude")
	fake.mu.Unlock()
	tick()
	if c.Wired() {
		t.Fatal("connected an agent the remote hasn't connected")
	}

	// disconnected on the Agents page before the switch was on (no record
	// of its own): not connected
	fake.set("claude", "model", "group/fast")
	stash(map[string]string{c.ID + disconnectedKey: "1"})
	tick()
	if c.Wired() {
		t.Fatal("an agent marked disconnected, never recorded, was connected")
	}
	forget(c.ID + disconnectedKey)

	// put back by magpie on quit (RewireOnStart connects it again): not
	// connected here, and not taken for a disconnect by hand
	fake.set("claude", "model", "group/fast")
	stash(map[string]string{autoKey(c.ID): "on", quitRestoredKey: `["claude"]`})
	tick()
	if c.Wired() || stashShared()[autoKey(c.ID)] != "on" {
		t.Fatalf("a quit-restored agent: wired %v, recorded %q", c.Wired(), stashShared()[autoKey(c.ID)])
	}
	forget(quitRestoredKey)

	// the switch off: nothing connected
	SetConnectAgents("office", false)
	tick()
	if c.Wired() {
		t.Fatal("connected with the switch turned off")
	}
}
