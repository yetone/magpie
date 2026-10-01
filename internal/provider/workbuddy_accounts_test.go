package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// wbFakeWho is the account wbFakeAccounts' WorkBuddy signs in to when it
// is asked, its tokens named after the uid.
type wbFakeWho struct {
	mu        sync.Mutex
	uid, nick string
}

func (f *wbFakeWho) set(uid, nick string) {
	f.mu.Lock()
	f.uid, f.nick = uid, nick
	f.mu.Unlock()
}

func wbFakeAccounts(t *testing.T) *wbFakeWho {
	t.Helper()
	who := &wbFakeWho{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok := func(data any) { json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data}) }
		who.mu.Lock()
		uid, nick := who.uid, who.nick
		who.mu.Unlock()
		switch r.URL.Path {
		case "/v2/plugin/auth/state":
			ok(map[string]any{"state": "s1", "authUrl": "https://login.codebuddy.cn/oauth?client_id=c"})
		case "/v2/plugin/auth/token":
			ok(map[string]any{"accessToken": "access-" + uid, "refreshToken": "refresh-" + uid,
				"expiresIn": 3600, "refreshExpiresIn": 7200, "domain": "www.codebuddy.cn", "tokenType": "Bearer"})
		case "/v2/plugin/login/account":
			ok(map[string]any{"uid": uid, "nickname": nick})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	oldEnd, oldInt := wbEndpoint, wbPollInterval
	wbEndpoint, wbPollInterval = srv.URL, 5*time.Millisecond
	t.Cleanup(func() { wbEndpoint, wbPollInterval = oldEnd, oldInt })
	wbTokens.Lock()
	wbTokens.m = map[string]wbCreds{}
	wbTokens.Unlock()
	return who
}

// wbDesktopAs signs WorkBuddy itself in to uid, nick.
func wbDesktopAs(t *testing.T, home, uid, nick string) {
	t.Helper()
	writeFile(t, wbAuthFile(home), map[string]any{
		"account": map[string]any{"uid": uid, "nickname": nick},
		"auth": map[string]any{"accessToken": "desktop-" + uid, "refreshToken": "r",
			"expiresAt": time.Now().Add(24 * time.Hour).UnixMilli(), "domain": "www.codebuddy.cn"},
	})
}

func wbUsers(ls []Login) []string {
	var out []string
	for _, l := range ls {
		out = append(out, l.User)
	}
	return out
}

func wbAccessOf(t *testing.T, user string) string {
	t.Helper()
	for _, a := range wbLogins(wbCN) {
		if a.User == user {
			return a.creds.Access
		}
	}
	t.Fatalf("no account %q", user)
	return ""
}

// Another account of the same nickname as WorkBuddy's own is added beside
// it: it was taken for WorkBuddy's own and let go, the sign-in said
// "signed in" and no account was added (#413).
func TestWorkBuddyAddSameNicknameAsOwn(t *testing.T) {
	home := signIn(t)
	fake := wbFakeAccounts(t)
	wbDesktopAs(t, home, "u1", "Me")
	Logins("workbuddy")
	fake.set("u2", "Me")
	st := wbSignInNow(t)
	if st.State != "done" || st.Using || st.Again || st.User != "Me (u2)" {
		t.Fatalf("sign-in: %+v", st)
	}
	ls := Logins("workbuddy")
	if len(ls) != 2 || ls[0].User != "Me" || !ls[0].Active || ls[1].User != "Me (u2)" {
		t.Fatalf("logins: %v", wbUsers(ls))
	}
	if a := wbAccessOf(t, "Me"); a != "desktop-u1" {
		t.Fatalf("WorkBuddy's own signs with %q", a)
	}
	if a := wbAccessOf(t, "Me (u2)"); a != "access-u2" {
		t.Fatalf("the added one signs with %q", a)
	}
}

// Two accounts magpie signed in that share a nickname are both kept: the
// second replaced the first (#413).
func TestWorkBuddyAddSameNicknameTwice(t *testing.T) {
	signIn(t)
	fake := wbFakeAccounts(t)
	fake.set("u2", "Me")
	if st := wbSignInNow(t); st.State != "done" || st.User != "Me" || st.Again {
		t.Fatalf("first: %+v", st)
	}
	fake.set("u3abcdef", "Me")
	if st := wbSignInNow(t); st.State != "done" || st.User != "Me (cdef)" || st.Again {
		t.Fatalf("second: %+v", st)
	}
	ls := Logins("workbuddy")
	if len(ls) != 2 || wbAccessOf(t, "Me") != "access-u2" || wbAccessOf(t, "Me (cdef)") != "access-u3abcdef" {
		t.Fatalf("logins: %v", wbUsers(ls))
	}

	// the first again: renewed where it is, and said so
	fake.set("u2", "Me")
	if st := wbSignInNow(t); st.State != "done" || st.User != "Me" || !st.Again {
		t.Fatalf("again: %+v", st)
	}
	if ls := Logins("workbuddy"); len(ls) != 2 {
		t.Fatalf("logins after again: %v", wbUsers(ls))
	}
}

// Accounts added one by one through WorkBuddy — WorkBuddy signed in to
// each, then picked on its page from magpie — each pushed the last off:
// the one WorkBuddy is signed in to was only read from WorkBuddy, so it
// went once WorkBuddy signed in to the next (#413). Signed in from magpie,
// it is magpie's and stays.
func TestWorkBuddyAddWhatWorkBuddyIsSignedInTo(t *testing.T) {
	home := signIn(t)
	fake := wbFakeAccounts(t)
	wbDesktopAs(t, home, "u1", "Alice")
	Logins("workbuddy")
	fake.set("u1", "Alice")
	st := wbSignInNow(t)
	if st.State != "done" || st.User != "Alice" || !st.Using || !st.Again {
		t.Fatalf("sign-in: %+v", st)
	}
	if ls := Logins("workbuddy"); len(ls) != 1 || ls[0].User != "Alice" || !ls[0].Active {
		t.Fatalf("logins: %v", wbUsers(ls))
	}

	wbDesktopAs(t, home, "u2", "Bob")
	ls := Logins("workbuddy")
	if len(ls) != 2 || ls[0].User != "Alice" || !ls[0].Active || ls[1].User != "Bob" {
		t.Fatalf("after WorkBuddy signed in to Bob: %v", wbUsers(ls))
	}
	if a := wbAccessOf(t, "Alice"); a != "access-u1" {
		t.Fatalf("Alice signs with %q", a)
	}

	fake.set("u2", "Bob")
	if st := wbSignInNow(t); st.State != "done" || st.User != "Bob" || !st.Using || !st.Again {
		t.Fatalf("Bob: %+v", st)
	}
	wbDesktopAs(t, home, "u3", "Carol")
	if ls := Logins("workbuddy"); len(ls) != 3 || ls[0].User != "Alice" || ls[1].User != "Bob" || ls[2].User != "Carol" {
		t.Fatalf("after WorkBuddy signed in to Carol: %v", wbUsers(ls))
	}
}
