package provider

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/settings"
)

// claudeTwoAccounts signs Claude Code in to a@example.com, then to
// b@example.com, as magpie sees both, and answers its settings.json.
func claudeTwoAccounts(t *testing.T) string {
	t.Helper()
	home := claudeHome(t)
	cred := claudeSignIn(t, home, time.Now().Add(time.Hour))
	profile := filepath.Join(home, ".claude.json")
	writeFile(t, profile, map[string]any{"oauthAccount": map[string]any{"emailAddress": "a@example.com", "accountUuid": "u-a"}})
	rememberLogins(true)
	writeFile(t, cred, map[string]any{"claudeAiOauth": map[string]any{"accessToken": "sk-ant-oat01-b", "refreshToken": "sk-ant-ort01-b",
		"expiresAt": time.Now().Add(time.Hour).UnixMilli(), "subscriptionType": "pro"}})
	writeFile(t, profile, map[string]any{"oauthAccount": map[string]any{"emailAddress": "b@example.com", "accountUuid": "u-b"}})
	forgetClaudeCredential()
	rememberLogins(true)
	old := ClaudeHandedOver
	t.Cleanup(func() { ClaudeHandedOver = old })
	ClaudeHandedOver = func(string) {}
	return filepath.Join(home, ".claude", "settings.json")
}

func claudeEnvOf(t *testing.T, path, k string) string {
	t.Helper()
	v, _ := edit.GetJSON(path, "env."+k)
	return v
}

func claudeSwitch(t *testing.T, user string) {
	t.Helper()
	if err := SwitchLogin("claude", user); err != nil {
		t.Fatal(err)
	}
}

// #1807 (kensenzhao): Claude Code signed in to its own subscription asks
// Anthropic itself, through its env's proxy. A switch to an account with a
// proxy of its own has Claude Code's new sessions go through it; a switch
// to one without takes it out again, and what else the env holds stays.
func TestClaudeSwitchTakesTheAccountsProxy(t *testing.T) {
	settingsPath := claudeTwoAccounts(t)
	writeFile(t, settingsPath, map[string]any{"env": map[string]any{"DISABLE_TELEMETRY": "1"}, "model": "opus"})
	if err := Save(Provider{ID: "claude", AccountProxies: map[string]string{"b@example.com": "10.0.0.9:8080"}}); err != nil {
		t.Fatal(err)
	}
	// signed in to b already, a proxy picked for it in the editor reaches
	// Claude Code at once
	if v := claudeEnvOf(t, settingsPath, "HTTPS_PROXY"); v != "http://10.0.0.9:8080" {
		t.Fatalf("b's proxy saved while on b: HTTPS_PROXY = %q", v)
	}
	claudeSwitch(t, "a@example.com")
	for _, k := range []string{"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY"} {
		if v := claudeEnvOf(t, settingsPath, k); v != "" {
			t.Fatalf("on a, which has no proxy, %s = %q", k, v)
		}
	}
	claudeSwitch(t, "b@example.com")
	for _, k := range []string{"HTTPS_PROXY", "HTTP_PROXY"} {
		if v := claudeEnvOf(t, settingsPath, k); v != "http://10.0.0.9:8080" {
			t.Fatalf("on b, %s = %q, want b's proxy", k, v)
		}
	}
	if v := claudeEnvOf(t, settingsPath, "NO_PROXY"); v != claudeNoProxy {
		t.Fatalf("NO_PROXY = %q: the gateway on loopback would go through the proxy", v)
	}
	claudeSwitch(t, "a@example.com")
	for _, k := range []string{"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY"} {
		if v := claudeEnvOf(t, settingsPath, k); v != "" {
			t.Fatalf("back on a, b's %s = %q stayed", k, v)
		}
	}
	if claudeEnvOf(t, settingsPath, "DISABLE_TELEMETRY") != "1" || jsonGetT(t, settingsPath, "model") != "opus" {
		t.Fatal("the rest of settings.json wasn't kept")
	}
	if _, err := os.Stat(claudeProxyRecordPath()); !os.IsNotExist(err) {
		t.Fatalf("a record of nothing written is kept: %v", err)
	}
}

func jsonGetT(t *testing.T, path, k string) string {
	t.Helper()
	v, _ := edit.GetJSON(path, k)
	return v
}

