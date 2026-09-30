package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/settings"
)

func TestLANSettingSharesCallerKeyStore(t *testing.T) {
	sandboxHome(t)
	handler := Handler(nil, nil)
	post := func(path, body string) map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	s := post("/api/settings/lan", `{"on":true}`)
	if s["lan"] != true || s["lanKey"] != nil {
		t.Fatal("setting generated separate key", s)
	}
	if keys, _ := access.List(); len(keys) != 0 {
		t.Fatal("generated a key without a name", keys)
	}
	created := post("/api/caller-keys/add-key", `{"name":"Remote laptop"}`)
	secret := created["secret"].(string)
	if who, ok := access.Authenticate(secret); !ok || who.KeyName != "Remote laptop" {
		t.Fatal("named key is not shared", who, ok)
	}
	off := post("/api/settings/lan", `{"on":false,"newKey":true}`)
	if off["lan"] != nil || off["lanKey"] != nil {
		t.Fatal("legacy rotate request changed settings", off)
	}
	if _, ok := access.Authenticate(secret); !ok {
		t.Fatal("toggling sharing revoked a key")
	}
}

func TestSettingsMigratesOldLANCredentialToManagedKey(t *testing.T) {
	sandboxHome(t)
	old := "sk-magpie-0123456789abcdef0123456789abcdef01234567"
	s := settings.Load()
	s.LAN, s.LANKey = true, old
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	handler := Handler(nil, nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	if w.Code != 200 || strings.Contains(w.Body.String(), old) || strings.Contains(w.Body.String(), `"lanKey"`) {
		t.Fatal("old credential leaked", w.Code, w.Body)
	}
	if settings.Load().LANKey != "" {
		t.Fatal("old credential remained in settings")
	}
	keys, _ := access.List()
	if len(keys) != 1 || keys[0].Name != "Local network (legacy)" {
		t.Fatal(keys)
	}
	who, ok := access.Authenticate(old)
	if !ok || who.KeyID != keys[0].ID {
		t.Fatal(who, ok)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/caller-keys", nil))
	if w.Code != 200 || strings.Contains(w.Body.String(), old) {
		t.Fatal("list exposed credential", w.Code, w.Body)
	}
}
