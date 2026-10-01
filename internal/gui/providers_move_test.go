package gui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
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
	oldMove, oldBack := moveProvider, moveBackProvider
	t.Cleanup(func() { moveProvider, moveBackProvider = oldMove, oldBack })
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
	moveProvider, moveBackProvider = check, check
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	for _, action := range []string{"move", "moveback"} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // the page went away
		r := httptest.NewRequest("POST", "/api/provider/"+action, strings.NewReader(`{"id":"zed"}`)).WithContext(ctx)
		mux.ServeHTTP(httptest.NewRecorder(), r)
	}
	if strings.Join(got, ",") != "zed: going,zed: going" {
		t.Fatalf("%v", got)
	}
}
