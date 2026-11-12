package update

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// A release's files are trusted by one signature, not by the feed's word.
// The feed (usemagpie.ai/api/latest) is only a courier: whoever can change
// its answer, the site, a proxy, a mirror, could swap a file and the hash
// beside it. Release CI signs "magpie release <version>\n" followed by the
// release's SHA256SUMS with a private key that lives only as the
// UPDATE_SIGNING_KEY secret of yetone/magpie-releases; the feed carries the
// SHA256SUMS text and the signature (Release.Signed), and magpie checks it
// against signingKey, below, which is built into the binary. A file's hash is
// then the one the signed list gives, and a feed hash that differs is refused.
//
// Versions from firstSigned on must carry a valid signature. A build that
// holds the key is a release from firstSigned on and only moves to a newer
// one, so every update it takes is of the signed era; the older releases
// (and the first update into the signed era, taken by a binary that has no
// key yet) are checked by SHA-256 from the feed as before.

// signingKey is the public half (base64, raw 32 bytes) of the release
// signing key. A var for tests.
var signingKey = "3XuYQEohIRDF+zxGQry1iXGZ1cfKYFX3vsST6GSSbYQ="

// firstSigned is the first release published with a SHA256SUMS.sig.
const firstSigned = "0.1.3862"

// Signed is a release's SHA256SUMS as published and its ed25519 signature
// (base64).
type Signed struct {
	Sums string `json:"sums"`
	Sig  string `json:"sig"`
}

// signedRequired reports whether version must carry a valid signature.
// MAGPIE_UPDATE_FEED, which points updates at a server of one's own for
// testing, lifts the requirement (that server can't sign with our key); a
// signature it does give is still checked.
func signedRequired(version string) bool {
	if os.Getenv("MAGPIE_UPDATE_FEED") != "" {
		return false
	}
	return !Newer(firstSigned, version)
}

// verify checks rel against the embedded key and, when it is signed, sets
// each asset's SHA256 to the signed list's and drops the files that list
// doesn't name. A release that must be signed and isn't, whose signature
// fails, or whose feed hash differs from the signed one is an error.
func (rel *Release) verify() error {
	if rel.Signed == nil {
		if signedRequired(rel.Version) {
			return fmt.Errorf("release %s is not signed; not installed", rel.Version)
		}
		return nil
	}
	pub, err := base64.StdEncoding.DecodeString(signingKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return errors.New("update signing key is not valid")
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(rel.Signed.Sig))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("release %s: the signature is not valid; not installed", rel.Version)
	}
	msg := "magpie release " + rel.Version + "\n" + rel.Signed.Sums
	if !ed25519.Verify(pub, []byte(msg), sig) {
		return fmt.Errorf("release %s: the signature does not match; not installed", rel.Version)
	}
	sums := map[string]string{}
	for _, line := range strings.Split(rel.Signed.Sums, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		if b, err := hex.DecodeString(f[0]); err != nil || len(b) != 32 {
			continue
		}
		sums[strings.TrimPrefix(f[1], "*")] = strings.ToLower(f[0])
	}
	for name, a := range rel.Assets {
		want, ok := sums[name]
		if !ok {
			delete(rel.Assets, name)
			continue
		}
		if a.SHA256 != "" && !strings.EqualFold(a.SHA256, want) {
			return fmt.Errorf("release %s: the feed's checksum for %s is not the signed one; not installed", rel.Version, name)
		}
		a.SHA256 = want
		rel.Assets[name] = a
	}
	return nil
}
