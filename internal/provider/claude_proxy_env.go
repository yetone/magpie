package provider

// Claude Code on its own subscription asks Anthropic itself, as the account
// it is signed in to, through the proxy its env names. An account's own
// proxy (AccountProxies) was only ever honoured on what the gateway sends,
// so a switch of the sign-in, by magpie (SwitchWhenSpent) or by the user,
// left every account on one route (#1807, kensenzhao). So while the Claude
// subscription has a proxy for any of its accounts, the env of Claude
// Code's settings.json names the proxy of the account it is signed in to:
// that account's own, else the subscription's (ProxyOfLogin), none for
// "direct". Claude Code reads its env as it starts, so it is the sessions
// started after a switch that go the new account's way, as with the
// sign-in itself.
//
// A proxy the user set in that env is theirs: magpie writes none beside
// it. magpie keeps the values it wrote (claude-proxy-env.json), and a key
// counts as its own only while it still holds that value; it takes out
// only those.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/yetone/magpie/internal/settings"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"
	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/netproxy"
)

// claudeProxyAddr are the keys Claude Code takes its proxy from (Node's
// and Bun's, each in both cases); one the user set says the route is
// theirs.
var claudeProxyAddr = []string{"HTTPS_PROXY", "HTTP_PROXY", "https_proxy", "http_proxy"}

// claudeProxyWrites are the keys magpie writes the account's proxy to.
var claudeProxyWrites = []string{"HTTPS_PROXY", "HTTP_PROXY"}

// claudeNoProxy keeps loopback off the proxy: magpie's gateway, and an
// MCP server on this machine, are reached straight.
const claudeNoProxy = "localhost,127.0.0.1,::1"

var claudeProxyEnvMu sync.Mutex

func claudeProxyRecordPath() string {
	return filepath.Join(filepath.Dir(Path()), "claude-proxy-env.json")
}

func claudeSettingsFile() string { return filepath.Join(appdir.ClaudeDir(), "settings.json") }

// readClaudeProxyRecord is what magpie last wrote to Claude Code's env, by
// key. One that can't be read is an error, never "nothing written": that
// would make magpie's own values look like the user's for good.
func readClaudeProxyRecord() (map[string]string, error) {
	m := map[string]string{}
	b, err := os.ReadFile(claudeProxyRecordPath())
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", claudeProxyRecordPath(), err)
	}
	return m, nil
}

