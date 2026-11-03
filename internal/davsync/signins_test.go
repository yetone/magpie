package davsync

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
)

// Sign-ins in sync (qianxunji and curtinp118 on Discord): with the switch
// on, a computer joining is signed in to what the other is, a sign-in
// renewed on one reaches the other, one removed on one goes from the
// other, and the newer of two is never written over by the older. The
// server only ever has them sealed.
func TestSyncSignIns(t *testing.T) {
	fake := &fakeDAV{files: map[string][]byte{}, etags: map[string]string{}, dirs: map[string]bool{"/dav": true}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	cfg := Config{URL: srv.URL + "/dav/", User: "me", Password: "pw", Passphrase: "correct horse", SignIns: true}
	now := func(t *testing.T) {
		t.Helper()
		if err := Now(context.Background()); err != nil {
			t.Fatal(err)
		}
		if v := Status(); v.Error != "" {
			t.Fatal(v.Error)
		}
	}
	logins := func(t *testing.T, rows ...string) {
		t.Helper()
		if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(settings.Dir(), "logins.json"), []byte("["+strings.Join(rows, ",")+"]"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gemini := func(user, token string, exp int) string {
		b, _ := json.Marshal(map[string]any{"agent": "gemini", "user": user,
			"auth": map[string]any{"access_token": token, "refresh_token": "r-" + token, "expiry_date": exp}})
		return string(b)
	}
	pluginAuth := func(t *testing.T, key, raw string) {
		t.Helper()
		if err := plugin.EditAuth(func(m map[string]json.RawMessage) (bool, error) {
			if raw == "" {
				delete(m, key)
			} else {
				m[key] = json.RawMessage(raw)
			}
			return true, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	here := func(t *testing.T) string {
		t.Helper()
		var all strings.Builder
		for _, p := range []string{filepath.Join(settings.Dir(), "logins.json"), plugin.AuthPath()} {
			b, _ := os.ReadFile(p)
			all.Write(b)
		}
		return all.String()
	}
	onServer := func() string {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return string(fake.files["/dav/magpie/magpie.magpie-backup"])
	}

	a, b := newComputer(t), newComputer(t)
	a.use(t)
	logins(t, gemini("a@x.com", "gem-a-1", 2000), gemini("both@x.com", "gem-both-old", 1000))
	pluginAuth(t, "kiro", `{"type":"oauth","access":"kiro-1","refresh":"kr","expires":2000,"accountId":"k@x.com"}`)
	pluginAuth(t, "zed", `{"type":"api","key":"zed-key"}`)
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	now(t)
	for _, s := range []string{"gem-a-1", "kiro-1", "zed-key"} {
		if strings.Contains(onServer(), s) {
			t.Fatalf("%q on the server in the clear", s)
		}
	}

	// b joins with a later sign-in of one account: it keeps its own
	b.use(t)
	logins(t, gemini("both@x.com", "gem-both-new", 5000))
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	now(t)
	h := here(t)
	for _, s := range []string{"gem-a-1", "kiro-1", "zed-key", "gem-both-new"} {
		if !strings.Contains(h, s) {
			t.Fatalf("b isn't signed in to %q after joining:\n%s", s, h)
		}
	}
	if strings.Contains(h, "gem-both-old") {
		t.Fatal("a's older sign-in was written over b's newer one")
	}
	if v := Status(); !v.SignIns {
		t.Fatalf("status: %+v", v)
	}

	// a gets b's later one; then a renews kiro and is signed out of zed
	a.use(t)
	now(t)
	if h := here(t); !strings.Contains(h, "gem-both-new") || strings.Contains(h, "gem-both-old") {
		t.Fatalf("a's sign-ins: %s", h)
	}
	pluginAuth(t, "kiro", `{"type":"oauth","access":"kiro-2","refresh":"kr","expires":3000,"accountId":"k@x.com"}`)
	pluginAuth(t, "zed", "")
	now(t)
	b.use(t)
	now(t)
	h = here(t)
	if !strings.Contains(h, "kiro-2") || strings.Contains(h, "kiro-1") {
		t.Fatalf("a's renewed sign-in didn't reach b:\n%s", h)
	}
	if strings.Contains(h, "zed-key") {
		t.Fatal("a sign-in removed on a stays on b")
	}

	// b turns sign-ins off: nothing is taken from a, and b's next sign-in
	// doesn't go
	off := cfg
	off.SignIns = false
	if err := Configure(off); err != nil {
		t.Fatal(err)
	}
	pluginAuth(t, "cursor", `{"type":"api","key":"cursor-b"}`)
	now(t)
	a.use(t)
	now(t)
	if h := here(t); strings.Contains(h, "cursor-b") || !strings.Contains(h, "kiro-2") || !strings.Contains(h, "gem-a-1") {
		t.Fatalf("a's sign-ins after b turned them off:\n%s", h)
	}
}
