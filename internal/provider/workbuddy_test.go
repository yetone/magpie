package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// wbAuthFile is where WorkBuddy keeps its signed-in session under a temp home.
func wbAuthFile(home string) string {
	base := filepath.Join(home, ".local", "share")
	switch runtime.GOOS {
	case "darwin":
		base = filepath.Join(home, "Library", "Application Support")
	case "windows":
		base = filepath.Join(home, "AppData", "Local")
	}
	return filepath.Join(base, "CodeBuddyExtension", "Data", "Public", "auth", "workbuddy-desktop.info")
}

// WorkBuddy's own account is read from its auth store; a second, signed in
// from magpie with WorkBuddy's own flow, is kept beside it with its tokens.
func TestWorkBuddyAccounts(t *testing.T) {
	home := signIn(t)
	wbTokens.Lock()
	wbTokens.m = map[string]wbCreds{}
	wbTokens.Unlock()

	// far-future so the own token isn't seen as near expiry
	future := time.Now().Add(24 * time.Hour).UnixMilli()
	writeFile(t, wbAuthFile(home), map[string]any{
		"account": map[string]any{"uid": "u1", "nickname": "旅行者", "phoneNumber": "13800000000"},
		"auth": map[string]any{
			"accessToken": "own-access", "refreshToken": "own-refresh",
			"expiresAt": future, "refreshExpiresAt": future,
			"domain": "www.codebuddy.cn", "tokenType": "Bearer",
		},
	})

	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok := func(data any) { json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data}) }
		retry := func(code int) { json.NewEncoder(w).Encode(map[string]any{"code": code, "msg": "pending"}) }
		switch r.URL.Path {
		case "/v2/plugin/auth/state":
			ok(map[string]any{"state": "s1", "authUrl": "https://login.codebuddy.cn/oauth?client_id=c"})
		case "/v2/plugin/auth/token":
			if polls.Add(1) < 2 {
				retry(wbRetryToken)
				return
			}
			ok(map[string]any{"accessToken": "two-access", "refreshToken": "two-refresh",
				"expiresIn": 3600, "refreshExpiresIn": 7200, "domain": "www.codebuddy.cn", "tokenType": "Bearer"})
		case "/v2/plugin/login/account":
			if r.Header.Get("Authorization") != "Bearer two-access" {
				w.WriteHeader(401)
				return
			}
			ok(map[string]any{"uid": "u2", "nickname": "Two", "phoneNumber": ""})
		case "/billing/meter/get-user-resource-summary":
			auth := r.Header.Get("Authorization")
			if auth != "Bearer own-access" && auth != "Bearer two-access" {
				w.WriteHeader(401)
				return
			}
			// capacities come back as strings, some fractional, as the live API sends them
			ok(map[string]any{"IsPaidUser": true, "Packages": []any{
				map[string]any{"PackageCode": "coding", "CycleTotalCapacity": "9500", "CycleRemainCapacity": "7500", "CycleUsedCapacity": "2000"},
				map[string]any{"PackageCode": "vibe", "CycleTotalCapacity": "500", "CycleRemainCapacity": "0", "CycleUsedCapacity": "500.00000000"},
			}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	oldEnd, oldInt := wbEndpoint, wbPollInterval
	wbEndpoint, wbPollInterval = srv.URL, 5*time.Millisecond
	defer func() { wbEndpoint, wbPollInterval = oldEnd, oldInt }()

	// WorkBuddy's own, named by its nickname
	who, c, ok := wbOwn(wbCN)
	if !ok || who != "旅行者" || c.Access != "own-access" || c.UID != "u1" {
		t.Fatalf("own: %v %q %+v", ok, who, c)
	}

	st, err := StartSignIn("workbuddy")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(st.URL)
	if u.Host != "login.codebuddy.cn" || u.Query().Get("version") == "" || u.Query().Get("loginSessionId") == "" {
		t.Fatalf("sign-in page: %s", st.URL)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, _ = WaitSignIn(ctx, st.ID)
	if st.State != "done" || st.User != "Two" || st.Using {
		t.Fatalf("done: %+v", st)
	}

	var users []string
	for _, l := range Logins("workbuddy") {
		users = append(users, l.User+map[bool]string{true: "*", false: ""}[l.Active]+map[bool]string{true: "+", false: ""}[l.On])
	}
	if got := strings.Join(users, " "); got != "旅行者*+ Two+" {
		t.Fatalf("logins: %s", got)
	}

	p, ok := find(All(), "workbuddy")
	if !ok || p.Account.User != "旅行者" || p.Chat != srv.URL+"/v2" || p.Icon != "workbuddy-color" {
		t.Fatalf("first: %+v", p)
	}

	// the first signs with the own account's token, id and domain
	req, _ := http.NewRequest("POST", p.Chat+"/chat/completions", nil)
	if err := p.Sign(context.Background(), req, Chat, []byte(`{}`)); err != nil ||
		req.Header.Get("Authorization") != "Bearer own-access" ||
		req.Header.Get("X-User-Id") != "u1" || req.Header.Get("X-Domain") != "www.codebuddy.cn" {
		t.Fatalf("own sign: %v %v", err, req.Header)
	}

	also := p.AlsoOn()
	if len(also) != 1 || also[0].Account.User != "Two" {
		t.Fatalf("also on: %+v", also)
	}
	req2, _ := http.NewRequest("POST", also[0].Chat+"/chat/completions", nil)
	if err := also[0].Sign(context.Background(), req2, Chat, []byte(`{}`)); err != nil ||
		req2.Header.Get("Authorization") != "Bearer two-access" || req2.Header.Get("X-User-Id") != "u2" {
		t.Fatalf("second signs with its own token: %v %v", err, req2.Header)
	}
	if ms := p.Account.models(); len(ms) != len(wbModels) || ms[0].ID != "auto" {
		t.Fatalf("models: %+v", ms)
	}

	// credits, aggregated from the resource summary
	use := LoginUsage(context.Background(), "workbuddy")
	q := use["旅行者"]
	if q.Error != "" || len(q.Windows) != 1 || q.Windows[0].Name != "Credits" || q.Windows[0].Used != 25 {
		t.Fatalf("usage: %+v", q)
	}

	if err := SwitchLogin("workbuddy", "Two"); err != nil {
		t.Fatal(err)
	}
	if p, _ := find(All(), "workbuddy"); p.Account.User != "Two" {
		t.Fatalf("after switch: %+v", p.Account)
	}
	if err := SwitchLogin("workbuddy", "旅行者"); err != nil {
		t.Fatal(err)
	}
	if err := ForgetLogin("workbuddy", "Two"); err != nil {
		t.Fatal(err)
	}
	if ls := Logins("workbuddy"); len(ls) != 1 {
		t.Fatalf("after forget: %+v", ls)
	}

	// signed out of WorkBuddy, with none of magpie's: no WorkBuddy subscription
	os.Remove(wbAuthFile(home))
	if _, ok := find(All(), "workbuddy"); ok {
		t.Fatal("workbuddy without an account")
	}
}

// A near-expired own token is refreshed before use, and the refreshed one
// is kept in memory (WorkBuddy's file is never written).
func TestWorkBuddyRefresh(t *testing.T) {
	home := signIn(t)
	wbTokens.Lock()
	wbTokens.m = map[string]wbCreds{}
	wbTokens.Unlock()

	past := time.Now().Add(-time.Minute).UnixMilli()  // access already lapsed
	far := time.Now().Add(24 * time.Hour).UnixMilli() // refresh still good
	writeFile(t, wbAuthFile(home), map[string]any{
		"account": map[string]any{"uid": "u1", "nickname": "R", "phoneNumber": ""},
		"auth": map[string]any{
			"accessToken": "stale", "refreshToken": "good-refresh",
			"expiresAt": past, "refreshExpiresAt": far, "domain": "www.codebuddy.cn", "tokenType": "Bearer",
		},
	})

	var refreshed atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/plugin/auth/token/refresh" {
			if r.Header.Get("X-Refresh-Token") != "good-refresh" {
				w.WriteHeader(401)
				return
			}
			refreshed.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
				"accessToken": "fresh", "expiresIn": 3600,
			}})
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	old := wbEndpoint
	wbEndpoint = srv.URL
	defer func() { wbEndpoint = old }()

	p, ok := find(All(), "workbuddy")
	if !ok {
		t.Fatal("no workbuddy account")
	}
	req, _ := http.NewRequest("POST", p.Chat+"/chat/completions", nil)
	if err := p.Sign(context.Background(), req, Chat, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer fresh" {
		t.Fatalf("Authorization = %q, want the refreshed token", got)
	}
	if refreshed.Load() != 1 {
		t.Fatalf("refreshed %d times, want 1", refreshed.Load())
	}
	// a second request reuses the cached fresh token, no new refresh
	req2, _ := http.NewRequest("POST", p.Chat+"/chat/completions", nil)
	if err := p.Sign(context.Background(), req2, Chat, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if refreshed.Load() != 1 {
		t.Fatalf("refreshed again: %d", refreshed.Load())
	}
	// WorkBuddy's own file is left as it was
	var s wbStoredSession
	if !readJSON(wbAuthFile(home), &s) || wbString(s.Auth.AccessToken) != "stale" {
		t.Fatalf("own file was written: %+v", s.Auth)
	}
}

// An encrypted access token at rest means magpie can't read the account.
func TestWorkBuddyEncryptedOwn(t *testing.T) {
	home := signIn(t)
	writeFile(t, wbAuthFile(home), map[string]any{
		"account": map[string]any{"uid": "u1", "nickname": "E"},
		"auth": map[string]any{
			"accessToken": map[string]any{"$wbEncrypted": 1, "envelope": "x", "scheme": "asym-v1"},
			"domain":      "www.codebuddy.cn",
		},
	})
	if _, _, ok := wbOwn(wbCN); ok {
		t.Fatal("read an encrypted own account")
	}
}
