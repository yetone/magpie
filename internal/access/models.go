package access

import (
	"errors"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// CleanModels is a key's model list as kept (#882): each pattern trimmed,
// empty and repeated ones dropped. A pattern names a provider and a model,
// "<provider>/<model>", every model of one, "<provider>/*", or a routing
// group, "group/<name>".
func CleanModels(in []string) ([]string, error) {
	var out []string
	for _, m := range in {
		m = strings.TrimSpace(m)
		if m == "" || slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, m) }) {
			continue
		}
		if !strings.Contains(m, "/") || strings.HasPrefix(m, "/") || strings.HasSuffix(m, "/") {
			return nil, errors.New("A key's model is <provider>/<model> or <provider>/*, like openai/gpt-5 or anthropic/*: " + m)
		}
		if len(m) > 300 {
			return nil, errors.New("A key's model is at most 300 characters")
		}
		out = append(out, m)
	}
	if len(out) > 500 {
		return nil, errors.New("A key can list at most 500 models")
	}
	return out, nil
}

// HeldModels is the models a key that lists models and accounts may
// use. A key that names some accounts and no model may use only the
// models of those accounts' providers, "<provider>/*" each (Linka on
// Discord): in the Gateway page's "Models this key may use", ticking an
// account and no model narrows the key to that account, and never leaves
// it every model of every other provider, as it did when the accounts
// held only the providers they name (#905). A key that lists models is
// held to those, its accounts holding the providers they name, as before.
// An account kept out ("<provider>/!<ref>", LeiWang_2000 on X) holds the
// key to no model: a key that only keeps some out may use every model.
func HeldModels(models, accounts []string) []string {
	if len(models) > 0 || !picksAccounts(accounts) {
		return models
	}
	var out []string
	for _, a := range accounts {
		pid, _, ok := strings.Cut(strings.TrimSpace(a), "/")
		if !ok || pid == "" || Excluded(a) {
			continue
		}
		m := pid + "/*"
		if !slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, m) }) {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		// no account entry names a provider: held to nothing rather
		// than let off to every model
		return []string{"/"}
	}
	return out
}

// Restricted says the key may use only the models or accounts it lists.
func (who Identity) Restricted() bool { return len(who.Models) > 0 || len(who.Accounts) > 0 }

// Allows says one of ids is among the key's models; any, for a key that
// lists none. An Identity's models are already HeldModels: a key that
// names accounts and no model may use its accounts' providers' models.
func (who Identity) Allows(ids ...string) bool {
	if len(who.Models) == 0 {
		return true
	}
	for _, id := range ids {
		if id != "" && ModelAllowed(who.Models, id) {
			return true
		}
	}
	return false
}

// ModelAllowed says id ("<provider>/<model>") matches one of patterns,
// letter case aside; a "*" in a pattern stands for any run of characters,
// slashes too. No patterns allow every model.
func ModelAllowed(patterns []string, id string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if strings.EqualFold(p, id) {
			return true
		}
		if strings.Contains(p, "*") && globRe(p).MatchString(id) {
			return true
		}
	}
	return false
}

// globs keeps each pattern's expression compiled, as a model list asks
// for every model of it (#1762): a pattern is its expression whatever key
// holds it, so none is ever stale. It is let go of whole past maxGlobs.
var globs struct {
	sync.Mutex
	m map[string]*regexp.Regexp
}

const maxGlobs = 4096

func globRe(p string) *regexp.Regexp {
	globs.Lock()
	re, ok := globs.m[p]
	globs.Unlock()
	if ok {
		return re
	}
	re = compileGlob(p)
	globs.Lock()
	if globs.m == nil || len(globs.m) >= maxGlobs {
		globs.m = map[string]*regexp.Regexp{}
	}
	globs.m[p] = re
	globs.Unlock()
	return re
}

func compileGlob(p string) *regexp.Regexp {
	parts := strings.Split(p, "*")
	for i, s := range parts {
		parts[i] = regexp.QuoteMeta(s)
	}
	return regexp.MustCompile("(?is)^" + strings.Join(parts, ".*") + "$")
}
