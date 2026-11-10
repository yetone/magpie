package redact

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/lazyre"
)

// A local model (internal/pii, Settings → Privacy's "Find with a local
// model") finds what no rule can: a name or an address in free text, a
// password with no password= before it. Its spans are masked beside the
// rules', under the same switches: a name, an address, a phone number, an
// email or an account number is personal data, a secret is a secret.

// Found is a span the model found in a text: byte offsets, its label,
// and how sure the model is of it (0 to 1; masking drops what is under
// the threshold the user set, internal/masking).
type Found struct {
	Start, End int
	Label      string
	Score      float64 `json:"score,omitempty"`
}

// Model finds spans in each of texts, in order. It never fails: one that
// can't find (the model didn't start) panics, so that the request is held
// back rather than sent unmasked (gwmw.Safely).
type Model func(texts []string) [][]Found

// modelKinds are the placeholders' kinds for the model's labels. Its
// private_date and private_url are left out: a date in a request is
// nearly always a log's or a version's, and a URL the agent's own.
var modelKinds = map[string]string{
	"private_person":  "PERSON",
	"private_address": "ADDRESS",
	"private_phone":   "PHONE",
	"private_email":   "EMAIL",
	"account_number":  "ACCOUNT",
	"secret":          "SECRET",
}

// modelCat is the category (Options.covers) of each kind the model finds.
func modelCat(kind string) string {
	if kind == "SECRET" {
		return ""
	}
	return catPersonal
}

