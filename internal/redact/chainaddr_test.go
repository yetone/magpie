package redact

import "testing"

// A TRON address is public on its chain, as an EVM one is: USDT's
// contract went as a placeholder (StringKe, #7973). It goes as written in
// a message and in a tool call's arguments. Its checksum is what tells it
// from a secret, so a run of its length that fails the checksum is
// masked, and so is an address given a key's name.
func TestTronAddressIsNoSecret(t *testing.T) {
	const tron = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t" // USDT's contract
	for _, s := range []string{"send 10 USDT to " + tron + " now", `{"tx": {"contract": "` + tron + `"}}`} {
		if text, args := secretOut(t, tron, s); !text || !args {
			t.Errorf("a TRON address in %q was masked (text kept %v, arguments kept %v)", s, text, args)
		}
	}
	const broken = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6u" // its checksum fails
	if text, args := secretOut(t, broken, "see "+broken+" here"); text || args {
		t.Errorf("%q fails TRON's checksum, yet it went out", broken)
	}
	for _, s := range []string{"private key: " + tron, "the key " + tron, "token " + tron} {
		if text, args := secretOut(t, tron, s); text || args {
			t.Errorf("an address called a key went out: %q", s)
		}
	}
}

// A Solana public key is 32 bytes in base58, and so is a 32-byte seed: no
// check tells them apart, so neither is let go by its shape. A 64-byte
// Solana secret key is the rules' (solanaKey).
func TestSolanaSizedRunStaysMasked(t *testing.T) {
	for _, v := range []string{
		"Dep42V32GMzFRmC9iZThAuL1Y8J3sCnmqbrGNzc3f58p", // a 32-byte seed
		"So11111111111111111111111111111111111111112",  // wrapped SOL's mint
	} {
		if text, args := secretOut(t, v, "see "+v+" here"); text || args {
			t.Errorf("%q went out (text %v, arguments %v)", v, text, args)
		}
	}
}