// A proxy the user set in Claude Code's env is theirs: magpie writes no
// account's beside it, and never takes it out (LESSONS: magpie's default
// never overrides a value the user set).
func TestClaudeAccountProxyKeepsTheUsersOwn(t *testing.T) {
	settingsPath := claudeTwoAccounts(t)
	writeFile(t, settingsPath, map[string]any{"env": map[string]any{"https_proxy": "http://mine:7890", "NO_PROXY": "corp.example"}})
	if err := Save(Provider{ID: "claude", AccountProxies: map[string]string{"b@example.com": "http://10.0.0.9:8080"}}); err != nil {
		t.Fatal(err)
	}
	claudeSwitch(t, "a@example.com")
	claudeSwitch(t, "b@example.com")
	if v := claudeEnvOf(t, settingsPath, "https_proxy"); v != "http://mine:7890" {
		t.Fatalf("the user's https_proxy = %q", v)
	}
	for _, k := range []string{"HTTPS_PROXY", "HTTP_PROXY"} {
		if v := claudeEnvOf(t, settingsPath, k); v != "" {
			t.Fatalf("%s = %q written beside the user's own proxy", k, v)
		}
	}
	if v := claudeEnvOf(t, settingsPath, "NO_PROXY"); v != "corp.example" {
		t.Fatalf("the user's NO_PROXY = %q", v)
	}
	claudeSwitch(t, "a@example.com")
	if v := claudeEnvOf(t, settingsPath, "https_proxy"); v != "http://mine:7890" {
		t.Fatalf("the user's https_proxy = %q after a switch to an account without one", v)
	}
}

// One magpie wrote that the user then changed is the user's from then on:
// a switch to an account without a proxy leaves it.
func TestClaudeAccountProxyEditedByTheUserStays(t *testing.T) {
	settingsPath := claudeTwoAccounts(t)
	if err := Save(Provider{ID: "claude", AccountProxies: map[string]string{"b@example.com": "http://10.0.0.9:8080"}}); err != nil {
		t.Fatal(err)
	}
	claudeSwitch(t, "a@example.com")
	claudeSwitch(t, "b@example.com")
	if err := edit.SetJSON(settingsPath, edit.KV{Path: "env.HTTPS_PROXY", Value: "http://mine:7890"}); err != nil {
		t.Fatal(err)
	}
	claudeSwitch(t, "a@example.com")
	if v := claudeEnvOf(t, settingsPath, "HTTPS_PROXY"); v != "http://mine:7890" {
		t.Fatalf("the user's HTTPS_PROXY = %q", v)
	}
	// what was still magpie's goes
	if v := claudeEnvOf(t, settingsPath, "HTTP_PROXY"); v != "" {
		t.Fatalf("magpie's HTTP_PROXY = %q stayed", v)
	}
	// and with the user's own there, b's isn't written over it
	claudeSwitch(t, "b@example.com")
	if v := claudeEnvOf(t, settingsPath, "HTTPS_PROXY"); v != "http://mine:7890" {
		t.Fatalf("b's proxy written over the user's: %q", v)
	}
}

