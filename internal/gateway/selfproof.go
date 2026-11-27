package gateway

// Is the magpie at an address this magpie, or another one? Any magpie's
// GET / names itself "magpie", so a reverse proxy in front of this
// gateway (#1985) and a magpie on a server of the user's (Alex on
// Discord: the cloud magpie imported into Nowledge Mem) answer the same,
// and the Agents page counted an agent on the cloud magpie as connected
// here. A probe asks GET /?self=<nonce> and this magpie answers
// self: HMAC-SHA256(its secret, nonce). The secret is made up once, kept
// in the settings folder, so every magpie process of this computer (the
// app, a `magpie serve`) answers alike, and never leaves it: another
// magpie can't answer for this one, nor can a relay that echoes what it
// is sent.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/settings"
)

// SelfParam is the query parameter a probe's nonce goes in. A query, not
// a header: a cache in front of a proxy keys on it.
const SelfParam = "self"

var selfKey struct {
	sync.Mutex
	path string
	key  []byte
}

// selfSecret is this magpie's secret, made the first time it is asked
// for: written to a file of its own and linked into place, so two
// processes making it at once agree on the one that got there first. nil
// when it can't be read or made, which proves nothing.
func selfSecret() []byte {
	p := filepath.Join(settings.Dir(), "self-secret")
	selfKey.Lock()
	defer selfKey.Unlock()
	if selfKey.path == p {
		return selfKey.key
	}
	read := func() []byte {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		k, err := hex.DecodeString(strings.TrimSpace(string(b)))
		if err != nil || len(k) != 32 {
			return nil
		}
		return k
	}
	k := read()
	if k == nil {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return nil
		}
		tmp, err := os.CreateTemp(filepath.Dir(p), ".self-secret-*")
		if err != nil {
			return nil
		}
		_, werr := tmp.WriteString(hex.EncodeToString(b) + "\n")
		cerr := tmp.Close()
		if werr == nil && cerr == nil {
			os.Link(tmp.Name(), p) // fails when another got there first
		}
		os.Remove(tmp.Name())
		if k = read(); k == nil {
			return nil
		}
	}
	selfKey.path, selfKey.key = p, k
	return k
}

// SelfProof is what this magpie answers a probe's nonce with; "" when it
// has no secret to answer with.
func SelfProof(nonce string) string {
	k := selfSecret()
	if k == nil || nonce == "" {
		return ""
	}
	m := hmac.New(sha256.New, k)
	m.Write([]byte(nonce))
	return hex.EncodeToString(m.Sum(nil))
}

// IsSelfProof says proof is this magpie's answer to nonce.
func IsSelfProof(nonce, proof string) bool {
	want := SelfProof(nonce)
	return want != "" && hmac.Equal([]byte(want), []byte(proof))
}
