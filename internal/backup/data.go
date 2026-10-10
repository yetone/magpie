package backup

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// dataFormat is a file of bytes sealed as a backup is, but not one: the
// usage a computer shares through sync (#542). Open refuses it.
const dataFormat = "magpie-data"

// keys are the keys derived this run, by passphrase and salt: a computer
// seals every file it shares, and every backup, with one salt, and opens
// each other computer's with that one's, so a sync derives a key once a
// computer rather than once a file, PBKDF2 being slow on purpose.
var (
	keysMu sync.Mutex
	keys   = map[[32]byte][]byte{}
	salts  = map[[32]byte][]byte{}
)

// saltFor is the salt this run seals with under pass.
func saltFor(pass string) []byte {
	p := sha256.Sum256([]byte(pass))
	keysMu.Lock()
	defer keysMu.Unlock()
	salt := salts[p]
	if salt == nil {
		salt = make([]byte, 16)
		rand.Read(salt)
		salts[p] = salt
	}
	return salt
}

func derived(pass string, e envelope) ([]byte, error) {
	k := sha256.Sum256(fmt.Appendf(nil, "%s\x00%d\x00%x", pass, e.Iterations, e.Salt))
	keysMu.Lock()
	defer keysMu.Unlock()
	if key, ok := keys[k]; ok {
		return key, nil
	}
	key, err := deriveKey(pass, e)
	if err != nil {
		return nil, err
	}
	keys[k] = key
	return key, nil
}

// SealData seals data under the passphrase, as Seal does a bundle.
func SealData(data []byte, pass string) ([]byte, error) {
	if pass == "" {
		return nil, errors.New("sealing needs a passphrase")
	}
	e := envelope{Format: dataFormat, Version: 1, KDF: "pbkdf2-sha256", Iterations: iterations, Salt: saltFor(pass), Nonce: make([]byte, 12)}
	rand.Read(e.Nonce)
	key, err := derived(pass, e)
	if err != nil {
		return nil, err
	}
	gcm, err := gcmOf(key)
	if err != nil {
		return nil, err
	}
	e.Data = gcm.Seal(nil, e.Nonce, data, header(e))
	return json.Marshal(e)
}

// OpenData opens what SealData sealed.
func OpenData(sealed []byte, pass string) ([]byte, error) {
	var e envelope
	if json.Unmarshal(sealed, &e) != nil || e.Format != dataFormat {
		return nil, errors.New("not a file magpie sealed")
	}
	if e.Version != 1 || e.KDF != "pbkdf2-sha256" {
		return nil, errors.New("sealed by a newer magpie")
	}
	if e.Iterations < 100_000 || e.Iterations > 10_000_000 || len(e.Nonce) != 12 || len(e.Salt) < 16 {
		return nil, errors.New("not a file magpie sealed")
	}
	key, err := derived(pass, e)
	if err != nil {
		return nil, err
	}
	gcm, err := gcmOf(key)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, e.Nonce, e.Data, header(e))
	if err != nil {
		return nil, ErrPassphrase
	}
	return plain, nil
}
