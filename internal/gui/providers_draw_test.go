package gui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A provider whose vendor list names image models says which, for its
// editor to list apart from the models agents chat with.
func TestProviderInfoListsImageModels(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[{"id":"gpt-5.5"},{"id":"gpt-image-2"},{"id":"flux-kontext-pro"}]}`)
	}))
	defer up.Close()
	p := provider.Provider{ID: "relay", Name: "Relay", Chat: up.URL + "/v1", Key: "key"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(t.Context()); err != nil {
		t.Fatal(err)
	}
	j := providerInfo(p, nil)
	if got := strings.Join(j.DrawIDs, ","); got != "gpt-image-2,flux-kontext-pro" || j.Draws != 2 {
		t.Fatalf("drawIds %q draws %d", got, j.Draws)
	}
	for _, m := range j.Models {
		if m.ID != "gpt-5.5" {
			t.Fatalf("image model %s among the chat ones", m.ID)
		}
	}
}