// modelSpans are o.found's spans for s that o covers, trimmed of the
// spaces the model takes in with a word (and a Chinese name of what it
// ran on into, zhTrim), and with what it gets wrong often left out
// (modelTakes); and the Chinese names it missed (zhNameSpans: only
// nicknames where the model finds names itself, o.NamesByModel).
func modelSpans(s string, o Options) []span {
	if o.Model == nil || !o.covers(catPersonal) && len(o.found[s]) == 0 {
		return nil
	}
	var out []span
	for _, f := range o.found[s] {
		kind := modelKinds[f.Label]
		if kind == "" || !o.covers(modelCat(kind)) || f.Start < 0 || f.End > len(s) || f.Start >= f.End {
			continue
		}
		a, b := f.Start, f.End
		if kind == "SECRET" {
			// a JSON key the model took in with its value
			// (ledgerId":"gl_Wq7r…) is the key's, not the secret's
			if k := strings.LastIndex(s[a:b], `":"`); k >= 0 {
				a += k + 3
			}
		}
		edge := isSpace
		if kind != "SECRET" {
			edge = delimiter
		}
		for a < b && edge(s[a]) {
			a++
		}
		for b > a && edge(s[b-1]) {
			b--
		}
		if kind == "PERSON" {
			a, b = zhTrim(s, a, b)
		}
		if a < b && utf8.ValidString(s[a:b]) && modelTakes(kind, s[a:b]) && modelFits(s, a, b, kind, o.args) && !(kind == "PERSON" && zhWordNotName(s, a, b)) {
			out = append(out, span{a, b, kind, ByModel, f.Score, false})
		}
	}
	if o.covers(catPersonal) {
		var person []span
		for _, f := range out {
			if f.kind == "PERSON" {
				person = append(person, f)
			}
		}
		out = append(out, zhNameSpans(s, person, o.NamesByModel)...)
	}
	return out
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// delimiter says c may end a span of the model's and is no part of a
// name, a phone number, an address or an account: a space, or what is
// between values in JSON or a CSV row. The model's span may start at the
// token before the value, and a phone number in {"phone":"+86 139 …"}
// came as `":"+86 139 …`, or in a CSV row as `,+81 90-…`, and was no
// number to the PHONE check: it went as written (#3591). A secret's
// edges are its own (a password may start with a quote).
func delimiter(c byte) bool { return isSpace(c) || strings.IndexByte(`"',;:`, c) >= 0 }

// modelTakes says v is masked as the model's kind: a secret as long as the
// rules ask of one, never an id a vendor or a tool made (req_…, toolu_…,
// call_…), which the agent and the vendor match against another; a name
// of two letters at least, and never a snake_case word. The model took
// Codex's sequential_cutoff for a name, and a value the vendor checks
// against its own list went as a placeholder: 400 on every request until
// the local model was turned off (#3144). Such a word is a protocol's or a
// program's, wherever it is: a setting, a tool's argument, code.
func modelTakes(kind, v string) bool {
	if hexLiteral.MatchString(v) {
		return false
	}
	switch kind {
	case "SECRET":
		return len(v) >= 8 && !madeID(v) && !namesThing(v) && !versionValue(v)
	case "PERSON":
		// (an ordinary Chinese word, 文案 or 关键词, is left where it is
		// read: zhWordNotName)
		return utf8.RuneCountInString(v) >= 2 && strings.IndexFunc(v, unicode.IsLetter) >= 0 && !snakeWord(v)
	case "ACCOUNT":
		return !madeID(v)
	case "EMAIL":
		// as the rule's: a file's name (icon@2x.png) or an example's is no
		// address, nor a host's name with no @ (eu.stg.acme.io), nor a
		// symbol (system@got, foo@@GLIBC_2.2.5): realEmail's mailHost
		return strings.Contains(v, "@") && realEmail(v)
	}
	return true
}

// snakeWord says v is ASCII letters and digits joined by underscores
// (sequential_cutoff, list_threads, mcp__codex_app).
func snakeWord(v string) bool {
	if !strings.Contains(v, "_") || strings.Trim(v, "_") == "" {
		return false
	}
	for _, c := range v {
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// namesThing says v, which the model took for a secret, names a thing
// rather than unlocking one: a UUID (a session's, a turn's, a request's;
// window_id's has a :0 after it), a digest with its algorithm's name
// (sha256 9b74…), a file's path. The model takes all three for secrets;
// Codex's turn metadata went as {{SECRET_…}} in each id (#1977). A key or
// a password in a field named for one is still the rules' (secretNames).
func namesThing(v string) bool {
	if u, n, ok := strings.Cut(v, ":"); ok && n != "" && strings.Trim(n, digits) == "" {
		v = u
	}
	if uuidRe.MatchString(v) || digestRe.MatchString(v) || sriRe.MatchString(v) || slugRe.MatchString(v) || fileNameRe.MatchString(v) || modelName(v) {
		return true
	}
	return !strings.ContainsAny(v, " \t\n") && (strings.HasPrefix(v, "/") || strings.HasPrefix(v, "~/") ||
		strings.HasPrefix(v, "./") || strings.HasPrefix(v, "../") || len(v) > 3 && v[1] == ':' && v[2] == '\\')
}

// modelName says v is a model's or a toolchain's name: words in lower
// case joined by dashes, one of them a size (70b, 8x7b, 32k) or a date
// (20241022, 2026-09-01). The model took groqAPIModel = "llama3-70b-8192"
// and claudeAPIModel's claude-3-5-sonnet-20241022 for secrets (10-26).
// A key is written in neither case nor shape.
func modelName(v string) bool {
	if !dashedRe.MatchString(v) {
		return false
	}
	if dateIn.MatchString(v) {
		return true
	}
	for _, p := range strings.Split(v, "-") {
		if sizePartRe.MatchString(p) {
			return true
		}
	}
	return false
}

var (
	dashedRe   = lazyre.New(`^[a-z][a-z0-9.]*(?:-[a-z0-9.]+)+$`)
	sizePartRe = lazyre.New(`^(?:\d+x)?\d+(?:\.\d+)?[bkm]$|^20\d{6}$`)
	dateIn     = lazyre.New(`(?:^|-)20\d\d-[01]\d-[0-3]\d(?:-|$)`)
)

var (
	uuidRe = lazyre.New(`^(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	// a model's, a repo's or a package's name (qwen/qwen3-14b, acme/web)
	slugRe = lazyre.New(`^[a-z0-9][a-z0-9._@-]*(?:/[a-z0-9._@-]+)+$`)
	// a file's name in lower case (make-icons.sh, run_all.py)
	fileNameRe = lazyre.New(`^[a-z0-9][a-z0-9_-]*(?:\.[a-z0-9_-]+)*\.[a-z][a-z0-9]{0,4}$`)
	digestRe   = lazyre.New(`^(?i)(?:md5|sha-?(?:1|224|256|384|512)|blake2b?|blake3)[ :=-]+[0-9a-f]{32,128}$`)
	// a package's digest as a lock file pins it (Subresource Integrity:
	// package-lock.json's and pnpm-lock.yaml's "integrity"), sha512- and
	// the hash in base64, with the } or quote the model took in after it.
	// Privacy Filter took each one for a secret.
	sriRe = lazyre.New(`^sha(?:256|384|512)-[A-Za-z0-9+/]{43,86}={0,2}[}"',;)\]]*$`)
)

// madeID says v is an id of the shape vendors and tools give theirs: a
// short lower-case prefix, an underscore, then letters and digits
// (req_011CS…, toolu_01A…, call_x9…, msg_…), or more words and a nanoid's
// - and _ (req_gateway_7c41…, tb_Ra4Kp9-ZqT2_…). Stripe's keys have that
// shape too (sk_live_…, rk_test_…): one is no id.
func madeID(v string) bool {
	p, rest, ok := strings.Cut(v, "_")
	if !ok || len(p) < 2 || len(p) > 8 || len(rest) < 8 {
		return false
	}
	for _, c := range p {
		if c < 'a' || c > 'z' {
			return false
		}
	}
	plain := true
	for _, c := range rest {
		if c == '_' || c == '-' {
			plain = false
		} else if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return plain || !keyHeads[p]
}

// keyHeads are the prefixes of keys written as an id is (sk_live_…,
// whsec_…, shpat_…).
var keyHeads = map[string]bool{"sk": true, "rk": true, "pk": true, "whsec": true, "shpat": true, "shpss": true, "xoxb": true, "xoxp": true}

// withModel is o with what o.Model finds in texts, the texts that may be
// masked: each once, the short ones left out.
func withModel(o Options, texts []string) Options {
	if o.Model == nil || !o.Secrets && !o.Personal {
		return o
	}
	var ask []string
	seen := map[string]bool{}
	for _, t := range texts {
		if len(t) >= 3 && !seen[t] {
			seen[t] = true
			ask = append(ask, t)
		}
	}
	if len(ask) == 0 {
		return o
	}
	got := o.Model(ask)
	o.found = make(map[string][]Found, len(ask))
	for i, t := range ask {
		if i < len(got) && len(got[i]) > 0 {
			o.found[t] = got[i]
		}
	}
	return o
}

// modelFits says the model's value at s[a:b] is what its kind says by
// where it is, as modelTakes does by what it is. Run on what agents send
// (3 MB of Claude Code's requests, 10-25), the model masked ~1,800
// values where the rules masked 17, most of them wrongly: the count in
// each turn's <total_tokens>14983666 tokens left</total_tokens> as an
// account, a run's id as a phone, "Claude" and "Opus 5.5" in the system
// prompt as people, /Users/yetone's folder as a person, the tail of
// toolu_01… and req_011C… as a name or a secret, a grep pattern as a
// secret. Each goes as written now:
//   - a piece cut out of a longer word, whatever its kind;
//   - a name with a digit, a slash, an @ or a file's dot in it, all in
//     lower case (magpie, claude-in-chrome), a path's part, or an
//     assistant's or a model's own (Claude, Codex, Haiku 4.5);
//   - an account that is a commit's hash, a version, or a number with
//     nothing before it to say it is an account's; a phone number that is
//     digits only with nothing to say it is one, and no 0 or + to start,
//     or a Unix time whatever is before it;
//   - a secret that is a pattern's words, a|b|c, holds a UUID (a session's
//     file) or is a piece of JSON;
//   - an account or a phone number that is a size (1624x1487), drawing
//     data (an SVG path's M16 0H44…) or a diff's index line; a phone with
//     fewer than 7 digits, an address with no letter.
//
// Run on 22 more Claude Code sessions after a reporter on X found "95%"
// of the model's finds wrong (10-26), it still took 69 values the rules
// didn't, ~55 of them wrongly, and these go as written now too:
//   - a decimal (a metric's 104.21875033916251) or "run 48213097715" as a
//     phone;
//   - as a secret: a value right after an id's name (with ID: kq3m8zx2t,
//     <task-id>…, "backgroundTaskId":"…"), a model's or a repo's name
//     (qwen/qwen3-14b), a file's (make-icons.sh), a grep's regexp
//     (a\b\|b), a time in words (release about 17:05Z), and a hex run
//     whose key cue is further back than "key:" or "token is" (env -u
//     GH_TOKEN gh run view 48213097715);
//   - a path as an address, a host's name with no @ as an email;
//   - as a name, an acronym (CI), a product in camel case (CodeRabbit) and
//     a word the text also has as a host's name (Greptile, app.greptile.com).
//
// A name's words each start with a capital, but for the particles names
// have (van, de, bin): "local LLM", "User yetone" and claudeKeychain are
// a program's or a log's words.
func modelFits(s string, a, b int, kind string, args bool) bool {
	if a > 0 && wordByte(s[a-1]) && wordByte(s[a]) || b < len(s) && wordByte(s[b-1]) && wordByte(s[b]) {
		return false
	}
	v := s[a:b]
	if evmRe.MatchString(v) {
		// a wallet's or a contract's address (0x and 40 hex digits) is
		// public, on its chain for anyone to read, and the vendor's model
		// needs it as written: masked, it took the placeholder for a
		// private key and told the user to rotate it (0x0_air on X,
		// 10-28). A key is 64 hex digits; one called a key is masked still.
		return kind == "SECRET" && secretCued(s, a)
	}
	if packageAt(s, a, b) {
		return false
	}
	switch kind {
	case "PERSON":
		return personFits(s, a, b, v, args)
	case "ACCOUNT":
		if hexRe.MatchString(v) && strings.ContainsAny(v, "abcdef") || versionRe.MatchString(v) || !numberLike(v) {
			return false
		}
		return strings.Trim(v, digits) != "" || cued(s, a, accountCues)
	case "PHONE":
		if !numberLike(v) || countDigits(v) < 7 || epochNumber(v) || decimalRe.MatchString(v) || unicode.IsLetter(rune(v[0])) {
			return false
		}
		return strings.Trim(v, digits) != "" || v[0] == '0' || cued(s, a, phoneCues)
	case "ADDRESS":
		// a path is no address (-Users-x-projects-a/2b1a….jsonl); a Chinese
		// one has no spaces, a Latin one has them
		if isASCII(v) && !strings.ContainsAny(v, " \t\n") && strings.ContainsAny(v, `/\`) || programWords(v) {
			return false
		}
		return strings.IndexFunc(v, unicode.IsLetter) >= 0
	case "SECRET":
		// a name a call is made on is no value: gctx.GetInt64 in
		// gctx.GetInt64("user_id") went as a made-up key (#4444). Only a
		// name the '(' touches is a call's: in prose a secret may stand
		// before a bracket of words ("the token is k3Xq9vB2mZ (rotated)"),
		// and the model is asked about prose too, so notCode's wider
		// reading of the rules' values isn't taken here
		if b < len(s) && s[b] == '(' && identPath.MatchString(v) {
			return false
		}
		if !wordByte(v[0]) || patternWords(v) || regexpLike(v) || idAfter(s, a) || strings.ContainsRune(v, ' ') && clockIn.MatchString(v) || uuidIn.MatchString(v) || strings.Contains(v, `": `) || strings.Contains(v, `", "`) || linkRe.MatchString(v) {
			return false
		}
		// a piece of a longer base64 run (an image, a signature) is cut
		// from it at a '+' or '/'
		if a > 1 && (s[a-1] == '+' || s[a-1] == '/') && wordByte(s[a-2]) || b+1 < len(s) && (s[b] == '+' || s[b] == '/') && wordByte(s[b+1]) {
			return false
		}
		// a bare hex run is a hash or an id unless something calls it a key;
		// one with words after it is a hash in a tool's output
		if f := strings.Fields(v); len(f) > 1 && hexRe.MatchString(f[0]) || modCacheRe.MatchString(v) {
			return false
		}
		if pinnedAfter(s, b) {
			return false
		}
		if strings.Trim(v, digits) == "" {
			return numberNamedSecret(s, a)
		}
		if wordsOnly(v) && !assignedSecret(s, a) {
			return false
		}
		return !hexRe.MatchString(v) || secretCued(s, a)
	case "EMAIL":
		// a URL's user:password@host is no address, as for the rule's (#3538)
		return wordByte(v[0]) && !modCacheRe.MatchString(v) && !inUserinfo(s, a)
	}
	return true
}

// programWords says v, which the model took for a person's or a place's,
// is a program's words: it has an underscore (node_modules, __pycache__,
// node_modules 太, the model running on into the sentence after it), or
// it is Latin letters in lower case with no digit (dist, vendor,
// site-packages, ls node_modules). No name and no address is written so:
// a name has its capitals (personFits), an address its number or its
// capitals (42 Wallaby Way), a Chinese one its characters. The model took
// such a folder for personal data and the agent read a placeholder where
// the folder was ("node_modules给识别成姓名了", kekek711 on X, 10-29).
func programWords(v string) bool {
	if strings.Contains(v, "_") {
		return true
	}
	return isASCII(v) && strings.IndexFunc(v, unicode.IsLetter) >= 0 && strings.IndexFunc(v, func(r rune) bool { return unicode.IsUpper(r) || unicode.IsDigit(r) }) < 0
}

// wordsOnlyRe is words in lower case and nothing else, joined by - . or
// _: drizzle-orm, reflect-metadata, lodash.merge, secretstorage; and a
// folder's name as a path or Python writes it, with the slash after it
// or underscores around it: node_modules/, __pycache__.
var wordsOnlyRe = lazyre.New(`^_*[a-z]+(?:[-._][a-z]+)*_*/?$`)

// wordsOnly says v, which the model took for a secret, is made of words
// in lower case alone, as a package's, a tool's or a setting's name is.
// A key is random: it has a digit, a capital or a symbol in it, and the
// SECRET rules ask a digit of a value after password= too (secretValue).
// AstrLink Guard took drizzle-orm for a secret in a tsup config's
// external list in a Markdown fence (dniray on Discord, v0.1.1976; 0.515
// on that line alone), after v0.1.1932 and v0.1.1976 had each let it go
// in more places (an import, package.json, a tsc error): the places a
// package's name is written can't all be listed, so the shape decides. Such a value is a
// secret only where it is assigned to a secret's name (assignedSecret):
// PASSWORD=opensesame, "password": "letmeinplease", the wifi password is
// purplemonkey.
func wordsOnly(v string) bool { return wordsOnlyRe.MatchString(v) }

// assignedSecret says the value at s[a] is given to a secret's name: the
// name, then =, :, is or 是 (and the quotes around them), right before it.
// A list's comma before it is no assignment, whatever is further back:
// 'jsonwebtoken', 'drizzle-orm' (secretCued takes the token in that).
func assignedSecret(s string, a int) bool {
	before := strings.ToLower(strings.TrimRight(s[max(0, a-64):a], " \t\"'`“「"))
	for _, sep := range []string{"=", ":", "：", " is", " was", "是", "为", "為"} {
		if !strings.HasSuffix(before, sep) {
			continue
		}
		name := strings.TrimRight(before[:len(before)-len(sep)], " \t\"'`:")
		i := len(name)
		for i > 0 && (wordByte(name[i-1]) || name[i-1] == '.' || name[i-1] == '-') {
			i--
		}
		if i == len(name) {
			// a Chinese name: 密码是, 令牌为
			return slices.ContainsFunc(secretCues, func(c string) bool { return c[0] >= 0x80 && strings.HasSuffix(name, c) })
		}
		return slices.ContainsFunc(secretCues, func(c string) bool { return c[0] < 0x80 && strings.Contains(name[i:], c) }) ||
			strings.Contains(name[i:], "pass") || strings.Contains(name[i:], "pwd") || strings.Contains(name[i:], "credential")
	}
	return false
}

// pinnedAfter says the value that ends at s[b] is a package's name, as
// a requirement pins its version after it: django-rest-passwordreset==1.4.0,
// tokenizers>=0.15, a-lib~=2.1 (pip's comparisons). AstrLink Guard took a
// package with password in its name for a secret, and the agent read
// {{SECRET_…}}==1.4.0 in requirements.txt (dniray on Discord, v0.1.1917).
// No secret has a version pinned after it; a password before @ and an
// address (s3cret@10.0.0.5) is no pin.
func pinnedAfter(s string, b int) bool {
	rest := s[b:]
	for _, op := range []string{"===", "==", ">=", "<=", "~=", "!="} {
		if strings.HasPrefix(rest, op) {
			rest = strings.TrimLeft(rest[len(op):], " ")
			return rest != "" && rest[0] >= '0' && rest[0] <= '9'
		}
	}
	return false
}

// moduleRe is what code names a module by: drizzle-orm, @scope/name/sub,
// node:fs, ./db/schema, golang.org/x/net, serde_json::Value.
var moduleRe = lazyre.New(`^(?:[a-z]+:)?@?[A-Za-z0-9_.~-]+(?:(?:/|::)[A-Za-z0-9_.~@-]+)*$`)

// moduleAt says s[a:b] is the module a line of code loads: import { eq }
// from 'drizzle-orm', require(' jsonwebtoken '), import('x'), from
// django.contrib import auth, import torch, use jsonwebtoken::encode.
// AstrLink Guard took a package with a word like token in its name, or
// one alone in its quotes, for a secret (dniray on Discord, v0.1.1917:
// from ' {{SECRET_…}} '), and the agent read an import it could not
// resolve. A secret is never the module code loads. Prose is not code:
// "the token from 'a1b2…'" and "copy it from a1b2…" are judged as before,
// as a from counts only on an import or export line or after } or *.
func moduleAt(s string, a, b int) bool {
	if !moduleRe.MatchString(s[a:b]) {
		return false
	}
	line := strings.LastIndexByte(s[:a], '\n') + 1
	head := codeHead(s[line:a])
	end := strings.IndexByte(s[b:], '\n')
	if end < 0 {
		end = len(s) - b
	}
	tail := strings.TrimLeft(s[b:b+end], " \t")
	if q := strings.TrimRight(head, " \t"); q != "" && strings.ContainsRune("'\"`", rune(q[len(q)-1])) {
		// quoted, and ended in the same quote: from ' drizzle-orm '
		if tail == "" || tail[0] != q[len(q)-1] {
			return false
		}
		q = strings.TrimRight(q[:len(q)-1], " \t")
		if c := strings.TrimSuffix(q, "("); c != q {
			q = strings.TrimRight(c, " \t")
			return wordEnd(q, "require") || wordEnd(q, "import")
		}
		if !wordEnd(q, "from") {
			// import 'reflect-metadata'
			return q == "import"
		}
		q = strings.TrimRight(strings.TrimSuffix(q, "from"), " \t")
		return strings.HasPrefix(q, "import") || strings.HasPrefix(q, "export") ||
			strings.HasSuffix(q, "}") || strings.HasSuffix(q, "*") || q == ""
	}
	// unquoted, the word starts the line: Python's and Rust's
	switch strings.TrimRight(head, " \t") {
	case "from":
		return strings.HasPrefix(tail, "import ")
	case "import":
		return tail == "" || tail[0] == ',' || tail[0] == ';' || strings.HasPrefix(tail, "as ")
	case "use", "extern crate":
		return strings.HasPrefix(tail, "::") || tail == "" || tail[0] == ';' || tail[0] == '{'
	}
	return false
}

// codeHead is a line of code up to a value, without what a tool puts
// before the code: an editor's or cat -n's line number (Claude Code's
// Read gives "     1→import drizzle from 'drizzle-orm'", cat -n a tab,
// grep -n a colon) and a diff's + or -. With the number before it, a
// default import was no import line (dniray, v0.1.1965).
func codeHead(h string) string {
	h = strings.TrimLeft(h, " \t")
	i := 0
	for i < len(h) && h[i] >= '0' && h[i] <= '9' {
		i++
	}
	if i > 0 {
		for _, sep := range []string{"→", "\t", ":", "|"} {
			if strings.HasPrefix(h[i:], sep) {
				h = strings.TrimLeft(h[i+len(sep):], " \t")
				break
			}
		}
	}
	if h != "" && (h[0] == '+' || h[0] == '-') {
		h = strings.TrimLeft(h[1:], " \t")
	}
	return h
}

// packageAt says s[a:b] is a package's or a module's name by where it is:
// the module code loads (moduleAt), a dependency a manifest pins
// (dependencyAt), a folder under node_modules or site-packages, the
// module an error could not find (missingModuleAt), a package an install
// names (installedAt) or one pinned at its version with @
// (drizzle-orm@0.36.4, pinnedAt). The local model took drizzle-orm for a
// secret in package.json, a tsc error or a sentence; masked there, the
// name was known (knownSpans) and masked on the import line too, which
// moduleAt had let go when the model found it there (dniray on Discord,
// v0.1.1965: from ' {{SECRET_o2tu7vej}} ' still). knownSpans asks this
// too: no secret is a name code loads or a manifest pins, whatever
// found it before.
func packageAt(s string, a, b int) bool {
	// the whole of a name a piece of it was found in: drizzle-orm of
	// drizzle-orm/pg-core, name of @scope/name
	for b+1 < len(s) && s[b] == '/' && moduleByte(s[b+1]) {
		for b++; b < len(s) && moduleByte(s[b]); b++ {
		}
	}
	if a > 2 && s[a-1] == '/' {
		i := a - 1
		for i > 0 && moduleByte(s[i-1]) {
			i--
		}
		if i < a-2 && s[i] == '@' {
			a = i
		}
	}
	if !moduleRe.MatchString(s[a:b]) {
		return false
	}
	return moduleAt(s, a, b) || dependencyAt(s, a, b) || underPackages(s, a) ||
		missingModuleAt(s, a, b) || installedAt(s, a, b) || pinnedAt(s, a, b)
}

func moduleByte(c byte) bool { return wordByte(c) || c == '.' || c == '-' || c == '~' || c == '@' }

// dependencyAt says s[a:b] is a dependency's name as a manifest pins it:
// a JSON key whose value is a version or a range ("drizzle-orm":
// "^0.36.4", "x": "workspace:*") or TOML's at a line's start (jsonwebtoken
// = "9.2.0"). A key whose value is no version ({"Zq8v…": "hunter2"}) is
// judged as before.
func dependencyAt(s string, a, b int) bool {
	rest := ""
	if a > 0 && s[a-1] == '"' && b < len(s) && s[b] == '"' {
		rest = strings.TrimLeft(s[b+1:], " \t")
		if !strings.HasPrefix(rest, ":") {
			return false
		}
		rest = strings.TrimLeft(rest[1:], " \t")
	} else {
		line := strings.LastIndexByte(s[:a], '\n') + 1
		if strings.Trim(s[line:a], " \t") != "" {
			return false
		}
		rest = strings.TrimLeft(s[b:], " \t")
		if !strings.HasPrefix(rest, "=") || strings.HasPrefix(rest, "==") {
			return false
		}
		rest = strings.TrimLeft(rest[1:], " \t")
		if strings.HasPrefix(rest, "{") {
			// jsonwebtoken = { version = "9", features = […] }
			return strings.HasPrefix(strings.TrimLeft(rest[1:], " \t"), "version")
		}
	}
	if !strings.HasPrefix(rest, `"`) {
		return false
	}
	v, _, ok := strings.Cut(rest[1:], `"`)
	return ok && depVersion(v)
}

// depVersion says v is what a manifest pins a dependency at: a version
// or a range of them (^0.36.4, >=0.30.0 <1, 9, 1.x, *), a workspace's or
// a catalog's, a tag (latest, next), or where it comes from (npm:,
// file:, link:, git+…, github:).
func depVersion(v string) bool {
	for _, p := range []string{"workspace:", "catalog:", "npm:", "file:", "link:", "portal:", "git+", "git:", "github:", "http://", "https://"} {
		if strings.HasPrefix(v, p) {
			return true
		}
	}
	switch v {
	case "*", "latest", "next", "beta", "canary", "x":
		return true
	}
	for _, part := range strings.Fields(strings.NewReplacer("||", " ", " - ", " ").Replace(v)) {
		if !depRangeRe.MatchString(part) {
			return false
		}
	}
	return v != ""
}

var depRangeRe = lazyre.New(`^(?:[~^]|[<>]=?|=)?[vV]?\d+(?:\.(?:\d+|[xX*]))*(?:[-+][0-9A-Za-z.-]+)?$`)

// underPackages says the name at a is a folder packages are installed in:
// node_modules/drizzle-orm/…, site-packages/secretstorage/….
func underPackages(s string, a int) bool {
	return strings.HasSuffix(s[:a], "node_modules/") || strings.HasSuffix(s[:a], "node_modules\\") ||
		strings.HasSuffix(s[:a], "site-packages/") || strings.HasSuffix(s[:a], "site-packages\\")
}

// missingModuleAt says s[a:b] is quoted after the words an error names a
// module it could not load with: tsc's and Node's Cannot find module
// 'x', Python's No module named 'x', a bundler's Can't resolve 'x'.
//
// The words stand right before the quote, and what is read of what is
// before them is the last 64 bytes of it, as assignedSecret reads the 64
// before the value it judges (above). The whole text before the quote was
// read and lowered here, once for each place a known value turns up at in
// a session's body (knownSpans asks packageAt), so a long session paid its
// whole length again for every value already masked in it — a 555 KB turn
// held minutes in the masking and was answered 504 (#4766). The spaces
// before the quote are trimmed first, so the window ends at the words
// however many stand between. Lowering is rune by rune with no context,
// so lowering the window gives the same last bytes as lowering the whole
// text: a body written in Chinese, or one a rune cut at the window's
// start, is read the same way and as cheaply.
func missingModuleAt(s string, a, b int) bool {
	if a == 0 || b >= len(s) || !strings.ContainsRune("'\"`", rune(s[a-1])) || s[b] != s[a-1] {
		return false
	}
	t := strings.TrimRight(s[:a-1], " \t")
	q := strings.ToLower(t[max(0, len(t)-64):])
	for _, w := range []string{"find module", "find package", "module named", "resolve", "resolve import", "resolve module"} {
		if wordEnd(q, w) {
			return true
		}
	}
	return false
}

// installRe is a package manager's command that names packages after it.
var installRe = lazyre.New(`(?:^|[\s;&|(` + "`" + `$])(?:npm|pnpm|yarn|bun|npx|pip|pip3|uv|poetry|pipx|cargo|gem|composer|deno|go)(?: +(?:pip|-[\w-]+))* +(?:add|install|i|remove|rm|uninstall|un|get|require|update|upgrade|up)(?: |$)`)

// installedAt says s[a:b] is one of the packages a command on its line
// installs or removes: pnpm add drizzle-orm postgres, npm i -D x,
// pip install a-lib. A value after a flag for a credential (--token,
// --auth) is that flag's, and judged as before.
func installedAt(s string, a, b int) bool {
	if b < len(s) && !isSpace(s[b]) && s[b] != ';' && s[b] != '&' && s[b] != '|' && s[b] != ')' {
		return false
	}
	line := strings.LastIndexByte(s[:a], '\n') + 1
	ms := installRe.FindAllStringIndex(s[line:a], -1)
	if len(ms) == 0 {
		return false
	}
	args := strings.Fields(s[line+ms[len(ms)-1][1] : a])
	if a > 0 && !isSpace(s[a-1]) {
		return false
	}
	for _, w := range args {
		if strings.ContainsAny(w, ";&|<>`$") {
			return false
		}
	}
	if n := len(args); n > 0 && strings.HasPrefix(args[n-1], "-") && credFlag(args[n-1]) {
		return false
	}
	return true
}

// credFlag says a command's flag names a credential (--token, --auth-key).
func credFlag(f string) bool {
	f = strings.ToLower(f)
	for _, w := range []string{"token", "key", "pass", "secret", "auth", "cred"} {
		if strings.Contains(f, w) {
			return true
		}
	}
	return false
}

// pinnedAt says s[a:b] is a package's name pinned at a version with @, as
// npm and pnpm write one (drizzle-orm@0.36.4, js-tokens@4.0.0:): a name
// in lower case, as npm's are, and a version of three numbers that is no
// IPv4 address, so s3cretPass@10.0.0.5 is a password still.
func pinnedAt(s string, a, b int) bool {
	if b >= len(s) || s[b] != '@' || strings.ToLower(s[a:b]) != s[a:b] {
		return false
	}
	m := pinVersionRe.FindString(s[b+1:])
	return m != "" && !(len(s) > b+1+len(m) && s[b+1+len(m)] == '.')
}

var pinVersionRe = lazyre.New(`^[\^~]?v?\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]*[0-9A-Za-z])?`)

// wordEnd says s ends in the word w.
func wordEnd(s, w string) bool {
	return strings.HasSuffix(s, w) && (len(s) == len(w) || !wordByte(s[len(s)-len(w)-1]))
}

func wordByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func personFits(s string, a, b int, v string, args bool) bool {
	if strings.ContainsAny(v, "0123456789/\\@<>|=_") || strings.IndexFunc(v, unicode.IsUpper) < 0 && isASCII(v) {
		return false
	}
	// a file's name: LESSONS.md, a dot with a letter right after it
	for i := 0; i+1 < len(v); i++ {
		if v[i] == '.' && (v[i+1] >= 'a' && v[i+1] <= 'z' || v[i+1] >= 'A' && v[i+1] <= 'Z') {
			return false
		}
	}
	// a path's part: /Users/yetone, ~/.orca/
	if a > 0 && (s[a-1] == '/' || s[a-1] == '\\' || s[a-1] == '.' && a > 1 && (s[a-2] == '/' || s[a-2] == '\\')) || b < len(s) && (s[b] == '/' || s[b] == '\\') {
		return false
	}
	if strings.ContainsRune(v, ':') {
		return false
	}
	// an acronym (CI, API), a product's name in camel case (CodeRabbit,
	// OpenAI) and a site's (Greptile beside app.greptile.com) are no one's
	if w := strings.Fields(v); len(w) == 1 && (strings.Trim(v, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") == "" || camelProduct(v) || hostIn(s, v)) {
		return false
	}
	// an app's or a product's name the model took for a first name, where
	// its sentence speaks of a thing (thingname.go); never in a tool
	// call's arguments, which name whom a tool acts for
	// ({"recipient":"Rina"})
	if w := strings.Fields(v); len(w) == 1 && isASCII(v) && !args && thingNamed(s, a, b) {
		return false
	}
	for i, w := range strings.Fields(v) {
		if c := w[0]; c >= 'a' && c <= 'z' && (i == 0 || !nameParticles[w]) {
			return false
		}
		if assistantNames[strings.ToLower(w)] {
			return false
		}
	}
	return true
}

// camelProduct says v has a capital after three lower-case letters at
// least, as products are written (CodeRabbit, OpenAI) and names are not
// (McDonald, DeAndre, MacKenzie).
func camelProduct(v string) bool {
	lower := 0
	for i := 0; i < len(v); i++ {
		switch c := v[i]; {
		case c >= 'a' && c <= 'z':
			lower++
		case c >= 'A' && c <= 'Z':
			if lower >= 3 {
				return true
			}
			lower = 0
		default:
			lower = 0
		}
	}
	return false
}

// hostIn says s has the word w, in any case, as a host's name: a domain's
// first part or one after a dot (greptile.com, app.greptile.com), never
// an address's (matt.lee@…).
func hostIn(s, w string) bool {
	ls, lw := strings.ToLower(s), strings.ToLower(w)
	for i := 0; ; {
		k := strings.Index(ls[i:], lw+".")
		if k < 0 {
			return false
		}
		k += i
		i = k + 1
		if k > 0 && !strings.ContainsRune("/.@ \t\n\"'(`", rune(ls[k-1])) {
			continue
		}
		if m := hostTail.FindString(ls[k+len(lw):]); m != "" {
			return true
		}
	}
}

// hostTail is a domain's rest after its first part: .com, .greptile.com,
// ending where the host does (not at an @ or more of a word)
var hostTail = lazyre.New(`^(?:\.[a-z0-9-]+)*\.[a-z]{2,6}(?:[/:\s"'),\x60]|$)`)

// nameParticles are the words in lower case a name may have between its
// others: Ludwig van Beethoven, Ibn al Haytham.
var nameParticles = map[string]bool{
	"van": true, "von": true, "der": true, "den": true, "de": true, "da": true, "del": true, "della": true,
	"di": true, "du": true, "la": true, "le": true, "bin": true, "binti": true, "al": true, "el": true,
	"y": true, "dos": true, "das": true, "do": true, "ter": true, "ten": true, "zu": true,
}

// numberLike says v is written as a number is: digits, and the spaces,
// dashes, dots, brackets and + that group them, with a letter or two at
// most (an IBAN's, ext.). A size is no number (1624x1487).
func numberLike(v string) bool {
	if sizeRe.MatchString(v) || strings.Contains(v, "..") {
		return false
	}
	letters := 0
	for _, c := range v {
		switch {
		case c >= '0' && c <= '9', strings.ContainsRune(" -.()+/", c):
		case unicode.IsLetter(c):
			letters++
		default:
			return false
		}
	}
	return letters <= 4 && countDigits(v)*2 > len(v)
}

// epochNumber says v is a Unix time, as a log or a tool's JSON has one:
// ten digits, or thirteen in milliseconds, and nothing else, starting with
// 1. The model took "timestamp": 1792903687 for a phone, with "tool_call"
// before it as the cue (#3577). No phone is written so: a North American
// number's area code starts with 2 to 9, and a Chinese mobile has eleven
// digits (the PHONE rule's).
func epochNumber(v string) bool {
	if len(v) != 10 && len(v) != 13 || v[0] != '1' {
		return false
	}
	return strings.Trim(v, digits) == ""
}

func countDigits(v string) int {
	n := 0
	for i := 0; i < len(v); i++ {
		if v[i] >= '0' && v[i] <= '9' {
			n++
		}
	}
	return n
}

func isASCII(v string) bool {
	for i := 0; i < len(v); i++ {
		if v[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// assistantNames are what agents and models call themselves, in every
// system prompt: the model takes each for a person's name.
var assistantNames = map[string]bool{
	"claude": true, "chatgpt": true, "gpt": true, "codex": true, "gemini": true, "copilot": true,
	"grok": true, "kimi": true, "qwen": true, "deepseek": true, "cursor": true, "devin": true,
	"kiro": true, "opencode": true, "opus": true, "sonnet": true, "haiku": true, "fable": true,
	"glm": true, "doubao": true, "magpie": true, "jev": true,
}

var (
	hexRe = lazyre.New(`^[0-9a-f]{7,64}$`)
	// an EVM chain's address: 0x and 40 hex digits, in either case or
	// mixed (EIP-55's checksum)
	evmRe     = lazyre.New(`^0[xX][0-9a-fA-F]{40}$`)
	decimalRe = lazyre.New(`^\d+\.\d+$`)
	// a number written in hex as code and notes write one, of 64 bits at
	// most: a memory address (0x004EA5E0), a constant, a flag. The model
	// took such an address in reverse-engineering notes for a secret, and
	// the vendor's model read {{SECRET_…}} where the address was
	// (TakaNaShi_ShiKi on X); as an account number it went as
	// {{ACCOUNT_…}}. modelTakes takes one as no kind. No key is that
	// short, and a cue isn't asked as for a bare hex run (secretCued): the
	// function at an address is often named for a key (DecryptKey:
	// 0x004EA5E0). A key written so is longer (0x and 64 hex digits) and
	// is masked still.
	hexLiteral = lazyre.New(`^0[xX][0-9a-fA-F]{1,16}$`)
	// a time of day in words the model took for a passphrase (release
	// about 17:05Z)
	clockIn   = lazyre.New(`(?:^|\s)\d{1,2}:\d{2}(?::\d{2})?Z?(?:\s|$)`)
	versionRe = lazyre.New(`^v?\d+(?:\.\d+)+$`)
	sizeRe    = lazyre.New(`^\d+ ?[x×] ?\d+$`)
	uuidIn    = lazyre.New(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	// a name in Go's module cache (d6426ecaaf0a28de@v31)
	modCacheRe = lazyre.New(`^[0-9a-f]{16}@v\d+$`)
	// a link, or a host's name with a path (discord.gg/…)
	linkRe = lazyre.New(`^(?:[a-z][a-z0-9+.-]*://|[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+/)`)
)

var (
	accountCues = []string{"account", "acct", "a/c", "iban", "routing", "sort code", "card", "账号", "帐号", "账户", "帐户", "卡号", "户名"}
	secretCues  = []string{"key", "token", "secret", "password", "passwd", "密钥", "密码", "令牌"}
	phoneCues   = []string{"phone", "tel", "mobile", "cell", "call", "whatsapp", "sms", "电话", "手机", "联系", "致电"}
)

// cued says one of cues is in the 40 bytes before at, in any case.
func cued(s string, at int, cues []string) bool {
	before := strings.ToLower(s[max(0, at-40):at])
	for _, c := range cues {
		if strings.Contains(before, c) {
			return true
		}
	}
	return false
}

// regexpLike says v is a regexp's: grep's \| between alternatives, or a
// \b word boundary (qwen3-max\b\|qwen/qwen3).
func regexpLike(v string) bool {
	return strings.Contains(v, `\|`) || strings.Contains(v, `\b`)
}

// idAfter says the value at s[a:] is right after the word id, as a
// tool's output names a task's or a job's (with ID: kq3m8zx2t, (ID:
// bv7n2wq5p), <task-id>…, "backgroundTaskId":"…", "org_id": "…"): an id
// is no secret.
func idAfter(s string, a int) bool {
	t := strings.TrimRight(s[max(0, a-24):a], " :=#(>\"'`")
	n := len(t)
	if n < 2 || !strings.EqualFold(t[n-2:], "id") {
		return false
	}
	return n == 2 || !unicode.IsLetter(rune(t[n-3])) || t[n-2] == 'I' && t[n-1] == 'd'
}

// secretCued says a hex run at s[at:] is called a key, a token or a
// password: one of secretCues is right before it, with the cue's own word
// and an is, a colon or an = between (api key: 9f2c…, token is 9f2c…). A
// cue further back is about something else: GH_TOKEN in "env -u GH_TOKEN
// gh run view 48213097715" is not the run's.
func secretCued(s string, at int) bool {
	before := strings.ToLower(s[max(0, at-40):at])
	for _, c := range secretCues {
		k := strings.LastIndex(before, c)
		if k < 0 {
			continue
		}
		gap := strings.TrimLeftFunc(before[k+len(c):], func(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) })
		words := strings.FieldsFunc(gap, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
		if len(words) == 0 || len(words) == 1 && (words[0] == "is" || words[0] == "was" || words[0] == "是" || words[0] == "为") {
			return true
		}
	}
	return false
}

// numberNamedSecret says the run of decimal digits at s[a:], which the
// model took for a secret, is given a secret's name: the name right
// before it, then =, :, is or a tag's > (and quotes), ends with a key's,
// a token's, a password's or a PIN's word (api_key=, "token": , PIN:,
// 密码是). Digits alone are a count, a time or an id everywhere else.
// secretCued, which a hex run asks, takes a cue anywhere in the name and
// read Claude Code's <total_tokens>14998720 tokens left</total_tokens>
// as a token: every turn's count went as {{SECRET_…}} (steven_4422 on
// Discord, 10-31). A name that ends past the word counts what it names
// (total_tokens, max_tokens, token_count, keys).
func numberNamedSecret(s string, a int) bool {
	before := strings.ToLower(s[max(0, a-40):a])
	for {
		t := strings.TrimRight(before, " \t\"'`=:：>“「")
		for _, sep := range []string{" is", " was", "是", "为", "為"} {
			t = strings.TrimSuffix(t, sep)
		}
		if t == before {
			break
		}
		before = t
	}
	if before == "" {
		return false
	}
	for _, c := range numberSecretNames {
		// a PIN's word stands alone (pin, user_pin), not the end of
		// another's (spin, mapping)
		if strings.HasSuffix(before, c) && (c != "pin" || len(before) == 3 || !unicode.IsLetter(rune(before[len(before)-4]))) {
			return true
		}
	}
	return false
}

// numberSecretNames are what a name that gives digits a secret's place
// ends with.
var numberSecretNames = []string{"key", "token", "secret", "password", "passwd", "pass", "pwd", "passcode", "pin", "密钥", "密码", "令牌", "口令"}

// patternWords says v is three or more words joined by |, as a grep's or
// a regexp's alternatives are.
func patternWords(v string) bool {
	parts := strings.Split(v, "|")
	if len(parts) < 3 {
		return false
	}
	for _, p := range parts {
		if p == "" || strings.IndexFunc(p, unicode.IsLetter) < 0 || strings.Trim(p, alnum+"_.-") != "" {
			return false
		}
	}
	return true
}
