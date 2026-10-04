package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/agentenv"
)

// A remote magpie that offers fewer models, one of its providers renamed:
// a Refresh here drops the picks made from its old list, keeps the one
// typed in by hand, and a Refresh that fails changes nothing (akic404 on
// Discord).
func TestRefreshDropsPicksTheListNoLongerHas(t *testing.T) {
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

	old := `{"data":[{"id":"ws-ba5my-cn-beijing/qwen3.6-max"},{"id":"ws-ba5my-cn-beijing/qwen3.6-plus"},{"id":"ws-ba5my-cn-beijing/glm-5"},{"id":"relay/gpt-6"}]}`
	now := `{"data":[{"id":"bailian/qwen3.6-max"},{"id":"relay/gpt-6"}]}`
	var list atomic.Value
	list.Store(old)
	var down atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(list.Load().(string)))
	}))
	defer srv.Close()

	if err := Save(Provider{ID: "remote", Name: "Remote", Preset: RemoteMagpiePreset, Chat: srv.URL, Key: "sk-remote"}); err != nil {
		t.Fatal(err)
	}
	fetch := func() error {
		p, err := Find("remote")
		if err != nil {
			t.Fatal(err)
		}
		_, err = p.Fetch(context.Background())
		return err
	}
	if err := fetch(); err != nil {
		t.Fatal(err)
	}
	// picked from the list, and one typed in by hand
	p, _ := Find("remote")
	p.Models = []string{"ws-ba5my-cn-beijing/qwen3.6-max", "ws-ba5my-cn-beijing/glm-5", "relay/gpt-6", "mine/typed"}
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}

	list.Store(now)
	if err := fetch(); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("remote")
	if want := []string{"relay/gpt-6", "mine/typed"}; !slices.Equal(p.Models, want) {
		t.Fatalf("picks after the refresh: %v, want %v", p.Models, want)
	}
	var ids []string
	for _, m := range p.Exposed() {
		ids = append(ids, m.ID)
	}
	for _, id := range ids {
		if strings.HasPrefix(id, "ws-ba5my") {
			t.Fatalf("a model the remote no longer lists is still offered: %v", ids)
		}
	}

	// a refresh that fails keeps the list and the picks as they were
	down.Store(true)
	if err := fetch(); err == nil {
		t.Fatal("a refresh of a server that is down succeeded")
	}
	p, _ = Find("remote")
	if want := []string{"relay/gpt-6", "mine/typed"}; !slices.Equal(p.Models, want) {
		t.Fatalf("picks after a failed refresh: %v, want %v", p.Models, want)
	}
	if live, _, ok := p.live(); !ok || len(live) != 2 {
		t.Fatalf("list after a failed refresh: %v %v", live, ok)
	}
}
