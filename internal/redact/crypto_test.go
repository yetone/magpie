package redact

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"testing"
)

// The crypto wallet keys are made here rather than written out where a
// format lets them be: a key in the source is a key in every clone. The
// ones written out are published test vectors that never held funds, each
// split across two literals so a scanner reading this file does not take
// it for a key. (Rex Zhou, #4025, wrote the first of these tests.)
const (
	b58         = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	base32Chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
	// Anvil's and Hardhat's first account, printed by every local node
	anvilKey = "ac0974bec39a17e36ba4a6b4d238ff94" + "4bacb478cbed5efcae784d7bf4f2ff80"
	// BIP-0032 test vector 1's master key
	tv1xprv = "xprv9s21ZrQH143K3QTDL4LXw2F7HEK3wJUD2nW2nRk4stbPy6cq3jPPq" +
		"jiChkVvvNKmPGJxWUtg6LnF5kejMRNNU3TGtRBeJgk33yuGBxrMPHi"
	tv1xpub = "xpub661MyMwAqRbcFtXgS5sYJABqqG9YLmC4Q1Rdap9gSE8NqtwybGhePY2gZ29" +
		"ESFjqJoCu1Rupje8YtGqsefD265TMg7usUDFdp6W1EGMcet8"
	// the Bitcoin wiki's WIF example
	wikiWIF = "5HueCGU8rMjxEXxiPuD5BD" + "ku4MkFqeZyd4dZ1jvhTVqvbTLvyTJ"
	// NIP-19's nsec example
	nip19nsec = "nsec1vl029mgpspedva04g90vltkh6fvh240zqt" + "v9k0t9af8935ke9laqsnlfe5"
	// BIP-0039's first and second English test vectors
	bip39v1 = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
	bip39v2 = "legal winner thank year wave sausage worth useful legal winner thank yellow"
)

// rep is s repeated to n characters.
func rep(s string, n int) string { return strings.Repeat(s, n/len(s)+1)[:n] }

// pad is a run of n characters of alphabet a, from i.
func pad(a string, n, i int) string {
	b := make([]byte, n)
	for k := range b {
		b[k] = a[(i+k)%len(a)]
	}
	return string(b)
}

