package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
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
	keys, _ := access.List()
	key := keys[0]
	id := key.ID
	if keys, _ := access.List(); len(keys) != 1 || keys[0].ID != id || keys[0].Name != "Magpie" {
		t.Fatal("LAN shortcut is not a named key", keys)
	}
	old, err := access.Update("copy-key", access.Change{Key: id})
	if err != nil || strings.Contains(key.Masked, old) {
		t.Fatal("LAN settings leaked secret", key, err)
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
	post("/api/settings/lan", `{"on":true}`)
	if _, ok := access.Authenticate(old); !ok || settings.Load().LANKeyID != id {
		t.Fatal("toggling sharing replaced the LAN key")
	}
	post("/api/caller-keys/rename-key", `{"key":"`+id+`","name":"Desk"}`)
	usage.Append(usage.Record{Time: time.Now(), CallerKeyID: id, CallerKeyName: "Magpie", Input: 30, Status: 200})
	post("/api/settings/lan", `{"on":true,"newKey":true}`)
	keys, _ = access.List()
	key = keys[0]
	if key.ID != id || key.Name != "Desk" || key.Secret != "" {
		t.Fatal("rotation lost key identity or leaked secret", key)
	}
	if _, ok := access.Authenticate(old); ok {
		t.Fatal("rotated credential still works")
	}
	next, err := access.Update("copy-key", access.Change{Key: id})
	if err != nil || next == old {
		t.Fatal("key was not rotated", err)
	}
	if who, ok := access.Authenticate(next); !ok || who.KeyID != id {
		t.Fatal("rotated key is invalid", who, ok)
	}
	if _, ok := access.Authenticate(secret); !ok {
		t.Fatal("rotating LAN revoked another named key")
	}
	if recs := usage.Load(time.Time{}); len(recs) != 1 || recs[0].CallerKeyID != id || recs[0].Input != 30 {
		t.Fatal("rotation changed historical usage", recs)
	}
	post("/api/settings", `{"theme":"dark"}`)
	if settings.Load().LANKeyID != id {
		t.Fatal("saving unrelated preferences lost LAN key reference")
	}
	post("/api/caller-keys/off-key", `{"key":"`+id+`"}`)
	post("/api/settings/lan", `{"on":true,"newKey":true}`)
	keys, _ = access.List()
	if !keys[0].Off {
		t.Fatal("rotation re-enabled a disabled key")
	}
	post("/api/caller-keys/remove-key", `{"key":"`+id+`"}`)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	if keys, _ := access.List(); len(keys) != 1 {
		t.Fatal("settings resurrected a deleted key", w.Body)
	}
	post("/api/settings/lan", `{"on":true,"newKey":true}`)
	if settings.Load().LANKeyID == id {
		t.Fatal("replacement reused a deleted identity")
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
	if settings.Load().LANKey != old {
		t.Fatal("migration did not retain the old credential for older Magpie")
	}
	keys, _ := access.List()
	if len(keys) != 1 || keys[0].Name != "Magpie" || settings.Load().LANKeyID != keys[0].ID {
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

func TestGatewayConnectionAddressesFollowLANSharing(t *testing.T) {
	sandboxHome(t)
	s := settings.Load()
	for _, on := range []bool{false, true, false} {
		s.LAN = on
		if err := settings.Save(s); err != nil {
			t.Fatal(err)
		}
		g := providersState().Gateway
		if g.LAN != on {
			t.Fatal("key block visibility does not follow sharing", g.LAN)
		}
		if !on && len(g.LANURLs) != 0 {
			t.Fatal("shared addresses remain after sharing is off", g.LANURLs)
		}
		if on && !slices.Equal(g.LANURLs, gateway.LANURLs()) {
			t.Fatal("Connect does not list the gateway's network addresses", g.LANURLs)
		}
	}
}
