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
// again. A value once masked is masked wherever a request has it, whatever
// words are around it (known.go): the vendor writes about a placeholder in
// words no rule knows, and the agent sends that text back with the value.
package redact

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/publicsuffix"
)

// Options says what to mask.
type Options struct {
	Secrets  bool     // API keys, private keys, tokens, passwords
	Personal bool     // emails, phone numbers, ID and bank card numbers
	Words    []string // the user's own: names, codenames, hosts
	// WordsAnyCase matches Words in any case: Nightjar masks NIGHTJAR and
	// nightjar too. Off, a word is matched as written.
	WordsAnyCase bool
	// WordsWhole matches Words only as whole words (acmilan_ on Discord):
	// ABC masks "ABC IS" and 'ABC' but not XABCD, and 123456 masks no part
	// of 91234567, 0.123456 or 123456.78 (wholeWord). Off, a word is found
	// anywhere, inside a longer one too.
	WordsWhole bool
	Rules      []Rule // the user's own rules, masked with the secrets
	// Kinds turns each of the Categories of personal data on or off by its
	// id, where the user chose; one not in it is as its Category.On says.
	// None of them is masked while Personal is off.
	Kinds map[string]bool
	// Model, where set, finds more in the texts to mask (model.go)
	Model Model
	// Route names where a request masked for a vendor may go (the gateway
	// sets it: the providers a model, its fallbacks or a routing group's
	// members are served by). A text sent on it before goes on it again
	// as it went (sentSpans); on another route it is masked afresh.
	Route string
	// MinScore is the score the user has Model's finds need (Settings →
	// Privacy's slider), which Model's own finds are held to already. A
	// value only the model found, masked before at a lower score, isn't
	// masked again as known under it (knownSpans).
	MinScore float64
	// NamesByModel says Model finds Chinese names itself (AstrLink
	// Guard): beside it only a nickname (小王, 老李) is found by the
	// words around it (zhNameSpans)
	NamesByModel bool
	// Shape sends a key as a made-up key of its form, not a placeholder
	// (shape.go)
	Shape bool

	// Actions is what is done with a value of a kind (a Hit's Kind) other
	// than masking it, where the user chose: Warn sends it as written,
	// Block masks it and the request is held back (the gateway's). A kind
	// not in it is masked.
	Actions map[string]string
	// Allow are values the user lets go as written, whichever finder finds
	// them: each a whole value, in any case.
	Allow []string

	// Seen, where set, is told of each value masked, warned of or blocked
	Seen func(Hit)

	// found are the spans Model found, by text, for one request's texts
	found map[string][]Found
	// sent is masking for a vendor (Mask, MaskJSON): a text masked
	// before on Route has the model's finds and the known values it had
	// then (sentSpans)
	sent bool
}

// covers says o masks what a rule of category cat finds: "" are the
// secrets, catPersonal the personal data always masked with it, any other
// one of the Categories.
func (o Options) covers(cat string) bool {
	switch cat {
	case "":
		return o.Secrets
	case catPersonal:
		return o.Personal
	}
	if !o.Personal {
		return false
	}
	if on, ok := o.Kinds[cat]; ok {
		return on
	}
	return categoryOn[cat]
}

// rule finds one kind of value. The match is group 1 when the pattern has
// one, else all of it; bound are the characters that may not touch it on
// either side (a key inside a longer word is no key); ok checks it further.
type rule struct {
	kind    string
	re      *regexp.Regexp
	markers []string // one of these is in any text it can match
	// fold are markers too, looked for in the text in lower case, for a
	// pattern that matches its words in any case (Passport, PASSPORT)
	fold  []string
	bound string
	ok    func(string) bool
	// at checks the value at s[a:b] by what is around it: a dotted quad
	// right after "version" is no address
	at func(s string, a, b int) bool
	// fix, where set, checks the value in place of ok and may shorten it:
	// an IBAN printed in groups of four, followed by a word in capitals
	fix func(s string, a, b int) (int, int, bool)
	// back and ahead, where ahead is set, are how far before and after
	// one of fold a match can be: the pattern is only run there, as one
	// that takes its words in any case is slow over all of a long text
	back, ahead int
	// cat is what turns the rule on (Options.covers): "" the secrets
	cat string
	// field is a rule that takes a value after a field's name and = or :,
	// a space allowed between them: a value that is the next field's
	// assignment is not this one's (fieldMatches)
	field bool
	// yours is a rule the user wrote (customRules)
	yours bool
	// anyGroup takes the first group that matched, for a pattern with a
	// group in each of its alternatives
	anyGroup bool
	// groups, where set, are the kinds of a match's groups, each masked as
	// a value of its own (the user and the host of user@host), and groupOK
	// checks each
	groups  []string
	groupOK []func(string) bool
}

const (
	alnum   = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	tokenCh = alnum + "_-"
	digits  = "0123456789"
)

