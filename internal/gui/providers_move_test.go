package gui

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A move goes on though the page that asked for it closes: stopped
// halfway, it leaves the accounts in neither place.
func TestMoveOutlivesRequest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("PATH", t.TempDir())
	oldMove, oldBack, oldAdopt := moveProvider, moveBackProvider, adoptProvider
	t.Cleanup(func() { moveProvider, moveBackProvider, adoptProvider = oldMove, oldBack, oldAdopt })
	var got []string
	check := func(ctx context.Context, id string) error {
		_, deadline := ctx.Deadline()
		if ctx.Err() != nil || !deadline {
			got = append(got, id+": stopped with the page")
		} else {
			got = append(got, id+": going")
		}
		return nil
	}
	moveProvider, moveBackProvider, adoptProvider = check, check, check
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	for _, action := range []string{"move", "moveback", "adopt"} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // the page went away
		r := httptest.NewRequest("POST", "/api/provider/"+action, strings.NewReader(`{"id":"zed"}`)).WithContext(ctx)
		mux.ServeHTTP(httptest.NewRecorder(), r)
	}
	if strings.Join(got, ",") != "zed: going,zed: going,zed: going" {
		t.Fatalf("%v", got)
	}
}

// Try again in the editor moves with the models as the editor shows them
// picked: the failed move said to untick one the plugin doesn't serve,
// and the untick wasn't saved (noting_ever on X: deep-model unticked, and
// WorkBuddy AI's move failed on deep-model all the same). A move that
// says no models leaves the picks as they are.
func TestMoveTakesEditorPicks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("PATH", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "sub", Name: "Sub", Chat: "http://127.0.0.1:1/v1", Models: []string{"auto", "deep-model"}}); err != nil {
		t.Fatal(err)
	}
	oldMove := moveProvider
	t.Cleanup(func() { moveProvider = oldMove })
	var seen [][]string
	moveProvider = func(ctx context.Context, id string) error {
		p, err := provider.Find(id)
		if err != nil {
			return err
		}
		seen = append(seen, p.Models)
		return nil
	}
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	for _, body := range []string{`{"id":"sub"}`, `{"id":"sub","models":["auto"]}`, `{"id":"sub"}`} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/move", strings.NewReader(body)))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body)
		}
	}
	if fmt.Sprint(seen) != "[[auto deep-model] [auto] [auto]]" {
		t.Fatalf("the move saw the picks %v", seen)
	}
}
