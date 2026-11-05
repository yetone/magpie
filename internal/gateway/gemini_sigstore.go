package gateway

import (
	"crypto/sha256"
	"encoding/base64"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// sigStore keeps Gemini's thought signatures that a tool call's id refers
// to (#2411), one file per signature under magpie's folder, named by its
// hash: the same signature is one file however often it comes, and two
// magpies writing it write the same bytes. A file is written whole
// (temp-and-rename) so a reader never sees half of one. A signature not
// read for sigKeep is removed; its call then goes to Gemini as unsigned
// history, which Gemini takes.
const (
	sigKeyBytes = 12 // 16 base64url characters in the id
	sigKeep     = 90 * 24 * time.Hour
)

var (
	sigDir = func() string { return filepath.Join(settings.Dir(), "gemini-signatures") }

	sigPruneMu   sync.Mutex
	sigPrunedAt  time.Time
	sigPruneTick = time.Hour // a write lists the folder at most this often
)

func sigKey(sig string) string {
	h := sha256.Sum256([]byte(sig))
	return base64.RawURLEncoding.EncodeToString(h[:sigKeyBytes])
}

// keepSignature stores sig and gives the key an id refers to it by, or ""
// when it couldn't be stored: the id then carries the signature itself.
func keepSignature(sig string) string {
	key, dir := sigKey(sig), sigDir()
	path := filepath.Join(dir, key)
	if b, err := os.ReadFile(path); err == nil && string(b) == sig {
		now := time.Now()
		os.Chtimes(path, now, now)
		return key
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return ""
	}
	_, werr := tmp.WriteString(sig)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), path) != nil {
		os.Remove(tmp.Name())
		return ""
	}
	pruneSignatures(dir)
	return key
}

// keptSignature is the signature stored under key, or "" when magpie no
// longer has it or its file doesn't hash to key.
func keptSignature(key string) string {
	if len(key) != base64.RawURLEncoding.EncodedLen(sigKeyBytes) {
		return ""
	}
	if _, err := base64.RawURLEncoding.DecodeString(key); err != nil {
		return ""
	}
	path := filepath.Join(sigDir(), key)
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 || sigKey(string(b)) != key {
		return ""
	}
	now := time.Now()
	os.Chtimes(path, now, now)
	return string(b)
}

// pruneSignatures removes, at most once per sigPruneTick, the signatures
// not read for sigKeep and temp files a write left behind.
func pruneSignatures(dir string) {
	sigPruneMu.Lock()
	if time.Since(sigPrunedAt) < sigPruneTick {
		sigPruneMu.Unlock()
		return
	}
	sigPrunedAt = time.Now()
	sigPruneMu.Unlock()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || e.IsDir() {
			continue
		}
		age := time.Since(info.ModTime())
		if age > sigKeep || (e.Name()[0] == '.' && age > time.Hour) {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// Which provider gave a signature (a reply's tool call or text): a
// signature goes back only to the provider that gave it (ownSignatures).
// Vertex AI and AI Studio serve the same Gemini models, and a routing
// group of both falls over from one to the other mid-conversation; the
// other answers a signature it didn't give with 400 "Corrupted thought
// signature" (mimi001234 on Discord). It is kept beside the signature,
// <key>.by, the provider's id in it, keyed by the signature's hash, so
// it reads the same whichever way the signature came back: in a call's
// id, in extra_content, or in a Gemini client's own thoughtSignature.
const signerExt = ".by"

// noteSigner records that provider by gave sig. A record that couldn't
// be written leaves the signature's giver unknown (signerOf).
func noteSigner(sig, by string) {
	if sig == "" || by == "" || sig == skipSignature {
		return
	}
	dir := sigDir()
	path := filepath.Join(dir, sigKey(sig)+signerExt)
	if b, err := os.ReadFile(path); err == nil && string(b) == by {
		now := time.Now()
		os.Chtimes(path, now, now)
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return
	}
	_, werr := tmp.WriteString(by)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), path) != nil {
		os.Remove(tmp.Name())
		return
	}
	pruneSignatures(dir)
}

// signerOf is the id of the provider that gave sig, or "" when magpie
// doesn't know: a signature from before signers were kept, one whose
// record couldn't be written or read, or one a client made up.
func signerOf(sig string) string {
	if sig == "" || sig == skipSignature {
		return ""
	}
	path := filepath.Join(sigDir(), sigKey(sig)+signerExt)
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	now := time.Now()
	os.Chtimes(path, now, now)
	return string(b)
}

// foreignSignature says sig is known to come from a provider other than
// by. A signature whose giver isn't known goes as it did before signers
// were kept: dropping it would cost every conversation from before the
// upgrade its signatures on the very provider that gave them, and the
// one that can't read it says so (signatureRefused), which is answered
// by asking again without.
func foreignSignature(sig, by string) bool {
	from := signerOf(sig)
	return from != "" && from != by
}
