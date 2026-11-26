package gui

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"strings"
)

// What a passkey sign-in checks of a browser's WebAuthn answer, with the
// standard library alone (no WebAuthn module: a new dependency is a review
// stop, docs/contributors.md). magpie asks for no attestation, so it reads
// the public key a registration carries and never judges who made the
// authenticator; it checks the challenge it gave, the page's origin, the
// relying party's id and, on a sign-in, the signature.

// COSE algorithms magpie takes: ES256, EdDSA and RS256, which every
// platform authenticator and security key makes one of.
const (
	coseES256 = -7
	coseEdDSA = -8
	coseRS256 = -257
)

// passkeyAlgs is the order the browser is asked to pick in.
var passkeyAlgs = []int{coseES256, coseEdDSA, coseRS256}

// authenticator data flags (WebAuthn §6.1)
const (
	flagUP = 0x01 // the user was present
	flagUV = 0x04 // the user was verified
	flagAT = 0x40 // a credential follows
	flagED = 0x80 // extensions follow
)

// b64u reads base64url with or without padding, as browsers and libraries
// write it either way.
func b64u(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

// clientData is the part of clientDataJSON magpie judges.
type clientData struct {
	Type        string `json:"type"`
	Challenge   string `json:"challenge"`
	Origin      string `json:"origin"`
	CrossOrigin bool   `json:"crossOrigin"`
}

// checkClientData reads clientDataJSON and checks it is of kind typ
// ("webauthn.create" or "webauthn.get"), for challenge, made by a page at
// origin, not in another site's frame.
func checkClientData(raw []byte, typ string, challenge []byte, origin string) error {
	var c clientData
	if err := json.Unmarshal(raw, &c); err != nil {
		return fmt.Errorf("the browser's answer isn't WebAuthn's: %v", err)
	}
	if c.Type != typ {
		return fmt.Errorf("the browser's answer is %q, not %q", c.Type, typ)
	}
	got, err := b64u(c.Challenge)
	if err != nil || len(challenge) == 0 || !bytes.Equal(got, challenge) {
		return errors.New("the browser's answer is for another challenge")
	}
	if c.Origin == "" || c.Origin != origin {
		return fmt.Errorf("the browser's answer came from %q, not %q", c.Origin, origin)
	}
	if c.CrossOrigin {
		return errors.New("the browser's answer came from a frame of another site")
	}
	return nil
}

// passkeyOrigin checks origin is one a passkey can be made at: https, or
// http on localhost (a secure context), on a name rather than an address
// (WebAuthn takes no IP address as a relying party's id). It returns the
// relying party's id, the origin's host name.
func passkeyOrigin(origin string) (rpID string, err error) {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.User != nil {
		return "", fmt.Errorf("%q isn't an origin", origin)
	}
	h := strings.ToLower(u.Hostname())
	switch {
	case net.ParseIP(h) != nil:
		return "", errPasskeyNeedsName
	case u.Scheme == "https":
	case u.Scheme == "http" && (h == "localhost" || strings.HasSuffix(h, ".localhost")):
	default:
		return "", errPasskeyNeedsHTTPS
	}
	return h, nil
}

var (
	errPasskeyNeedsHTTPS = errors.New("passkeys need https")
	errPasskeyNeedsName  = errors.New("passkeys need the page opened at a name, not an IP address")
)

// authData is authenticator data, read.
type authData struct {
	rpIDHash []byte
	flags    byte
	count    uint32
	credID   []byte // with flagAT
	cose     []byte // the credential's COSE key, with flagAT
}

// parseAuthData reads authenticator data (WebAuthn §6.1).
func parseAuthData(b []byte) (authData, error) {
	var a authData
	if len(b) < 37 {
		return a, errors.New("the authenticator data is too short")
	}
	a.rpIDHash, a.flags, a.count = b[:32], b[32], binary.BigEndian.Uint32(b[33:37])
	rest := b[37:]
	if a.flags&flagAT != 0 {
		if len(rest) < 18 {
			return a, errors.New("the authenticator data's credential is cut short")
		}
		n := int(binary.BigEndian.Uint16(rest[16:18]))
		rest = rest[18:]
		if n == 0 || n > 1023 || len(rest) < n {
			return a, errors.New("the authenticator data's credential id is cut short")
		}
		a.credID, rest = rest[:n], rest[n:]
		_, after, err := cborRead(rest, 0)
		if err != nil {
			return a, fmt.Errorf("the credential's public key: %v", err)
		}
		a.cose, rest = rest[:len(rest)-len(after)], after
	}
	if a.flags&flagED != 0 {
		_, after, err := cborRead(rest, 0)
		if err != nil {
			return a, fmt.Errorf("the authenticator data's extensions: %v", err)
		}
		rest = after
	}
	if len(rest) != 0 {
		return a, errors.New("the authenticator data has bytes past its end")
	}
	return a, nil
}

// checkAuthData checks a is for rpID and the user was there.
func checkAuthData(a authData, rpID string) error {
	want := sha256.Sum256([]byte(rpID))
	if !bytes.Equal(a.rpIDHash, want[:]) {
		return fmt.Errorf("the passkey is for another site than %s", rpID)
	}
	if a.flags&flagUP == 0 {
		return errors.New("the authenticator says no one was there")
	}
	return nil
}

// newCredential is what a registration gives: the credential's id, its
// public key as PKIX DER, its algorithm, and its signature count.
type newCredential struct {
	id    []byte
	spki  []byte
	alg   int
	count uint32
}

// readAttestation reads a registration's attestationObject for rpID: the
// authenticator data and the credential in it. Its attestation statement
// isn't judged, as magpie asks for none.
func readAttestation(raw []byte, rpID string) (newCredential, error) {
	var c newCredential
	v, rest, err := cborRead(raw, 0)
	if err != nil {
		return c, fmt.Errorf("the attestation: %v", err)
	}
	if len(rest) != 0 {
		return c, errors.New("the attestation has bytes past its end")
	}
	m, ok := v.(map[any]any)
	if !ok {
		return c, errors.New("the attestation isn't a map")
	}
	ad, ok := m["authData"].([]byte)
	if !ok {
		return c, errors.New("the attestation has no authenticator data")
	}
	a, err := parseAuthData(ad)
	if err != nil {
		return c, err
	}
	if err := checkAuthData(a, rpID); err != nil {
		return c, err
	}
	if a.flags&flagAT == 0 {
		return c, errors.New("the authenticator made no credential")
	}
	pub, alg, err := coseKey(a.cose)
	if err != nil {
		return c, err
	}
	spki, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return c, err
	}
	return newCredential{id: a.credID, spki: spki, alg: alg, count: a.count}, nil
}

