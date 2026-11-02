package provider

import (
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// claimsToken is an id token carrying claims as written, byte for byte.
func claimsToken(claims string) string {
	return "e30." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".sig"
}

// codexClaimsSignIn signs Codex in to email with an id token of claims,
// the auth claim's keys as a real ChatGPT sign-in's are (2026-11-02).
func codexClaimsSignIn(t *testing.T, home, email, plan, auth string) {
	t.Helper()
	claims := `{"email":"` + email + `","email_verified":true,"https://api.openai.com/auth":{` +
		`"chatgpt_account_id":"acct-` + email + `","chatgpt_plan_type":"` + plan + `",` + auth +
		`"chatgpt_user_id":"user-x","groups":[],"organizations":[],"user_id":"user-x"}}`
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      claimsToken(claims),
			"access_token":  fakeJWT(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix())}),
			"refresh_token": "r-" + email, "account_id": "acct-" + email,
		},
	})
}

func untilOf(ls []Login) map[string]*time.Time {
	out := map[string]*time.Time{}
	for _, l := range ls {
		out[l.User] = l.Until
	}
	return out
}

// Each ChatGPT account's row says when its paid plan ends, as its sign-in's
// chatgpt_subscription_active_until says: the one Codex is signed in to and
// a saved one alike. A date gone by (OpenAI checks the plan now and then,
// so a token refreshed today can carry last month's) and a sign-in with no
// such claim say nothing (danzi002 on Discord).
func TestCodexLoginUntil(t *testing.T) {
	home := signIn(t) // me@example.com: no subscription claims
	rememberLogins(true)
	codexClaimsSignIn(t, home, "pro@example.com", "pro",
		`"chatgpt_subscription_active_start":"2020-08-18T04:45:15+00:00","chatgpt_subscription_active_until":"2020-09-18T04:45:15+00:00","chatgpt_subscription_last_checked":"2020-09-15T17:30:44.647759+00:00",`)
	rememberLogins(true)
	codexClaimsSignIn(t, home, "plus@example.com", "plus",
		`"chatgpt_subscription_active_start":"2099-10-19T15:12:01+00:00","chatgpt_subscription_active_until":"2099-11-19T16:12:01+00:00","chatgpt_subscription_last_checked":"2099-10-27T19:16:18.236460+00:00",`)
	rememberLogins(true)

	want := time.Date(2099, 11, 19, 16, 12, 1, 0, time.UTC)
	check := func(when string) {
		t.Helper()
		u := untilOf(Logins("codex"))
		if len(u) != 3 {
			t.Fatalf("%s: accounts %v", when, u)
		}
		if u["plus@example.com"] == nil || !u["plus@example.com"].Equal(want) {
			t.Errorf("%s: plus@ until %v, want %v", when, u["plus@example.com"], want)
		}
		if u["pro@example.com"] != nil {
			t.Errorf("%s: pro@'s date gone by is shown: %v", when, u["pro@example.com"])
		}
		if u["me@example.com"] != nil {
			t.Errorf("%s: me@ has no such claim, shown %v", when, u["me@example.com"])
		}
	}
	check("signed in")
	if err := SwitchLogin("codex", "me@example.com"); err != nil {
		t.Fatal(err)
	}
	check("saved")

	b, _ := json.Marshal(Logins("codex"))
	if !strings.Contains(string(b), `"until":"2099-11-19T16:12:01Z"`) {
		t.Fatalf("the list the GUI reads has no until: %s", b)
	}
}

// A ChatGPT API account's row says it too, from its own sign-in's tokens.
func TestSIWCLoginUntil(t *testing.T) {
	signIn(t)
	c := siwcCreds{ClientID: "c", HostID: "h", Email: "api@example.com", Access: "a", Refresh: "r", Expires: time.Now().Add(time.Hour),
		IDToken: claimsToken(`{"email":"api@example.com","https://api.openai.com/auth":{"chatgpt_plan_type":"plus","chatgpt_subscription_active_until":"2099-11-19T16:12:01+00:00"}}`)}
	auth, _ := json.Marshal(c)
	if err := writeLogins([]savedLogin{{Agent: ChatGPTAPIID, User: "api@example.com", Auth: auth}}); err != nil {
		t.Fatal(err)
	}
	ls := Logins(ChatGPTAPIID)
	if len(ls) != 1 || ls[0].Until == nil || !ls[0].Until.Equal(time.Date(2099, 11, 19, 16, 12, 1, 0, time.UTC)) {
		t.Fatalf("ChatGPT API rows: %+v", ls)
	}
}
