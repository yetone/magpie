package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/fx"
	"github.com/yetone/magpie/internal/settings"
)

// The Settings page's currency row, and any cost shown before it's ever
// opened, both need the dollar-to-yuan rate: state() and settingsState()
// both carry it (#212). state() asks for it only once cny is actually
// chosen — usd never touches the network for a rate nothing shows with.
func TestFXInState(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	fx.Reset()

	// pre-seed the fx cache so nothing here ever reaches the network
	cache := fx.CachePath()
	if err := os.MkdirAll(filepath.Dir(cache), 0o755); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Hour)
	b, err := json.Marshal(fx.Rate{CNYPerUSD: 7.25, At: at})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache, b, 0o644); err != nil {
		t.Fatal(err)
	}
	fx.Reset()

	if err := settings.Save(settings.Settings{Currency: "usd"}); err != nil {
		t.Fatal(err)
	}
	if got := state().FX; got.Rate != 0 {
		t.Fatalf("usd: state().FX = %+v, want no rate fetched", got)
	}
	if got := settingsState().FX; got.Rate != 7.25 || got.Stale {
		t.Fatalf("settingsState always carries the rate: %+v", got)
	}

	if err := settings.Save(settings.Settings{Currency: "cny"}); err != nil {
		t.Fatal(err)
	}
	fx.Reset()
	if got := state().FX; got.Rate != 7.25 || got.At == nil || got.Stale {
		t.Fatalf("cny: state().FX = %+v", got)
	}
}

// The state never waits on the rate's endpoint (#541): with yesterday's
// rate cached and the endpoint taking 3s, it answers at once with
// yesterday's, and with the new one once that is in.
func TestFXStateDoesNotWait(t *testing.T) {
	sandboxHome(t)
	fx.Reset()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(3 * time.Second):
		case <-release:
		}
		w.Write([]byte(`{"result":"success","rates":{"CNY":7.31}}`))
	}))
	t.Cleanup(func() {
		close(release)
		fx.Settle()
		srv.Close()
		fx.Reset()
	})
	t.Cleanup(fx.SetRateURL(srv.URL))
	cache := fx.CachePath()
	os.MkdirAll(filepath.Dir(cache), 0o755)
	b, _ := json.Marshal(fx.Rate{CNYPerUSD: 7.25, At: time.Now().Add(-24 * time.Hour)})
	if err := os.WriteFile(cache, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := settings.Save(settings.Settings{Currency: "cny"}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	got := state().FX
	if took := time.Since(start); took > time.Second {
		t.Fatalf("the state waited %v on the rate", took)
	}
	if got.Rate != 7.25 || !got.Stale {
		t.Fatalf("first look: %+v, want yesterday's 7.25, stale", got)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		if got = state().FX; got.Rate == 7.31 && !got.Stale {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the new rate never came in: %+v", got)
		}
	}
}
