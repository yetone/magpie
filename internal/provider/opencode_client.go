package provider

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"regexp"
	"strings"
)

// OpenCodeVersion is the OpenCode release magpie says it is to OpenCode's
// gateway. Zen's free tier turns away anything older than 1.18.0 (426
// "OpenCode 1.18.0 or newer is required to use the free tier").
const OpenCodeVersion = "1.18.34"

// OpenCodeClient makes a request to OpenCode's gateway (Zen or Go) carry
// what OpenCode itself sends there (packages/opencode/src/session/llm/
// request.ts, for a provider whose id starts with "opencode"): its
// User-Agent, the session in x-opencode-session, the turn in
// x-opencode-request, and x-opencode-client and x-opencode-project. Zen's
// free models are served to no one else ("OpenCode's free tier can only be
// used from within OpenCode"). session names the conversation; one already
// in OpenCode's form (ses_…) goes as it is, any other becomes one, the same
// for the same conversation, which Zen routes and caches by.
func OpenCodeClient(h http.Header, session string) {
	h.Set("User-Agent", "opencode/"+OpenCodeVersion)
	h.Set("x-opencode-session", openCodeID("ses", session))
	h.Set("x-opencode-request", openCodeID("msg", ""))
	h.Set("x-opencode-client", "cli")
	// OpenCode's project outside a git repository
	h.Set("x-opencode-project", "global")
}

var openCodeIDRe = regexp.MustCompile(`^[a-z]{3}_[0-9a-f]{12}[0-9A-Za-z]{14}$`)

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// openCodeID is an id as OpenCode makes them (prefix_, 12 hex digits and 14
// base62 ones): seed's own when it is one with this prefix, else made from
// seed, or at random when there is none.
func openCodeID(prefix, seed string) string {
	if strings.HasPrefix(seed, prefix+"_") && openCodeIDRe.MatchString(seed) {
		return seed
	}
	var b [32]byte
	if seed != "" {
		b = sha256.Sum256([]byte(seed))
	} else if _, err := rand.Read(b[:]); err != nil {
		b = sha256.Sum256([]byte(randomUUID()))
	}
	id := []byte(prefix + "_" + hex.EncodeToString(b[:6]))
	for _, c := range b[6:20] {
		id = append(id, base62[int(c)%len(base62)])
	}
	return string(id)
}
