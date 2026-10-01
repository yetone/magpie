package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/settings"
)

// GET /v1/magpie/quotas answers another machine with the key of the
// gateway shared on the local network (#389: read over a LAN or tailscale);
// a wrong key, none, or a gateway not shared still get nothing, and the
// refusal says how.
func TestQuotasOverLAN(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	h := lanGuard(New().Handler())
	call := func(from string, hdr ...string) (int, string) {
		r := httptest.NewRequest("GET", "/v1/magpie/quotas", nil)
		r.RemoteAddr = from
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	keys, secrets := newCaller(t, "Quota client")
	key := secrets[0]
	if c, b := call("100.64.0.7:5000", "Authorization", "Bearer "+key); c != 200 || !strings.Contains(b, `"object":"list"`) {
		t.Fatal("shared, with the key:", c, b)
	}
	if c, b := call("192.168.1.9:5000", "x-api-key", key); c != 200 {
		t.Fatal("shared, x-api-key:", c, b)
	}
	if c, _ := call("192.168.1.9:5000", "Authorization", "Bearer wrong"); c != http.StatusUnauthorized {
		t.Fatal("a wrong key got", c)
	}
	if c, _ := call("192.168.1.9:5000"); c != http.StatusUnauthorized {
		t.Fatal("no key got", c)
	}
	if c, _ := call("127.0.0.1:5000"); c != 200 {
		t.Fatal("loopback got", c)
	}

	if _, err := access.Update("off-key", access.Change{Key: keys[0].ID}); err != nil {
		t.Fatal(err)
	}
	if c, _ := call("192.168.1.9:5000", "Authorization", "Bearer "+key); c != http.StatusUnauthorized {
		t.Fatal("disabled key got", c)
	}
	shared := settings.Load()
	shared.LAN = false
	if err := settings.Save(shared); err != nil {
		t.Fatal(err)
	}
	if c, _ := call("192.168.1.9:5000", "Authorization", "Bearer "+key); c != http.StatusForbidden {
		t.Fatal("not shared got", c)
	}
	// MAGPIE_ADDR's open gateway has no key to show, so its quotas stay here
	t.Setenv("MAGPIE_ADDR", "0.0.0.0:3425")
	c, b := call("192.168.1.9:5000", "Authorization", "Bearer "+key)
	if c != http.StatusForbidden || !strings.Contains(b, "Share on local network") || !strings.Contains(b, "x-api-key") {
		t.Fatal("MAGPIE_ADDR, not shared:", c, b)
	}
	if c, _ := call("[::1]:5000"); c != 200 {
		t.Fatal("loopback got", c)
	}
}
