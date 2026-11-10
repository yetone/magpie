package access

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode"
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
		if len(m) > 300 {
			return nil, errors.New("A key's model is at most 300 characters")
		}
		if IsPattern(m) {
			// refused here, never read as "every model" or as none later
			if _, err := compilePattern(m); err != nil {
				return nil, fmt.Errorf("A key's model pattern %s is not a valid regular expression: %v", m, err)
			}
			out = append(out, m)
			continue
		}
		if !strings.Contains(m, "/") || strings.HasPrefix(m, "/") || strings.HasSuffix(m, "/") {
			return nil, errors.New("A key's model is <provider>/<model>, <provider>/*, or a pattern re:<regular expression>, like openai/gpt-5, anthropic/* or re:^gpt-5: " + m)
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
// slashes too, and "re:<expression>" is a regular expression
// (PatternPrefix). No patterns allow every model.
func ModelAllowed(patterns []string, id string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if IsPattern(p) {
			if patternMatches(p, id) {
				return true
			}
			continue
		}
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

// PatternPrefix starts a model entry that is a regular expression
// (tsingchao on X): "re:^gpt-5" holds a key to every model whose id has
// that match. Go's RE2 syntax, letter case aside, unanchored (write ^ and $
// to anchor), tried on the whole id "<provider>/<model>" and on the model
// alone, so "re:^gpt-" names GPT models of any provider. It never names a
// routing group ("group/<name>" does that), so a pattern like "re:.*"
// doesn't hand a key every group of the user's own.
const PatternPrefix = "re:"

// IsPattern says the entry is a regular expression (PatternPrefix).
func IsPattern(p string) bool {
	return len(p) >= len(PatternPrefix) && strings.EqualFold(p[:len(PatternPrefix)], PatternPrefix)
}

func compilePattern(p string) (*regexp.Regexp, error) {
	expr := strings.TrimSpace(p[len(PatternPrefix):])
	if expr == "" {
		return nil, errors.New("it is empty")
	}
	return regexp.Compile("(?i)" + expr)
}

var patterns struct {
	sync.Mutex
	m map[string]*regexp.Regexp // nil for one that doesn't compile
}

// patternMatches says id is one the regular expression entry p names. An
// entry that doesn't compile (CleanModels refuses it, so only a file
// edited by hand has one) names no model: it never lets a key off to all.
func patternMatches(p, id string) bool {
	if len(id) >= 6 && strings.EqualFold(id[:6], "group/") {
		return false
	}
	patterns.Lock()
	re, ok := patterns.m[p]
	patterns.Unlock()
	if !ok {
		re, _ = compilePattern(p)
		patterns.Lock()
		if patterns.m == nil || len(patterns.m) >= maxGlobs {
			patterns.m = map[string]*regexp.Regexp{}
		}
		patterns.m[p] = re
		patterns.Unlock()
	}
	if re == nil {
		return false
	}
	if re.MatchString(id) {
		return true
	}
	if _, model, ok := strings.Cut(id, "/"); ok {
		return re.MatchString(model)
	}
	return false
}

// MaxAliases is the most model names a key may give.
const MaxAliases = 200

// CleanAliases is a key's model names as kept: each name a key's client
// asks by, mapped to the model it is, "<provider>/<model>" or a routing
// group "group/<name>" (tsingchao on X). A name is one word of at most 120
// characters that is no pattern.
func CleanAliases(in map[string]string) (map[string]string, error) {
	if len(in) > MaxAliases {
		return nil, fmt.Errorf("A key can name at most %d models", MaxAliases)
	}
	out := map[string]string{}
	for name, target := range in {
		name, target = strings.TrimSpace(name), strings.TrimSpace(target)
		if name == "" && target == "" {
			continue
		}
		if name == "" || target == "" {
			return nil, errors.New("A model name needs both the name and the model it stands for")
		}
		if len(name) > 120 || strings.ContainsFunc(name, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || r == '*' }) || IsPattern(name) {
			return nil, errors.New("A model name is one word of at most 120 characters, with no * or re: pattern: " + name)
		}
		if !strings.Contains(target, "/") || strings.HasPrefix(target, "/") || strings.HasSuffix(target, "/") || strings.Contains(target, "*") || IsPattern(target) || len(target) > 300 {
			return nil, errors.New("A model name stands for one model, <provider>/<model> like openai/gpt-5: " + target)
		}
		if strings.EqualFold(name, target) {
			return nil, errors.New("A model name must differ from the model: " + name)
		}
		if slices.ContainsFunc(slices.Collect(maps.Keys(out)), func(o string) bool { return strings.EqualFold(o, name) }) {
			return nil, errors.New("A model name is given once: " + name)
		}
		out[name] = target
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// AliasFor is the model a name the key gives stands for, letter case aside.
func (who Identity) AliasFor(name string) (string, bool) {
	for a, t := range who.Aliases {
		if strings.EqualFold(a, name) {
			return t, true
		}
	}
	return "", false
}

// AliasOf is the name the key gives the model id, if it does (case aside).
func (who Identity) AliasOf(id string) (string, bool) {
	for a, t := range who.Aliases {
		if strings.EqualFold(t, id) {
			return a, true
		}
	}
	return "", false
}