// The subscription's proxy is the way of an account without its own, once
// any account has one; "direct" and a SOCKS proxy, which Claude Code can't
// use, write none.
func TestClaudeAccountProxyChoice(t *testing.T) {
	claudeTwoAccounts(t)
	for _, c := range []struct {
		name string
		p    Provider
		user string
		want string
	}{
		{"no account has one", Provider{ID: "claude", Proxy: "http://sub:1"}, "a@example.com", ""},
		{"the subscription's", Provider{ID: "claude", Proxy: "http://sub:1", AccountProxies: map[string]string{"b@example.com": "http://b:2"}}, "a@example.com", "http://sub:1"},
		{"its own", Provider{ID: "claude", Proxy: "http://sub:1", AccountProxies: map[string]string{"b@example.com": "http://b:2"}}, "B@example.com", "http://b:2"},
		{"direct", Provider{ID: "claude", Proxy: "http://sub:1", AccountProxies: map[string]string{"b@example.com": "direct"}}, "b@example.com", ""},
		{"socks", Provider{ID: "claude", AccountProxies: map[string]string{"b@example.com": "socks5://127.0.0.1:1080"}}, "b@example.com", ""},
	} {
		if err := Save(c.p); err != nil {
			t.Fatal(c.name, err)
		}
		if got := claudeAccountProxy(c.user); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// A settings.json that can't be read is unknown, never empty: it is left
// as it is, the proxy unwritten.
func TestClaudeAccountProxyLeavesABrokenSettings(t *testing.T) {
	settingsPath := claudeTwoAccounts(t)
	broken := []byte(`{"env": {"HTTPS_PROXY": "http://mine:1"`)
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, broken, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "claude", AccountProxies: map[string]string{"b@example.com": "http://10.0.0.9:8080"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := SyncClaudeProxyEnv("b@example.com"); err == nil {
		t.Fatal("no error for a settings.json that can't be read")
	}
	if b, _ := os.ReadFile(settingsPath); string(b) != string(broken) {
		t.Fatalf("settings.json changed: %s", b)
	}
}

// claudeLog is what magpie logs while f runs.
func claudeLog(t *testing.T, f func()) string {
	t.Helper()
	var buf bytes.Buffer
	old, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	defer func() { log.SetOutput(old); log.SetFlags(flags) }()
	f()
	return buf.String()
}

// #1886: an account's proxy is logged without its user and password, the
// switch's line and SOCKS's alike, while Claude Code's env still gets the
// proxy whole. The credential is where the user writes it: with and
// without a scheme, percent-encoded, a user alone.
func TestClaudeAccountProxyLogsNoCredentials(t *testing.T) {
	settingsPath := claudeTwoAccounts(t)
	for _, c := range []struct{ proxy, env, shown string }{
		{"http://alice:s3cret@10.0.0.9:8080", "http://alice:s3cret@10.0.0.9:8080", "http://10.0.0.9:8080"},
		{"alice:s3cret@10.0.0.9:8080", "http://alice:s3cret@10.0.0.9:8080", "http://10.0.0.9:8080"},
		{"http://ali%40ce:s3%3Acr%2Fet@10.0.0.9:8080", "http://ali%40ce:s3%3Acr%2Fet@10.0.0.9:8080", "http://10.0.0.9:8080"},
		{"http://alice@10.0.0.9:8080", "http://alice@10.0.0.9:8080", "http://10.0.0.9:8080"},
		{"socks5://alice:s3cret@127.0.0.1:1080", "", "socks5://127.0.0.1:1080"},
		{"socks5h://ali%40ce:s3%40cret@127.0.0.1:1080", "", "socks5h://127.0.0.1:1080"},
		{"socks5://alice@127.0.0.1:1080", "", "socks5://127.0.0.1:1080"},
	} {
		if err := Save(Provider{ID: "claude", AccountProxies: map[string]string{"b@example.com": c.proxy}}); err != nil {
			t.Fatal(c.proxy, err)
		}
		claudeSwitch(t, "a@example.com")
		got := claudeLog(t, func() { claudeSwitch(t, "b@example.com") })
		if !strings.Contains(got, c.shown) {
			t.Errorf("%s: the log doesn't name the proxy as %s:\n%s", c.proxy, c.shown, got)
		}
		for _, secret := range []string{"ali", "s3"} {
			if strings.Contains(got, secret) {
				t.Errorf("%s: the log shows %q:\n%s", c.proxy, secret, got)
			}
		}
		if v := claudeEnvOf(t, settingsPath, "HTTPS_PROXY"); v != c.env {
			t.Errorf("%s: Claude Code's HTTPS_PROXY = %q, want %q", c.proxy, v, c.env)
		}
	}
}

// An account that chose a saved proxy has Claude Code's env hold its
// address with the login; editing the saved proxy writes the new one, and
// deleting it leaves a proxy that goes nowhere, not direct.
func TestClaudeAccountSavedProxy(t *testing.T) {
	settingsPath := claudeTwoAccounts(t)
	saved := func(url string) {
		t.Helper()
		var ps []settings.NamedProxy
		if url != "" {
			ps = []settings.NamedProxy{{ID: "pxc", Name: "C", URL: url, User: "me", Pass: "pw"}}
		}
		s := settings.Load()
		s.Proxies = ps
		if err := settings.Save(s); err != nil {
			t.Fatal(err)
		}
	}
	saved("10.0.0.9:8080")
	if err := Save(Provider{ID: "claude", AccountProxies: map[string]string{"b@example.com": "@pxc"}}); err != nil {
		t.Fatal(err)
	}
	if v := claudeEnvOf(t, settingsPath, "HTTPS_PROXY"); v != "http://me:pw@10.0.0.9:8080" {
		t.Fatalf("HTTPS_PROXY = %q", v)
	}
	saved("10.0.0.10:9090")
	ProxyEdited()
	if v := claudeEnvOf(t, settingsPath, "HTTPS_PROXY"); v != "http://me:pw@10.0.0.10:9090" {
		t.Fatalf("after the edit HTTPS_PROXY = %q", v)
	}
	s := settings.Load()
	s.Proxies = nil
	settings.Save(s)
	ProxyEdited()
	if v := claudeEnvOf(t, settingsPath, "HTTPS_PROXY"); v != netproxy.DeadProxy {
		t.Fatalf("with the saved proxy gone HTTPS_PROXY = %q, want a proxy that goes nowhere", v)
	}
}