// enc58 is b in base58.
func enc58(b []byte) string {
	n := new(big.Int).SetBytes(b)
	var out []byte
	m, radix := new(big.Int), big.NewInt(58)
	for n.Sign() > 0 {
		n.DivMod(n, radix, m)
		out = append(out, b58[m.Int64()])
	}
	for _, x := range b {
		if x != 0 {
			break
		}
		out = append(out, '1')
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

// enc58check is p with its double-sha256 checksum, in base58.
func enc58check(p []byte) string {
	h := sha256.Sum256(p)
	h = sha256.Sum256(h[:])
	return enc58(append(append([]byte{}, p...), h[:4]...))
}

// testSeed is a 32-byte seed made from name.
func testSeed(name string) []byte {
	h := sha256.Sum256([]byte("magpie crypto test " + name))
	return h[:]
}

// solanaSecret is the 64-byte ed25519 secret key of name's seed, as Solana
// keeps it: the seed, then the public key.
func solanaSecret(name string) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(testSeed(name))
}

func byteList(b []byte) string {
	parts := make([]string, len(b))
	for i, x := range b {
		parts[i] = fmt.Sprint(x)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// stellar is a StrKey: version, 32 bytes, CRC16 little-endian.
func stellar(version byte, body []byte) string {
	p := append([]byte{version}, body...)
	c := crc16XModem(p)
	p = append(p, byte(c), byte(c>>8))
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(p)
}

func cryptoValues() map[string]string {
	return map[string]string{
		"WALLET_KEY":     "0x" + anvilKey,
		"WIF_KEY":        wikiWIF,
		"BIP32_KEY":      tv1xprv,
		"SOLANA_KEY":     enc58(solanaSecret("sol")),
		"SOLANA_KEYPAIR": byteList(solanaSecret("keypair")),
		"NEAR_KEY":       "ed25519:" + enc58(solanaSecret("near")),
		"SUI_KEY":        "suiprivkey1" + pad("0123456789abcdefghijklmnopqrstuvwxyz", 58, 4),
		"APTOS_KEY":      "ed25519-priv-0x" + rep("9f2c4b6d8e0a", 64),
		"STELLAR_KEY":    stellar(18<<3, testSeed("stellar")),
		"NOSTR_KEY":      nip19nsec,
		"AGE_KEY":        "AGE-SECRET-KEY-1" + pad("QPZRY9X8GF2TVDW0S3JN54KHCE6MUA7L", 58, 6),
		"ZCASH_KEY":      "secret" + "-extended-key-main1" + pad("0123456789abcdefghijklmnopqrstuvwxyz", 160, 9),
		"SEED_PHRASE":    bip39v2,
	}
}

// TestMaskCryptoKeys: every shape goes as its own kind with the secrets on,
// and comes back as it was. Personal data alone leaves them as written.
func TestMaskCryptoKeys(t *testing.T) {
	vals := cryptoValues()
	for _, kind := range []string{"WALLET_KEY", "WIF_KEY", "BIP32_KEY", "SOLANA_KEY", "SOLANA_KEYPAIR", "NEAR_KEY", "SUI_KEY", "APTOS_KEY", "STELLAR_KEY", "NOSTR_KEY", "AGE_KEY", "ZCASH_KEY", "SEED_PHRASE"} {
		Forget()
		v := vals[kind]
		in := "the key is " + v + " ok"
		switch kind {
		case "WALLET_KEY":
			in = "PRIVATE_KEY " + v + " ok"
		case "SEED_PHRASE":
			in = "seed phrase: " + v + " ok"
		}
		out, n := Mask(in, Options{Secrets: true})
		if n != 1 || !strings.Contains(out, "{{"+kind+"_") || strings.Contains(out, v) {
			t.Errorf("%s: %d masked: %s", kind, n, out)
			continue
		}
		if back := Restore(out, false); back != in {
			t.Errorf("%s: restored %q, want %q", kind, back, in)
		}
		Forget()
		if off, n := Mask(in, Options{Personal: true}); n != 0 || off != in {
			t.Errorf("%s: masked with the secrets off (%d): %s", kind, n, off)
		}
	}
	Forget()
}

// TestMaskCryptoChecked: the shapes with no prefix are taken only when the
// key proves itself, as real keys of each chain do: compressed WIF, other
// chains' version bytes, an 87-character Solana key, a keyfile across
// lines.
func TestMaskCryptoChecked(t *testing.T) {
	compressed := enc58check(append(append([]byte{0x80}, testSeed("wif")...), 1))
	litecoin := enc58check(append([]byte{0xb0}, testSeed("ltc")...))
	testnet := enc58check(append(append([]byte{0xef}, testSeed("tbtc")...), 1))
	var sol87 string
	for i := 0; sol87 == ""; i++ {
		if s := enc58(solanaSecret(fmt.Sprint("short", i))); len(s) == 87 {
			sol87 = s
		}
	}
	keyfile := strings.ReplaceAll(byteList(solanaSecret("lines")), ",", ",\n  ")
	for _, c := range []struct{ kind, v string }{
		{"WIF_KEY", compressed},
		{"WIF_KEY", litecoin},
		{"WIF_KEY", testnet},
		{"SOLANA_KEY", sol87},
		{"SOLANA_KEYPAIR", keyfile},
		{"BIP32_KEY", "tprv" + tv1xprv[4:]}, // the prefix is checksummed: not a key
	} {
		Forget()
		out, n := Mask("value "+c.v+" here", Options{Secrets: true})
		want := !strings.HasPrefix(c.v, "tprv")
		if got := n == 1 && strings.Contains(out, "{{"+c.kind+"_"); got != want {
			t.Errorf("%s %q: masked %v, want %v: %s", c.kind, c.v, got, want, out)
		}
	}
	Forget()
}

// TestMaskCryptoWalletKeyNeedsItsName: 0x and 64 hex digits is a key after
// a key's name, in a config, a flag, code and prose. Elsewhere it is a
// transaction hash, a block hash, a storage slot or a sha256 sum, and goes
// as written.
func TestMaskCryptoWalletKeyNeedsItsName(t *testing.T) {
	k := "0x" + anvilKey
	for _, in := range []string{
		"PRIVATE_KEY=" + k,
		"export DEPLOYER_PRIVATE_KEY=" + k,
		"DEPLOYER_PK=" + k,
		"forge script Deploy.s.sol --broadcast --private-key " + k,
		"cast send --private-key " + anvilKey + " 0xabc",
		`const signer = new ethers.Wallet("` + k + `", provider)`,
		`account = Account.from_key(priv_key) # priv_key = "` + k + `"`,
		"privKey: '" + k + "',",
		"私钥 " + k,
		"我的私钥是" + k,
		"signingKey=" + k,
		"STARKNET_PRIVATE_KEY=0x" + anvilKey[:60],
	} {
		Forget()
		out, _ := Mask(in, Options{Secrets: true})
		if strings.Contains(out, anvilKey[:56]) {
			t.Errorf("%q: key left in %q", in, out)
		}
	}
	h := rep("4c0883a69102937d", 64)
	slot := "360894a13ba1a3210667c828492db98dca3e2076cc3735a920a3ca505d382bbc"
	sum := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	for _, in := range []string{
		"see 0x" + h + " on chain",
		`{"transactionHash":"0x` + h + `","blockHash":"0x` + slot + `"}`,
		"bytes32 internal constant IMPLEMENTATION_SLOT = 0x" + slot + ";",
		"wallet tx hash 0x" + h,
		"the wallet's address 0x" + h,
		"privateKeyToPublic(0x" + h + ")",
		"wallet_id: 0x" + h,
		sum + "  secrets.env",
		"secrets.env sha256 " + sum,
		"sha256:" + sum,
		"pkg-lock " + sum,
		"key 0x" + h, // "key" alone names anything in code
		"0x" + rep("4c0883a6", 40),
	} {
		Forget()
		if out, n := Mask(in, Options{Secrets: true}); n != 0 {
			t.Errorf("%q masked: %s", in, out)
		}
	}
	Forget()
}

// TestMaskCryptoLookalikes: values that share a piece of a key's shape and
// are not keys: an address, a public key, a digest, a lockfile's hashes, a
// uuid, a base64 blob, a Solana transaction signature. They go as written,
// so a vendor can still read them.
func TestMaskCryptoLookalikes(t *testing.T) {
	sig := sha512.Sum512([]byte("a transaction"))
	kept := []string{
		"0x" + rep("4c0883a6", 40),                               // an address, 20 bytes
		rep("a1b2c3d4", 40),                                      // a git sha
		"commit 9fceb02d0ae598e95dc970b74767f19372d61af8",        // a git sha
		rep("e3b0c44298fc1c14", 64),                              // a bare sha256
		"sha256:" + rep("e3b0c44298fc1c14", 64),                  // a digest, named
		"550e8400-e29b-41d4-a716-446655440000",                   // a uuid
		tv1xpub,                                                  // BIP32's public key, checksum and all
		"ypub" + pad(b58, 107, 3),                                // more public keys
		"xprv" + pad(b58, 107, 3),                                // an xprv's shape with no checksum
		enc58(solanaSecret("pub").Public().(ed25519.PublicKey)),  // a Solana public key
		enc58(sig[:]),                                            // a transaction signature, 64 bytes
		pad(b58, 88, 11),                                         // 88 base58 characters
		pad(b58, 87, 13),                                         // 87 of them
		"5" + pad(b58, 50, 7),                                    // WIF's shape, no checksum
		"K" + pad(b58, 51, 7),                                    // compressed WIF's shape
		"1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2",                     // a Bitcoin address
		"QmYwAPJzv5CZsnA625s3Xf2nemtYgPpHdWEz79ojWnPbdG",         // an IPFS CID
		"bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq",             // a bech32 address
		"npub1" + pad("qpzry9x8gf2tvdw0s3jn54khce6mua7l", 58, 1), // NIP-19's public key
		"ed25519-pub-0x" + rep("9f2c4b6d8e0a", 64),               // AIP-80's public key
		"ed25519:" + enc58(solanaSecret("near").Public().(ed25519.PublicKey)), // NEAR's public key
		"suiprivkey" + pad("0123456789abcdefghijklmnopqrstuvwxyz", 58, 4),     // no trailing 1
		"age-secret-key-1" + pad("QPZRY9X8GF2TVDW0S3JN54KHCE6MUA7L", 58, 6),   // age is capitals
		"S" + pad(base32Chars, 55, 2),                                         // a seed's shape, no CRC
		stellar(6<<3, testSeed("stellar")),                                    // a Stellar account id, G…
		"SOMEVERYLONGCONSTANTNAMEWRITTENINCAPITALSWITHOUTANYBREAKS",           // 56 capitals from S
		byteList(sig[:]),           // a sha512 as 64 numbers
		byteList(make([]byte, 64)), // 64 zeros
	}
	for _, v := range kept {
		Forget()
		in := "value " + v + " here"
		if out, n := Mask(in, Options{Secrets: true}); n != 0 || out != in {
			t.Errorf("masked %q (%d): %s", v, n, out)
		}
	}
	Forget()
}

// TestMaskCryptoLockfilesAndBlobs: a lockfile's integrity hashes and
// base64 blobs, a thousand of each, go as written. A base64 run with no +
// or / in it is a base58 run as long as a key, so each is checked by the
// key it would have to be.
func TestMaskCryptoLockfilesAndBlobs(t *testing.T) {
	Forget()
	defer Forget()
	var lock, blobs strings.Builder
	for i := range 1000 {
		h := sha512.Sum512([]byte(fmt.Sprint("package ", i)))
		fmt.Fprintf(&lock, "    \"integrity\": \"sha512-%s\",\n", base64.StdEncoding.EncodeToString(h[:]))
		s := sha256.Sum256([]byte(fmt.Sprint("module ", i)))
		fmt.Fprintf(&lock, "example.com/m%d v1.0.%d h1:%s\n", i, i, base64.StdEncoding.EncodeToString(s[:]))
		fmt.Fprintf(&lock, "  resolved \"https://registry.example/-/p-%d.tgz#%s\"\n", i, hex.EncodeToString(s[:]))
		blob := make([]byte, 0, 96)
		for len(blob) < 96 {
			h := sha512.Sum512(append([]byte(fmt.Sprint("blob ", i)), blob...))
			blob = append(blob, h[:]...)
		}
		fmt.Fprintf(&blobs, "%s\n%s\n", base64.StdEncoding.EncodeToString(blob), base64.RawURLEncoding.EncodeToString(blob))
	}
	for name, s := range map[string]string{"lockfile": lock.String(), "base64": blobs.String()} {
		if out, n := Mask(s, Options{Secrets: true}); n != 0 {
			for _, l := range strings.Split(out, "\n") {
				if strings.Contains(l, "{{") {
					t.Errorf("%s: masked %d, first: %s", name, n, l)
					break
				}
			}
		}
	}
}

// TestMaskCryptoJSON: the gateway masks a body's strings, where a key
// arrives as a value of its own, with no field name beside it.
func TestMaskCryptoJSON(t *testing.T) {
	Forget()
	defer Forget()
	sol := cryptoValues()["SOLANA_KEY"]
	body := `{"tool":"wallet","input":{"key":"` + sol + `"}}`
	out, n := MaskJSON([]byte(body), Options{Secrets: true})
	if n != 1 || strings.Contains(string(out), sol) {
		t.Fatalf("%d masked: %s", n, out)
	}
	var v struct {
		Input struct {
			Key string `json:"key"`
		} `json:"input"`
	}
	if err := json.Unmarshal(out, &v); err != nil || !strings.HasPrefix(v.Input.Key, "{{SOLANA_KEY_") {
		t.Fatalf("json: %v: %s", err, out)
	}
	if back := RestoreJSON(out); string(back) != body {
		t.Fatalf("restored %s, want %s", back, body)
	}
}

// TestMaskCryptoKnown: a key masked in one turn is masked again in the
// next, with the same placeholder, so a conversation reads the same and
// the vendor's cache still matches.
func TestMaskCryptoKnown(t *testing.T) {
	Forget()
	defer Forget()
	sol := cryptoValues()["SOLANA_KEY"]
	first, n := Mask("my key "+sol, Options{Secrets: true})
	if n != 1 {
		t.Fatalf("%d masked: %s", n, first)
	}
	ph := placeholderRe.FindString(first)
	if ph == "" {
		t.Fatalf("no placeholder: %s", first)
	}
	second, n := Mask("again "+sol, Options{Secrets: true})
	if n != 1 || !strings.Contains(second, ph) {
		t.Fatalf("second turn %d: %s, want %s", n, second, ph)
	}
}

// TestMaskCryptoLengths: a prefixed key is the whole run, so one character
// more or less is another value, and stays as written.
func TestMaskCryptoLengths(t *testing.T) {
	cases := []struct {
		kind string
		n    int
		ok   bool
	}{
		{"NOSTR_KEY", 63, true}, {"NOSTR_KEY", 62, false}, {"NOSTR_KEY", 64, false},
		{"AGE_KEY", 74, true}, {"AGE_KEY", 73, false}, {"AGE_KEY", 75, false},
		{"SUI_KEY", 69, true}, {"SUI_KEY", 68, false}, {"SUI_KEY", 70, false},
		{"NEAR_KEY", 96, true}, {"NEAR_KEY", 95, true}, {"NEAR_KEY", 93, false}, {"NEAR_KEY", 97, false},
	}
	for _, c := range cases {
		Forget()
		var v string
		switch c.kind {
		case "NOSTR_KEY":
			v = "nsec1" + pad("qpzry9x8gf2tvdw0s3jn54khce6mua7l", c.n-5, 1)
		case "AGE_KEY":
			v = "AGE-SECRET-KEY-1" + pad("QPZRY9X8GF2TVDW0S3JN54KHCE6MUA7L", c.n-16, 6)
		case "SUI_KEY":
			v = "suiprivkey1" + pad("0123456789abcdefghijklmnopqrstuvwxyz", c.n-11, 4)
		case "NEAR_KEY":
			v = "ed25519:" + pad(b58, c.n-8, 5)
		}
		in := "key " + v + " end"
		out, n := Mask(in, Options{Secrets: true})
		if c.ok && (n != 1 || !strings.Contains(out, "{{"+c.kind+"_")) {
			t.Errorf("%s at %d: %d masked: %s", c.kind, c.n, n, out)
		}
		if !c.ok && n != 0 {
			t.Errorf("%s at %d masked, want as written: %s", c.kind, c.n, out)
		}
	}
	Forget()
}

// TestMaskCryptoSeedPhrase: 12 to 25 BIP-0039 words after a cue are a
// phrase. Fewer, words outside the list, a run with no cue, and a sentence
// after "recovery" or "seed" are not. "wallet" and "secret" are no cues.
func TestMaskCryptoSeedPhrase(t *testing.T) {
	words := strings.Fields("legal winner thank year wave sausage worth useful letter advice cage absurd")
	phrase := func(n int) string {
		out := make([]string, n)
		for i := range out {
			out[i] = words[i%len(words)]
		}
		return strings.Join(out, " ")
	}
	for _, c := range []struct{ in, v string }{
		{"seed phrase: " + bip39v1, bip39v1},
		{"mnemonic = \"" + bip39v2 + "\"", bip39v2},
		{"MNEMONIC=" + strings.ToUpper(bip39v2[:1]) + bip39v2[1:], strings.ToUpper(bip39v2[:1]) + bip39v2[1:]},
		{"Recovery phrase:\n  " + phrase(24), phrase(24)},
		{"seed: " + phrase(15) + ". Keep it safe", phrase(15)},
		{"seed phrase: " + phrase(12) + " and then more words", phrase(12)},
		{"seed phrase: " + phrase(25), phrase(25)},
		{"Seed phrase: my words are " + phrase(12), phrase(12)},
		{"助记词：" + phrase(12), phrase(12)},
		{"シード: " + phrase(18), phrase(18)},
	} {
		Forget()
		out, n := Mask(c.in, Options{Secrets: true})
		if n != 1 || !strings.Contains(out, "{{SEED_PHRASE_") || strings.Contains(out, c.v) {
			t.Errorf("%q: %d masked: %s", c.in, n, out)
		}
		if back := Restore(out, false); back != c.in {
			t.Errorf("%q: restored %q", c.in, back)
		}
	}
	// a 13th list word after a phrase is not the phrase's
	Forget()
	if out, _ := Mask("seed phrase: "+phrase(12)+" able", Options{Secrets: true}); !strings.HasSuffix(out, "}} able") {
		t.Errorf("13th word taken: %s", out)
	}
	for _, in := range []string{
		"seed phrase: " + phrase(11),
		phrase(12), // no cue at all
		"the wallet app lists " + phrase(12),
		"the secret is " + phrase(12),
		"recovery mode lets the system restore state after crash without losing user data",
		"seed the database with sample users before running each integration suite again today",
		"the phrase appears inside several long comments where people explain their design choices",
		"Seeded random numbers produce stable output across machines when tests compare golden files",
		"mnemonic: " + phrase(6) + " zebras " + phrase(6),
	} {
		Forget()
		if out, n := Mask(in, Options{Secrets: true}); n != 0 || out != in {
			t.Errorf("masked %q (%d): %s", in, n, out)
		}
	}
	Forget()
}

// TestBIP39List: the embedded list is BIP-0039's: 2,048 words of 3 to 8
// letters, from abandon to zoo.
func TestBIP39List(t *testing.T) {
	m := bip39English()
	if len(m) != 2048 || !m["abandon"] || !m["zoo"] || m["the"] {
		t.Fatalf("%d words", len(m))
	}
	if h := sha256.Sum256([]byte(bip39EnglishText)); hex.EncodeToString(h[:]) != "2f5eed53a4727b4bf8880d8f3f199efc90e58503646d9ff8eff3a2ed3b24dbda" {
		t.Fatalf("english.txt changed: %x", h)
	}
}

// TestMaskCryptoLongInput: a megabyte of base58, of hex after a cue and of
// list words after a cue is read through once; the patterns are RE2's, so
// none of them backtracks.
func TestMaskCryptoLongInput(t *testing.T) {
	Forget()
	defer Forget()
	for _, s := range []string{
		rep(b58, 1<<20),
		"PRIVATE_KEY=" + rep("0123456789abcdef", 1<<20),
		"seed phrase: " + rep("abandon ", 1<<20),
		strings.Repeat("[1,2,3,", 1<<17),
	} {
		Mask(s, Options{Secrets: true})
	}
}

func BenchmarkMaskCryptoProse(b *testing.B) {
	var sb strings.Builder
	for sb.Len() < 1<<20 {
		sb.WriteString("The function reads the config, checks each field and returns an error when a value is missing. ")
		sb.WriteString(`{"id":"550e8400-e29b-41d4-a716-446655440000","sha":"9fceb02d0ae598e95dc970b74767f19372d61af8"}` + "\n")
	}
	s := sb.String()
	b.SetBytes(int64(len(s)))
	for b.Loop() {
		Forget()
		Mask(s, Options{Secrets: true})
	}
}

// TestMaskExchangeKeys: an exchange's API key, secret and passphrase go by
// their fields' names, as ccxt configs, .env files and request headers
// have them. A passphrase field named for a wifi network's is one too.
func TestMaskExchangeKeys(t *testing.T) {
	key := "vmPUZE6mv9SD5VNHk4HlWFsOr6aKE2zvsw0MuIgwCIPy6utIco14y7Ju91duEh8A"
	secret := "NhqPtmdSJYdKjVHjA7PZj4Mge3R5YNiP1e3UZjInClVN65XAbvqqM6A7H5fATj0j"
	pass := "Okx9Pass2024"
	for _, c := range []struct{ in, v string }{
		{"BINANCE_API_KEY=" + key, key},
		{"BINANCE_SECRET_KEY=" + secret, secret},
		{`exchange = ccxt.okx({"apiKey": "` + key + `", "secret": "` + secret + `", "password": "` + pass + `"})`, secret},
		{"OKX_PASSPHRASE=" + pass, pass},
		{`{"passphrase": "` + pass + `"}`, pass},
		{"OK-ACCESS-PASSPHRASE: " + pass, pass},
		{`privKey = "` + secret + `"`, secret},
		{"priv_key: " + secret, secret},
	} {
		Forget()
		if out, _ := Mask(c.in, Options{Secrets: true}); strings.Contains(out, c.v) {
			t.Errorf("%q: left in %q", c.in, out)
		}
	}
	Forget()
}