var rules = append([]rule{
	{kind: "PRIVATE_KEY", re: regexp.MustCompile(`-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY(?: BLOCK)?-----[\s\S]+?-----END (?:[A-Z0-9]+ )*PRIVATE KEY(?: BLOCK)?-----`), markers: []string{"PRIVATE KEY"}},
	{kind: "API_KEY", re: regexp.MustCompile(`sk-(?:ant-|proj-|or-|svcacct-|admin-)?[A-Za-z0-9_-]{20,}`), markers: []string{"sk-"}, bound: tokenCh, ok: notASlug},
	// a gateway's or a relay's own: oc_sk_…, or_sk_… (#195)
	{kind: "API_KEY", re: regexp.MustCompile(`[a-z]{2,4}_sk_[A-Za-z0-9_-]{20,}`), markers: []string{"_sk_"}, bound: tokenCh, ok: secretValue},
	// Volcano Ark and the relays in front of it issue keys with an ark-
	// prefix (#2946)
	{kind: "API_KEY", re: regexp.MustCompile(`ark-[A-Za-z0-9][A-Za-z0-9_-]{15,}`), markers: []string{"ark-"}, bound: tokenCh, ok: secretValue},
	{kind: "API_KEY", re: regexp.MustCompile(`(?:gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{40,}|glpat-[A-Za-z0-9_-]{20,})`), markers: []string{"gh", "github_pat_", "glpat-"}, bound: tokenCh},
	{kind: "API_KEY", re: regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`), markers: []string{"AIza"}, bound: tokenCh},
	{kind: "API_KEY", re: regexp.MustCompile(`xox[abposr]-[0-9A-Za-z-]{10,}`), markers: []string{"xox"}, bound: tokenCh},
	{kind: "API_KEY", re: regexp.MustCompile(`(?:sk|rk)_live_[0-9A-Za-z]{20,}`), markers: []string{"_live_"}, bound: tokenCh},
	{kind: "API_KEY", re: regexp.MustCompile(`(?:AKIA|ASIA)[0-9A-Z]{16}`), markers: []string{"AKIA", "ASIA"}, bound: alnum},
	{kind: "API_KEY", re: regexp.MustCompile(`(?:hf_[A-Za-z0-9]{30,}|gsk_[A-Za-z0-9]{40,}|xai-[A-Za-z0-9]{40,}|npm_[A-Za-z0-9]{36}|pypi-[A-Za-z0-9_-]{50,}|dop_v1_[a-f0-9]{64}|SG\.[A-Za-z0-9_-]{22}\.[A-Za-z0-9_-]{43})`),
		markers: []string{"hf_", "gsk_", "xai-", "npm_", "pypi-", "dop_v1_", "SG."}, bound: tokenCh},
	{kind: "TOKEN", re: regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), markers: []string{"eyJ"}, bound: tokenCh},
	// the password of user:password@host, or of :password@host as a Redis
	// URL has it with no user
	{kind: "PASSWORD", re: regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s:@/?#"'<>]*:([^\s@/?#"'<>]{3,})@`), markers: []string{"://"}, ok: notAVariable, at: hiddenInURL},
	// password = …, API_KEY: "…", as .env files and configs have them
	{kind: "SECRET", re: regexp.MustCompile(`(?i)[A-Za-z0-9_.-]*(?:` + secretNames.pattern() + `)[A-Za-z0-9_.-]*` + quote + `[ \t]*[:=][ \t]*` + quote + `(` + secretCh + `{8,})`),
		markers: secretNames.markers(), ok: secretValue, at: fieldAt, field: true},
	// an env var's name says what it holds in the upper case: a
	// SERVICE_X_KEY, a DB_PASS (#2946). The names above have a word for the
	// secret in them; these only end in one, and only in the upper case is
	// that a secret's name — in the lower case a name that ends in key is
	// code's: a sort key, a cache key, a public key (notAPlainKey turns the
	// upper-case ones of those away too).
	{kind: "SECRET", re: regexp.MustCompile(`(?:^|[^A-Za-z0-9_.-])[A-Za-z0-9_.-]*_(?:KEY|PASS)` + quote + `[ \t]*[:=][ \t]*` + quote + `(` + secretCh + `{8,})`),
		markers: fieldNames{{"key", "key"}, {"pass", "pass"}}.markers(), ok: secretValue, at: namedSecretAt, field: true},
	// an Authorization or Cookie header's value is a secret whatever it
	// looks like: an opaque bearer token, a session value (#2946)
	{kind: "SECRET", re: regexp.MustCompile(`(?im)^[ \t]*(?:authorization|cookie)[ \t]*:[ \t]*(\S[^\r\n]{7,})`),
		markers: fieldNames{{"authorization", "authorization"}, {"cookie", "cookie"}}.markers(), ok: headerValue},
	// a field called key and nothing more, as providers.json has one: "key":
	// "oc_sk_…" (#195). Only a long value of letters and digits both, since
	// code calls anything a key: a map's "key": "value", sort_key, keyboard.
	{kind: "SECRET", re: regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_.-])` + quote + `key` + quote + `[ \t]*[:=][ \t]*` + quote + `(` + secretCh + `{16,})`),
		markers: fieldNames{{"key", "key"}}.markers(), ok: secretValue, at: fieldAt, field: true},
	// a password after its name in Chinese, Japanese or Korean, as people
	// write one into a message: 密码：786kih, 密碼是 hunter2, パスワード: … .
	// Unlike password = in a config, nothing in code is called 密码, so
	// any value of four or more is taken, digits only too (CyberCaius on
	// Discord); a sentence's closing stop is no part of it.
	{kind: "PASSWORD", re: cjkNamed(cjkPasswordNames), fold: lowerAll(cjkPasswordNames), back: 64, ahead: 100, ok: notAVariable, field: true},
	// a key, a secret or a token after its name the same way: 密钥为 …,
	// 令牌是 …, 金鑰： … . A key of digits only is taken after 为, 是, ：
	// or = when it has six or more and counts nothing, so 密钥为12231241
	// is masked and 密钥为 2048 位 is left.
	{kind: "SECRET", re: cjkNamed(cjkKeyNames), fold: lowerAll(cjkKeyNames), back: 64, ahead: 100, at: keyAt, field: true},
	// an English name for one before 为, 為, 是 or ：, as a Chinese message
	// has it: apikey 为 …, token 是 …, API Key：… (CyberCaius on Discord).
	// password = … and API_KEY: … in a config are the SECRET rule's above.
	{kind: "SECRET", re: regexp.MustCompile(`(?i)(?:` + secretNames.pattern() + `|pwd|api[ ]key|access[ ]key|secret[ ]key|key|\bpin(?:[ ]?code)?)` + quote +
		`[ \t]*(?:[为為是][ \t]*[:：]?|：)[ \t]*(?:\\?["'“「])?(` + secretCh + `{3,}[A-Za-z0-9_\-/+=~!@#%^&*])`),
		fold: cjkCopulas, back: 24, ahead: 260, at: keyAt, field: true},

	{kind: "EMAIL", re: regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`), markers: []string{"@"}, bound: alnum + "_%+-", ok: realEmail, at: emailAt, cat: catPersonal},
	{kind: "ID_CARD", re: regexp.MustCompile(`[1-9]\d{5}(?:18|19|20)\d{2}(?:0[1-9]|1[0-2])(?:0[1-9]|[12]\d|3[01])\d{3}[\dXx]`), bound: alnum, ok: chineseID, cat: catPersonal},
	{kind: "PHONE", re: regexp.MustCompile(`(?:\+?86[- ]?)?1[3-9]\d{9}`), bound: alnum, at: phoneAt, cat: catPersonal},
	{kind: "BANK_CARD", re: regexp.MustCompile(`[3-6]\d{3}(?:[ -]?\d{4}){2,3}(?:[ -]?\d{1,3})?`), bound: alnum, ok: luhn, cat: catPersonal},
}, moreRules...)

// cjkPasswordNames are what a password is called in Chinese, Japanese and
// Korean.
var cjkPasswordNames = []string{"密码", "密碼", "口令", "PIN码", "PIN碼", "PIN 码", "安全码", "安全碼", "パスワード", "暗証番号", "비밀번호", "암호", "비번"}

// cjkKeyNames are what a key, a secret or a token is called in Chinese,
// Simplified and Traditional, Japanese and Korean.
var cjkKeyNames = []string{
	"密钥", "密鑰", "秘钥", "秘鑰", "私钥", "私鑰", "金钥", "金鑰", "令牌", "权杖", "權杖", "授权码", "授權碼",
	"秘密鍵", "トークン", "シークレット", "APIキー", "アクセスキー",
	"토큰", "비밀키", "API 키", "API키",
}

// cjkCopulas are what stands between a name and its value in a Chinese
// message: 为, 為, 是, a full-width colon.
var cjkCopulas = []string{"为", "為", "是", "："}

// lowerAll is names as fold has them, in lower case.
func lowerAll(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = strings.ToLower(n)
	}
	return out
}

// cjkNamed is a value after one of names: after ：, :, =, 为, 為, 是, は
// or 는, quoted or not, or after nothing but a space.
func cjkNamed(names []string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)(?:` + strings.Join(names, "|") + `)[ \t]*(?:[:：=][ \t]*|[为為是][ \t]*[:：]?[ \t]*|は[ \t]*|는[ \t]*)?(?:\\?["'“「])?(` + secretCh + `{3,63}[A-Za-z0-9_\-/+=~!@#%^&*])`)
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

// HasPlaceholder says whether s holds text written as Mask writes a
// placeholder, in JSON or not.
func HasPlaceholder(s string) bool {
	return strings.Contains(s, "{{") && placeholderRe.MatchString(s)
}

// notAVariable turns away what stands in for a value: ${PASS}, <password>, ****.
func notAVariable(v string) bool {
	if strings.ContainsAny(v[:1], "$%{<[*") {
		return false
	}
	return strings.Trim(v, "*xX.") != ""
}

// keyValue is a key's or a token's value, not a word or a number: letters
// and digits both, or 16 characters or more that aren't all digits. token 是
// expired, key 是 value and 密钥为 2048 are left.
func keyValue(v string) bool {
	if !notAVariable(v) {
		return false
	}
	letter := strings.IndexFunc(v, func(r rune) bool { return r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' }) >= 0
	return letter && (strings.ContainsAny(v, digits) || len(v) >= 16)
}

// keyAt is keyValue for s[a:b], or six digits or more right after 为, 為,
// 是, ：, :, =, は or 는 that count nothing: a key said in a message is
// often a number (密钥为12231241, token 是 20261025, CyberCaius on
// Discord), while 密钥为 2048 位 and 令牌 300000 毫秒 are a size and a
// time.
func keyAt(s string, a, b int) bool {
	v := s[a:b]
	if pathValue(v) || versionValue(v) {
		// where a key is kept: 私钥 ~/.ssh/id_ed25519, or a release's
		// version: token 是 0.15.0-rc1 (fieldvalue.go)
		return false
	}
	if keyValue(v) {
		return true
	}
	if len(v) < 6 || strings.Trim(v, digits) != "" {
		return false
	}
	before := strings.TrimRight(s[:a], " \t\"'“「\\")
	after := strings.TrimLeft(s[b:], " \t")
	return slices.ContainsFunc(keySeparators, func(c string) bool { return strings.HasSuffix(before, c) }) &&
		!slices.ContainsFunc(quantityUnits, func(u string) bool { return strings.HasPrefix(after, u) })
}

// keySeparators are what stands between a name and a key's value.
var keySeparators = []string{"为", "為", "是", "：", ":", "=", "は", "는"}

// quantityUnits are what a number counts when one follows it: bits, bytes,
// a time, a count.
var quantityUnits = []string{"位", "比特", "字节", "字節", "个", "個", "次", "秒", "毫秒", "分钟", "分鐘", "小时", "小時", "天", "年", "bit", "byte", "ms", "sec", "min", "ビット", "バイト", "비트", "바이트"}

// secretValue is a value that looks like a secret, not code: letters and
// digits both, and not a call, a reference or a placeholder of its own.
func secretValue(v string) bool {
	if !notAVariable(v) || strings.HasPrefix(v, "process.env") || strings.HasPrefix(v, "os.") {
		return false
	}
	return strings.ContainsAny(v, digits) && strings.IndexFunc(v, func(r rune) bool { return r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' }) >= 0
}

// identPath is a name in code, or a field read through one: Uint8Array,
// details.2, opts.token2.
var identPath = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z0-9_]+)*$`)

// notCode turns away a value at s[a:b] that is code naming a secret, not
// the secret: a call (privateKey: toBase64Url(…), secretKey =
// fromBase64Url(…)), or, unquoted after a colon, a type or a read that
// code goes on from (secretKey: Uint8Array;, private_key: details.2,).
// A secret written in code is a string, so in quotes; one after = or at a
// line's end (.env files, logs, YAML) is kept (Kieran Zhang on X, 10-25).
func notCode(s string, a, b int) bool {
	if !identPath.MatchString(s[a:b]) {
		return true
	}
	rest := strings.TrimLeft(s[b:], " \t")
	if strings.HasPrefix(rest, "(") {
		return false
	}
	if a > 0 && (s[a-1] == '"' || s[a-1] == '\'') || !strings.HasSuffix(strings.TrimRight(s[:a], " \t"), ":") {
		return true
	}
	return rest == "" || !strings.ContainsRune(";,)}]|?&<>", rune(rest[0]))
}

// notUserinfo turns away what looks like an email at s[a:b] but is a
// URL's user:password@host or user@host: its @ ends the userinfo, so
// what is before it is a user name or a password and what is after it
// the host. postgres://app:hunter2pass@db.acme-corp.cn:5432/x went as
// app:{{EMAIL_…}}:5432, the password and the host one address, where the
// password is the PASSWORD rule's and the host stays (#3538). An address
// in a URL's path or query (?to=bob@acme.cn) and in mailto: is an
// email still.
func notUserinfo(s string, a, _ int) bool {
	return !inUserinfo(s, a)
}

// emailAt is where an email at s[a:b] can be: not in a URL's userinfo
// (notUserinfo), and with no dot touching it that makes it part of a
// longer name: none before it, and none after it that a letter, digit,
// - or _ follows (a@b.io.12 is no address). A dot after it that ends a
// sentence is the sentence's: "The user's email address is
// jo@quill.dev." masked nothing while the dot was a bound character, and
// the address went to the vendor as written.
func emailAt(s string, a, b int) bool {
	if a > 0 && s[a-1] == '.' {
		return false
	}
	if b+1 < len(s) && s[b] == '.' && strings.IndexByte(tokenCh, s[b+1]) >= 0 {
		return false
	}
	return notUserinfo(s, a, b)
}

// inUserinfo says s[a] is inside a URL's authority, after its scheme://
// and before the host: nothing from there back to the // is a /, ?, #,
// space, quote, < or > or @.
func inUserinfo(s string, a int) bool {
	i := a
	for i > 0 && !strings.ContainsRune(" \t\r\n/?#\"'<>@`", rune(s[i-1])) {
		i--
	}
	if i < 3 || s[i-1] != '/' || s[i-2] != '/' || s[i-3] != ':' {
		return false
	}
	j := i - 3 // the scheme ends at s[j], its colon
	k := j
	for k > 0 && (s[k-1] >= 'a' && s[k-1] <= 'z' || s[k-1] >= 'A' && s[k-1] <= 'Z' || s[k-1] >= '0' && s[k-1] <= '9' || strings.IndexByte("+.-", s[k-1]) >= 0) {
		k--
	}
	for k < j && !(s[k] >= 'a' && s[k] <= 'z' || s[k] >= 'A' && s[k] <= 'Z') {
		k++ // a scheme starts with a letter
	}
	return k < j
}

// hiddenInURL says the password at s[a:b], in a URL's user:password@,
// is hidden by masking it: it is not also written as it is elsewhere in
// the same URL, as its user name, a part of its host or of its path. A
// test database's URL often gives one word for all three:
// postgresql://geodatafusion:geodatafusion@127.0.0.1:55432/geodatafusion
// is the PostGIS test of the geodatafusion repo. Its password was masked
// while the URL still had it twice as written, and once masked the word
// was known (knownSpans): the project's folders, its crate, its .toml
// and cargo test -p geodatafusion went to the vendor as a made-up word
// for the rest of the run, as a password (xuanwo on X, 10-29: "很多项目名
// 被识别成了 password", 26M masked in a day). postgres:postgres,
// root:root and guest:guest are the same: anyone who reads the user
// reads the password. A password not written elsewhere in its URL is
// masked as before, whatever it looks like: app:geodatafusion@ masks it
// there and, as known, wherever it is next.
func hiddenInURL(s string, a, b int) bool {
	pw := s[a:b]
	i := strings.LastIndex(s[:a], "//")
	if i < 0 || s[a-1] != ':' {
		return true
	}
	if s[i+2:a-1] == pw {
		return false // the user name
	}
	// the host and the path, to the URL's end
	end := b + 1
	for end < len(s) && !strings.ContainsRune(" \t\r\n\"'<>`\\)]}", rune(s[end])) {
		end++
	}
	if b+1 > end {
		return true
	}
	for _, part := range strings.FieldsFunc(s[b+1:end], func(r rune) bool { return strings.ContainsRune("/:.?#&=;,@", r) }) {
		if part == pw {
			return false
		}
	}
	return true
}

// realEmail is whether v, matched as an address, is someone's. git@ is
// the user every git host is reached by over SSH (git@github.com:o/r.git,
// git@gitee.com, a company's own git@git.corp.com), no one's mailbox, and
// was masked so a remote no longer read as one (jumy on Discord).
// notASlug is false for words in lower case joined by hyphens, as a
// page's address names an article about SK hynix
// (cnbc.com/…/sk-hynix-third-quarter-profit.html), which went to the model
// as a key. A key's characters are random: it has a capital, or a run of
// 16 or more with no hyphen (DeepSeek's sk- and 32 hex digits, OpenRouter's
// sk-or-v1- and 64), or hex digits alone, as a UUID's groups (sk- and a
// UUID in lower case) are, which no word but a few like "face" is.
func notASlug(v string) bool {
	if strings.ContainsFunc(v, func(r rune) bool { return r >= 'A' && r <= 'Z' }) {
		return true
	}
	if !strings.ContainsFunc(strings.TrimPrefix(v, "sk-"), func(r rune) bool {
		return r != '-' && r != '_' && (r < '0' || r > '9') && (r < 'a' || r > 'f')
	}) {
		return true
	}
	for _, w := range strings.FieldsFunc(v, func(r rune) bool { return r == '-' || r == '_' }) {
		if len(w) >= 16 {
			return true
		}
	}
	return false
}

// headerValue says the rest of an Authorization or Cookie line is a
// header's value, not prose that begins with the word ("Authorization:
// the user must sign in first"): a credential, after a scheme word or
// alone (Bearer …, Basic …), or a cookie's name=value pairs. One that
// stands for a variable (${TOKEN}, <token>, ****) is left alone.
func headerValue(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" || !notAVariable(v) {
		return false
	}
	notLetter := func(r rune) bool { return !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z') }
	f := strings.Fields(v)
	if len(f) == 1 || len(f) == 2 && strings.IndexFunc(f[0], notLetter) < 0 {
		// a word alone is prose's: a credential has a digit or a sign in it
		cred := f[len(f)-1]
		return len(cred) >= 8 && notAVariable(cred) && strings.IndexFunc(cred, notLetter) >= 0
	}
	for _, kv := range strings.Split(v, ";") {
		if kv = strings.TrimSpace(kv); kv == "" {
			continue
		}
		name, _, ok := strings.Cut(kv, "=")
		if !ok || name == "" || strings.ContainsAny(name, " \t") {
			return false
		}
	}
	return strings.Contains(v, "=")
}

// notAPlainKey turns away the upper-case name before the value at s[:a]
// when it only ends in key and holds no secret: a sort key, a cache key, a
// prompt cache key, a public key (#2946).
func notAPlainKey(s string, a, b int) bool {
	i := a
	for i > 0 && strings.IndexByte(`"' `+"\t:=", s[i-1]) >= 0 {
		i--
	}
	end := i
	for i > 0 && strings.IndexByte(alnum+"_.-", s[i-1]) >= 0 {
		i--
	}
	name := strings.ToLower(s[i:end])
	return !strings.HasSuffix(name, "sort_key") && !strings.HasSuffix(name, "cache_key") &&
		!strings.HasSuffix(name, "prompt_cache_key") && !strings.HasSuffix(name, "public_key")
}

// notPlainKeyOrCode keeps both boundaries on a name that only ends in _KEY
// or _PASS: plain keys stay as written, and so does code naming a secret.
func notPlainKeyOrCode(s string, a, b int) bool {
	return notAPlainKey(s, a, b) && notCode(s, a, b)
}

// namedSecretAt is notPlainKeyOrCode with the value-side reading the SECRET
// rules' fieldAt gained later: a const TOKEN_KEY = "access_token_v2" is
// code naming a token, and its value reads as a version, so both stay.
func namedSecretAt(s string, a, b int) bool {
	return notPlainKeyOrCode(s, a, b) && fieldAt(s, a, b)
}

func realEmail(v string) bool {
	at := strings.LastIndexByte(v, '@')
	if strings.EqualFold(v[:at], "git") {
		return false
	}
	host := strings.ToLower(v[at+1:])
	if !mailHost(v[:at], host) {
		return false
	}
	if fileHost(host) || docFiles[host] || !mailTLD(host[strings.LastIndexByte(host, '.')+1:]) {
		return false
	}
	for _, fake := range []string{"example.com", "example.org", "example.net", "localhost", ".test", ".invalid", ".example"} {
		if host == strings.TrimPrefix(fake, ".") || strings.HasSuffix(host, fake) {
			return false
		}
	}
	return !strings.HasPrefix(host, "noreply") && !strings.Contains(v, "noreply@")
}

// mailHost is whether host, after local and an @, can be a mail domain:
// it has a dot, labels of letters, digits and hyphens (or a name's own
// script's: 例子.中国), and is not a
// linker's table; and local has no @ of its own. A disassembly's symbols
// read as addresses: system@plt, puts@got.plt, a version after one @ or
// two (memcpy@GLIBC_2.14, foo@@GLIBC_2.2.5, foo@Base). .got is a
// top-level domain, and the local model took system@got for an email, so
// a reverse engineer had to turn masking off (ch35tnut_ on X, 10-28).
func mailHost(local, host string) bool {
	if strings.HasSuffix(local, "@") || !strings.Contains(host, ".") || strings.Trim(host, ".") != host {
		return false
	}
	linker := true
	for _, l := range strings.Split(host, ".") {
		if l == "" || strings.ContainsFunc(l, func(r rune) bool { return r < 0x80 && r != '-' && !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') }) {
			return false
		}
		if !linkerWords[l] {
			linker = false
		}
	}
	return !linker
}

// linkerWords are the names of a binary's tables a symbol is reached
// through (system@plt, puts@got.plt, foo@plt.sec), no domain's labels.
var linkerWords = map[string]bool{"plt": true, "got": true, "sec": true, "iplt": true, "igot": true}

// docFiles are the files agents read by name, on a domain's ending
// (.md): "@AGENTS.md" after a word, as a prompt names one, is no address
// (ydzh on Discord: xxx@Agents.md was masked).
var docFiles = map[string]bool{
	"agents.md": true, "claude.md": true, "gemini.md": true, "readme.md": true, "skill.md": true,
	"changelog.md": true, "contributing.md": true, "license.md": true, "lessons.md": true, "todo.md": true,
	"copilot-instructions.md": true, "crush.md": true, "qwen.md": true, "iflow.md": true, "warp.md": true,
}

// localTLDs are names an organisation's own mail can end in that no
// registry has: an address there is someone's all the same.
var localTLDs = map[string]bool{"local": true, "internal": true, "lan": true, "corp": true, "home": true, "intranet": true, "localdomain": true}

// mailTLD is whether tld, an address's last label, is one mail can be
// sent to: in the public suffix list's ICANN part, or an organisation's
// own. A decorator's or a module's attribute after a name
// (x@pytest.fixture, ydzh on Discord) ends in a word no registry has.
func mailTLD(tld string) bool {
	if localTLDs[tld] {
		return true
	}
	_, icann := publicsuffix.PublicSuffix(tld)
	return icann
}

// fileExt are file name extensions that are no top-level domain, so
// what ends in one is a file's name and not an address: an image a Mac
// saved at twice its size, 261020_14-16-11@2x.jpg, went to the model as
// {{EMAIL_…}} and Pi could no longer read the file (#3029).
// Other files are named so too (intro@2x.mp4, strings@base.json), and
// none of these is a domain either (IANA's list, checked 2026-10-21).
var fileExt = map[string]bool{
	"jpg": true, "jpeg": true, "png": true, "gif": true, "webp": true, "svg": true, "heic": true, "heif": true,
	"avif": true, "bmp": true, "tif": true, "tiff": true, "ico": true, "icns": true, "pdf": true,
	"svgz": true, "apng": true, "jxl": true, "psd": true, "eps": true, "lottie": true,
	"mp4": true, "m4v": true, "webm": true, "mkv": true, "avi": true, "mp3": true, "m4a": true, "wav": true,
	"ogg": true, "flac": true, "aac": true, "ttf": true, "otf": true, "woff": true, "woff2": true, "eot": true,
	"json": true, "yaml": true, "yml": true, "toml": true, "xml": true, "txt": true, "csv": true, "tsv": true,
	"log": true, "html": true, "htm": true, "css": true, "scss": true, "less": true, "js": true, "mjs": true,
	"cjs": true, "jsx": true, "ts": true, "tsx": true, "doc": true, "docx": true, "xls": true, "xlsx": true,
	"ppt": true, "pptx": true, "gz": true, "tgz": true, "tar": true, "rar": true, "7z": true, "dmg": true,
}

// scaleExt are file extensions that are top-level domains too (.zip,
// .mov, .md, .map): a name ending in one is a file's only with a scale
// before it, icon@2x.zip, intro@1.5x.mov, while ops@files.zip is an
// address.
var scaleExt = map[string]bool{"zip": true, "mov": true, "md": true, "map": true}

var scaleRe = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?x$`)

// fileHost says host, what follows the @ of what looks like an address,
// is the end of a file's name: an extension no domain has (fileExt), or a
// scale and an extension that is a domain too (scaleExt).
func fileHost(host string) bool {
	dot := strings.LastIndexByte(host, '.')
	ext := host[dot+1:]
	return fileExt[ext] || dot > 0 && scaleExt[ext] && scaleRe.MatchString(host[:dot])
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
	mu     sync.RWMutex
	values = map[string]string{} // placeholder → value
	kinds  = map[string]bool{}   // the kinds of values, for wordTail
	// used is when each placeholder was last made or met again, by
	// clock: makeRoom lets the ones used longest ago go first
	used    = map[string]uint64{}
	clock   uint64
	keyOf   []byte
	keyPath string // guarded by mu, as keyOf is
)

// maxValues bounds what is held; past it the half used longest ago is let
// go, and a request that still has them brings them back.
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

// tag is the placeholder v of kind goes as, made and not remembered: a
// value sent as written (Warn) is named by it in the record of what was
// found, and a preview names each value by it.
func tag(kind, v string) string {
	h := hmac.New(sha256.New, key())
	h.Write([]byte(kind + "\x00" + v))
	return "{{" + kind + "_" + strings.ToLower(b32.EncodeToString(h.Sum(nil))[:8]) + "}}"
}

func placeholder(kind, v string) string {
	p := tag(kind, v)
	mu.Lock()
	if _, ok := values[p]; !ok {
		makeRoom()
		values[p] = v
		kinds[kind] = true
		addKnown(p, v)
	}
	touch(p)
	mu.Unlock()
	return p
}

// touch says p was used now; called with mu held.
func touch(p string) {
	clock++
	used[p] = clock
}

// makeRoom lets the half of values used longest ago go when they are
// full; called with mu held. What a request in flight has just masked, or
// met again, is the newest: let go at random, a value masked a moment
// before could be gone by the time its placeholder came back, and the
// answer kept the placeholder.
func makeRoom() {
	if len(values) < maxValues {
		return
	}
	ps := make([]string, 0, len(values))
	for p := range values {
		ps = append(ps, p)
	}
	sort.Slice(ps, func(i, j int) bool { return used[ps[i]] < used[ps[j]] })
	for _, p := range ps[:len(ps)/2] {
		delete(values, p)
		delete(shapes, p)
		delete(used, p)
	}
	reindexKnown()
}

// put is how o sends a value: as its placeholder, or as a made-up key of
// its form (Shape).
func (o Options) put() func(kind, v string) string {
	if o.Shape {
		return standIn(shaped)
	}
	return standIn(placeholder)
}

// ---- masking --------------------------------------------------------------------

type span struct {
	start, end int
	kind       string
	by         string  // which finder found it: one of the By constants
	score      float64 // the model's score for one it found, else 0
	// bare: a whole bare value of JSON text (inStrings), whose
	// placeholder goes in quoted
	bare bool
}

// Which finder found a value (Hit.By): magpie's rules, the user's own
// rules, the user's words, the local model (with the Chinese names it
// brings, which run only with it), or a value masked before (known.go).
const (
	ByRule  = "rule"
	ByYours = "yours"
	ByWord  = "word"
	ByModel = "model"
	ByKnown = "known"
)

// What is done with a value of a kind (Options.Actions) other than
// masking it.
const (
	Warn  = "warn"  // sent as written, and recorded
	Block = "block" // masked, and the request held back
)

// Hit is one value found: its kind, the finder that found it, the
// placeholder it went as (or would have, for one sent as written), what
// was done with it ("" masked, Warn, Block) and its Hint. It never holds
// the value itself.
type Hit struct {
	Kind, By, Placeholder, Action string
	Head                          string
	Len                           int
}

// Value is the value behind placeholder p, while this run of magpie
// remembers it.
func Value(p string) (string, bool) {
	mu.RLock()
	defer mu.RUnlock()
	v, ok := values[p]
	return v, ok
}

// Hint is as much of v as may be kept where v itself may not: its first
// character (the first few of a key's, which name its kind: sk-, ghp_)
// and how many characters it has.
func Hint(kind, v string) (head string, n int) {
	n = utf8.RuneCountInString(v)
	if n < 2 {
		return "", n
	}
	k := 1
	if _, personal := kindCats[kind]; !personal && kind != "TERM" && n >= 16 {
		// a key's prefix, up to where its random part starts
		k = 0
		for _, c := range v {
			if k >= 4 || !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ-_", c) {
				break
			}
			k++
			if c == '-' || c == '_' {
				break
			}
		}
		k = max(k, 1)
	}
	r := []rune(v)
	return string(r[:k]), n
}

// anyCases are the patterns that find a user's word in any case, by word.
var anyCases sync.Map

// anyCase finds w as written but in any case (Unicode's simple folding, so
// a word whose case changes its length in bytes is still found whole).
func anyCase(w string) *regexp.Regexp {
	if re, ok := anyCases.Load(w); ok {
		return re.(*regexp.Regexp)
	}
	re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(w))
	anyCases.Store(w, re)
	return re
}

// wordRune says r is a letter, a digit or _ of a script that puts spaces
// between words. Chinese and Japanese don't, so a word of theirs is found
// inside their text, and a Latin word right beside a Chinese letter
// ("用ABC做") is still whole.
func wordRune(r rune) bool {
	if r == '_' {
		return true
	}
	if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
		return false
	}
	return !unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana)
}

// wholeWord says s[a:b], a word the user asked to mask, is a word of its
// own in s: where its first or last letter is a wordRune, the letter
// beside it in s isn't one. A number is one with its decimals, so a digit
// at its edge also has no "." and digit beside it: 123456 is not whole in
// 0.123456 or 123456.78, while "v1." ends a sentence.
func wholeWord(s string, a, b int) bool {
	first, _ := utf8.DecodeRuneInString(s[a:b])
	last, _ := utf8.DecodeLastRuneInString(s[a:b])
	if wordRune(first) && a > 0 {
		r, n := utf8.DecodeLastRuneInString(s[:a])
		if wordRune(r) {
			return false
		}
		if unicode.IsDigit(first) && r == '.' {
			if p, _ := utf8.DecodeLastRuneInString(s[:a-n]); unicode.IsDigit(p) {
				return false
			}
		}
	}
	if wordRune(last) && b < len(s) {
		r, n := utf8.DecodeRuneInString(s[b:])
		if wordRune(r) {
			return false
		}
		if unicode.IsDigit(last) && r == '.' {
			if f, _ := utf8.DecodeRuneInString(s[b+n:]); unicode.IsDigit(f) {
				return false
			}
		}
	}
	return true
}

// Mask swaps what o covers in s for placeholders, and says how many.
func Mask(s string, o Options) (string, int) {
	o.sent = true
	return mask(s, withModel(o, []string{s}), o.put(), false)
}

// Preview is Mask with nothing remembered: each value goes as the
// placeholder, or with Options.Shape the made-up key, it would have (a
// user name its made-up name, an IP address its public-ip- name), which
// isn't kept for Restore or as a value seen before (known.go).
func Preview(s string, o Options) string {
	put := tag
	if o.Shape {
		put = func(kind, v string) string {
			name, k := splitName(v)
			if f, ok := shapeOf(kind, k); ok {
				return name + f
			}
			return tag(kind, v)
		}
	}
	tagOrShape := put
	put = func(kind, v string) string {
		if kind == "USERNAME" {
			return standInOf(v) // a user name's made-up name (shape.go)
		}
		if kind == "IP" {
			if at, _, ok := ipStandIns(v); ok {
				return at(0) // an address's public-ip- name (shape.go)
			}
		}
		return tagOrShape(kind, v)
	}
	out, _ := mask(s, withModel(o, []string{s}), put, false)
	return out
}

// mask swaps what o covers in s for what put makes of each value.
//
// escaped says s holds JSON text of its own (asJSON: a tool call's
// arguments), which the vendor reads after one more unescaping. There the
// finders read s as the vendor does (unescapedView): the \n before
// @mcp.tool is a newline, not an n that starts an address, and the \n
// before jo@quill.dev is no letter of it either. No value starts or ends
// inside an escape (outsideEscapes), and what is kept to put back is the
// value as it reads unescaped, so Restore, which escapes it again in
// arguments, gives the agent what it wrote. Read as plain text, the n of
// a \n went as the first letter of n@mcp.tool, the backslash was left
// before the placeholder (\{{EMAIL_…}}), the arguments were JSON no
// longer, and Ollama Cloud turned away every turn that replayed the call
// with 400 "invalid tool call arguments" (#3022).
func mask(raw string, o Options, put func(kind, v string) string, escaped bool) (string, int) {
	if len(raw) < 3 {
		return raw, 0
	}
	s := raw // what the finders read: as long as raw, a place in one is the same in the other
	if escaped {
		s = unescapedView(raw)
		put = unescapedPut(put)
	}
	var found []span
	for _, w := range o.Words {
		if w = strings.TrimSpace(w); len(w) < 2 {
			continue
		}
		// a date is matched as that date, however it is written (dateword.go)
		if spans, ok := dateSpans(s, w); ok {
			found = append(found, spans...)
			continue
		}
		// next finds w at or after i; one inside a longer word, where the
		// user asked for whole words, is passed over a letter at a time, so
		// "XABC ABC" still finds the second
		next := func(i int) (int, int) {
			if o.WordsAnyCase {
				if m := anyCase(w).FindStringIndex(s[i:]); m != nil {
					return i + m[0], i + m[1]
				}
				return -1, -1
			}
			if j := strings.Index(s[i:], w); j >= 0 {
				return i + j, i + j + len(w)
			}
			return -1, -1
		}
		for i := 0; i <= len(s); {
			a, b := next(i)
			if a < 0 {
				break
			}
			if o.WordsWhole && !wholeWord(s, a, b) {
				_, n := utf8.DecodeRuneInString(s[a:])
				i = a + max(n, 1)
				continue
			}
			found = append(found, span{a, b, "TERM", ByWord, 0, false})
			i = max(b, a+1)
		}
	}
	found = append(found, ruleSpans(s, o)...)
	// after the rules', so where a rule finds the same value it keeps the
	// kind it has always had
	if o.sent {
		// the model was asked about raw
		now := modelSpans(raw, o)
		kept := sentSpans(sentModel, raw, o, func() []span { return now })
		learnLate(raw, now, kept, o, escaped)
		found = append(found, kept...)
		found = append(found, sentSpans(sentKnown, s, o, func() []span { return knownSpans(s, o) })...)
	} else {
		found = append(found, modelSpans(raw, o)...) // the model was asked about raw
		found = append(found, knownSpans(s, o)...)
	}
	if escaped {
		found = inStrings(raw, outsideEscapes(raw, found))
	}
	found = noNumbers(s, noIDs(s, found))
	if o.sent {
		noteScores(raw, found) // what is masked for a vendor, not a preview
	}
	return apply(raw, found, put, o)
}

// ruleSpans are what the rules, magpie's and the user's own, find in s,
// the text the finders read.
func ruleSpans(s string, o Options) []span {
	return memoRules(s, o, func() []span {
		if len(s) >= ruleMemoMin {
			ruleRuns.Add(1)
		}
		var found []span
		all := rules
		if o.Secrets && len(o.Rules) > 0 {
			// the user's first: where one of theirs and one of magpie's find
			// the same value, it goes by the name they gave it
			all = append(customRules(o.Rules), rules...)
		}
		lower := ""
		// split are matches a rule took apart into values of their own
		// (rkato@box17 as a user and a host): what another rule found from
		// the same place in them, an email, goes
		var split []span
		for _, r := range all {
			if !o.covers(r.cat) {
				continue
			}
			if len(r.markers) > 0 && !containsAny(s, r.markers) {
				continue
			}
			if len(r.fold) > 0 {
				if lower == "" {
					lower = lowerASCII(s)
				}
				if !containsAny(lower, r.fold) {
					continue
				}
			}
			for _, m := range r.matches(s, lower) {
				if len(r.groups) > 0 {
					took := false
					for g, kind := range r.groups {
						if a, b := m[2*g+2], m[2*g+3]; a >= 0 && r.takes(s, a, b, r.groupOK[g]) {
							found = append(found, span{a, b, kind, r.by(), 0, false})
							took = true
						}
					}
					if took {
						split = append(split, span{m[2], m[1], "", "", 0, false})
					}
					continue
				}
				a, b := m[0], m[1]
				for g := 1; 2*g+1 < len(m); g++ {
					if m[2*g] >= 0 {
						a, b = m[2*g], m[2*g+1]
						break
					}
					if !r.anyGroup {
						break
					}
				}
				ok := r.ok
				if r.fix != nil {
					var good bool
					if a, b, good = r.fix(s, a, b); !good {
						continue
					}
					ok = nil
				}
				if r.takes(s, a, b, ok) {
					found = append(found, span{a, b, r.kind, r.by(), 0, false})
				}
			}
		}
		for _, sp := range split {
			found = slices.DeleteFunc(found, func(f span) bool {
				return f.kind == "EMAIL" && f.start == sp.start && f.end <= sp.end
			})
		}
		return found
	})
}

// learnLate makes known what the model finds in raw now that it didn't
// the first time raw was sent on o.Route (now, beside kept): raw goes as
// it went, but a value in it the model has read since is masked in new
// text, as it would be had raw been masked with it (known.go). Nothing
// is told (o.Seen): it isn't masked here.
func learnLate(raw string, now, kept []span, o Options, escaped bool) {
	var late []span
	for _, f := range now {
		if !slices.ContainsFunc(kept, func(k span) bool { return k.start == f.start && k.end == f.end && k.kind == f.kind }) {
			late = append(late, f)
		}
	}
	if len(late) == 0 {
		return
	}
	if escaped {
		late = outsideEscapes(raw, late)
	}
	noteScores(raw, late)
	o.Seen = nil
	put := o.put()
	if escaped {
		put = unescapedPut(put)
	}
	apply(raw, late, put, o)
}

// unescapedView is s, JSON text, as long as s and read as the vendor reads
// it once unescaped, as far as the finders tell: an escape that stands for
// a newline, a tab or another control character has its letter read as
// one (\n as \ and a newline), and a \u escape's u and digits as line
// breaks, so neither starts or ends a word. A backslash, a quote and a
// slash escaped stay as written: the rules read \" as a quote and C:\\Users
// as a Windows path.
func unescapedView(s string) string {
	if strings.IndexByte(s, '\\') < 0 {
		return s
	}
	b := []byte(s)
	for i := 0; i+1 < len(b); i++ {
		if b[i] != '\\' {
			continue
		}
		i++
		switch b[i] {
		case 'n', 'b', 'f':
			b[i] = '\n'
		case 'r':
			b[i] = '\r'
		case 't':
			b[i] = '\t'
		case 'u':
			for j := i; j < i+5 && j < len(b); j++ {
				b[j] = '\n'
			}
			i += 4
		}
	}
	return string(b)
}

// unescapedPut is put given each value unescaped, as the vendor and the
// agent read it: a private key's \n are newlines. A value that isn't a
// JSON string's text goes as it is.
func unescapedPut(put func(kind, v string) string) func(kind, v string) string {
	return func(kind, v string) string {
		if strings.IndexByte(v, '\\') >= 0 {
			var u string
			if json.Unmarshal([]byte(`"`+v+`"`), &u) == nil {
				v = u
			}
		}
		return put(kind, v)
	}
}

// outsideEscapes is found with no value starting or ending inside one of
// the escapes of s, JSON text: after an escape's backslash, as the n of
// \n, or among a \u00e9's digits. Masked there, it would leave a
// backslash before the placeholder or cut one off from its letter, and s
// would be JSON no longer (#3022). A value is cut back to the escapes'
// edges instead, so what it holds is still masked: the zoe@quill.dev of a
// model's "nzoe@quill.dev" read in \nzoe@quill.dev, a user's rule that
// reads up to the backslash of \". One left with nothing goes. Every
// finder's values go through here: the rules', the user's words, the
// model's and those known before.
func outsideEscapes(s string, found []span) []span {
	if len(found) == 0 || strings.IndexByte(s, '\\') < 0 {
		return found
	}
	// esc[i], for i inside an escape past its backslash: where the escape
	// starts and ends, plus one each so a zero is outside one
	type edges struct{ from, to int }
	esc := make([]edges, len(s)+1)
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			continue
		}
		n := 2
		if i+1 < len(s) && s[i+1] == 'u' {
			n = 6
		}
		end := min(i+n, len(s))
		for j := i + 1; j < end; j++ {
			esc[j] = edges{i + 1, end + 1}
		}
		i = end - 1
	}
	out := found[:0]
	for _, f := range found {
		if e := esc[f.start]; e.to > 0 {
			f.start = e.to - 1
		}
		if e := esc[f.end]; e.from > 0 {
			f.end = e.from - 1
		}
		if f.start < f.end {
			out = append(out, f)
		}
	}
	return out
}

// inStrings is found, for s JSON text (a tool call's arguments), with
// each value inside the one string it starts in: cut at that string's
// closing quote, or started at the first string inside it. A value that
// is a whole bare value (a number under "password":) is kept, and its
// placeholder goes in quoted (span.bare); one in nothing else, across a
// key's quotes, a colon or a brace, goes. Masked over the quote, the
// arguments were JSON no longer: the local model reads a password's ! and
// the "} after it as one token, and {"cmd":"export DB_PASSWORD=Hunt3r!"}
// went as {"cmd":"export DB_PASSWORD={{SECRET_…}}}; a user's rule
// tok-\S+ took the "} too. The vendor turned away every turn that
// replayed the call, and Codex reconnected over and over
// (xiaojiang091499 on X). Every finder's values go through here, as
// through outsideEscapes. Text that isn't JSON is left as it is.
func inStrings(s string, found []span) []span {
	if len(found) == 0 || !json.Valid([]byte(s)) {
		return found
	}
	// in[i] is 1 + which string literal byte i is the text of, 0 for one
	// outside every string (its quotes among them)
	in := make([]int32, len(s))
	n := int32(0)
	for i := 0; i < len(s); i++ {
		if s[i] != '"' {
			continue
		}
		n++
		j := i + 1
		for ; j < len(s) && s[j] != '"'; j++ {
			in[j] = n
			if s[j] == '\\' && j+1 < len(s) {
				j++
				in[j] = n
			}
		}
		i = j
	}
	out := found[:0]
	for _, f := range found {
		a := f.start
		for a < f.end && in[a] == 0 {
			a++
		}
		if a == f.end {
			if bareValue(s, f.start, f.end) {
				f.bare = true
				out = append(out, f)
			}
			continue
		}
		b := a
		for b < f.end && in[b] == in[a] {
			b++
		}
		f.start, f.end = a, b
		out = append(out, f)
	}
	return out
}

// bareValue says s[a:b], JSON text outside its strings, is all of one
// bare value: an object's (after :) or an array's (after [ or ,), with
// the , } or ] that ends it after it.
func bareValue(s string, a, b int) bool {
	if a >= b || strings.ContainsAny(s[a:b], " \t\r\n{}[]\",:") {
		return false
	}
	i := a - 1
	for i >= 0 && isSpace(s[i]) {
		i--
	}
	k := b
	for k < len(s) && isSpace(s[k]) {
		k++
	}
	return i >= 0 && strings.IndexByte(":[,", s[i]) >= 0 && (k == len(s) || strings.IndexByte(",}]", s[k]) >= 0)
}

// noNumbers is found without what is a piece of a decimal number of s:
// digits right after the point of 5.13800138000, or right before the
// point of 13800138000.5. A number is judged whole, and the fraction of
// a height, a price or a coordinate is not a phone or a card number
// because its digits happen to check (willz on Discord). Nor is a number
// that stands bare as a JSON value: "timestamp": 17929036837 in a session
// file is a count or a time, and a phone, a card or an ID number in JSON
// is a string (id.null on Discord). Only personal data's kinds, whoever
// found them (a rule, the model, a value known before): a secret is
// masked wherever it is, and what the user chose (their words, their
// rules) stays theirs.
func noNumbers(s string, found []span) []span {
	return slices.DeleteFunc(found, func(f span) bool {
		_, personal := kindCats[f.kind]
		return personal && f.by != ByWord && f.by != ByYours && (inDecimal(s, f.start, f.end) || jsonNumber(s, f.start, f.end))
	})
}

// jsonNumber says s[a:b] is all of a bare JSON number, by where it sits:
// digits only, an object's value ("key": …) or an element of an array of
// numbers ([…, …]), and nothing after it but the , } or ] that ends a
// value. "phone": "13800138000" is a string, and phone:13800138000 and
// "call 13800138000, thanks" are text, so each is still masked.
func jsonNumber(s string, a, b int) bool {
	if a >= b || strings.Trim(s[a:b], digits) != "" {
		return false
	}
	space := func(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }
	k := b
	for k < len(s) && space(s[k]) {
		k++
	}
	if k < len(s) && strings.IndexByte(",}]", s[k]) < 0 {
		return false
	}
	i := a - 1
	for i >= 0 && space(s[i]) {
		i--
	}
	if i < 0 {
		return false
	}
	switch s[i] {
	case ':':
		for i--; i >= 0 && space(s[i]); i-- {
		}
		return i >= 0 && s[i] == '"'
	case '[', ',':
		// back over the numbers before it to the array's [
		for ; i >= 0; i-- {
			if s[i] == '[' {
				return true
			}
			if strings.IndexByte(digits+",.-+eE", s[i]) < 0 && !space(s[i]) {
				return false
			}
		}
	}
	return false
}

// inDecimal says s[a:b] starts with a digit that is a decimal's fraction
// (a digit and a point before it) or ends with a digit that is its whole
// part (a point and a digit after it).
func inDecimal(s string, a, b int) bool {
	isDigit := func(i int) bool { return i >= 0 && i < len(s) && s[i] >= '0' && s[i] <= '9' }
	return isDigit(a) && a >= 2 && s[a-1] == '.' && isDigit(a-2) ||
		isDigit(b-1) && b+1 < len(s) && s[b] == '.' && isDigit(b+1)
}

// by is the finder r is, for Hit.By.
func (r rule) by() string {
	if r.yours {
		return ByYours
	}
	return ByRule
}

// matches are r's matches in s, lower being lowerASCII(s), as
// FindAllStringSubmatchIndex has them: in all of s, or, for a rule with
// ahead set, only around its fold markers.
func (r rule) matches(s, lower string) [][]int {
	if r.ahead == 0 {
		return r.findAll(s)
	}
	var wins [][2]int
	for _, f := range r.fold {
		for i := 0; ; {
			j := strings.Index(lower[i:], f)
			if j < 0 {
				break
			}
			at := i + j
			i = at + len(f)
			// from the line's start, or from where a word starts no further
			// back than back: what the pattern takes for the text's start is
			// where a line or a word starts in s too. The line's start is
			// looked for no further back than that: a text of one long
			// line, as a request body is, isn't read back to its start for
			// each marker.
			start := max(0, at-r.back)
			if nl := strings.LastIndexByte(s[start:at], '\n'); nl >= 0 {
				start += nl + 1
			} else if start > 0 {
				for start < at && (strings.IndexByte(alnum+"_.-", s[start-1]) >= 0 || s[start]&0xc0 == 0x80) {
					start++
				}
				if start == at && strings.IndexByte(alnum+"_", s[at-1]) >= 0 {
					continue // inside a longer word
				}
			}
			wins = append(wins, [2]int{start, min(len(s), i+r.ahead)})
		}
	}
	sort.Slice(wins, func(i, j int) bool { return wins[i][0] < wins[j][0] })
	var out [][]int
	for k := 0; k < len(wins); k++ {
		w := wins[k]
		for k+1 < len(wins) && wins[k+1][0] <= w[1] {
			k++
			w[1] = max(w[1], wins[k][1])
		}
		for _, m := range r.findAll(s[w[0]:w[1]]) {
			for x := range m {
				if m[x] >= 0 {
					m[x] += w[0]
				}
			}
			out = append(out, m)
		}
	}
	return out
}

// findAll are r's matches in s: each of them, or, for a rule that reads
// a field's value after its name (field), none whose value is the next
// field (fieldMatches).
func (r rule) findAll(s string) [][]int {
	if r.field {
		return fieldMatches(r.re, s)
	}
	return r.re.FindAllStringSubmatchIndex(s, -1)
}

// lowerASCII is s with its ASCII letters in lower case, as long as s, so a
// place in one is the same place in the other (a byte of a character
// outside ASCII is never an ASCII letter). It is s itself, not a copy,
// where s has no capital.
func lowerASCII(s string) string {
	i := 0
	for i < len(s) && !('A' <= s[i] && s[i] <= 'Z') {
		i++
	}
	if i == len(s) {
		return s
	}
	b := []byte(s)
	for ; i < len(b); i++ {
		if c := b[i]; 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// takes says s[a:b] is a value r masks: nothing it may not touch beside
// it, and its checks pass.
func (r rule) takes(s string, a, b int, ok func(string) bool) bool {
	if b <= a {
		return false
	}
	if r.bound != "" && (a > 0 && strings.IndexByte(r.bound, s[a-1]) >= 0 || b < len(s) && strings.IndexByte(r.bound, s[b]) >= 0) {
		return false
	}
	if ok != nil && !ok(s[a:b]) {
		return false
	}
	return r.at == nil || r.at(s, a, b)
}

// apply swaps each of found in s for what put makes of its value, the
// first and longest where they overlap, nothing inside a placeholder. A
// value o allows is left as it is, and one of a kind o warns of is left
// and told. o.Seen, where set, is told of each.
func apply(s string, found []span, put func(kind, v string) string, o Options) (string, int) {
	if len(found) == 0 {
		return s, 0
	}
	// nothing inside a placeholder already there, or a made-up key
	found = outsideShapes(s, found)
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
	last, next, n := 0, 0, 0 // next: where the next value may start
	for _, f := range found {
		if f.start < next {
			continue
		}
		next = f.end
		v := s[f.start:f.end]
		if o.allows(v) {
			continue
		}
		act := o.Actions[f.kind]
		if act != Warn && act != Block {
			act = ""
		}
		h := Hit{Kind: f.kind, By: f.by, Action: act}
		h.Head, h.Len = Hint(f.kind, v)
		if act == Warn {
			if o.Seen != nil {
				h.Placeholder = tag(f.kind, v)
				o.Seen(h)
			}
			continue
		}
		b.WriteString(s[last:f.start])
		h.Placeholder = put(f.kind, v)
		if f.bare {
			b.WriteString(`"` + h.Placeholder + `"`)
		} else {
			b.WriteString(h.Placeholder)
		}
		if o.Seen != nil {
			o.Seen(h)
		}
		last, n = f.end, n+1
	}
	b.WriteString(s[last:])
	return b.String(), n
}

// allows says the user lets v go as written (Options.Allow).
func (o Options) allows(v string) bool {
	for _, a := range o.Allow {
		if strings.EqualFold(strings.TrimSpace(a), v) {
			return true
		}
	}
	return false
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

// placeholderBody is a placeholder without its braces, as a model may write
// one back: API_KEY_k3v9x2mq, phone_b5puzbfv, PHONE_B5PUZBFV.
var placeholderBody = regexp.MustCompile(`[A-Za-z][A-Za-z0-9_]*_[A-Za-z2-7]{8}`)

// Restore puts the values back in s. escaped is for text that is itself
// JSON (a tool call's arguments), where a value goes in escaped.
//
// A model doesn't always write a placeholder back as it was given: it
// writes {{ PHONE_b5puzbfv }} as a template has it, drops a brace, or the
// braces, or changes the case. Any of those is put back too when what is
// left names a placeholder this magpie made: its kind and the 8 of its
// hash, which no other text has by chance (shensmart on Discord).
func Restore(s string, escaped bool) string { return restore(s, escaped, nil) }

// restore is Restore, counting in put, when it isn't nil, each
// placeholder it put a value back for.
func restore(s string, escaped bool, put map[string]int) string {
	mu.RLock()
	defer mu.RUnlock()
	if len(values) == 0 {
		return s
	}
	s = restoreShapes(s, put)
	if !strings.Contains(s, "_") {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range placeholderBody.FindAllStringIndex(s, -1) {
		a, z := m[0], m[1]
		if a < last || a > 0 && isWordByte(s[a-1]) || z < len(s) && isWordByte(s[z]) {
			continue
		}
		p := canonical(s[a:z])
		v, ok := values[p]
		if !ok {
			continue
		}
		a, z = braces(s, a, z)
		if a < last {
			continue
		}
		if put != nil {
			put[p]++
		}
		b.WriteString(s[last:a])
		if escaped {
			v = jsonEscape(v)
		}
		b.WriteString(v)
		last = z
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// canonical is the placeholder Mask wrote for body, whatever its case.
func canonical(body string) string {
	cut := len(body) - 8
	return "{{" + strings.ToUpper(body[:cut]) + strings.ToLower(body[cut:]) + "}}"
}

func isWordByte(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_'
}

// braces widens s[a:z], a placeholder's body, over the braces the model
// wrote around it, and the spaces inside them: both of {{ and }}, or one
// of them dropped ({{X} or {X}}), or just {{ or just }}. A single brace
// on one side alone is the text's own ({X as in code) and stays.
func braces(s string, a, z int) (int, int) {
	la, l := a, 0
	for la > 0 && (s[la-1] == ' ' || s[la-1] == '\t') {
		la--
	}
	for l < 2 && la > 0 && s[la-1] == '{' {
		la--
		l++
	}
	rz, r := z, 0
	for rz < len(s) && (s[rz] == ' ' || s[rz] == '\t') {
		rz++
	}
	for r < 2 && rz < len(s) && s[rz] == '}' {
		rz++
		r++
	}
	if l == 2 || l == 1 && r >= 1 {
		a = la
	}
	if r == 2 || r == 1 && l >= 1 {
		z = rz
	}
	return a, z
}

// maxPlaceholder is the longest placeholder Mask writes: {{, a kind (a
// user's rule may name one up to maxKind long), _, the 8 of the hash, }}.
const maxPlaceholder = len("{{") + maxKind + len("_") + 8 + len("}}")

// partialTail is how much of the end of s may be the start of a
// placeholder the next piece of a stream finishes, in any of the ways
// Restore takes one back: {{API_, {{ phone_b5, PHONE_b5puzbfv} — or, with
// no brace before it, a last word that starts as a placeholder this magpie
// made does (PHO, PHONE_b5pu), or is one whose }} may still come.
func partialTail(s string) int {
	if n := braceTail(s); n > 0 {
		return n
	}
	return max(wordTail(s), shapeTail(s))
}

// braceTail is a tail from {, or {{, on: then spaces, a placeholder's body
// or the start of one, then spaces and one } at most.
func braceTail(s string) int {
	i := strings.LastIndexByte(s, '{')
	if i < 0 {
		return 0
	}
	if i > 0 && s[i-1] == '{' {
		i--
	}
	t := s[i:]
	if len(t) >= maxPlaceholder+4 || strings.Contains(t, "}}") {
		return 0
	}
	body := strings.TrimLeft(t, "{")
	body = strings.TrimLeft(body, " \t")
	if body == "" {
		return len(t) // {, {{, {{␠
	}
	if c := body[0]; !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z') {
		return 0
	}
	j := 0
	for j < len(body) && isWordByte(body[j]) {
		j++
	}
	rest := strings.TrimLeft(body[j:], " \t")
	if rest != "" && rest != "}" {
		return 0
	}
	if j < len(body) && placeholderBody.FindString(body[:j]) != body[:j] {
		return 0 // a space or a brace after something that is no placeholder
	}
	return len(t)
}

// wordTail is the last word of s, where it may be a placeholder's body as
// a model writes one without braces: the start of one of the kinds this
// magpie has made placeholders of, up to the 8 of its hash. A word of
// text that only starts like a kind is held for an event and then goes
// on, unchanged.
func wordTail(s string) int {
	i := len(s)
	for i > 0 && isWordByte(s[i-1]) {
		i--
	}
	w := s[i:]
	if w == "" || len(w) > maxKind+9 || !(w[0] >= 'A' && w[0] <= 'Z') {
		return 0
	}
	u := strings.ToUpper(w)
	mu.RLock()
	defer mu.RUnlock()
	for k := range kinds {
		k += "_"
		if strings.HasPrefix(k, u) {
			return len(w)
		}
		if strings.HasPrefix(u, k) && len(u)-len(k) <= 8 && strings.Trim(strings.ToLower(u[len(k):]), "abcdefghijklmnopqrstuvwxyz234567") == "" {
			return len(w)
		}
	}
	return 0
}
