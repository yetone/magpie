package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRenewLogins(t *testing.T) {
	home := signIn(t)
	rememberLogins(true) // me@ saved…
	codexSignIn(t, home, "work@example.com", "r-work")
	rememberLogins(true) // …and codex is on work@ now

	var got []string
	status := http.StatusOK
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		got = append(got, body["refresh_token"])
		if status != http.StatusOK {
			w.WriteHeader(status)
			w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": fakeJWT(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix())}),
			"refresh_token": body["refresh_token"] + "-2"})
	}))
	defer fake.Close()
	old := codexTokenURL
	codexTokenURL = fake.URL
	t.Cleanup(func() { codexTokenURL = old })
	ctx := context.Background()
	saved := func() savedLogin {
		for _, l := range readLogins() {
			if l.User == "me@example.com" {
				return l
			}
		}
		t.Fatal("me@ isn't saved")
		return savedLogin{}
	}

	// seen in codex's hands a moment ago: not due
	if rs := RenewLogins(ctx, keepAliveEvery); len(rs) != 1 || rs[0].User != "me@example.com" || rs[0].Renewed || len(got) != 0 {
		t.Fatalf("renewed what wasn't due: %+v %v", rs, got)
	}
	// every account, now: me@ is refreshed in logins.json, codex's own isn't touched
	if rs := RenewLogins(ctx, 0); len(rs) != 1 || !rs[0].Renewed || rs[0].Err != "" {
		t.Fatalf("renew: %+v", rs)
	}
	if strings.Join(got, ",") != "r" {
		t.Fatalf("refreshed with %v", got)
	}
	l := saved()
	if !strings.Contains(string(l.Auth), `"r-2"`) || time.Since(l.Renewed) > time.Minute || !renewalDue(l, keepAliveEvery, time.Now().Add(25*time.Hour)) {
		t.Fatalf("saved after renewal: %+v", l)
	}
	var live codexAuth
	readJSON(filepath.Join(home, ".codex", "auth.json"), &live)
	if live.Tokens.RefreshToken != "r-work" {
		t.Fatalf("the agent's own sign-in changed: %+v", live.Tokens)
	}

	// no answer worth the name: it may yet renew, so nothing is marked
	status = http.StatusServiceUnavailable
	if rs := RenewLogins(ctx, 0); rs[0].Err == "" || rs[0].Lapsed || saved().Lapsed != "" {
		t.Fatalf("a 503 lapsed it: %+v", rs)
	}
	// refused: the sign-in is gone, and says so until it's made again
	status = http.StatusBadRequest
	rs := RenewLogins(ctx, 0)
	if !rs[0].Lapsed || !strings.Contains(rs[0].Err, "sign-in has expired") {
		t.Fatalf("refused: %+v", rs)
	}
	for _, l := range Logins("codex") {
		if (l.User == "me@example.com") != (l.Lapsed != "") {
			t.Fatalf("lapsed: %+v", l)
		}
	}
	// a 403 may be a proxy's: marked or not, the next refresh is tried
	status = http.StatusForbidden
	if rs := RenewLogins(ctx, 0); rs[0].Lapsed || saved().Lapsed == "" || len(got) != 4 {
		t.Fatalf("403: %+v %v", rs, got)
	}

	// signed in again: no longer lapsed, once it's saved again
	status = http.StatusOK
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      fakeJWT(map[string]any{"email": "me@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro", "chatgpt_account_id": "acct-1"}}),
			"access_token":  fakeJWT(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix())}),
			"refresh_token": "r-new", "account_id": "acct-1",
		},
	})
	rememberLogins(true)
	if err := SwitchLogin("codex", "work@example.com"); err != nil {
		t.Fatal(err)
	}
	ls := Logins("codex")
	for _, l := range ls {
		if l.Lapsed != "" {
			t.Fatalf("still lapsed: %+v", l)
		}
	}
	if len(ls) != 2 {
		t.Fatalf("%d accounts: %+v", len(ls), ls)
	}
}

func TestRenewalDue(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	codex := func(last time.Time) []byte {
		b, _ := json.Marshal(map[string]any{"tokens": map[string]any{"access_token": "a"}, "last_refresh": last.Format(time.RFC3339Nano)})
		return b
	}
	for _, c := range []struct {
		name string
		l    savedLogin
		due  bool
	}{
		{"never renewed, seen long ago", savedLogin{Agent: "codex", Seen: now.Add(-48 * time.Hour)}, true},
		{"seen in the agent's hands today", savedLogin{Agent: "codex", Seen: now.Add(-time.Hour)}, false},
		{"renewed today", savedLogin{Agent: "codex", Seen: now.Add(-72 * time.Hour), Renewed: now.Add(-time.Hour)}, false},
		{"codex refreshed itself lately", savedLogin{Agent: "codex", Seen: now.Add(-72 * time.Hour), Auth: codex(now.Add(-2 * time.Hour))}, false},
		{"codex a day since", savedLogin{Agent: "codex", Seen: now.Add(-72 * time.Hour), Auth: codex(now.Add(-25 * time.Hour))}, true},
	} {
		if got := renewalDue(c.l, keepAliveEvery, now); got != c.due {
			t.Errorf("%s: due %v", c.name, got)
		}
	}
}