// coseKey reads a COSE_Key (RFC 9053) of an algorithm magpie takes.
func coseKey(b []byte) (crypto.PublicKey, int, error) {
	v, rest, err := cborRead(b, 0)
	if err != nil || len(rest) != 0 {
		return nil, 0, errors.New("the credential's public key isn't COSE")
	}
	m, ok := v.(map[any]any)
	if !ok {
		return nil, 0, errors.New("the credential's public key isn't a COSE map")
	}
	num := func(k int64) (int64, bool) { n, ok := m[k].(int64); return n, ok }
	bs := func(k int64) []byte { b, _ := m[k].([]byte); return b }
	kty, _ := num(1)
	alg, _ := num(3)
	switch {
	case kty == 2 && alg == coseES256:
		crv, _ := num(-1)
		x, y := bs(-2), bs(-3)
		if crv != 1 || len(x) != 32 || len(y) != 32 {
			return nil, 0, errors.New("the credential's ES256 key isn't on P-256")
		}
		// a point off the curve is refused by ecdh, which crypto/ecdsa
		// keys go through on use; check it here so a bad one isn't kept
		pt := append([]byte{4}, append(append([]byte{}, x...), y...)...)
		pk, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), pt)
		if err != nil {
			return nil, 0, errors.New("the credential's ES256 key isn't a point on P-256")
		}
		return pk, coseES256, nil
	case kty == 1 && alg == coseEdDSA:
		crv, _ := num(-1)
		x := bs(-2)
		if crv != 6 || len(x) != ed25519.PublicKeySize {
			return nil, 0, errors.New("the credential's EdDSA key isn't Ed25519")
		}
		return ed25519.PublicKey(x), coseEdDSA, nil
	case kty == 3 && alg == coseRS256:
		n, e := bs(-1), bs(-2)
		if len(n) < 256 || len(e) == 0 || len(e) > 4 {
			return nil, 0, errors.New("the credential's RS256 key is under 2048 bits")
		}
		ev := new(big.Int).SetBytes(e)
		if !ev.IsInt64() || ev.Int64() < 3 || ev.Int64()%2 == 0 {
			return nil, 0, errors.New("the credential's RS256 exponent isn't usable")
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(ev.Int64())}, coseRS256, nil
	}
	return nil, 0, fmt.Errorf("the credential's key is of COSE type %d, algorithm %d, which magpie doesn't take", kty, alg)
}

