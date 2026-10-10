package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/update"
)

// An update check that failed going direct is tried again as soon as a
// proxy is set on the Settings page, through it (#294), rather than the
// error staying up till the next check six hours on. The answer to the
// save already finds it checking: the page reads /api/update straight
// after, and an error still up then was drawn with nothing asking again.
// Marked inside the check's goroutine, it was still up in about a third of
// runs here.
func TestSettingsProxyRechecksAFailedUpdate(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	var hits atomic.Int32
	px := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host == "feed.magpie.invalid" {
			hits.Add(1)
		}
		json.NewEncoder(w).Encode(update.Release{Version: "9.9.9"})
	}))
	defer px.Close()
	// direct, the feed is nowhere to be found
	if err := settings.Save(settings.Settings{Proxy: "direct"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAGPIE_UPDATE_FEED", "http://feed.magpie.invalid/api/latest")
	old := updates
	updates = &updater{}
	defer func() { updates = old }()
	updates.check()
	if j := updates.json(); j.State != "error" {
		t.Fatalf("direct: %+v", j)
	}

	rec := httptest.NewRecorder()
	mux := Handler(nil, nil)
	procs := runtime.GOMAXPROCS(1)
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/settings", strings.NewReader(`{"proxy":"`+px.URL+`"}`)))
	j := updates.json()
	runtime.GOMAXPROCS(procs)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if j.State == "error" {
		t.Fatalf("the save answered with the old error up: %+v", j)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		j := updates.json()
		if j.State != "error" && j.State != "checking" {
			if j.Latest != "9.9.9" || hits.Load() != 1 {
				t.Fatalf("%+v, %d through the proxy", j, hits.Load())
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("not checked again through the proxy: %+v, %d through it", j, hits.Load())
		}
		time.Sleep(20 * time.Millisecond)
	}
}
