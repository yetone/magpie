package davsync

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// Sign-ins in sync with one subscription left out on one computer
// (Ethereal499 on X: Claude's accounts kept out of sync, since Claude is
// wary of where they are used). On the computer leaving Claude out, its
// Claude accounts never go up, the server's never come in, and none here
// is taken away for the server lacking it; every other sign-in syncs as
// before, and the server's Claude accounts go on for the computer that
// syncs them. The preview counts no Claude account either.
func TestSyncSignInsExcept(t *testing.T) {
	fake := &fakeDAV{files: map[string][]byte{}, etags: map[string]string{}, dirs: map[string]bool{"/dav": true}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	cfg := Config{URL: srv.URL + "/dav/", User: "me", Password: "pw", Passphrase: "correct horse", SignIns: true}
	noClaude := cfg
	noClaude.SignInsExcept = []string{"claude"}
	now := func(t *testing.T) {
		t.Helper()
		if err := Now(context.Background()); err != nil {
			t.Fatal(err)
		}
		if v := Status(); v.Error != "" {
			t.Fatal(v.Error)
		}
	}
	file := func() string { return filepath.Join(settings.Dir(), "logins.json") }
	rows := func(t *testing.T) []map[string]any {
		t.Helper()
		var out []map[string]any
		b, err := os.ReadFile(file())
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil || json.Unmarshal(b, &out) != nil {
			t.Fatalf("logins.json: %v %s", err, b)
		}
		return out
	}
	write := func(t *testing.T, rs []map[string]any) {
		t.Helper()
		if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(rs)
		if err := os.WriteFile(file(), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	claude := func(user, token string, exp int) map[string]any {
		return map[string]any{"agent": "claude", "user": user,
			"profile": map[string]any{"emailAddress": user, "organizationUuid": "org-" + user},
			"auth":    map[string]any{"claudeAiOauth": map[string]any{"accessToken": token, "refreshToken": "r-" + token, "expiresAt": exp}}}
	}
	gemini := func(user, token string, exp int) map[string]any {
		return map[string]any{"agent": "gemini", "user": user,
			"auth": map[string]any{"access_token": token, "refresh_token": "r-" + token, "expiry_date": exp}}
	}
	add := func(t *testing.T, r map[string]any) { t.Helper(); write(t, append(rows(t), r)) }
	here := func(t *testing.T) string {
		t.Helper()
		b, _ := os.ReadFile(file())
		return string(b)
	}
	has := func(t *testing.T, who string, want, not []string) {
		t.Helper()
		h := here(t)
		for _, s := range want {
			if !strings.Contains(h, s) {
				t.Fatalf("%s hasn't %q:\n%s", who, s, h)
			}
		}
		for _, s := range not {
			if strings.Contains(h, s) {
				t.Fatalf("%s has %q:\n%s", who, s, h)
			}
		}
	}

	// a syncs every sign-in; b joins syncing them all too
	a, b := newComputer(t), newComputer(t)
	a.use(t)
	write(t, []map[string]any{claude("a@x.com", "claude-a", 2000), gemini("a@x.com", "gem-a", 2000)})
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	now(t)
	b.use(t)
	write(t, []map[string]any{gemini("b@x.com", "gem-b", 2000)})
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	now(t)
	has(t, "b, joined", []string{"claude-a", "gem-a", "gem-b"}, nil)

	// b leaves Claude out, then signs in to a Claude account of its own:
	// it doesn't go up, while its new Gemini one does
	if err := Configure(noClaude); err != nil {
		t.Fatal(err)
	}
	if v := Status(); !slices.Equal(v.SignInsExcept, []string{"claude"}) {
		t.Fatalf("status: %+v", v.SignInsExcept)
	}
	add(t, claude("b@x.com", "claude-b", 3000))
	add(t, gemini("b2@x.com", "gem-b2", 3000))
	now(t)
	a.use(t)
	now(t)
	has(t, "a", []string{"claude-a", "gem-b", "gem-b2"}, []string{"claude-b"})

	// a removes its Claude account and signs in to another: b keeps the
	// one it has, and doesn't get the new one, nor count it in a preview
	write(t, slices.DeleteFunc(rows(t), func(r map[string]any) bool { return r["user"] == "a@x.com" && r["agent"] == "claude" }))
	add(t, claude("c@x.com", "claude-c", 4000))
	now(t)
	has(t, "a", []string{"claude-c"}, []string{"claude-a"})
	b.use(t)
	p, err := Preview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p.SignIns != 0 {
		t.Fatalf("the preview counts %d sign-ins changing here, Claude's left out", p.SignIns)
	}
	now(t)
	has(t, "b", []string{"claude-a", "claude-b", "gem-a", "gem-b2"}, []string{"claude-c"})

	// and a, syncing Claude, still has the server's Claude accounts after b's
	// syncs: b's leaving them out took none away there
	a.use(t)
	now(t)
	has(t, "a, after b's syncs", []string{"claude-c", "gem-b", "gem-b2"}, []string{"claude-a", "claude-b"})

	// a setup from before the choice leaves nothing out
	var old Config
	if err := json.Unmarshal([]byte(`{"url":"x","passphrase":"p","signIns":true}`), &old); err != nil || old.SignInsExcept != nil {
		t.Fatalf("%+v %v", old, err)
	}
}
