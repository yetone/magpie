package redact

import (
	"crypto/sha256"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/yetone/magpie/internal/lazyre"
)

// Scrubbing is for what is kept rather than sent — the request archive: a
// secret goes for good, as [REDACTED:KIND], with no placeholder made and
// nothing held to put it back. Everything Mask finds as a secret goes,
// and whatever is in a field or header named for one (Authorization,
// x-api-key, Cookie, access_token, client_secret…), whatever it looks like.

// Scrubbed is what a field or header named for a secret holds once scrubbed.
const Scrubbed = "[REDACTED]"

func scrubbed(kind, _ string) string { return "[REDACTED:" + kind + "]" }

// secretName is a field's or header's name that says its value is a secret.
// A name that ends in pass (a db_pass) is one too (#2946); a name that only
// ends in key is not — a sort key, a cache key, a public key hold no secret.
var secretName = lazyre.New(`(?i)` + secretNames.pattern() + `|authori[sz]ation|cookie|^key$|^pass$|_pass$`)

// SecretName says a field or header called name holds a secret: a token,
// key, password or cookie — not a count of tokens (max_tokens).
func SecretName(name string) bool {
	n := strings.ToLower(name)
	return secretName.MatchString(n) && !strings.HasSuffix(n, "tokens") && !strings.HasSuffix(n, "token_count")
}

// Scrub takes the secrets Mask finds out of s.
func Scrub(s string) string {
	if len(s) < scrubMemoMin {
		return scrub(s)
	}
	// What Scrub makes of s depends on s and on the values known (known.go)
	// alone: a result is kept under the knownGen it was made at, and none
	// made before a value became known is used again.
	gen := knownGen.Load()
	k := sha256.Sum256([]byte(s))
	if out, ok := scrubMemo.get(k, gen, s); ok {
		return out
	}
	out := scrub(s)
	scrubMemo.put(k, gen, s, out)
	return out
}

func scrub(s string) string {
	if len(s) >= 256 {
		scrubRuns.Add(1)
	}
	out, _ := mask(s, Options{Secrets: true}, scrubbed, false)
	return out
}

// scrubRuns counts the strings of 256 bytes or more the rules have run
// over (a test reads it).
var scrubRuns atomic.Uint64

// Scrub remembers what it made of a long string. An agent sends its whole
// history on every turn (Codex: every item of a session, each turn), and
// what is kept of each request (the request archive, a gateway
// conversation) is scrubbed whole, so without it every turn ran each rule
// over the whole history again: a core's worth while Codex ran
// (yetone/magpie-releases#24). A short string costs less to scrub than to
// look up. Only a hash of a string is kept, and what it became when that
// is not the string itself.
const (
	scrubMemoMin     = 256
	scrubMemoEntries = 1 << 15
	scrubMemoBytes   = 32 << 20
)

type scrubEntry struct {
	gen  uint64
	same bool   // s came back as it was
	out  string // else what it became
}

// scrubMemo holds two generations of results: when the newer is full, it
// becomes the older and the older goes, so what is in use stays and at
// most twice the bounds are held.
var scrubMemo memo

type memo struct {
	sync.Mutex
	cur, old map[[32]byte]scrubEntry
	bytes    int
}

func (m *memo) get(k [32]byte, gen uint64, s string) (string, bool) {
	m.Lock()
	defer m.Unlock()
	e, ok := m.cur[k]
	if !ok {
		if e, ok = m.old[k]; ok && e.gen == gen {
			m.add(k, e)
		}
	}
	if !ok || e.gen != gen {
		return "", false
	}
	if e.same {
		return s, true
	}
	return e.out, true
}

func (m *memo) put(k [32]byte, gen uint64, s, out string) {
	e := scrubEntry{gen: gen, same: out == s}
	if !e.same {
		e.out = strings.Clone(out)
	}
	m.Lock()
	defer m.Unlock()
	m.add(k, e)
}

