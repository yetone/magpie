package redact

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	_ "embed"
	"encoding/base32"
	"math/big"
	"strconv"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/lazyre"
)

// Crypto wallet secrets, masked with the other secrets (Options.Secrets):
// the private keys wallets and chain tools export, in the shapes they
// export them in. A wallet key is a secret like an API key, and no vendor
// needs one. An agent that pastes a keyfile into a message sends it to
// every provider behind magpie. (Rex Zhou, #4025)
//
// A shape alone is no key where a hash or a signature has it too, so a
// shape with no prefix of its own is taken only when the key proves
// itself: Bitcoin's WIF and BIP-0032's xprv carry a base58check checksum,
// a Stellar seed a CRC16, and a Solana key is the 32-byte seed followed
// by its own public key, which ed25519 derives again. A Solana
// transaction signature has the same 64 bytes and 88 characters, and
// fails that. A shape with a prefix no other value has (suiprivkey1,
// nsec1, AGE-SECRET-KEY-1, ed25519-priv-0x, secret-extended-key-) is
// taken by that prefix.
//
// An EVM key is 0x and 64 hex digits, which is also a transaction hash, a
// block hash, a storage slot and every keccak constant in Solidity, and
// has no checksum. It is taken only after a name that says it is a key:
// PRIVATE_KEY, --private-key, signer, deployer, new Wallet(…), 私钥. A
// bare 64 hex digits likewise, as a sha256 sum has them. In a field named
// for it (private_key = …, "privateKey": …) the secrets' own rule finds
// it too.
//
// A seed phrase is taken after a word that says what it is (seed,
// mnemonic, recovery, phrase, 助记词, ニーモニック), and only when each of its 12 to 25
// words is in BIP-0039's English list, as BIP-0039, Electrum and Algorand
// phrases are: a sentence after "recovery" is no phrase, since "the",
// "of" and "is" are in no wordlist. Monero's own wordlist isn't kept.
var cryptoRules = []rule{
	// Ethereum, BSC, Polygon, Arbitrum, Base, Avalanche, TRON, Starknet:
	// 0x and the 32 bytes in hex (Starknet writes fewer digits than 64),
	// or the 64 digits alone, after a key's name
	{kind: "WALLET_KEY", fold: walletKeyCues, back: 64, ahead: 160, bound: alnum, at: walletKeyAt,
		re: lazyre.New(`(?i)0x[0-9a-f]{56,64}|[0-9a-f]{64}`)},

	// Bitcoin, Litecoin, Dogecoin and the other chains' WIF: a run of 51
	// or 52 base58 characters whose checksum adds up
	{kind: "WIF_KEY", bound: alnum, ok: wifKey, may: b58Run(51),
		re: lazyre.New(`[1-9A-HJ-NP-Za-km-z]{51,}`)},

	// a BIP32 extended private key: xprv, yprv, zprv, tprv and the
	// multisig Yprv, Zprv, Uprv, Vprv; 78 bytes and a checksum. An xpub is
	// a public key and goes as written.
	{kind: "BIP32_KEY", markers: []string{"prv"}, bound: alnum, ok: bip32Key,
		re: lazyre.New(`[xyztuvYZUV]prv[1-9A-HJ-NP-Za-km-z]{107}`)},

	// Solana, as Phantom, Backpack and Solflare export it: the 64-byte
	// secret key in base58, its second half the first half's public key
	{kind: "SOLANA_KEY", bound: alnum, ok: solanaKey, may: b58Run(85),
		re: lazyre.New(`[1-9A-HJ-NP-Za-km-z]{85,}`)},

	// Solana's own keyfile (solana-keygen, ~/.config/solana/id.json): the
	// same 64 bytes as 64 numbers
	{kind: "SOLANA_KEYPAIR", markers: []string{"["}, ok: solanaKeypair,
		re: lazyre.New(`\[\s*\d{1,3}(?:\s*,\s*\d{1,3}){63}\s*\]`)},

	// NEAR: ed25519: and the 64-byte secret key in base58 (the public key
	// is ed25519: and 32 bytes, 43 or 44 characters)
	{kind: "NEAR_KEY", markers: []string{"ed25519:"}, bound: alnum,
		re: lazyre.New(`ed25519:[1-9A-HJ-NP-Za-km-z]{86,88}`)},

	// Sui: sui keytool export writes suiprivkey1… in bech32
	{kind: "SUI_KEY", markers: []string{"suiprivkey1"}, bound: alnum,
		re: lazyre.New(`suiprivkey1[0-9a-z]{58}`)},

	// Aptos, in AIP-80's form: ed25519-priv-0x…, secp256k1-priv-0x…
	{kind: "APTOS_KEY", markers: []string{"-priv-0x"}, bound: alnum,
		re: lazyre.New(`(?:ed25519|secp256k1)-priv-0x[0-9a-fA-F]{64}`)},

	// Stellar's secret seed: S and 55 base32 characters, version byte and
	// CRC16 checked
	{kind: "STELLAR_KEY", bound: alnum, ok: stellarSeed, may: base32Run(56),
		re: lazyre.New(`S[A-Z2-7]{55}`)},

	// a Nostr identity's secret key (NIP-19): nsec1… in bech32
	{kind: "NOSTR_KEY", markers: []string{"nsec1"}, bound: alnum,
		re: lazyre.New(`nsec1[02-9ac-hj-np-z]{58}`)},

	// an age identity: AGE-SECRET-KEY-1… in bech32, in capitals
	{kind: "AGE_KEY", markers: []string{"AGE-SECRET-KEY-"}, bound: alnum,
		re: lazyre.New(`AGE-SECRET-KEY-1[0-9A-Z]{58}`)},

	// Zcash's extended spending key, for the mainnet or the testnet
	{kind: "ZCASH_KEY", markers: []string{"secret-extended-key-"}, bound: alnum,
		re: lazyre.New(`secret-extended-key-(?:main|test)1[0-9a-z]{150,320}`)},

	// a seed phrase after the words that say what it is. "wallet" and
	// "secret" are no cues on their own. fix keeps the longest run of
	// BIP-0039 words in what follows, if it is long enough.
	{kind: "SEED_PHRASE", fold: seedCues, back: 32, ahead: 400, fix: seedPhraseAt,
		re: lazyre.New(`(?i)(?:seed|mnemonic|phrase|recovery|助记词|助記詞|シード|ニーモニック|시드|니모닉)[^.\n]{0,24}?(?:\n[ \t]*)?\b([a-z]{3,8}(?:[ \t]+[a-z]{3,8}){11,40})\b`)},
}

