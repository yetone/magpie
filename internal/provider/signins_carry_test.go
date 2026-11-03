package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
)

// Sign-ins in a backup or a sync (qianxunji and curtinp118 on Discord:
// restoring on a VPS meant signing in to every subscription again). These
// hold what the switch promises: what is newer here is never written
// over, an account that could be more than one here is left alone, and a
// sign-in is taken away only when it was removed on the other side.

// geminiLogin is a saved Gemini CLI account whose token runs out at exp
// (ms), the way Google's oauth_creds.json says when it was renewed.
func geminiLogin(user, token string, exp int64) savedLogin {
	auth := fmt.Sprintf(`{"access_token":%q,"refresh_token":"r-%s","expiry_date":%d}`, token, token, exp)
	return savedLogin{Agent: "gemini", User: user, Auth: json.RawMessage(auth)}
}

func onlyHashes(s *SignIns) map[string]string { return s.Hashes() }

func TestMergeSignInsTheLaterRenewedStays(t *testing.T) {
	older := &SignIns{Logins: []savedLogin{geminiLogin("a@x.com", "old", 2000)}}
	newerS := &SignIns{Logins: []savedLogin{geminiLogin("a@x.com", "new", 3000)}}

	// the server's is newer: it comes in, and stays on the server
	shared, plan, hashes := MergeSignIns(older, newerS, nil)
	if len(plan.put) != 1 || !strings.Contains(string(plan.put[0].login.Auth), `"new"`) {
		t.Fatalf("the server's newer sign-in isn't put in here: %+v", plan.put)
	}
	if want := onlyHashes(newerS); hashes["gemini|a@x.com"] != want["gemini|a@x.com"] || !strings.Contains(string(shared.Logins[0].Auth), `"new"`) {
		t.Fatalf("the server keeps the older sign-in: %v", hashes)
	}

	// this computer's is newer: nothing is put in here, and it goes up,
	// whatever the last sync knew
	shared, plan, _ = MergeSignIns(newerS, older, map[string]string{"gemini|a@x.com": onlyHashes(older)["gemini|a@x.com"]})
	if !plan.Empty() {
		t.Fatalf("an older sign-in is put over the newer one here: %+v", plan)
	}
	if !strings.Contains(string(shared.Logins[0].Auth), `"new"`) {
		t.Fatalf("the newer sign-in here doesn't go up: %s", shared.Logins[0].Auth)
	}
}

func TestMergeSignInsRemovedOnlyWhenTheOtherSideRemovedIt(t *testing.T) {
	a := geminiLogin("a@x.com", "tok", 2000)
	local := &SignIns{Logins: []savedLogin{a}}
	id, h := "gemini|a@x.com", onlyHashes(local)["gemini|a@x.com"]
	empty := &SignIns{}

	// never synced with it: the server not having it is no removal
	if _, plan, hashes := MergeSignIns(local, empty, nil); len(plan.remove) != 0 || hashes[id] != h {
		t.Fatalf("an account the server never had is taken away here: %+v %v", plan.remove, hashes)
	}
	// the server had it, as it is here, and hasn't now: removed there
	if _, plan, hashes := MergeSignIns(local, empty, map[string]string{id: h}); len(plan.remove) != 1 || plan.remove[0] != id || hashes[id] != "" {
		t.Fatalf("an account removed on another computer stays: %+v %v", plan.remove, hashes)
	}
	// it was renewed here since: it stays, and goes back up
	if _, plan, hashes := MergeSignIns(local, empty, map[string]string{id: "older"}); len(plan.remove) != 0 || hashes[id] != h {
		t.Fatalf("an account renewed here since is taken away: %+v %v", plan.remove, hashes)
	}
	// a server file without sign-ins at all (a computer with them off, or
	// an older magpie): nothing is taken away
	if _, plan, _ := MergeSignIns(local, nil, map[string]string{id: h}); len(plan.remove) != 0 {
		t.Fatalf("a file without sign-ins takes them away here: %+v", plan.remove)
	}
	// on the server only, as the last sync left it: removed here, so it
	// isn't put back here, and goes from the server
	if _, plan, hashes := MergeSignIns(empty, local, map[string]string{id: h}); len(plan.put) != 0 || hashes[id] != "" {
		t.Fatalf("an account removed here comes back: %+v %v", plan.put, hashes)
	}
	// on the server only, never seen here: put in here
	if _, plan, _ := MergeSignIns(empty, local, nil); len(plan.put) != 1 {
		t.Fatalf("a computer joining doesn't get the server's account: %+v", plan.put)
	}
}

