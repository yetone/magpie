package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// magpie provider refresh <id> fetches a provider's list again, as the
// app editor's Refresh does: a pick the new list no longer has is dropped
// (akic404 on Discord: the TUI and CLI had no way to fetch a provider's
// models after it was added).
func TestProviderRefreshCmd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("DSH_HOME", "")
	var mu sync.Mutex
	ids := []string{"alpha", "beta"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var data []map[string]any
		for _, id := range ids {
			data = append(data, map[string]any{"id": id, "object": "model"})
		}
		json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
	}))
	defer srv.Close()
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: srv.URL + "/v1", Models: []string{"alpha", "beta"}}); err != nil {
		t.Fatal(err)
	}
	if err := providerCmd([]string{"provider", "refresh", "relay"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := mustFind(t, "relay").Fetched(); !ok {
		t.Fatal("refresh fetched no list")
	}
	mu.Lock()
	ids = []string{"alpha", "gamma"}
	mu.Unlock()
	if err := providerCmd([]string{"provider", "refresh", "relay"}); err != nil {
		t.Fatal(err)
	}
	if got := mustFind(t, "relay").Models; !reflect.DeepEqual(got, []string{"alpha"}) {
		t.Fatalf("picks after the list lost beta: %v", got)
	}
	if err := providerCmd([]string{"provider", "refresh", "relay", "alpha"}); err == nil {
		t.Fatal("refresh took model ids to pick")
	}
	if err := providerCmd([]string{"provider", "refresh"}); err == nil {
		t.Fatal("refresh without an id")
	}
}
