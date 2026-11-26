package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
)

// Antigravity's accounts, onto Antigravity (local agy CLI). The withdrawn
// Antigravity plugin (and the built-in before it) signed in with
// Antigravity's own Google OAuth client, the one agy's own sign-in uses
// (1071006060591-…), so agy takes those sign-ins as its own: agy-cli's
// auth.import gives each an agy home of its own and keeps it once agy runs
// with it there. The token goes nowhere it didn't go: agy sends it to
// Google, as Antigravity did (@tongzhenbao on X).
const (
	agyCLIID      = "agy-cli"
	agyCLIPackage = "@magpie-community/opencode-agy-cli-auth"
	// 0.1.145: a Claude Code conversation keeps its agy session when a
	// Bash cd moves its working folder or ToolSearch loads a tool mid-turn,
	// instead of replaying it into a new one (_paper_hands on X), and
	// AGY_BRIDGE_STRICT_REPLAY refuses a replay that would leave history
	// out (NullBurn, plugins#1495); 0.1.143: a Claude Code model switch,
	// which rewrites the system prompt's model line, keeps the agy session
	// instead of replaying the
	// conversation into a new one (chet030714 on X); 0.1.142: a tool loop
	// cut by a plugin host restart resumes its agy session with only the
	// step's results (chet030714, xuanbo1889245 on X);
	// 0.1.140: usage is read through agy alone (`agy -p /usage`), never
	// with agy's stored token against Google's quota endpoints, and a
	// slow read says agy is still reading instead of "timed out"
	// (9uK7K2AqpF10987, Jayc1world on X); 0.1.139: a replay over agy's
	// 128 KiB prompt cap goes in whole over
	// several prompts instead of being cut to the newest turns
	// (yayoi_no_yume on X); 0.1.133: each account moved gets an agy home
	// of its own, and a request of one never goes on in another's agy, so
	// one account's 429 no longer rests the other (Archivemylife on X);
	// 0.1.68: auth.import takes the old plugin's sign-ins
	agyCLIMin = "0.1.145"
)

// agyMoving are the Antigravity accounts agy-cli doesn't have yet, as the
// withdrawn plugin keeps them: its own sign-ins, then the built-in's that
// never moved onto it. One agy-cli has (by email) is left out.
func agyMoving() []map[string]any {
	if settings.AccountDiscoveryOff() {
		return nil
	}
	have := map[string]bool{}
	for _, a := range plugin.Auths(agyCLIID) {
		if who := agyWho(a); who != "" {
			have[who] = true
		}
	}
	seen := map[string]bool{}
	var out []map[string]any
	add := func(a map[string]any) {
		rt, _, _ := strings.Cut(str(a["refresh"]), "|")
		who := agyWho(a)
		if a["type"] != "oauth" || rt == "" || seen[rt] || (who != "" && (have[who] || seen[who])) {
			return
		}
		seen[rt] = true
		if who != "" {
			seen[who] = true
		}
		out = append(out, a)
	}
	auths := plugin.Auths("antigravity")
	// the provider's first account first, then the rest by key
	keys := make([]string, 0, len(auths))
	for k := range auths {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keyBefore(keys[i], keys[j]) })
	for _, k := range keys {
		add(auths[k])
	}
	if mv := movers["antigravity"]; mv != nil && !Moved("antigravity") {
		if accts, err := mv.out(); err == nil {
			for _, m := range accts {
				add(m.Auth)
			}
		}
	}
	return out
}

// keyBefore puts a provider's bare key (its first account) before its
// "#…" ones, the rest in order.
func keyBefore(a, b string) bool {
	ah, bh := strings.Contains(a, "#"), strings.Contains(b, "#")
	if ah != bh {
		return !ah
	}
	return a < b
}

func agyWho(a map[string]any) string {
	who := str(a["accountId"])
	if who == "" {
		if md, ok := a["metadata"].(map[string]any); ok {
			who = str(md["email"])
		}
	}
	if who == "" {
		who = str(a["email"])
	}
	return strings.ToLower(strings.TrimSpace(who))
}

// AgyMoveUsers are the accounts MoveToAgy would bring in, by email ("" for
// one that doesn't say), for the Providers page's line.
func AgyMoveUsers() []string {
	var out []string
	for _, a := range agyMoving() {
		out = append(out, agyWho(a))
	}
	return out
}

