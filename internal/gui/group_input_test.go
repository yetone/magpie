package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestGroupInputAPIRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("PATH", "")
	mux := http.NewServeMux()
	groupRoutes(mux)
	save := func(body string) int {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/groups/save", strings.NewReader(body)))
		return rec.Code
	}
	if code := save(`{"id":"declared","name":"Declared","members":["missing/model"],"input":["text","image"],"family":"kept","context":123456}`); code != 200 {
		t.Fatalf("save %d", code)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/groups", nil))
	var state groupsJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	for _, g := range state.Groups {
		if g.ID == "declared" && (g.Ready || !reflect.DeepEqual(g.EffectiveInput, []string{"text", "image"})) {
			t.Fatalf("unready declaration lost: %+v", g)
		}
	}
	if code := save(`{"id":"renamed","from":"declared","members":["missing/model"],"input":["text","image"],"family":"kept","context":123456}`); code != 200 {
		t.Fatalf("rename %d", code)
	}
	found := false
	for _, g := range provider.Groups() {
		if g.ID == "renamed" {
			found = true
			if g.Family != "kept" || g.Context != 123456 || !reflect.DeepEqual(g.Input, []string{"text", "image"}) {
				t.Fatalf("rename lost settings: %+v", g)
			}
		}
	}
	if !found {
		t.Fatal("renamed group missing")
	}
	if code := save(`{"id":"renamed","members":["missing/model"],"input":null}`); code != 200 {
		t.Fatalf("auto %d", code)
	}
	for _, g := range provider.Groups() {
		if g.ID == "renamed" && g.Input != nil {
			t.Fatal("null did not restore auto")
		}
	}
	if code := save(`{"id":"bad","members":["missing/model"],"input":["text","audio"]}`); code < 400 {
		t.Fatal("unsupported input accepted")
	}
}
