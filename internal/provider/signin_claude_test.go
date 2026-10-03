package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeClaudeAccount is who a fake `claude auth login` signs in to.
type fakeClaudeAccount struct {
	email, org, orgUUID, plan, refresh, name string
}

// fakeClaudeLogin points claudeExecutable at a `claude` whose `auth login`
// signs in to a: as Claude Code does, it opens its page through $BROWSER,
// which comes back to a listener of its own on this machine (the test's
// callback server here), prints the page whose code is pasted into it, and
// keeps the sign-in in $CLAUDE_CONFIG_DIR. own is the script that answers
// `auth status` with no CLAUDE_CONFIG_DIR, "" for no answer. opens says
// whether the browser it's given opens anything; magpie's own is replaced
// with a script that writes the page down as magpie would.
func fakeClaudeLogin(t *testing.T, a fakeClaudeAccount, own string, opens bool) string {
	t.Helper()
	shellFakes(t)
	dir := t.TempDir()
	goFile := filepath.Join(dir, "go")
	cb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" || r.URL.Query().Get("code") != "the-code" || r.URL.Query().Get("state") != "st" {
			w.WriteHeader(400)
			return
		}
		os.WriteFile(goFile, nil, 0o600)
		w.Write([]byte("signed in"))
	}))
	t.Cleanup(cb.Close)
	port := cb.URL[strings.LastIndex(cb.URL, ":")+1:]
	opened := "https://claude.com/cai/oauth/authorize?code=true&" + url.Values{"redirect_uri": {"http://localhost:" + port + "/callback"}, "state": {"st"}}.Encode()
	printed := "https://claude.com/cai/oauth/authorize?code=true&" + url.Values{"redirect_uri": {"https://platform.claude.com/oauth/code/callback"}, "state": {"st"}}.Encode()
	if own == "" {
		own = "exit 1"
	}
	exe := filepath.Join(dir, "claude")
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1 $2" = "auth status" ]; then
  if [ -n "$CLAUDE_CONFIG_DIR" ]; then cat "$CLAUDE_CONFIG_DIR/status.json"; exit 0; fi
  %s
fi
[ "$1 $2" = "auth login" ] || exit 2
echo "Opening browser to sign in…"
"$BROWSER" '%s'
echo "If the browser didn't open, visit: %s"
printf 'Paste code here if prompted > '
exec 3<&0
( while read -r line; do echo "$line" >> "$CLAUDE_CONFIG_DIR/.pasted"; done ) <&3 >/dev/null 2>&1 &
reader=$!
while :; do
  [ -f '%s' ] && break
  grep -qx 'the-code#st' "$CLAUDE_CONFIG_DIR/.pasted" 2>/dev/null && break
  sleep 0.05
