package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	// 0.1.68: auth.import takes the old plugin's sign-ins
	agyCLIMin = "0.1.68"
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
	if PluginCLIMissing(agyCLIID) != "" {
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

// installAgy and importPlugin are InstallPluginCLI and ImportPluginFiles,
// for tests to stand in for.
var (
	installAgy   = InstallPluginCLI
	importPlugin = ImportPluginFiles
)