// own, Home and Hidden accounts and a plugin's row never go; a file that
// can't be read is an error, never none (a sync would take them away
// elsewhere)
func TestCollectSignInsCarriesMagpiesOwnCopiesOnly(t *testing.T) {
	signIn(t)
	ls := []savedLogin{
		geminiLogin("a@x.com", "tok", 2000),
		{Agent: "gemini", User: "own@x.com"}, // the agent's own, read in place
		{Agent: "grok", User: "g@x.com", Home: "/somewhere", Auth: json.RawMessage(`{"k":1}`)},
		func() savedLogin { l := geminiLogin("hid@x.com", "h", 1); l.Hidden = "x"; return l }(),
	}
	if err := writeLogins(ls); err != nil {
		t.Fatal(err)
	}
	s, err := CollectSignIns()
	if err != nil {
		t.Fatal(err)
	}
	var users []string
	for _, l := range s.Logins {
		users = append(users, l.User)
	}
	if strings.Join(users, ",") != "a@x.com" {
		t.Fatalf("carried: %v", users)
	}
	if err := os.WriteFile(loginsPath(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CollectSignIns(); err == nil {
		t.Fatal("an unreadable logins.json is carried as no sign-ins")
	}
}

func savedNow(t *testing.T) []savedLogin {
	t.Helper()
	ls, err := readLoginsFile()
	if err != nil {
		t.Fatal(err)
	}
	return ls
}

// A backup made before the account was renewed here never writes its
// older token over the newer one; a later one does, and a missing one is
// added.
func TestRestoreSignInsNeverWritesAnOlderTokenOver(t *testing.T) {
	signIn(t)
	if err := writeLogins([]savedLogin{geminiLogin("a@x.com", "here", 3000)}); err != nil {
		t.Fatal(err)
	}
	stale := &SignIns{Logins: []savedLogin{geminiLogin("a@x.com", "stale", 2000), geminiLogin("b@x.com", "b", 2000)}}
	r, err := RestoreSignIns(stale)
	if err != nil {
		t.Fatal(err)
	}
	if r.Added != 1 || r.Updated != 0 || r.Kept != 1 {
		t.Fatalf("result: %+v", r)
	}
	for _, l := range savedNow(t) {
		if l.User == "a@x.com" && !strings.Contains(string(l.Auth), `"here"`) {
			t.Fatalf("the stale backup wrote over the newer sign-in: %s", l.Auth)
		}
	}
	later := &SignIns{Logins: []savedLogin{geminiLogin("a@x.com", "later", 4000)}}
	if r, err := RestoreSignIns(later); err != nil || r.Updated != 1 {
		t.Fatalf("a later sign-in isn't put in: %+v %v", r, err)
	}
	for _, l := range savedNow(t) {
		if l.User == "a@x.com" && !strings.Contains(string(l.Auth), `"later"`) {
			t.Fatalf("not updated: %s", l.Auth)
		}
	}
}

// Two seats of one email here, on two organizations (the Team seats
// that once lost both auth.json files, c13f3bfb), and a backup's account
// that names the email alone: it could be either, so neither is written
// over.
func TestRestoreSignInsLeavesAnAmbiguousAccount(t *testing.T) {
	signIn(t)
	one := savedLogin{Agent: "claude", User: "me@x.com", Profile: json.RawMessage(`{"emailAddress":"me@x.com","organizationUuid":"org-a"}`), Auth: json.RawMessage(`{"claudeAiOauth":{"accessToken":"one","expiresAt":2000}}`)}
	two := savedLogin{Agent: "claude", User: "me@x.com", Profile: json.RawMessage(`{"emailAddress":"me@x.com","organizationUuid":"org-b"}`), Auth: json.RawMessage(`{"claudeAiOauth":{"accessToken":"two","expiresAt":2000}}`)}
	if err := writeLogins([]savedLogin{one, two}); err != nil {
		t.Fatal(err)
	}
	if n := len(savedNow(t)); n != 2 {
		t.Fatalf("fixture: %d accounts saved", n)
	}
	in := savedLogin{Agent: "claude", User: "me@x.com", Auth: json.RawMessage(`{"claudeAiOauth":{"accessToken":"new","expiresAt":9000}}`)}
	r, err := RestoreSignIns(&SignIns{Logins: []savedLogin{in}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Added+r.Updated != 0 {
		t.Fatalf("an account that could be either of two here was written: %+v", r)
	}
	for _, l := range savedNow(t) {
		if strings.Contains(string(l.Auth), `"new"`) {
			t.Fatalf("written over: %+v", l)
		}
	}
}

func carriedCodexAuth(email, acct, refresh, last string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"auth_mode": "chatgpt", "last_refresh": last,
		"tokens": map[string]any{
			"id_token":      fakeJWT(map[string]any{"email": email, "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro", "chatgpt_account_id": acct}}),
			"access_token":  fakeJWT(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix())}),
			"refresh_token": refresh, "account_id": acct,
		},
	})
	return b
}