// verifyAssertion checks sig over authenticator data and clientDataJSON's
// hash with the stored PKIX key of algorithm alg (WebAuthn §7.2 step 20).
func verifyAssertion(spki []byte, alg int, authenticatorData, clientDataJSON, sig []byte) error {
	pub, err := x509.ParsePKIXPublicKey(spki)
	if err != nil {
		return err
	}
	h := sha256.Sum256(clientDataJSON)
	signed := append(append([]byte{}, authenticatorData...), h[:]...)
	ok := false
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		if alg == coseES256 {
			d := sha256.Sum256(signed)
			ok = ecdsa.VerifyASN1(k, d[:], sig)
		}
	case ed25519.PublicKey:
		if alg == coseEdDSA {
			ok = ed25519.Verify(k, signed, sig)
		}
	case *rsa.PublicKey:
		if alg == coseRS256 {
			d := sha256.Sum256(signed)
			ok = rsa.VerifyPKCS1v15(k, crypto.SHA256, d[:], sig) == nil
		}
	}
	if !ok {
		return errors.New("the passkey's signature doesn't hold")
	}
	return nil
}

// cborRead reads one CBOR data item (RFC 8949) off b, as much of CBOR as
// WebAuthn's attestation objects and COSE keys use: integers, byte and
// text strings, arrays, maps (integer or text keys), tags (their content
// read, the tag dropped), and simple values and floats. Lengths are
// definite, as CTAP2's canonical CBOR has them. It returns the rest of b.
func cborRead(b []byte, depth int) (any, []byte, error) {
	if depth > 16 {
		return nil, nil, errors.New("CBOR nested too deep")
	}
	if len(b) == 0 {
		return nil, nil, errors.New("CBOR cut short")
	}
	major, info := b[0]>>5, b[0]&0x1f
	b = b[1:]
	var n uint64
	switch {
	case info < 24:
		n = uint64(info)
	case info == 24 && len(b) >= 1:
		n, b = uint64(b[0]), b[1:]
	case info == 25 && len(b) >= 2:
		n, b = uint64(binary.BigEndian.Uint16(b)), b[2:]
	case info == 26 && len(b) >= 4:
		n, b = uint64(binary.BigEndian.Uint32(b)), b[4:]
	case info == 27 && len(b) >= 8:
		n, b = binary.BigEndian.Uint64(b), b[8:]
	default:
		return nil, nil, errors.New("CBOR length cut short or indefinite")
	}
	switch major {
	case 0:
		if n > 1<<63-1 {
			return nil, nil, errors.New("CBOR integer out of range")
		}
		return int64(n), b, nil
	case 1:
		if n > 1<<63-1 {
			return nil, nil, errors.New("CBOR integer out of range")
		}
		return -1 - int64(n), b, nil
	case 2, 3:
		if n > uint64(len(b)) {
			return nil, nil, errors.New("CBOR string cut short")
		}
		if major == 3 {
			return string(b[:n]), b[n:], nil
		}
		return append([]byte{}, b[:n]...), b[n:], nil
	case 4:
		if n > uint64(len(b)) {
			return nil, nil, errors.New("CBOR array cut short")
		}
		out := make([]any, 0, n)
		for i := uint64(0); i < n; i++ {
			v, rest, err := cborRead(b, depth+1)
			if err != nil {
				return nil, nil, err
			}
			out, b = append(out, v), rest
		}
		return out, b, nil
	case 5:
		if n > uint64(len(b))/2 {
			return nil, nil, errors.New("CBOR map cut short")
		}
		out := make(map[any]any, n)
		for i := uint64(0); i < n; i++ {
			k, rest, err := cborRead(b, depth+1)
			if err != nil {
				return nil, nil, err
			}
			switch k.(type) {
			case int64, string:
			default:
				return nil, nil, errors.New("CBOR map key isn't an integer or text")
			}
			v, rest, err := cborRead(rest, depth+1)
			if err != nil {
				return nil, nil, err
			}
			if _, dup := out[k]; dup {
				return nil, nil, errors.New("CBOR map has a key twice")
			}
			out[k], b = v, rest
		}
		return out, b, nil
	case 6:
		return cborRead(b, depth+1)
	}
	// major 7: simple values and floats, whose bits were read as n
	switch {
	case info == 20:
		return false, b, nil
	case info == 21:
		return true, b, nil
	case info == 22, info == 23:
		return nil, b, nil
	case info >= 25 && info <= 27:
		return n, b, nil // a float's bits: nothing magpie reads is one
	case info < 24:
		return nil, b, nil
	}
	return nil, nil, errors.New("CBOR simple value isn't one")
}