// walletKeyCues are what a field or a flag holding a wallet's private key
// has in its name, in lower case: PRIVATE_KEY, privKey, --private-key,
// signer, DEPLOYER_PK, new Wallet(…), 私钥.
var walletKeyCues = []string{"priv", "secret", "signer", "signing", "deployer", "wallet", "pk", "私钥", "私鑰", "秘密鍵", "개인키", "비밀키"}

// notKeyWords, between a cue and the hex after it, say the hex is
// something else of the key's: privateKeyToPublic(0x…), wallet tx hash
// 0x…, secrets.env sha256 ….
var notKeyWords = []string{"hash", "sha", "digest", "sum", "pub", "addr", "tx", "fingerprint", "id"}

// walletKeyAt says the hex at s[a:b] comes after a wallet key's name, on
// its own line and at most 48 bytes before it, with nothing between that
// says it is a hash, an address or a public key.
func walletKeyAt(s string, a, b int) bool {
	start := max(0, a-48)
	if nl := strings.LastIndexByte(s[start:a], '\n'); nl >= 0 {
		start += nl + 1
	}
	before := lowerASCII(s[start:a])
	cue, end := -1, 0
	for _, c := range walletKeyCues {
		for i := 0; ; {
			j := strings.Index(before[i:], c)
			if j < 0 {
				break
			}
			at := i + j
			i = at + len(c)
			// pk only as a word of a name: PK, DEPLOYER_PK, pk=, not pkg
			if c == "pk" && (at > 0 && isLetter(before[at-1]) || i < len(before) && isLetter(before[i])) {
				continue
			}
			if at > cue {
				cue, end = at, i
			}
		}
	}
	if cue < 0 {
		return false
	}
	between := before[end:]
	for _, w := range notKeyWords {
		if w == "id" {
			// an id's name: wallet_id, walletId, wallet id
			if t := strings.TrimLeft(between, "_- "); strings.HasPrefix(t, "id") && (len(t) == 2 || !isLetter(t[2])) {
				return false
			}
			continue
		}
		if strings.Contains(between, w) {
			return false
		}
	}
	return true
}

func isLetter(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }

const b58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

var b58Index = func() (t [256]int8) {
	for i := range t {
		t[i] = -1
	}
	for i := 0; i < len(b58Alphabet); i++ {
		t[b58Alphabet[i]] = int8(i)
	}
	return t
}()

// base58 is s decoded, or nil where s has a character outside base58.
func base58(s string) []byte {
	n := new(big.Int)
	radix := big.NewInt(58)
	for i := 0; i < len(s); i++ {
		d := b58Index[s[i]]
		if d < 0 {
			return nil
		}
		n.Mul(n, radix)
		n.Add(n, big.NewInt(int64(d)))
	}
	zeros := 0
	for zeros < len(s) && s[zeros] == '1' {
		zeros++
	}
	return append(make([]byte, zeros), n.Bytes()...)
}

// base58check is the payload of s, when s is base58 and its last four
// bytes are the double sha256 of the rest.
func base58check(s string) []byte {
	b := base58(s)
	if len(b) < 5 {
		return nil
	}
	p, sum := b[:len(b)-4], b[len(b)-4:]
	h := sha256.Sum256(p)
	h = sha256.Sum256(h[:])
	if !bytes.Equal(h[:4], sum) {
		return nil
	}
	return p
}

// wifKey says v is a private key in WIF: a version byte and 32 bytes,
// and 0x01 after them for a compressed key, with its checksum.
func wifKey(v string) bool {
	if len(v) != 51 && len(v) != 52 {
		return false
	}
	p := base58check(v)
	return len(p) == 33 || len(p) == 34 && p[33] == 1
}

