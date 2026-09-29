// Package redact keeps secrets on the machine. What the gateway sends a
// vendor has its secrets (API keys, private keys, tokens, passwords in
// connection strings and assignments), and when asked personal data and the
// user's own words, swapped for placeholders like {{API_KEY_k3v9x2mq}}; what
// the vendor says back has them swapped back, streams included, so the agent
// and the user see the real thing while the vendor never does.
//
// A placeholder is the same for the same value every time (a keyed hash of
// it), so a conversation's history reads the same turn after turn — what a
// vendor cached of it stays good, and a thinking block's signature still
// matches the text it was made for — and one placeholder is never two
// values. The values are held in memory only, by placeholder, as requests
// bring them; a restart loses nothing, since the next request brings them
// again.
package redact

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
)

// Options says what to mask.
type Options struct {
	Secrets  bool     // API keys, private keys, tokens, passwords
	Personal bool     // emails, phone numbers, ID and bank card numbers
	Words    []string // the user's own: names, codenames, hosts
	Rules    []Rule   // the user's own rules, masked with the secrets
}

// rule finds one kind of value. The match is group 1 when the pattern has
// one, else all of it; bound are the characters that may not touch it on
// either side (a key inside a longer word is no key); ok checks it further.
type rule struct {
	kind     string
	re       *regexp.Regexp
	markers  []string // one of these is in any text it can match
	bound    string
	ok       func(string) bool
	personal bool
}

const (
	alnum   = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	tokenCh = alnum + "_-"
	digits  = "0123456789"
)

var rules = []rule{
	{kind: "PRIVATE_KEY", re: regexp.MustCompile(`-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY(?: BLOCK)?-----[\s\S]+?-----END (?:[A-Z0-9]+ )*PRIVATE KEY(?: BLOCK)?-----`), markers: []string{"PRIVATE KEY"}},
	{kind: "API_KEY", re: regexp.MustCompile(`sk-(?:ant-|proj-|or-|svcacct-|admin-)?[A-Za-z0-9_-]{20,}`), markers: []string{"sk-"}, bound: tokenCh},
	// a gateway's or a relay's own: oc_sk_…, or_sk_… (#195)
	{kind: "API_KEY", re: regexp.MustCompile(`[a-z]{2,4}_sk_[A-Za-z0-9_-]{20,}`), markers: []string{"_sk_"}, bound: tokenCh, ok: secretValue},
	{kind: "API_KEY", re: regexp.MustCompile(`(?:gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{40,}|glpat-[A-Za-z0-9_-]{20,})`), markers: []string{"gh", "github_pat_", "glpat-"}, bound: tokenCh},
	{kind: "API_KEY", re: regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`), markers: []string{"AIza"}, bound: tokenCh},
	{kind: "API_KEY", re: regexp.MustCompile(`xox[abposr]-[0-9A-Za-z-]{10,}`), markers: []string{"xox"}, bound: tokenCh},
	{kind: "API_KEY", re: regexp.MustCompile(`(?:sk|rk)_live_[0-9A-Za-z]{20,}`), markers: []string{"_live_"}, bound: tokenCh},
	{kind: "API_KEY", re: regexp.MustCompile(`(?:AKIA|ASIA)[0-9A-Z]{16}`), markers: []string{"AKIA", "ASIA"}, bound: alnum},
	{kind: "API_KEY", re: regexp.MustCompile(`(?:hf_[A-Za-z0-9]{30,}|gsk_[A-Za-z0-9]{40,}|xai-[A-Za-z0-9]{40,}|npm_[A-Za-z0-9]{36}|pypi-[A-Za-z0-9_-]{50,}|dop_v1_[a-f0-9]{64}|SG\.[A-Za-z0-9_-]{22}\.[A-Za-z0-9_-]{43})`),
		markers: []string{"hf_", "gsk_", "xai-", "npm_", "pypi-", "dop_v1_", "SG."}, bound: tokenCh},
	{kind: "TOKEN", re: regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), markers: []string{"eyJ"}, bound: tokenCh},
	// the password of user:password@host
	{kind: "PASSWORD", re: regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s:@/?#"'<>]+:([^\s@/?#"'<>]{3,})@`), markers: []string{"://"}, ok: notAVariable},
	// password = …, API_KEY: "…", as .env files and configs have them
	{kind: "SECRET", re: regexp.MustCompile(`(?i)[A-Za-z0-9_.-]*(?:` + secretNames.pattern() + `)[A-Za-z0-9_.-]*` + quote + `[ \t]*[:=][ \t]*` + quote + `(` + secretCh + `{8,})`),
		markers: secretNames.markers(), ok: secretValue},
	// a field called key and nothing more, as providers.json has one: "key":
	// "oc_sk_…" (#195). Only a long value of letters and digits both, since
	// code calls anything a key: a map's "key": "value", sort_key, keyboard.
	{kind: "SECRET", re: regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_.-])` + quote + `key` + quote + `[ \t]*[:=][ \t]*` + quote + `(` + secretCh + `{16,})`),
		markers: fieldNames{{"key", "key"}}.markers(), ok: secretValue},

	{kind: "EMAIL", re: regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`), markers: []string{"@"}, bound: alnum + "._%+-", ok: realEmail, personal: true},
	{kind: "ID_CARD", re: regexp.MustCompile(`[1-9]\d{5}(?:18|19|20)\d{2}(?:0[1-9]|1[0-2])(?:0[1-9]|[12]\d|3[01])\d{3}[\dXx]`), bound: alnum, ok: chineseID, personal: true},
	{kind: "PHONE", re: regexp.MustCompile(`(?:\+?86[- ]?)?1[3-9]\d{9}`), bound: digits, personal: true},
	{kind: "BANK_CARD", re: regexp.MustCompile(`[3-6]\d{3}(?:[ -]?\d{4}){2,3}(?:[ -]?\d{1,3})?`), bound: digits, ok: luhn, personal: true},
}

