package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
)

// adminCall sends one request to the gateway as lanGuard serves it.
func adminCall(h http.Handler, method, path, from, host, key string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader("{}"))
	r.RemoteAddr = from
	r.Host = "127.0.0.1:3425"
	if host != "" {
		r.Host = host
	}
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestAdminAPIGuards(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	var seen *http.Request
	SetAdminHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r
		w.Write([]byte("managed " + r.URL.Path))
	}))
	t.Cleanup(func() { adminHandler.Store(nil) })
	keys, secrets := newCaller(t, "plain", "local admin", "any admin", "reader")
	for i, level := range map[int]string{1: access.AdminLocal, 2: access.AdminAny} {
		if _, err := access.Update("admin-key", access.Change{Key: keys[i].ID, Admin: level}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := access.Update("access-key", access.Change{Key: keys[3].ID, ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	plain, local, anyKey, reader := secrets[0], secrets[1], secrets[2], secrets[3]
	h := lanGuard(New().Handler())
	const path = AdminPath + "providers"
	const loop, lan = "127.0.0.1:50000", "192.0.2.7:50000"

	t.Run("no key", func(t *testing.T) {
		if w := adminCall(h, "GET", path, loop, "", "", nil); w.Code != 401 {
			t.Errorf("from this computer with no key: %d, want 401", w.Code)
		}
		if w := adminCall(h, "GET", path, loop, "", "not-a-key", nil); w.Code != 401 {
			t.Errorf("with a key magpie doesn't know: %d, want 401", w.Code)
		}
		if w := adminCall(h, "GET", path, lan, "", "", nil); w.Code != 401 {
			t.Errorf("from the network with no key: %d, want 401", w.Code)
		}
	})
	t.Run("a key without Admin", func(t *testing.T) {
		for _, from := range []string{loop, lan} {
			w := adminCall(h, "GET", path, from, "", plain, nil)
			if w.Code != 403 || !strings.Contains(w.Body.String(), AdminCode) {
				t.Errorf("from %s: %d %s, want 403 %s", from, w.Code, w.Body, AdminCode)
			}
		}
		if w := adminCall(h, "GET", path, loop, "", reader, nil); w.Code != 403 {
			t.Errorf("read-only key: %d, want 403", w.Code)
		}
	})
	t.Run("a key with Admin", func(t *testing.T) {
		seen = nil
		w := adminCall(h, "GET", path, loop, "", local, nil)
		if w.Code != 200 || w.Body.String() != "managed /providers" {
			t.Fatalf("local admin from this computer: %d %q", w.Code, w.Body)
		}
		if seen.Header.Get("Authorization") != "" {
			t.Error("the key was passed on to the handler")
		}
		if w := adminCall(h, "GET", path, lan, "", local, nil); w.Code != 403 {
			t.Errorf("a local-only admin key from the network: %d, want 403", w.Code)
		}
		if w := adminCall(h, "GET", path, lan, "", anyKey, nil); w.Code != 200 {
			t.Errorf("an any admin key from the network: %d, want 200", w.Code)
		}
		// the key may be in x-api-key too
		r := httptest.NewRequest("GET", path, nil)
		r.Host = "127.0.0.1:3425"
		r.RemoteAddr = loop
		r.Header.Set("x-api-key", local)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != 200 {
			t.Errorf("x-api-key: %d", rec.Code)
		}
	})
	t.Run("a browser", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			hdr  map[string]string
		}{
			{"Origin null", map[string]string{"Origin": "null"}},
			{"a foreign page", map[string]string{"Origin": "https://evil.example"}},
			{"a page on localhost", map[string]string{"Origin": "http://localhost:3000"}},
			{"a localhost subdomain", map[string]string{"Origin": "http://a.localhost"}},
			{"its own origin", map[string]string{"Origin": "http://127.0.0.1:3425"}},
			{"an app's scheme", map[string]string{"Origin": "app://obsidian.md"}},
			{"Sec-Fetch-Site alone", map[string]string{"Sec-Fetch-Site": "same-origin"}},
			{"Sec-Fetch-Mode alone", map[string]string{"Sec-Fetch-Mode": "cors"}},
		} {
			// no key, a key without Admin, and a key with it: none gets in
			for who, key := range map[string]string{"no key": "", "plain key": plain, "admin key": local} {
				w := adminCall(h, "POST", AdminPath+"provider/save", loop, "", key, tc.hdr)
				if w.Code == 200 {
					t.Errorf("%s with %s: served", tc.name, who)
				}
				if key == local && w.Code != 403 {
					t.Errorf("%s with the admin key: %d, want 403", tc.name, w.Code)
				}
			}
		}
	})
	t.Run("a rebound name", func(t *testing.T) {
		for who, key := range map[string]string{"no key": "", "plain key": plain, "admin key": local} {
			w := adminCall(h, "GET", path, loop, "attacker.example:3425", key, nil)
			if w.Code == 200 {
				t.Errorf("Host attacker.example with %s: served", who)
			}
			if key == local && w.Code != 403 {
				t.Errorf("Host attacker.example with the admin key: %d, want 403", w.Code)
			}
		}
		if w := adminCall(h, "GET", path, loop, "localhost:3425", local, nil); w.Code != 200 {
			t.Errorf("Host localhost: %d", w.Code)
		}
	})
	t.Run("through a proxy", func(t *testing.T) {
		w := adminCall(h, "GET", path, loop, "", local, map[string]string{"X-Forwarded-For": "203.0.113.9"})
		if w.Code != 403 {
			t.Errorf("a local-only admin key through a proxy: %d, want 403", w.Code)
		}
	})
	t.Run("no handler", func(t *testing.T) {
		adminHandler.Store(nil)
		if w := adminCall(h, "GET", path, loop, "", local, nil); w.Code != 503 {
			t.Errorf("no handler set: %d, want 503", w.Code)
		}
	})
}