// AgyHasEvery reports whether Antigravity (local agy CLI) is signed in
// and has every account of the withdrawn Antigravity plugin, by email:
// then nothing is left to move, and the old plugin only sends requests
// agy-cli could. An old account with no email can't be matched, so it
// counts as not there. Account discovery being off changes nothing here:
// these are the user's own sign-ins.
func AgyHasEvery() bool {
	have := map[string]bool{}
	for _, a := range plugin.Auths(agyCLIID) {
		if who := agyWho(a); who != "" {
			have[who] = true
		}
	}
	if len(have) == 0 {
		return false
	}
	for _, a := range plugin.Auths("antigravity") {
		if a["type"] == "oauth" && !have[agyWho(a)] {
			return false
		}
	}
	return true
}

// MoveToAgy brings Antigravity's accounts into agy-cli: the plugin
// installed (or brought up to the version that takes them) and agy with
// it, then each account checked with agy and kept as agy-cli's. The old
// sign-ins are left as they are; one agy refuses is said, not moved.
func MoveToAgy(ctx context.Context) ([]ImportedAccount, error) {
	accts := agyMoving()
	if len(accts) == 0 {
		return nil, errors.New("no Antigravity account is left to move")
	}
	if err := installPlugin(ctx, agyCLIPackage, agyCLIMin); err != nil {
		return nil, installFailed(agyCLIPackage, err)
	}
	pps, err := pluginProviders(ctx)
	if err != nil {
		return nil, err
	}
	found := false
	for _, p := range pps {
		if p.ID == agyCLIID {
			found = p.Importable
		}
	}
	if !found {
		return nil, fmt.Errorf("%s takes no Antigravity accounts", agyCLIPackage)
	}
	forgetAccountCaches()
	// agy is downloaded from Google and checks each account with Google,
	// so it goes the way Antigravity's requests went: where Google is
	// reached only through Antigravity's own proxy (magpie in a container
	// in China), going direct failed the install every time (huiqiang6 on
	// X). agy-cli keeps that proxy for its requests after the move too.
	if err := carryAgyProxy(accts); err != nil {
		return nil, err
	}
	ctx = Via(ctx, PluginID(agyCLIID))
	if agyCLIMissing(agyCLIID) != "" {
		if err := installAgy(ctx, agyCLIID); err != nil {
			return nil, err
		}
	}
	text, err := json.Marshal(accts)
	if err != nil {
		return nil, err
	}
	return importPlugin(ctx, PluginID(agyCLIID), []string{string(text)})
}

// carryAgyProxy gives agy-cli the proxy Antigravity's requests went
// through: Antigravity's own, when agy-cli has no account yet and no proxy
// of its own; and each moving account's, where agy-cli would take another
// for it and has none of the account's own. A proxy the user chose in
// agy-cli is kept.
func carryAgyProxy(accts []map[string]any) error {
	from, ok := agyProxyFrom()
	if !ok {
		return nil
	}
	id := PluginID(agyCLIID)
	f, err := read()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(f.Providers, func(p Provider) bool { return p.ID == id })
	if i < 0 {
		f.Providers = append(f.Providers, Provider{ID: id})
		i = len(f.Providers) - 1
	}
	p := &f.Providers[i]
	changed := false
	if p.Proxy == "" && from.Proxy != "" && len(plugin.Auths(agyCLIID)) == 0 {
		p.Proxy, changed = from.Proxy, true
	}
	own := maps.Clone(p.AccountProxies)
	if own == nil {
		own = map[string]string{}
	}
	for _, a := range accts {
		k := accountKey(agyWho(a))
		want := from.AccountProxies[k]
		if want == "" {
			want = from.Proxy
		}
		if k != "" && want != "" && want != p.Proxy && own[k] == "" {
			own[k], changed = want, true
		}
	}
	if !changed {
		return nil
	}
	p.AccountProxies = normalAccountProxies(own)
	return store(f)
}

// agyProxyFrom is where Antigravity's proxy is kept: under the id its
// accounts were served by (the plugin's own, "antigravity-plugin", while
// the built-in was beside it), the first of them that has one.
func agyProxyFrom() (Provider, bool) {
	for _, id := range []string{PluginID("antigravity"), "antigravity", "antigravity-plugin"} {
		if p, ok := storedPicks(id); ok && (p.Proxy != "" || len(p.AccountProxies) > 0) {
			return p, true
		}
	}
	return Provider{}, false
}

// agyCLIMissing, installAgy and importPlugin are PluginCLIMissing,
// InstallPluginCLI and ImportPluginFiles, for tests to stand in for.
var (
	agyCLIMissing = PluginCLIMissing
	installAgy    = InstallPluginCLI
	importPlugin  = ImportPluginFiles
)