// reset forgets everything (a test starts from nothing).
func (m *memo) reset() {
	m.Lock()
	defer m.Unlock()
	m.cur, m.old, m.bytes = nil, nil, 0
}

// add is called with m held.
func (m *memo) add(k [32]byte, e scrubEntry) {
	if m.cur == nil || len(m.cur) >= scrubMemoEntries || m.bytes+len(e.out) > scrubMemoBytes {
		m.old, m.cur, m.bytes = m.cur, make(map[[32]byte]scrubEntry), 0
	}
	m.bytes += len(e.out) - len(m.cur[k].out)
	m.cur[k] = e
}

// ScrubHeader is a header's value with its secrets taken out: all of it
// for a header named for one or a credential (Bearer, Basic), else what
// Scrub finds in it.
func ScrubHeader(name, value string) string {
	v := strings.ToLower(strings.TrimSpace(value))
	if SecretHeader(name) || strings.HasPrefix(v, "bearer ") || strings.HasPrefix(v, "basic ") {
		return Scrubbed
	}
	return Scrub(value)
}

// SecretHeader says a header called name carries a credential, by the
// words of its name: Authorization, Cookie and Set-Cookie, x-api-key and
// api-key, X-Goog-Api-Key, an X-Client-Key, any *-Token (Copilot's
// Copilot-Session-Token, X-Amz-Security-Token), *-Secret, *-Auth, a
// request's *-Signature. A count of tokens is no secret: Anthropic's
// anthropic-ratelimit-tokens-remaining and OpenAI's
// x-ratelimit-remaining-tokens are what a rate limit is debugged by. Nor
// is a key that names no credential: Idempotency-Key.
func SecretHeader(name string) bool {
	n := strings.ToLower(name)
	words := strings.FieldsFunc(n, func(r rune) bool { return r == '-' || r == '_' || r == '.' })
	kept := words[:0:0]
	for i, w := range words {
		switch w {
		case "authorization", "authorisation", "auth", "cookie", "token", "secret", "password", "passwd",
			"pass", "credential", "credentials", "signature", "apikey", "csrf", "xsrf":
			return true
		case "key":
			if i == 0 || !headerKeyNotSecret[words[i-1]] {
				return true
			}
		case "tokens":
			continue // a count of them
		}
		for _, end := range []string{"token", "secret", "cookie", "apikey", "password"} {
			if strings.HasSuffix(w, end) {
				return true // accesstoken, clientsecret, setcookie
			}
		}
		kept = append(kept, w)
	}
	// what a field's name says (x-access-key, x-private-key-id), its count
	// of tokens left out
	return SecretName(strings.Join(kept, "-"))
}

// headerKeyNotSecret are the words before "key" in a header that names no
// credential.
var headerKeyNotSecret = map[string]bool{"idempotency": true, "cache": true, "public": true, "sort": true, "partition": true}

// ScrubJSON is a body with its secrets taken out: a JSON one string by
// string, a field named for a secret wholly; a stream's events each as
// JSON, line by line; anything else as text. A picture's data is left as
// it is.
func ScrubJSON(body []byte) []byte {
	if out, ok := scrubJSON(body); ok {
		return out
	}
	lines := strings.SplitAfter(string(body), "\n")
	for i, l := range lines {
		if rest, ok := strings.CutPrefix(l, "data:"); ok {
			trimmed := strings.TrimSpace(rest)
			if out, ok := scrubJSON([]byte(trimmed)); ok && trimmed != "" {
				lines[i] = "data: " + string(out) + l[len(strings.TrimRight(l, "\r\n")):]
				continue
			}
		}
		lines[i] = Scrub(l)
	}
	return []byte(strings.Join(lines, ""))
}

func scrubJSON(b []byte) ([]byte, bool) {
	return walk(b, func(_, key, s string) string {
		switch {
		case strings.HasPrefix(s, "data:"):
			return s
		case key != "" && SecretName(key):
			return Scrubbed
		}
		return Scrub(s)
	})
}
