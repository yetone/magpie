package gui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/browserext"
	"github.com/yetone/magpie/internal/gateway"
)

// The sheet says to reload the extension when the connected one is older
// than this magpie's (the browser runs what it loaded until then), and not
// when it is as new, or when none is connected.
func TestBrowserStateOutdated(t *testing.T) {
	sandboxHome(t)
	var st map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != gateway.BrowserStatusPath {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(st)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("MAGPIE_ADDR", strings.TrimPrefix(srv.URL, "http://"))
	if gateway.URL() != srv.URL {
		t.Fatalf("the gateway is at %s, not %s", gateway.URL(), srv.URL)
	}
	v := browserext.Version()
	for _, c := range []struct {
		connected bool
		ext       string
		want      bool
	}{{true, "0.0.1", true}, {true, v, false}, {true, v + ".1", false}, {false, "0.0.1", false}, {true, "", false}} {
		st = map[string]any{"connected": c.connected, "extension": c.ext}
		if got := browserState(context.Background()); got.Outdated != c.want || got.Extension != c.ext && c.connected {
			t.Errorf("connected %v, extension %q: %+v", c.connected, c.ext, got)
		}
	}
}
