package redact

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// Rule is one the user added (Settings → Privacy) for a secret magpie's own
// rules don't know, a gateway's key say (#195): what its placeholders are
// called, and the prefix its values start with or a pattern for them. Its
// values are masked with the secrets, as they are: the same placeholder
// for the same value, put back in what the vendor answers.
type Rule struct {
	Kind   string `json:"kind"`
	Prefix string `json:"prefix,omitempty"`
	// Regex is an RE2 pattern; the value is its group 1 when it has one,
	// else all of what it matches, as with magpie's own rules.
	Regex string `json:"regex,omitempty"`
}

// How many rules, and how long each part of one, a user may have.
const (
	MaxRules     = 32
	maxKind      = 24
	maxPrefix    = 64
	minPrefix    = 3
	maxRegex     = 300
	prefixTail   = `[A-Za-z0-9_\-]{8,}`
	customKind   = "CUSTOM"
	minCustomLen = 4
)

// RuleKind is kind as a placeholder can have it: upper case letters, digits
// and _, starting with a letter, at most 24 long; "" is CUSTOM.
func RuleKind(kind string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(kind)) {
		switch {
		case r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '.':
			b.WriteByte('_')
		}
	}
	k := strings.Trim(b.String(), "_")
	if k == "" {
		return customKind
	}
	if k[0] < 'A' || k[0] > 'Z' {
		k = customKind + "_" + k
	}
	if len(k) > maxKind {
		k = strings.TrimRight(k[:maxKind], "_")
	}
	return k
}

// CheckRules tidies the user's rules (the kind as a placeholder has it,
// spaces trimmed, the empty and the repeated dropped) and says what is
// wrong with one that can't be used: a pattern that doesn't compile, or
// matches nothing at all, a prefix too short, too many or too long.
func CheckRules(in []Rule) ([]Rule, error) {
	var out []Rule
	for _, r := range in {
		r.Prefix, r.Regex = strings.TrimSpace(r.Prefix), strings.TrimSpace(r.Regex)
		if r.Prefix == "" && r.Regex == "" {
			continue
		}
		r.Kind = RuleKind(r.Kind)
		if r.Prefix != "" && r.Regex != "" {
			return nil, fmt.Errorf("a masking rule has a prefix or a regular expression, not both: %s", r.Kind)
		}
		if r.Prefix != "" && (len(r.Prefix) < minPrefix || len(r.Prefix) > maxPrefix) {
			return nil, fmt.Errorf("a masking rule's prefix is %d to %d characters long, not %q", minPrefix, maxPrefix, r.Prefix)
		}
		if len(r.Regex) > maxRegex {
			return nil, fmt.Errorf("a masking rule's regular expression is at most %d characters long", maxRegex)
		}
		re, err := r.compile()
		if err != nil {
			return nil, fmt.Errorf("the masking rule %s has a regular expression that doesn't work: %v", r.Kind, err)
		}
		if re.MatchString("") {
			return nil, fmt.Errorf("the masking rule %s matches nothing at all, so it would mask everywhere: %s", r.Kind, r.Regex)
		}
		dup := false
		for _, o := range out {
			dup = dup || o == r
		}
		if !dup {
			out = append(out, r)
		}
	}
	if len(out) > MaxRules {
		return nil, fmt.Errorf("at most %d masking rules, not %d", MaxRules, len(out))
	}
	return out, nil
}

func (r Rule) compile() (*regexp.Regexp, error) {
	if r.Prefix != "" {
		return regexp.Compile(regexp.QuoteMeta(r.Prefix) + prefixTail)
	}
	return regexp.Compile(r.Regex)
}

// compiled holds each rule's pattern once it is made, nil for one that
// doesn't compile (a settings file written by hand): Mask runs on every
// string of every request.
var compiled sync.Map // Rule → *regexp.Regexp

// customRules are the user's rules as Mask runs them, the ones that work.
func customRules(in []Rule) []rule {
	var out []rule
	for _, r := range in {
		if strings.TrimSpace(r.Prefix) == "" && strings.TrimSpace(r.Regex) == "" || len(r.Regex) > maxRegex {
			continue
		}
		v, ok := compiled.Load(r)
		if !ok {
			re, err := r.compile()
			if err != nil || re.MatchString("") {
				re = nil
			}
			v, _ = compiled.LoadOrStore(r, re)
		}
		re := v.(*regexp.Regexp)
		if re == nil {
			continue
		}
		c := rule{kind: RuleKind(r.Kind), re: re, ok: func(v string) bool { return len(v) >= minCustomLen }}
		if r.Prefix != "" {
			c.markers = []string{r.Prefix}
		}
		out = append(out, c)
	}
	return out
}