done
kill $reader 2>/dev/null
rm -f '%[4]s' "$CLAUDE_CONFIG_DIR/.pasted"
cat > "$CLAUDE_CONFIG_DIR/.credentials.json" <<EOF
{"claudeAiOauth":{"accessToken":"sk-ant-oat01-x","refreshToken":"%s","expiresAt":%d,"scopes":["user:inference","user:profile"],"subscriptionType":"%s"}}
EOF
cat > "$CLAUDE_CONFIG_DIR/.claude.json" <<EOF
{"oauthAccount":{"emailAddress":"%s","organizationUuid":"%s","organizationName":"%s","displayName":"%s"}}
EOF
echo '{"loggedIn":true,"email":"%[8]s","subscriptionType":"%[7]s"}' > "$CLAUDE_CONFIG_DIR/status.json"
echo "Login successful."
`, own, opened, printed, goFile, a.refresh, time.Now().Add(time.Hour).UnixMilli(), a.plan, a.email, a.orgUUID, a.org, a.name)
	if err := os.WriteFile(exe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	browser := filepath.Join(dir, "browser")
	body := "#!/bin/sh\nexit 1\n"
	if opens {
		body = "#!/bin/sh\nprintf %s \"$1\" > \"$" + openedURLEnv + "\"\n"
	}
	os.WriteFile(browser, []byte(body), 0o755)
	oldExe, oldOpener := claudeExecutable, claudeURLOpener
	claudeExecutable = func() string { return exe }
	claudeURLOpener = func() string { return browser }
	forgetClaudeStatus()
	t.Cleanup(func() {
		claudeExecutable, claudeURLOpener = oldExe, oldOpener
		forgetClaudeStatus()
	})
	return exe
}

// noAnthropic fails the test if anything is asked of ClaudeBase.
func noAnthropic(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("asked Anthropic: %s %s", r.Method, r.URL)
		w.WriteHeader(500)
	}))
	t.Cleanup(srv.Close)
	old := ClaudeBase
	ClaudeBase = srv.URL
	t.Cleanup(func() { ClaudeBase = old })
}

// A browser that can't come back to this machine: the page Claude Code
// prints shows a code, pasted into magpie, which types it into Claude Code;
// or the address the browser ended on, handed to Claude Code's listener.
func TestClaudeSignInByPaste(t *testing.T) {
	claudeHome(t)
	noAnthropic(t)
	fakeClaudeLogin(t, fakeClaudeAccount{email: "paste@example.com", plan: "pro", refresh: "sk-ant-ort01-paste"}, "", false)

	st, err := StartSignIn("claude")
	if err != nil {
		t.Fatal(err)
	}
	if !st.PasteCode || !strings.Contains(st.URL, "platform.claude.com") {
		t.Fatalf("state %+v", st)
	}
	if err := SubmitSignInCallback(st.ID, "not a code"); err == nil {
		t.Fatal("took garbage")
	}
	if err := SubmitSignInCallback(st.ID, "the-code#st"); err != nil {
		t.Fatal(err)
	}
	if st = waitDone(t, st.ID); st.State != "done" || st.User != "paste@example.com" || st.Plan != "pro" || !st.Using {
		t.Fatalf("state %+v", st)
	}
	c, _, ok := claudeCredential()
	if !ok || c.OAuth.RefreshToken != "sk-ant-ort01-paste" {
		t.Fatalf("Claude Code's credentials %+v", c.OAuth)
	}
	// the sign-in's own directory is gone
	if es, _ := os.ReadDir(claudeDirsRoot()); len(es) != 0 {
		t.Fatalf("left behind: %v", es)
	}
}

func TestClaudeSignInByPastedAddress(t *testing.T) {
	claudeHome(t)
	noAnthropic(t)
	fakeClaudeLogin(t, fakeClaudeAccount{email: "addr@example.com", plan: "max", refresh: "sk-ant-ort01-addr"}, "", true)

	st, err := StartSignIn("claude")
	if err != nil {
		t.Fatal(err)
	}
	if !st.PasteCallback || !strings.Contains(st.URL, "localhost") {
		t.Fatalf("state %+v", st)
	}
	u, _ := url.Parse(st.URL)
	back, _ := url.Parse(u.Query().Get("redirect_uri"))
	if err := SubmitSignInCallback(st.ID, "http://localhost:1/callback?code=the-code&state=st"); err == nil {
		t.Fatal("took another port's address")
	}
	if err := SubmitSignInCallback(st.ID, back.String()+"?code=the-code&state=st"); err != nil {
		t.Fatal(err)
	}
	if st = waitDone(t, st.ID); st.State != "done" || st.User != "addr@example.com" {
		t.Fatalf("state %+v", st)
	}
}

// A saved account in use beside Claude Code's own runs Claude Code in a
// config directory of its own: its sign-in is put there once, what Claude
// Code makes of it is read back, and switching to it moves it out again.
func TestClaudeSavedAccountDir(t *testing.T) {
	home := claudeHome(t)
	noAnthropic(t)
	cred := claudeSignIn(t, home, time.Now().Add(time.Hour))
	writeFile(t, filepath.Join(home, ".claude.json"), map[string]any{"oauthAccount": map[string]any{"emailAddress": "old@example.com"}})
	fakeClaudeLogin(t, fakeClaudeAccount{email: "side@example.com", name: "Side", plan: "max", refresh: "sk-ant-ort01-side"}, "", true)

	st, err := StartSignIn("claude")
	if err != nil {
		t.Fatal(err)
	}
	finishInBrowser(t, st, "the-code")
	if st = waitDone(t, st.ID); st.State != "done" || st.Using {
		t.Fatalf("state %+v", st)
	}
	if err := SetLoginOn("claude", "side@example.com", true); err != nil {
		t.Fatal(err)
	}
	p, ok := find(All(), "claude")
	if !ok || len(p.AlsoOn()) != 1 {
		t.Fatalf("also on: %v %+v", ok, p.AlsoOn())
	}
	dir, own, err := p.AlsoOn()[0].Account.Token(context.Background())
	if err != nil || !own || dir != claudeAccountDir("side@example.com") {
		t.Fatalf("dir %q %v %v", dir, own, err)
	}
	c, ok := readClaudeDir(dir)
	if !ok || c.OAuth.RefreshToken != "sk-ant-ort01-side" {
		t.Fatalf("dir credentials %+v", c.OAuth)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ".claude.json")); !strings.Contains(string(b), "side@example.com") {
		t.Fatalf(".claude.json %s", b)
	}

	// Claude Code refreshes it there; magpie keeps what it made of it
	c.OAuth.RefreshToken = "sk-ant-ort01-side2"
	b, _ := c.marshal()
	os.WriteFile(filepath.Join(dir, ".credentials.json"), b, 0o600)
	if _, _, err := p.AlsoOn()[0].Account.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, l := range readLogins() {
		if l.User == "side@example.com" {
			if lc, _ := parseClaudeCredentials(l.Auth); lc.OAuth.RefreshToken != "sk-ant-ort01-side2" {
				t.Fatalf("saved %+v", lc.OAuth)
			}
		}
	}

	// switched to, it is Claude Code's own, and its directory is gone
	if err := SwitchLogin("claude", "side@example.com"); err != nil {
		t.Fatal(err)
	}
	var cc map[string]map[string]any
	readJSON(cred, &cc)
	if cc["claudeAiOauth"]["refreshToken"] != "sk-ant-ort01-side2" {
		t.Fatalf("credentials %v", cc["claudeAiOauth"])
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("directory kept: %v", err)
	}
}