// bip32Key says v is a BIP-0032 extended key, 78 bytes with its checksum.
func bip32Key(v string) bool {
	return len(base58check(v)) == 78
}

// solanaKey says v is a 64-byte ed25519 secret key in base58: a seed and
// the public key it derives.
func solanaKey(v string) bool {
	if len(v) < 85 || len(v) > 88 {
		return false
	}
	return seedAndPublic(base58(v))
}

// seedAndPublic says b is 32 bytes of seed and the ed25519 public key it
// derives, as Solana and NEAR keep a secret key.
func seedAndPublic(b []byte) bool {
	if len(b) != ed25519.PrivateKeySize {
		return false
	}
	return bytes.Equal(ed25519.NewKeyFromSeed(b[:ed25519.SeedSize]), b)
}

// solanaKeypair says v, a list of 64 numbers, is a Solana keyfile: each a
// byte, together a seed and its public key. A sha512's 64 bytes, as a
// test writes them, are not.
func solanaKeypair(v string) bool {
	parts := strings.Split(strings.Trim(v, "[] \t\r\n"), ",")
	if len(parts) != ed25519.PrivateKeySize {
		return false
	}
	b := make([]byte, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || n > 255 {
			return false
		}
		b[i] = byte(n)
	}
	return seedAndPublic(b)
}

// stellarSeed says v is a Stellar secret seed in StrKey: the seed's
// version byte (18 << 3), 32 bytes and a CRC16-XModem, little-endian.
func stellarSeed(v string) bool {
	b, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(v)
	if err != nil || len(b) != 35 || b[0] != 18<<3 {
		return false
	}
	c := crc16XModem(b[:33])
	return b[33] == byte(c) && b[34] == byte(c>>8)
}

func crc16XModem(b []byte) uint16 {
	var c uint16
	for _, x := range b {
		c ^= uint16(x) << 8
		for range 8 {
			if c&0x8000 != 0 {
				c = c<<1 ^ 0x1021
			} else {
				c <<= 1
			}
		}
	}
	return c
}

// seedCues are what a seed phrase is called, as fold has them.
var seedCues = []string{"seed", "mnemonic", "phrase", "recovery", "助记词", "助記詞", "シード", "ニーモニック", "시드", "니모닉"}

// bip39English is BIP-0039's English wordlist, which Electrum and
// Algorand use too: 2,048 words of 3 to 8 letters, from
// github.com/bitcoin/bips bip-0039/english.txt (sha256 2f5eed53…).
//
//go:embed bip39english.txt
var bip39EnglishText string

var bip39English = sync.OnceValue(func() map[string]bool {
	m := make(map[string]bool, 2048)
	for _, w := range strings.Fields(bip39EnglishText) {
		m[w] = true
	}
	return m
})

// phraseLengths are how many words a seed phrase has: BIP-0039's 12 to
// 24, and Algorand's 25.
var phraseLengths = []int{25, 24, 21, 18, 15, 12}

// seedPhraseAt is the seed phrase in the words at s[a:b]: the longest run
// of BIP-0039 words there, cut to a phrase's length (a word after it that
// happens to be in the list too is not the phrase's).
func seedPhraseAt(s string, a, b int) (int, int, bool) {
	list := bip39English()
	bestA, bestN, bestEnd := 0, 0, 0
	runA, runN := -1, 0
	ends := []int{}
	for i := a; i < b; {
		for i < b && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		j := i
		for j < b && s[j] != ' ' && s[j] != '\t' {
			j++
		}
		if i == j {
			break
		}
		if list[strings.ToLower(s[i:j])] {
			if runA < 0 {
				runA, runN, ends = i, 0, ends[:0]
			}
			runN++
			ends = append(ends, j)
			if runN > bestN {
				bestA, bestN, bestEnd = runA, runN, j
				for _, l := range phraseLengths {
					if runN >= l {
						bestEnd = ends[l-1]
						break
					}
				}
			}
		} else {
			runA = -1
		}
		i = j
	}
	if bestN < 12 {
		return 0, 0, false
	}
	return bestA, bestEnd, true
}

// b58Run is true of text with n base58 letters in a row: a byte scan in
// place of running a markerless pattern over every request.
func b58Run(n int) func(string) bool {
	return func(s string) bool {
		run := 0
		for i := 0; i < len(s); i++ {
			if b58Index[s[i]] >= 0 {
				if run++; run >= n {
					return true
				}
			} else {
				run = 0
			}
		}
		return false
	}
}

// base32Run is b58Run for RFC 4648 base32 in capitals (a Stellar seed).
func base32Run(n int) func(string) bool {
	return func(s string) bool {
		run := 0
		for i := 0; i < len(s); i++ {
			if c := s[i]; c >= 'A' && c <= 'Z' || c >= '2' && c <= '7' {
				if run++; run >= n {
					return true
				}
			} else {
				run = 0
			}
		}
		return false
	}
}
