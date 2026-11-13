package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Library's billion-context tab reads and changes billion-context.json
// through /api/library/bili/settings (@Luxzinw on X): a change is written
// to bili's own file, its other keys kept, and both answers carry the
// models magpie can give its summaries, a list even when there are none.
func TestBiliSettingsRoute(t *testing.T) {
	home := sandboxHome(t)
	p := filepath.Join(home, "bili", "billion-context.json")
	t.Setenv("BILI_CONFIG_FILE", p)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{\n  // mine\n  \"retained\": true\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := Handler(nil, nil)
	call := func(method, body string) map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, "/api/library/bili/settings", strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", method, rec.Code, rec.Body)
		}
		var v map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		if _, ok := v["models"].([]any); !ok {
			t.Fatalf("%s: no models list in %s", method, rec.Body)
		}
		return v
	}
	if v := call("GET", ""); v["path"] != p || v["nudge"] != false {
		t.Fatalf("read %+v", v)
	}
	if v := call("POST", `{"nudge":true,"growth":80000}`); v["nudge"] != true || v["growth"] != float64(80000) {
		t.Fatalf("changed %+v", v)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"// mine", `"retained": true`, `"requireNudge": true`, `"nudgeGrowthTokens": 80000`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("file lacks %s:\n%s", want, b)
		}
	}
}
