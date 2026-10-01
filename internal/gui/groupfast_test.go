package gui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The Routing view saves the members sent fast and is told which models
// have a fast mode, and which members are sent in it.
func TestGroupSaveFast(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, p := range []provider.Provider{
		{ID: "oa", Name: "OpenAI", Key: "k", Responses: "https://api.openai.com/v1", Models: []string{"gpt-6.1-sol"}},
		{ID: "rl", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"glm-5"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	groupRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/groups/save", strings.NewReader(
		`{"id":"sol","name":"Sol","members":["oa/gpt-6.1-sol:high","rl/glm-5"],"fast":["oa/gpt-6.1-sol:high"]}`)))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if g, _, _ := provider.FindGroup("group/sol"); !slices.Equal(g.Fast, []string{"oa/gpt-6.1-sol:high"}) {
		t.Fatalf("fast %v", g.Fast)
	}
	st := groupsState()
	for _, m := range st.Models {
		if want := m.ID == "oa/gpt-6.1-sol"; m.CanFast != want {
			t.Errorf("%s canFast %v", m.ID, m.CanFast)
		}
	}
	for _, g := range st.Groups {
		for _, m := range g.Info {
			if want := m.ID == "oa/gpt-6.1-sol:high"; m.Fast != want || m.CanFast != want {
				t.Errorf("%s fast %v canFast %v", m.ID, m.Fast, m.CanFast)
			}
		}
	}
}
