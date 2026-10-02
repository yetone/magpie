package provider

// PLUGIN-SERVED (see AGENTS.md): Factory ("factory") is a deprecated
// built-in subscription served by its plugin,
// @magpie-community/opencode-factory-auth, once moved onto it
// (provider.Moved; the default for a new sign-in). A moved one's sign-ins,
// models, requests and usage are all the plugin's, never this code's (only
// the move, in migrate*.go, still reads its accounts). A fix here alone
// doesn't reach those users; fix the plugin
// (github.com/magpie-community/plugins, packages/factory) and raise the
// mover's min in internal/provider/migrate_factory.go.

// A Factory account added by its API key (fk-…), as droid takes one from
// FACTORY_API_KEY (#506): droid's om() hands the key on as the token, sent as
// "Authorization: Bearer fk-…" with the headers every request carries, and
// never renewed. droid's active org (st(), X-Factory-Org-Id) is what a
// sign-in stored, so a key alone sends none; whoami, asked with the key,
// says whose it is and where Factory serves its org, as droid's yP checks a
// key before it is used ("invalid-api-key" when whoami refuses it).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// factoryKeyPrefix is droid's Uh: what a Factory API key starts with.
const factoryKeyPrefix = "fk-"

var factoryKeyRe = regexp.MustCompile(`\bfk-[A-Za-z0-9_\-]{8,}`)

// factoryKeysIn finds the API keys in pasted text: one a line, in a .env
// line (FACTORY_API_KEY=fk-…) or in JSON, each once.
func factoryKeysIn(texts []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range texts {
		for _, k := range factoryKeyRe.FindAllString(s, -1) {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	return out
}

// ImportFactoryKeys adds Factory accounts by their API keys, found in the
// pasted texts, each checked with whoami first, and says what became of
// each.
func ImportFactoryKeys(ctx context.Context, texts []string) ([]ImportedAccount, error) {
	keys := factoryKeysIn(texts)
	if len(keys) == 0 {
		return nil, errors.New("no Factory API keys (fk-…) in it")
	}
	if len(keys) > maxGoogleImport {
		return nil, fmt.Errorf("%d keys; at most %d at a time", len(keys), maxGoogleImport)
	}
	out := make([]ImportedAccount, len(keys))
	for i, k := range keys {
		out[i] = factoryAddKey(ctx, k)
	}
	forgetAccountCaches()
	return out, nil
}

// factoryKeyName is a key as it is shown before whoami names its account.
func factoryKeyName(k string) string {
	if len(k) <= 10 {
		return k
	}
	return k[:7] + "…" + k[len(k)-4:]
}

func factoryAddKey(ctx context.Context, key string) ImportedAccount {
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	c := factoryCreds{Access: key, Key: true}
	who, err := factoryWhoami(wctx, c)
	if err != nil {
		return ImportedAccount{User: factoryKeyName(key), Status: "failed", Error: "Factory didn't take this key: " + err.Error()}
	}
	c.Region, c.Prem, c.Email, c.UserID = who.Region, who.Prem, who.Email, who.UserID
	user := firstNonEmpty(who.Email, who.UserID)
	if user == "" {
		return ImportedAccount{User: factoryKeyName(key), Status: "failed", Error: "Factory took the key but didn't say whose account it is"}
	}
	status := "added"
	if l, ok := factoryLookup(user); ok {
		status = "updated"
		if was, ok := factorySaved(l); ok && was.Key && was.Access == key {
			status = "exists"
		}
	}
	auth, err := json.Marshal(c)
	if err != nil {
		return ImportedAccount{User: user, Status: "failed", Error: err.Error()}
	}
	if err := addSideLogin(savedLogin{Agent: "factory", User: user, Auth: auth}, "", func(savedLogin) {}); err != nil {
		return ImportedAccount{User: user, Status: "failed", Error: err.Error()}
	}
	_ = editSideLogin("factory", user, func(ls []savedLogin, i int) ([]savedLogin, error) {
		ls[i].Lapsed = ""
		return ls, nil
	})
	return ImportedAccount{User: user, Status: status}
}
