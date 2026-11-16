package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// remotePicksFake is a remote magpie that tells its agents' picks.
type remotePicksFake struct {
	mu    sync.Mutex
	picks map[string]map[string]string
	asked int
}

func (f *remotePicksFake) set(agent, key, ref string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.picks[agent] == nil {
		f.picks[agent] = map[string]string{}
	}
	f.picks[agent][key] = ref
}

func (f *remotePicksFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("x-api-key") != "secret" && r.Header.Get("Authorization") != "Bearer secret" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.URL.Path != provider.RemoteAgentModelsPath {
		http.NotFound(w, r)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked++
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(provider.AgentModels{Agents: f.picks})
}

func resetRemoteCache() {
	remoteCache.Lock()
	remoteCache.at, remoteCache.picks = time.Time{}, nil
	remoteCache.Unlock()
}

// A client of a remote magpie (KoiLx0 on X) connects Claude Code on the
// model the remote's Claude Code is on, follows the remote's change, and
// keeps a model the user picks here as theirs.
func TestConnectFollowsTheRemoteMagpiesPicks(t *testing.T) {
	home, _ := codexHome(t, "", "")
	resetRemoteCache()
	t.Cleanup(resetRemoteCache)
	fake := &remotePicksFake{picks: map[string]map[string]string{}}
	fake.set("claude", "model", "group/fast")
	srv := httptest.NewServer(fake)
	defer srv.Close()
	if err := provider.Save(provider.Provider{ID: "aaa", Name: "AAA", Chat: "https://a.example/v1", Key: "k", Models: []string{"claude-opus-5-5"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Chat: srv.URL, Key: "secret",
		Models: []string{"group/smart", "group/fast", "fake/claude-opus-9"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, path, `{"model": "opus"}`)

	c := claude(home)
	conn, err := c.ConnectHow()
	if err != nil {
		t.Fatal(err)
	}
	if m := writtenModel(path); m != "office/group/fast" || conn.How != "remote" {
		t.Fatalf("connected on %q (%s), not the remote's group/fast:\n%s", m, conn.How, readFile(path))
	}

	// the remote's user moves its Claude Code to group/smart: followed here
	fake.set("claude", "model", "group/smart")
	ctx := context.Background()
	c.followRemote(remotePicks(ctx, 0))
	if m := writtenModel(path); m != "office/group/smart" {
		t.Fatalf("the remote's change isn't followed: on %q", m)
	}

	// the user picks a model here: theirs, whatever the remote does after
	if err := c.Pick("model", "aaa/claude-opus-5-5"); err != nil {
		t.Fatal(err)
	}
	c.followRemote(remotePicks(ctx, 0))
	fake.set("claude", "model", "group/fast")
	c.followRemote(remotePicks(ctx, 0))
	if m := writtenModel(path); m != "aaa/claude-opus-5-5" {
		t.Fatalf("the user's pick was written over: on %q", m)
	}
	if rec := stashShared()[remoteKey("claude", "model")]; rec != "" {
		t.Fatalf("still followed after the user's pick: %q", rec)
	}

	// a pick the remote doesn't serve this computer is left alone
	if err := c.Pick("model", "office/group/smart"); err != nil {
		t.Fatal(err)
	}
	fake.set("claude", "model", "group/smart")
	c.followRemote(remotePicks(ctx, 0)) // on the remote's pick again: followed
	fake.set("claude", "model", "group/not-listed-here")
	c.followRemote(remotePicks(ctx, 0))
	if m := writtenModel(path); m != "office/group/smart" {
		t.Fatalf("moved to a model this magpie doesn't list: on %q", m)
	}
	fake.set("claude", "model", "fake/claude-opus-9")
	c.followRemote(remotePicks(ctx, 0))
	if m := writtenModel(path); m != "office/fake/claude-opus-9" {
		t.Fatalf("followed no more after a pick this magpie didn't list: on %q", m)
	}
}

// With no Remote magpie, connect asks no one and picks as before.
func TestConnectWithoutRemoteAsksNoOne(t *testing.T) {
	home, _ := codexHome(t, "", "")
	resetRemoteCache()
	t.Cleanup(resetRemoteCache)
	if err := provider.Save(provider.Provider{ID: "aaa", Name: "AAA", Chat: "https://a.example/v1", Key: "k", Models: []string{"first", "claude-opus-5-5"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, path, `{"model": "opus"}`)
	conn, err := claude(home).ConnectHow()
	if err != nil {
		t.Fatal(err)
	}
	if m := writtenModel(path); m != "aaa/claude-opus-5-5" || conn.How == "remote" {
		t.Fatalf("connected on %q (%s)", m, conn.How)
	}
}

// The remote side: a connected agent tells the catalog ref of its model,
// one not connected tells nothing.
func TestSharedModelsTellsTheRefs(t *testing.T) {
	home, _ := codexHome(t, "", "")
	resetRemoteCache()
	t.Cleanup(resetRemoteCache)
	if err := provider.Save(provider.Provider{ID: "aaa", Name: "AAA", Chat: "https://a.example/v1", Key: "k", Models: []string{"m1", "claude-opus-5-5"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, path, `{"model": "opus"}`)
	c := claude(home)
	if m := c.sharedModels(); len(m) != 0 {
		t.Fatalf("not connected, told %v", m)
	}
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	if m := c.sharedModels(); m["model"] != "aaa/claude-opus-5-5" {
		t.Fatalf("told %v", m)
	}
}