// fieldNames are what a field that holds a secret is called: a pattern for
// each name, and text that any match of it has in it, so a rule's markers
// and the names its pattern takes come from one list and can't drift apart.
type fieldNames []struct{ re, marker string }

// secretNames are what a field holding a secret has in its name:
// DB_PASSWORD, apiKey, x-api-key, client_secret.
var secretNames = fieldNames{
	{"password", "pass"}, {"passwd", "pass"},
	{"secret", "secret"},
	{"token", "token"},
	{`api[_-]?key`, "key"}, {`access[_-]?key`, "key"}, {`private[_-]?key`, "key"},
	{"credential", "credential"},
}

func (f fieldNames) pattern() string {
	res := make([]string, len(f))
	for i, n := range f {
		res[i] = n.re
	}
	return strings.Join(res, "|")
}

// markers are each name's marker as a name is written in lower case, in
// upper case and capitalised: password, PASSWORD, Password, apiKey.
func (f fieldNames) markers() []string {
	var out []string
	for _, n := range f {
		for _, m := range []string{n.marker, strings.ToUpper(n.marker), strings.ToUpper(n.marker[:1]) + n.marker[1:]} {
			if !slices.Contains(out, m) {
				out = append(out, m)
			}
		}
	}
	return out
}

const (
	// quote is a field's or a value's quote, if it has one: " or ', or \"
	// in JSON written inside a string
	quote = `(?:\\?["'])?`
	// secretCh are the characters of a secret's value
	secretCh = `[A-Za-z0-9_\-./+=~!@#%^&*]`
)

// placeholderRe is a placeholder as Mask writes it.
var placeholderRe = regexp.MustCompile(`\{\{[A-Z][A-Z0-9_]*_[a-z2-7]{8}\}\}`)

// notAVariable turns away what stands in for a value: ${PASS}, <password>, ****.
func notAVariable(v string) bool {
	if strings.ContainsAny(v[:1], "$%{<[*") {
		return false
	}
	return strings.Trim(v, "*xX.") != ""
}

