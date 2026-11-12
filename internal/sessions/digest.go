package sessions

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// digest is a file's head or sampled prefix as kept to know it again
// (headDigest, prefixHash): the first 16 bytes of its SHA-256, in the
// state itself. As hex strings, 64 and 74 bytes of their own, they were
// 144 bytes a file beside its state; 18k files' states held 2.6 MB of
// them (#2278). The zero digest is none: the file couldn't be read.
type digest [16]byte

// MarshalText is d in hex, "" for none.
func (d digest) MarshalText() ([]byte, error) {
	if d == (digest{}) {
		return nil, nil
	}
	return []byte(hex.EncodeToString(d[:])), nil
}

// UnmarshalText reads a digest as written, or as a magpie before it wrote
// the whole hash: the 64 hex of hashHead's, or prefixHash's "sample-v1:"
// before them, whose first 32 are this digest. A cache from before reads
// as the same files, so nothing is read again for it. Anything else is
// none, which matches no file.
func (d *digest) UnmarshalText(b []byte) error {
	*d = parseDigest(string(b))
	return nil
}

// parseDigest is the digest s spells (UnmarshalText), none when it spells
// none.
func parseDigest(s string) digest {
	var d digest
	s = strings.TrimPrefix(s, "sample-v1:")
	if (len(s) != 32 && len(s) != 64) || s == "" {
		return d
	}
	if _, err := hex.Decode(d[:], []byte(s[:32])); err != nil {
		return digest{}
	}
	return d
}

// String is d as MarshalText writes it, for the shards' gob, whose
// field stays a string so a shard written before reads the same.
func (d digest) String() string {
	b, _ := d.MarshalText()
	return string(b)
}

// headDigest is the digest of a file's first bytes, head.
func headDigest(head string) digest {
	sum := sha256.Sum256([]byte(head))
	return digest(sum[:16])
}