func writeClaudeProxyRecord(m map[string]string) error {
	if len(m) == 0 {
		if err := os.Remove(claudeProxyRecordPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(claudeProxyRecordPath(), append(b, '\n'))
}

// claudeAccountProxy is the proxy Claude Code signed in to user should go
// through, as an address its env takes; "" for none of magpie's. Only
// while the subscription has a proxy for one of its accounts: without
// one, every account goes the same way, and the subscription's proxy alone
// was never Claude Code's. A SOCKS proxy is left out: Claude Code takes
// http:// and https:// ones only.
func claudeAccountProxy(user string) string {
	s, ok := storedPicks("claude")
	if !ok || len(s.AccountProxies) == 0 || user == "" {
		return ""
	}
	v := strings.TrimSpace(ProxyOfLogin("claude", user))
	if v == "" || strings.EqualFold(v, "direct") {
		return ""
	}
	u, err := netproxy.Parse(v)
	if err != nil && settings.IsProxyRef(v) {
		// a saved proxy that is gone: sessions fail, they don't leave direct
		return netproxy.DeadProxy
	}
	if err != nil || u.Host == "" {
		return ""
	}
	if netproxy.IsSOCKS(u) {
		log.Printf("claude: %s's proxy %s is SOCKS, which Claude Code can't use; its sessions go the way they did", user, netproxy.Redact(v))
		return ""
	}
	return u.String()
}

// SyncClaudeProxyEnv has the env of Claude Code's settings.json name the
// proxy of the account user, the one it is signed in to (see the top of
// this file), and answers the proxy written, "" when none of magpie's is
// there now.
func SyncClaudeProxyEnv(user string) (string, error) {
	return setClaudeProxyEnv(claudeAccountProxy(user))
}

func setClaudeProxyEnv(want string) (string, error) {
	claudeProxyEnvMu.Lock()
	defer claudeProxyEnvMu.Unlock()
	rec, err := readClaudeProxyRecord()
	if err != nil {
		return "", err
	}
	if want == "" && len(rec) == 0 {
		return "", nil // nothing of magpie's there, nor to be
	}
	path := claudeSettingsFile()
	raw, err := edit.Read(path)
	if err != nil {
		return "", err
	}
	env := map[string]gjson.Result{}
	if len(bytes.TrimSpace(raw)) > 0 {
		stripped := jsonc.ToJSON(raw)
		if !json.Valid(stripped) {
			return "", fmt.Errorf("%s can't be read; its proxy left as it is", path)
		}
		gjson.GetBytes(stripped, "env").ForEach(func(k, v gjson.Result) bool {
			env[k.String()] = v
			return true
		})
	}
	cur := func(k string) (string, bool) {
		v, ok := env[k]
		return v.String(), ok
	}
	ours := func(k string) bool {
		v, ok := cur(k)
		return ok && rec[k] != "" && v == rec[k]
	}
	userRoute := false
	for _, k := range claudeProxyAddr {
		if _, ok := cur(k); ok && !ours(k) {
			userRoute = true
		}
	}
	var del []string
	set := map[string]string{}
	if want == "" || userRoute {
		for k := range rec {
			if ours(k) {
				del = append(del, "env."+k)
			}
		}
		if userRoute && want != "" {
			log.Printf("claude: settings.json's env has a proxy of the user's own; magpie leaves it, and writes no account's proxy")
		}
		want = ""
	} else {
		for _, k := range claudeProxyWrites {
			set[k] = want
		}
		// one the user set, in either case, is theirs
		_, upper := cur("NO_PROXY")
		_, lower := cur("no_proxy")
		if ours("NO_PROXY") || !upper && !lower {
			set["NO_PROXY"] = claudeNoProxy
		}
	}
	// what is about to be written is kept first: a crash between the two
	// then leaves a record of a value that isn't there, never a value of
	// magpie's that looks like the user's
	changed := len(del) > 0
	for k, v := range set {
		if c, ok := cur(k); !ok || c != v {
			changed = true
		}
	}
	if len(set) > 0 {
		if err := writeClaudeProxyRecord(set); err != nil {
			return "", err
		}
	}
	if changed {
		var kvs []edit.KV
		for _, k := range append(claudeProxyWrites, "NO_PROXY") {
			if v, ok := set[k]; ok {
				kvs = append(kvs, edit.KV{Path: "env." + k, Value: v})
			}
		}
		if len(kvs) > 0 {
			if err := edit.SetJSON(path, kvs...); err != nil {
				return "", err
			}
		}
		if len(del) > 0 {
			if err := edit.DelJSON(path, del...); err != nil {
				return "", err
			}
		}
	}
	if len(set) == 0 {
		if err := writeClaudeProxyRecord(nil); err != nil {
			return "", err
		}
	}
	return want, nil
}

// syncClaudeProxyEnvNow is SyncClaudeProxyEnv for the account Claude Code
// is signed in to now, for a change the switch didn't make: the user's own
// sign-in in Claude Code, or a proxy picked in the editor.
func syncClaudeProxyEnvNow() {
	if s, _ := storedPicks("claude"); len(s.AccountProxies) == 0 {
		if rec, err := readClaudeProxyRecord(); err == nil && len(rec) == 0 {
			return // none to write, none written
		}
	}
	user := ""
	for _, l := range Logins("claude") {
		if l.Active {
			user = l.User
		}
	}
	if _, err := SyncClaudeProxyEnv(user); err != nil {
		log.Printf("claude: writing the account's proxy to settings.json: %v", err)
	}
}

// ProxyEdited has Claude Code's env follow a saved proxy that was edited:
// its settings.json holds the address as a snapshot, so an account that
// chose the proxy by name needs it written again.
func ProxyEdited() { syncClaudeProxyEnvNow() }