// secretValue is a value that looks like a secret, not code: letters and
// digits both, and not a call, a reference or a placeholder of its own.
func secretValue(v string) bool {
	if !notAVariable(v) || strings.HasPrefix(v, "process.env") || strings.HasPrefix(v, "os.") {
		return false
	}
	return strings.ContainsAny(v, digits) && strings.IndexFunc(v, func(r rune) bool { return r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' }) >= 0
}

func realEmail(v string) bool {
	host := strings.ToLower(v[strings.LastIndexByte(v, '@')+1:])
	for _, fake := range []string{"example.com", "example.org", "example.net", "localhost", ".test", ".invalid", ".example"} {
		if host == strings.TrimPrefix(fake, ".") || strings.HasSuffix(host, fake) {
			return false
		}
	}
	return !strings.HasPrefix(host, "noreply") && !strings.Contains(v, "noreply@")
}

func chineseID(v string) bool {
	w := []int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
	sum := 0
	for i := 0; i < 17; i++ {
		sum += int(v[i]-'0') * w[i]
	}
	return strings.ToUpper(v[17:]) == string("10X98765432"[sum%11])
}

func luhn(v string) bool {
	var ds []int
	for _, r := range v {
		if r >= '0' && r <= '9' {
			ds = append(ds, int(r-'0'))
		}
	}
	if len(ds) < 15 || len(ds) > 19 {
		return false
	}
	sum := 0
	for i := range ds {
		d := ds[len(ds)-1-i]
		if i%2 == 1 {
			if d *= 2; d > 9 {
				d -= 9
			}
		}
		sum += d
	}
	return sum%10 == 0
}

// ---- placeholders ---------------------------------------------------------------

var (
	mu      sync.RWMutex
	values  = map[string]string{} // placeholder → value
	keyOf   []byte
	keyPath string // guarded by mu, as keyOf is
)

// maxValues bounds what is held; past it the oldest half is let go, and a
// request that still has them brings them back.
const maxValues = 200_000

func key() []byte {
	mu.RLock()
	k := keyOf
	mu.RUnlock()
	if k != nil {
		return k
	}
	mu.Lock()
	defer mu.Unlock()
	if keyOf != nil {
		return keyOf
	}
	keyOf = loadKey()
	return keyOf
}

// SetKeyPath chooses where the placeholder key is kept across restarts;
// "" keeps it in memory only. The gateway's owner sets it before requests
// arrive. Once the key is loaded, both it and its path stay fixed.
func SetKeyPath(path string) {
	mu.Lock()
	defer mu.Unlock()
	if keyOf == nil {
		keyPath = path
	}
}

// loadKey is called with mu held.
func loadKey() []byte {
	if keyPath != "" {
		if b, err := os.ReadFile(keyPath); err == nil && len(b) >= 32 {
			return b[:32]
		}
	}
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	if keyPath != "" {
		_ = os.MkdirAll(filepath.Dir(keyPath), 0o700)
		_ = os.WriteFile(keyPath, b, 0o600)
	}
	return b
}

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

func placeholder(kind, v string) string {
	h := hmac.New(sha256.New, key())
	h.Write([]byte(kind + "\x00" + v))
	p := "{{" + kind + "_" + strings.ToLower(b32.EncodeToString(h.Sum(nil))[:8]) + "}}"
	mu.Lock()
	if _, ok := values[p]; !ok {
		if len(values) >= maxValues {
			n := 0
			for k := range values {
				delete(values, k)
				if n++; n >= maxValues/2 {
					break
				}
			}
		}
		values[p] = v
	}
	mu.Unlock()
	return p
}

// Known says there are values to put back.
func Known() bool {
	mu.RLock()
	defer mu.RUnlock()
	return len(values) > 0
}

// ---- masking --------------------------------------------------------------------

type span struct {
	start, end int
	kind       string
}

// Mask swaps what o covers in s for placeholders, and says how many.
func Mask(s string, o Options) (string, int) {
	if len(s) < 3 {
		return s, 0
	}
	var found []span
	for _, w := range o.Words {
		if w = strings.TrimSpace(w); len(w) < 2 {
			continue
		}
		for i := 0; ; {
			j := strings.Index(s[i:], w)
			if j < 0 {
				break
			}
			found = append(found, span{i + j, i + j + len(w), "TERM"})
			i += j + len(w)
		}
	}
	all := rules
	if o.Secrets && len(o.Rules) > 0 {
		// the user's first: where one of theirs and one of magpie's find
		// the same value, it goes by the name they gave it
		all = append(customRules(o.Rules), rules...)
	}
	for _, r := range all {
		if r.personal && !o.Personal || !r.personal && !o.Secrets {
			continue
		}
		if len(r.markers) > 0 && !containsAny(s, r.markers) {
			continue
		}
		for _, m := range r.re.FindAllStringSubmatchIndex(s, -1) {
			a, b := m[0], m[1]
			if len(m) >= 4 && m[2] >= 0 {
				a, b = m[2], m[3]
			}
			if b <= a {
				continue
			}
			if r.bound != "" && (a > 0 && strings.IndexByte(r.bound, s[a-1]) >= 0 || b < len(s) && strings.IndexByte(r.bound, s[b]) >= 0) {
				continue
			}
			if r.ok != nil && !r.ok(s[a:b]) {
				continue
			}
			found = append(found, span{a, b, r.kind})
		}
	}
	if len(found) == 0 {
		return s, 0
	}
	// nothing inside a placeholder already there
	if strings.Contains(s, "{{") {
		for _, p := range placeholderRe.FindAllStringIndex(s, -1) {
			found = slices.DeleteFunc(found, func(f span) bool { return f.start < p[1] && f.end > p[0] })
		}
		if len(found) == 0 {
			return s, 0
		}
	}
	// the first to start, and of those the longest, wins where they overlap;
	// of two the same, the rule found first, so a value's placeholder is the
	// same every time (an API key in API_KEY=… is an API_KEY, not a SECRET)
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].start != found[j].start {
			return found[i].start < found[j].start
		}
		return found[i].end > found[j].end
	})
	var b strings.Builder
	last, n := 0, 0
	for _, f := range found {
		if f.start < last {
			continue
		}
		b.WriteString(s[last:f.start])
		b.WriteString(placeholder(f.kind, s[f.start:f.end]))
		last, n = f.end, n+1
	}
	b.WriteString(s[last:])
	return b.String(), n
}