// Codex's auth.json is the agent's own: written only when Codex is signed
// out here, never over the account it is signed in to.
func TestRestoreSignInsSignsCodexInOnlyWhereSignedOut(t *testing.T) {
	home := signIn(t)
	t.Setenv("CODEX_HOME", "")
	authPath := filepath.Join(home, ".codex", "auth.json")
	mine, _ := os.ReadFile(authPath)
	other := savedLogin{Agent: "codex", User: "other@x.com", Auth: carriedCodexAuth("other@x.com", "acct-2", "r-2", "2026-11-01T00:00:00Z")}
	s := &SignIns{Logins: []savedLogin{other}}
	s.Codex = loginRecID(other)

	r, err := RestoreSignIns(s)
	if err != nil {
		t.Fatal(err)
	}
	if r.Codex {
		t.Fatal("Codex signed in here was signed in to the backup's account")
	}
	if now, _ := os.ReadFile(authPath); string(now) != string(mine) {
		t.Fatalf("auth.json written over:\n%s", now)
	}

	if err := os.Remove(authPath); err != nil {
		t.Fatal(err)
	}
	r, err = RestoreSignIns(s)
	if err != nil {
		t.Fatal(err)
	}
	now, err := os.ReadFile(authPath)
	if !r.Codex || err != nil || credHash(now) != credHash(other.Auth) {
		t.Fatalf("Codex signed out isn't signed in to the backup's account: %+v %v %s", r, err, now)
	}
	if fi, err := os.Stat(authPath); err != nil || fi.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Fatalf("auth.json is readable by others: %v %v", fi.Mode(), err)
	}
}

// The plugins' sign-ins: the later one stays, one removed elsewhere goes,
// and a key here that holds another account is never written over.
func TestApplySignInsPlugins(t *testing.T) {
	signIn(t)
	write := func(m map[string]any) {
		b, _ := json.Marshal(m)
		if err := os.MkdirAll(filepath.Dir(plugin.AuthPath()), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(plugin.AuthPath(), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	read := func() map[string]json.RawMessage {
		m, err := plugin.AuthEntries()
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	write(map[string]any{
		"kiro":   map[string]any{"type": "oauth", "access": "here", "refresh": "r", "expires": 3000, "accountId": "k@x.com"},
		"cursor": map[string]any{"type": "oauth", "access": "c", "refresh": "r", "expires": 1000, "accountId": "c@x.com"},
	})
	local, err := CollectSignIns()
	if err != nil {
		t.Fatal(err)
	}
	prev := local.Hashes()
	remote := &SignIns{Plugins: map[string]json.RawMessage{
		"kiro": json.RawMessage(`{"type":"oauth","access":"stale","refresh":"r","expires":2000,"accountId":"k@x.com"}`),
		// the same key here is another account's: added beside it
		"zed": json.RawMessage(`{"type":"oauth","access":"z","refresh":"r","expires":5000,"accountId":"z@x.com"}`),
	}}
	// cursor's is gone from the server, unchanged here since: removed
	_, plan, _ := MergeSignIns(local, remote, prev)
	if _, err := ApplySignIns(plan); err != nil {
		t.Fatal(err)
	}
	m := read()
	if !strings.Contains(string(m["kiro"]), `"here"`) {
		t.Fatalf("a stale plugin sign-in was written over the newer one: %s", m["kiro"])
	}
	if _, ok := m["cursor"]; ok {
		t.Fatal("a sign-in removed on the other computer stays")
	}
	if !strings.Contains(string(m["zed"]), `"z"`) {
		t.Fatalf("the server's new sign-in isn't added: %v", m)
	}
}

// A sign-in renewed here between the merge's read and its write is kept:
// the plan was made over what was here then.
func TestApplySignInsKeepsWhatChangedSinceTheMerge(t *testing.T) {
	signIn(t)
	if err := writeLogins([]savedLogin{geminiLogin("a@x.com", "here", 1000)}); err != nil {
		t.Fatal(err)
	}
	local, err := CollectSignIns()
	if err != nil {
		t.Fatal(err)
	}
	remote := &SignIns{Logins: []savedLogin{geminiLogin("a@x.com", "server", 2000)}}
	_, plan, _ := MergeSignIns(local, remote, nil)
	if len(plan.put) != 1 {
		t.Fatalf("plan: %+v", plan)
	}
	// renewed here meanwhile
	if err := writeLogins([]savedLogin{geminiLogin("a@x.com", "renewed", 9000)}); err != nil {
		t.Fatal(err)
	}
	r, err := ApplySignIns(plan)
	if err != nil {
		t.Fatal(err)
	}
	if r.Updated != 0 || r.Kept != 1 {
		t.Fatalf("result: %+v", r)
	}
	if ls := savedNow(t); !strings.Contains(string(ls[0].Auth), `"renewed"`) {
		t.Fatalf("written over: %s", ls[0].Auth)
	}
}
