package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
)

// The tray panel's Routing tab opens the window's Routing page on the
// request clicked: the id goes along, and nothing else does.
func TestMainView(t *testing.T) {
	for _, c := range []struct {
		q    url.Values
		want string
	}{
		{url.Values{}, ""},
		{url.Values{"view": {"settings"}}, "settings"},
		{url.Values{"view": {"routing"}}, "routing"},
		{url.Values{"view": {"routing"}, "req": {"42"}}, "routing&req=42"},
		{url.Values{"view": {"settings"}, "req": {"42"}}, "settings"},
		{url.Values{"view": {"routing"}, "req": {"42&view=x"}}, "routing"},
		{url.Values{"view": {"routing"}, "req": {"-1"}}, "routing"},
		{url.Values{"view": {"usage"}}, "usage"},
		{url.Values{"view": {"usage"}, "tab": {"requests"}, "provider": {"codex"}}, "usage&tab=requests&provider=codex"},
		{url.Values{"view": {"usage"}, "tab": {"requests"}, "agent": {"claude-desktop"}, "provider": {"relay team"}}, "usage&tab=requests&provider=relay+team&agent=claude-desktop"},
		{url.Values{"view": {"usage"}, "tab": {"other"}, "provider": {"x&view=settings"}}, "usage"},
		{url.Values{"view": {"settings"}, "provider": {"codex"}}, "settings"},
	} {
		if got := mainView(c.q); got != c.want {
			t.Errorf("%v: %q, want %q", c.q, got, c.want)
		}
	}
}

func TestRouteLookup(t *testing.T) {
	sandboxHome(t)
	old := served.Swap(nil)
	t.Cleanup(func() { served.Store(old) })
	if err := os.MkdirAll(gateway.HistoryDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gateway.HistoryDir(), "2026-09-30.jsonl"), []byte(`{"id":123,"model":"wanted","done":true}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	traceRoutes(mux)
	for _, c := range []struct {
		id   string
		code int
	}{{"123", 200}, {"124", 404}, {"", 400}, {"-1", 400}, {"../123", 400}} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/gateway/route?id="+url.QueryEscape(c.id)+"&day=2026-09-30", nil))
		if w.Code != c.code {
			t.Fatalf("id %q: %d %s", c.id, w.Code, w.Body)
		}
		if c.code == 200 {
			var route gateway.Route
			if err := json.Unmarshal(w.Body.Bytes(), &route); err != nil || route.ID != 123 || route.Model != "wanted" {
				t.Fatalf("route: %+v, %v", route, err)
			}
		}
	}
	for _, day := range []string{"", "../2026-09-30", "2026-02-30"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/gateway/route?id=123&day="+url.QueryEscape(day), nil))
		if w.Code != 400 {
			t.Fatalf("invalid day %q: %d", day, w.Code)
		}
	}

}
