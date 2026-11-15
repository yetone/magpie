package provider

import (
	"context"
	"strings"
	"testing"
)

// LuvLetter (Discord, 10-27): magpie in Docker, a ChatGPT sign-in whose
// browser ended on http://localhost:1455/auth/callback?code=…&state=…, and
// that address pasted into the Import box beside the sign-in. The box read
// it as a refresh token, sent it to ChatGPT, and said the token had been
// used since. Pasted there, it finishes the sign-in it came from, as the
// sign-in's Callback URL field does, and nothing else is imported.
func TestImportBoxFinishesThePastedSignIn(t *testing.T) {
	claudeHome(t)
	trades := fakeCodexTokens(t, nil)

	st, err := StartSignIn("codex")
	if err != nil {
		t.Fatal(err)
	}
	defer CancelSignIn(st.ID)
	back := callbackAddress(t, st, "cx", "")

	// another sign-in's address: no sign-in finished, nothing traded, and
	// the reason says where such an address goes
	done, rest := PastedSignIns([]string{callbackAddress(t, st, "cx", "not-ours")})
	if len(rest) != 0 || len(done) != 1 || done[0].Status != "failed" || !strings.Contains(done[0].Error, "Callback URL") {
		t.Fatalf("done %+v rest %q", done, rest)
	}
	if n := trades.Load(); n != 0 {
		t.Fatalf("%d trades for another sign-in's address", n)
	}

	// the address, as pasted with a newline, beside an account file
	file := `{"type":"codex","refresh_token":"r-file"}`
	done, rest = PastedSignIns([]string{file, "  " + back + "\n"})
	if len(rest) != 1 || rest[0] != file {
		t.Fatalf("left to import: %q", rest)
	}
	if len(done) != 1 || done[0].Status != "added" || done[0].User != "cx@example.com" {
		t.Fatalf("done %+v", done)
	}
	if cur, _ := SignInStatus(st.ID); cur.State != "done" {
		t.Fatalf("sign-in %+v", cur)
	}
	if n := trades.Load(); n != 1 {
		t.Fatalf("%d trades", n)
	}

	// the same address again: its sign-in is over, so it says so, and the
	// code is not traded twice
	done, _ = PastedSignIns([]string{back})
	if len(done) != 1 || done[0].Status != "failed" || !strings.Contains(done[0].Error, "isn't waiting") {
		t.Fatalf("again: %+v", done)
	}
	if n := trades.Load(); n != 1 {
		t.Fatalf("%d trades", n)
	}
}

// Claude's page shows code#state for Claude Code's sign-in; pasted into the
// Import box, it is typed into the `claude auth login` waiting for it.
func TestImportBoxFinishesAPastedClaudeCode(t *testing.T) {
	claudeHome(t)
	noAnthropic(t)
	fakeClaudeLogin(t, fakeClaudeAccount{email: "paste@example.com", plan: "pro", refresh: "sk-ant-ort01-paste"}, "", false)

	st, err := StartSignIn("claude")
	if err != nil {
		t.Fatal(err)
	}
	defer CancelSignIn(st.ID)
	done, rest := PastedSignIns([]string{"the-code#st"})
	if len(rest) != 0 || len(done) != 1 || done[0].Status != "added" || done[0].User != "paste@example.com" {
		t.Fatalf("done %+v rest %q", done, rest)
	}
}

// What isn't a sign-in's end is left to import, and an address among
// refresh tokens is never sent to the vendor as one.
func TestImportTellsAddressesFromAccounts(t *testing.T) {
	t.Cleanup(AntigravityBuiltinForTest()) // the retired built-in route
	for _, s := range []string{
		`{"tokens":{"refresh_token":"rt_x"}}`,
		"rt_abc123",
		"1//0gAbc-def_ghi",
		"https://example.com/page",
		"http://localhost:1455/auth/callback?state=s",
		"a#b c",
	} {
		if _, ok := signInEndState(s); ok {
			t.Errorf("%q read as a sign-in's end", s)
		}
	}
	for _, s := range []string{
		"http://localhost:1455/auth/callback?code=ac_1&scope=openid&state=s1",
		"http://127.0.0.1:51121/oauth-callback?state=s1&code=4/0Ab",
		"http://localhost:1455/auth/callback?error=access_denied&state=s1",
		"code_1#s1",
	} {
		if st, ok := signInEndState(s); !ok || st != "s1" {
			t.Errorf("%q: state %q, %v", s, st, ok)
		}
	}

	claudeHome(t)
	trades := fakeCodexTokens(t, nil)
	res, err := ImportLogins(context.Background(), "codex", []string{"http://localhost:1455/auth/callback?code=ac_1&state=s1\n"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Status != "failed" || res[0].Error != "not a refresh token" {
		t.Fatalf("results %+v", res)
	}
	if n := trades.Load(); n != 0 {
		t.Fatalf("the address went to ChatGPT %d times", n)
	}
	gs, err := parseGoogleImport("antigravity", "1//0gAbc\nhttp://127.0.0.1:51121/oauth-callback?code=4/0Ab&state=s1")
	if err != nil || len(gs) != 2 || gs[0].refreshToken != "1//0gAbc" || gs[1].err != "not a refresh token" {
		t.Fatalf("google %+v %v", gs, err)
	}
}