func containsAny(s string, subs []string) bool {
	for _, x := range subs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}

// ---- restoring ------------------------------------------------------------------

// Restore puts the values back in s. escaped is for text that is itself
// JSON (a tool call's arguments), where a value goes in escaped.
func Restore(s string, escaped bool) string {
	if !strings.Contains(s, "{{") {
		return s
	}
	mu.RLock()
	defer mu.RUnlock()
	return placeholderRe.ReplaceAllStringFunc(s, func(p string) string {
		v, ok := values[p]
		if !ok {
			return p
		}
		if escaped {
			return jsonEscape(v)
		}
		return v
	})
}

// partialTail is how much of the end of s may be the start of a
// placeholder the next piece of a stream finishes.
func partialTail(s string) int {
	i := strings.LastIndexByte(s, '{')
	if i < 0 {
		return 0
	}
	// the {{ that opens it
	if i > 0 && s[i-1] == '{' {
		i--
	}
	t := s[i:]
	if len(t) > 32 || strings.Contains(t, "}}") {
		return 0
	}
	// {, {{, {{A…, {{API_KEY_ab…, {{API_KEY_abcdefgh}
	if t == "{" || t == "{{" {
		return len(t)
	}
	if !strings.HasPrefix(t, "{{") {
		return 0
	}
	body := strings.TrimSuffix(t[2:], "}")
	if body == "" || body[0] < 'A' || body[0] > 'Z' {
		return 0
	}
	for j := 0; j < len(body); j++ {
		c := body[j]
		if !(c >= 'A' && c <= 'Z' || c == '_' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return 0
		}
	}
	return len(t)
}
