package redact

import (
	"regexp"
	"strings"
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
var secretName = regexp.MustCompile(`(?i)` + secretNames.pattern() + `|authori[sz]ation|cookie|^key$`)

// SecretName says a field or header called name holds a secret: a token,
// key, password or cookie — not a count of tokens (max_tokens).
func SecretName(name string) bool {
	n := strings.ToLower(name)
	return secretName.MatchString(n) && !strings.HasSuffix(n, "tokens") && !strings.HasSuffix(n, "token_count")
}

// Scrub takes the secrets Mask finds out of s.
func Scrub(s string) string {
	out, _ := mask(s, Options{Secrets: true}, scrubbed)
	return out
}

// ScrubHeader is a header's value with its secrets taken out: all of it
// for a header named for one or a credential (Bearer, Basic), else what
// Scrub finds in it.
func ScrubHeader(name, value string) string {
	v := strings.ToLower(strings.TrimSpace(value))
	if SecretName(name) || strings.HasPrefix(v, "bearer ") || strings.HasPrefix(v, "basic ") {
		return Scrubbed
	}
	return Scrub(value)
}

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
