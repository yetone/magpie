package gui

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/scrypt"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/settings"
)

// Passkeys sign a browser in to magpie's page (magpie web, or the app's
// Browser access) in place of pasting its key (weixiongbirnary on X: every
// new browser or computer needed the key copied again). A browser signed
// in with the key adds one in Settings › Network and sharing; the page
// turned away then offers "Sign in with passkey" beside the key's field.
//
// A passkey stands for the key it was made under and nothing more: its
// sign-in sets the same cookie, holding that key, as pasting it does
// (webGuard), so it grants exactly what the key grants. The key isn't
// kept with it, only a slow hash of it (scrypt, with the file's own salt):
// once the page's key changes (MAGPIE_WEB_KEY changed, a run's own key
// made anew, Browser access's New key) no passkey made under the old one
// signs anyone in. Only public keys are kept, in passkeys.json beside
// settings.json, 0600, this computer's alone (neither sync nor a backup
// names it). A passkey works at the origin it was made at alone.

// passkeysFile is where passkeys are kept.
func passkeysFile() string { return filepath.Join(settings.Dir(), "passkeys.json") }

// passkeyStore is passkeys.json.
type passkeyStore struct {
	// Salt is the scrypt salt every Under is made with, base64.
	Salt     string        `json:"salt"`
	Passkeys []passkeyCred `json:"passkeys"`
}

// passkeyCred is one passkey: its credential id (base64url), the name it
// was given, the origin it was made at, its public key (PKIX DER, base64)
// and COSE algorithm, the hash of the key it was made under, and its
// authenticator's signature count.
type passkeyCred struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Origin    string    `json:"origin"`
	PublicKey string    `json:"publicKey"`
	Alg       int       `json:"alg"`
	Under     string    `json:"under"`
	Count     uint32    `json:"count,omitempty"`
	Created   time.Time `json:"created"`
	Used      time.Time `json:"used,omitzero"`
}

// passkeyMu holds passkeys.json's read and write together in this process.
var passkeyMu sync.Mutex

// loadPasskeys reads passkeys.json; none is an empty store. One that can't
// be read or parsed is an error, never empty: no passkey signs in on it,
// and nothing is written over it.
func loadPasskeys() (passkeyStore, error) {
	var s passkeyStore
	b, err := os.ReadFile(passkeysFile())
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return passkeyStore{}, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return passkeyStore{}, fmt.Errorf("%s: %w", passkeysFile(), err)
	}
	return s, nil
}

// savePasskeys writes passkeys.json readable by its owner alone.
func savePasskeys(s passkeyStore) error {
	p := passkeysFile()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	if f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err == nil {
		f.Close()
	}
	if s.Passkeys == nil {
		s.Passkeys = []passkeyCred{}
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	if err := edit.WriteAtomic(p, append(b, '\n')); err != nil {
		return err
	}
	return os.Chmod(p, 0o600)
}

// passkeyUnders caches keyUnder's answers: scrypt is slow on purpose.
var passkeyUnders sync.Map // [sha256(salt+key)] → string

// keyUnder is the hash passkeys made under key are kept with, for salt.
func keyUnder(salt, key string) string {
	id := sha256.Sum256([]byte(salt + "\x00" + key))
	if v, ok := passkeyUnders.Load(id); ok {
		return v.(string)
	}
	s, err := base64.StdEncoding.DecodeString(salt)
	if err != nil || len(s) < 16 {
		return "" // matches nothing: a passkey is never kept with ""
	}
	h, err := scrypt.Key([]byte(key), s, 1<<15, 8, 1, 32)
	if err != nil {
		return ""
	}
	v := base64.StdEncoding.EncodeToString(h)
	passkeyUnders.Store(id, v)
	return v
}

// passkeyUserID is the WebAuthn user handle for the passkeys of a key: the
// same for every one made under it, so a browser keeps one passkey per
// key for a site and a key made anew replaces the old key's.
func passkeyUserID(under string) []byte {
	h := sha256.Sum256([]byte("magpie passkey user\x00" + under))
	return h[:16]
}

// passkeysUnder lists the passkeys made under key, in the order made.
func passkeysUnder(key string) ([]passkeyCred, error) {
	passkeyMu.Lock()
	defer passkeyMu.Unlock()
	s, err := loadPasskeys()
	if err != nil {
		return nil, err
	}
	if len(s.Passkeys) == 0 {
		return nil, nil
	}
	u := keyUnder(s.Salt, key)
	var out []passkeyCred
	for _, c := range s.Passkeys {
		if u != "" && subtle.ConstantTimeCompare([]byte(c.Under), []byte(u)) == 1 {
			out = append(out, c)
		}
	}
	return out, nil
}

// dropPasskeysUnder removes every passkey made under key: Browser access's
// New key takes them away with the key.
func dropPasskeysUnder(key string) error {
	passkeyMu.Lock()
	defer passkeyMu.Unlock()
	s, err := loadPasskeys()
	if err != nil || len(s.Passkeys) == 0 {
		return err
	}
	u := keyUnder(s.Salt, key)
	kept := s.Passkeys[:0]
	for _, c := range s.Passkeys {
		if c.Under != u {
			kept = append(kept, c)
		}
	}
	if len(kept) == len(s.Passkeys) {
		return nil
	}
	s.Passkeys = kept
	return savePasskeys(s)
}

// passkeysMax is how many passkeys one key may have.
const passkeysMax = 50

// challenge is one the page handed a browser: for "create" or "get", made
// for a browser signed in with key (a "create"'s), until it expires.
type challenge struct {
	purpose, key string
	until        time.Time
}

// challenges are the ones a page has out, each taken once.
type challenges struct {
	mu sync.Mutex
	m  map[string]challenge
}

// challengeLife is how long a browser has to answer one (a variable for
// a test); challengesMax how many a page keeps out at once, the oldest
// dropped past it.
var challengeLife = 5 * time.Minute

const challengesMax = 128

func (cs *challenges) make(purpose, key string) []byte {
	b := make([]byte, 32)
	rand.Read(b)
	now := time.Now()
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.m == nil {
		cs.m = map[string]challenge{}
	}
	var oldest string
	for k, c := range cs.m {
		if now.After(c.until) {
			delete(cs.m, k)
		} else if oldest == "" || c.until.Before(cs.m[oldest].until) {
			oldest = k
		}
	}
	if len(cs.m) >= challengesMax {
		delete(cs.m, oldest)
	}
	cs.m[string(b)] = challenge{purpose: purpose, key: key, until: now.Add(challengeLife)}
	return b
}

// take hands back the challenge in clientDataJSON and forgets it, when it
// is one this page made for purpose and hasn't expired.
func (cs *challenges) take(clientDataJSON []byte, purpose string) ([]byte, challenge, bool) {
	var c clientData
	if json.Unmarshal(clientDataJSON, &c) != nil {
		return nil, challenge{}, false
	}
	b, err := b64u(c.Challenge)
	if err != nil {
		return nil, challenge{}, false
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	ch, ok := cs.m[string(b)]
	if !ok {
		return nil, challenge{}, false
	}
	delete(cs.m, string(b))
	if ch.purpose != purpose || time.Now().After(ch.until) {
		return nil, challenge{}, false
	}
	return b, ch, true
}

// passkeyPage is the passkey half of a page's sign-in: its key, its
// challenges, and whether its key is a run's own (new each start).
type passkeyPage struct {
	key   string
	runs  bool
	chals challenges
}

// webRunKeys are the keys made for one run of magpie web: a passkey made
// under one lasts until magpie web starts again, which the page says.
var webRunKeys sync.Map

// requestOrigin is the origin the browser named for a request from the
// page itself, after ownPageErr has judged it the page's own.
func requestOrigin(r *http.Request) string { return r.Header.Get("Origin") }

// readPasskeyJSON reads a JSON body of 64 KiB at most into v.
func readPasskeyJSON(rw http.ResponseWriter, r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(rw, r.Body, 64<<10)).Decode(v)
}

// signInOptions answers POST /_magpie/passkey/options: a challenge for a
// browser to sign in with a passkey. No credential is named: the browser
// offers the passkeys it has for this site, so the page tells no one
// which there are.
func (p *passkeyPage) signInOptions(rw http.ResponseWriter, r *http.Request) {
	if _, err := passkeyOrigin(requestOrigin(r)); err != nil {
		fail(rw, err)
		return
	}
	ch := p.chals.make("get", "")
	writeJSON(rw, map[string]any{
		"challenge":        base64.RawURLEncoding.EncodeToString(ch),
		"timeout":          int(challengeLife / time.Millisecond),
		"userVerification": "preferred",
	})
}

// passkeyAnswer is a browser's PublicKeyCredential, base64url.
type passkeyAnswer struct {
	ID                string `json:"id"`
	ClientDataJSON    string `json:"clientDataJSON"`
	AuthenticatorData string `json:"authenticatorData"`
	Signature         string `json:"signature"`
	AttestationObject string `json:"attestationObject"`
	Name              string `json:"name"`
	Back              string `json:"back"`
}

// signIn checks a passkey sign-in (POST /_magpie/passkey/sign-in): the
// challenge is one this page gave, the answer came from the origin the
// passkey was made at and is signed by it, and the passkey was made under
// the page's key now. It says which passkey, or why not.
func (p *passkeyPage) signIn(rw http.ResponseWriter, r *http.Request) (passkeyCred, string, error) {
	var in passkeyAnswer
	if err := readPasskeyJSON(rw, r, &in); err != nil {
		return passkeyCred{}, "", err
	}
	back := localPath(in.Back)
	cdj, err1 := b64u(in.ClientDataJSON)
	adb, err2 := b64u(in.AuthenticatorData)
	sig, err3 := b64u(in.Signature)
	id, err4 := b64u(in.ID)
	if err := errors.Join(err1, err2, err3, err4); err != nil || len(id) == 0 {
		return passkeyCred{}, back, errors.New("the browser's answer isn't a passkey's")
	}
	ch, _, ok := p.chals.take(cdj, "get")
	if !ok {
		return passkeyCred{}, back, errors.New("the sign-in took too long or was used: try again")
	}
	origin := requestOrigin(r)
	if err := checkClientData(cdj, "webauthn.get", ch, origin); err != nil {
		return passkeyCred{}, back, err
	}
	rpID, err := passkeyOrigin(origin)
	if err != nil {
		return passkeyCred{}, back, err
	}
	a, err := parseAuthData(adb)
	if err != nil {
		return passkeyCred{}, back, err
	}
	if err := checkAuthData(a, rpID); err != nil {
		return passkeyCred{}, back, err
	}
	passkeyMu.Lock()
	defer passkeyMu.Unlock()
	s, err := loadPasskeys()
	if err != nil {
		log.Printf("magpie web: passkeys can't be read: %v", err)
		return passkeyCred{}, back, errors.New("magpie can't read its passkeys")
	}
	want := base64.RawURLEncoding.EncodeToString(id)
	i := -1
	for j, c := range s.Passkeys {
		if c.ID == want {
			i = j
			break
		}
	}
	if i < 0 {
		return passkeyCred{}, back, errors.New("this passkey isn't one this magpie knows: it was removed, or made for another magpie")
	}
	c := s.Passkeys[i]
	if u := keyUnder(s.Salt, p.key); u == "" || subtle.ConstantTimeCompare([]byte(c.Under), []byte(u)) != 1 {
		return passkeyCred{}, back, errors.New("this passkey was made under a key this magpie no longer has: sign in with the key, then add a passkey again")
	}
	if c.Origin != origin {
		return passkeyCred{}, back, fmt.Errorf("this passkey was made at %s, not %s", c.Origin, origin)
	}
	spki, err := base64.StdEncoding.DecodeString(c.PublicKey)
	if err != nil {
		return passkeyCred{}, back, errors.New("magpie can't read this passkey's public key")
	}
	if err := verifyAssertion(spki, c.Alg, adb, cdj, sig); err != nil {
		return passkeyCred{}, back, err
	}
	// a count that doesn't go up says the authenticator was copied
	// (WebAuthn §6.1.1); synced passkeys keep it at 0
	if a.count != 0 || c.Count != 0 {
		if a.count <= c.Count {
			return passkeyCred{}, back, errors.New("this passkey's authenticator counted back: it may have been copied")
		}
	}
	c.Count, c.Used = a.count, time.Now().UTC()
	s.Passkeys[i] = c
	if err := savePasskeys(s); err != nil {
		log.Printf("magpie web: a passkey's use can't be written: %v", err)
	}
	return c, back, nil
}

// passkeyJSON is a passkey as the page lists it: never its key.
type passkeyJSON struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Origin  string    `json:"origin"`
	Created time.Time `json:"created"`
	Used    time.Time `json:"used,omitzero"`
}

// passkeysJSON is GET /api/passkeys: the passkeys made under the key this
// browser signed in with, and whether the key is a run's own, which
// passkeys go with when magpie web starts again. Whether the page can
// make one (https at a name) is the browser's to judge (passkeys.js):
// behind a proxy that ends TLS, this side sees plain http.
type passkeysJSON struct {
	Passkeys []passkeyJSON `json:"passkeys"`
	RunKey   bool          `json:"runKey,omitempty"`
}

func (p *passkeyPage) list() (passkeysJSON, error) {
	cs, err := passkeysUnder(p.key)
	if err != nil {
		return passkeysJSON{}, err
	}
	out := passkeysJSON{Passkeys: []passkeyJSON{}, RunKey: p.runs}
	for _, c := range cs {
		out.Passkeys = append(out.Passkeys, passkeyJSON{ID: c.ID, Name: c.Name, Origin: c.Origin, Created: c.Created, Used: c.Used})
	}
	return out, nil
}

// passkeyName is the name a passkey is listed with: what was typed, kept
// short, or "Passkey n".
func passkeyName(typed string, n int) string {
	s := strings.Join(strings.Fields(typed), " ")
	if utf8.RuneCountInString(s) > 60 {
		s = string([]rune(s)[:60])
	}
	if s == "" {
		s = fmt.Sprintf("Passkey %d", n)
	}
	return s
}

// routes serves the passkeys of the key this browser signed in with, for
// Settings: GET /api/passkeys lists them; POST /api/passkeys/options
// hands out a challenge to make one; POST /api/passkeys/add keeps the one
// the browser made; POST /api/passkeys/remove {"id"} takes one away.
// webGuard has let the browser in with the key, and ownPageErr has judged
// the request the page's own, before these run.
func (p *passkeyPage) routes() http.Handler {
	mux := http.NewServeMux()
	answer := func(rw http.ResponseWriter, r *http.Request) {
		l, err := p.list()
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, l)
	}
	mux.HandleFunc("GET /api/passkeys", answer)
	mux.HandleFunc("POST /api/passkeys/options", func(rw http.ResponseWriter, r *http.Request) {
		rpID, err := passkeyOrigin(requestOrigin(r))
		if err != nil {
			fail(rw, err)
			return
		}
		passkeyMu.Lock()
		s, err := loadPasskeys()
		if err == nil && s.Salt == "" {
			salt := make([]byte, 16)
			rand.Read(salt)
			s.Salt = base64.StdEncoding.EncodeToString(salt)
			err = savePasskeys(s)
		}
		passkeyMu.Unlock()
		if err != nil {
			fail(rw, err)
			return
		}
		under := keyUnder(s.Salt, p.key)
		var exclude []map[string]string
		for _, c := range s.Passkeys {
			if c.Under == under && c.Origin == requestOrigin(r) {
				exclude = append(exclude, map[string]string{"type": "public-key", "id": c.ID})
			}
		}
		if len(exclude) >= passkeysMax {
			fail(rw, fmt.Errorf("this key has %d passkeys, the most it may: remove one first", passkeysMax))
			return
		}
		var params []map[string]any
		for _, a := range passkeyAlgs {
			params = append(params, map[string]any{"type": "public-key", "alg": a})
		}
		ch := p.chals.make("create", p.key)
		name := "magpie"
		writeJSON(rw, map[string]any{
			"challenge": base64.RawURLEncoding.EncodeToString(ch),
			"rp":        map[string]string{"id": rpID, "name": name},
			"user": map[string]string{
				"id":          base64.RawURLEncoding.EncodeToString(passkeyUserID(under)),
				"name":        name + " · " + rpID,
				"displayName": name + " · " + rpID,
			},
			"pubKeyCredParams":       params,
			"excludeCredentials":     exclude,
			"authenticatorSelection": map[string]string{"residentKey": "required", "userVerification": "preferred"},
			"attestation":            "none",
			"timeout":                int(challengeLife / time.Millisecond),
		})
	})
	mux.HandleFunc("POST /api/passkeys/add", func(rw http.ResponseWriter, r *http.Request) {
		var in passkeyAnswer
		if err := readPasskeyJSON(rw, r, &in); err != nil {
			fail(rw, err)
			return
		}
		if err := p.add(r, in); err != nil {
			fail(rw, err)
			return
		}
		answer(rw, r)
	})
	mux.HandleFunc("POST /api/passkeys/remove", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			ID string `json:"id"`
		}
		if err := readPasskeyJSON(rw, r, &in); err != nil {
			fail(rw, err)
			return
		}
		if err := p.remove(in.ID); err != nil {
			fail(rw, err)
			return
		}
		answer(rw, r)
	})
	return mux
}

// add checks a registration and keeps its passkey under the page's key.
func (p *passkeyPage) add(r *http.Request, in passkeyAnswer) error {
	cdj, err1 := b64u(in.ClientDataJSON)
	att, err2 := b64u(in.AttestationObject)
	if err := errors.Join(err1, err2); err != nil {
		return errors.New("the browser's answer isn't a passkey's")
	}
	ch, made, ok := p.chals.take(cdj, "create")
	// a challenge made for another key (the key changed since) is none
	if !ok || made.key != p.key {
		return errors.New("making the passkey took too long: try again")
	}
	origin := requestOrigin(r)
	if err := checkClientData(cdj, "webauthn.create", ch, origin); err != nil {
		return err
	}
	rpID, err := passkeyOrigin(origin)
	if err != nil {
		return err
	}
	nc, err := readAttestation(att, rpID)
	if err != nil {
		return err
	}
	passkeyMu.Lock()
	defer passkeyMu.Unlock()
	s, err := loadPasskeys()
	if err != nil {
		return err
	}
	if s.Salt == "" {
		return errors.New("making the passkey took too long: try again")
	}
	id := base64.RawURLEncoding.EncodeToString(nc.id)
	under := keyUnder(s.Salt, p.key)
	n := 0
	for _, c := range s.Passkeys {
		if c.ID == id {
			return errors.New("magpie has this passkey already")
		}
		if c.Under == under {
			n++
		}
	}
	if n >= passkeysMax {
		return fmt.Errorf("this key has %d passkeys, the most it may: remove one first", passkeysMax)
	}
	s.Passkeys = append(s.Passkeys, passkeyCred{
		ID: id, Name: passkeyName(in.Name, n+1), Origin: origin,
		PublicKey: base64.StdEncoding.EncodeToString(nc.spki), Alg: nc.alg,
		Under: under, Count: nc.count, Created: time.Now().UTC(),
	})
	if err := savePasskeys(s); err != nil {
		return err
	}
	log.Printf("magpie web: a passkey was added for %s", origin)
	return nil
}

// remove takes away the passkey id, when it is one of the page's key's.
func (p *passkeyPage) remove(id string) error {
	passkeyMu.Lock()
	defer passkeyMu.Unlock()
	s, err := loadPasskeys()
	if err != nil {
		return err
	}
	under := keyUnder(s.Salt, p.key)
	for i, c := range s.Passkeys {
		if c.ID == id && c.Under == under {
			s.Passkeys = append(s.Passkeys[:i], s.Passkeys[i+1:]...)
			return savePasskeys(s)
		}
	}
	return errors.New("no such passkey")
}

// hasAny says whether the page's key has a passkey, for the sign-in page
// to say why its button is missing where passkeys can't work.
func (p *passkeyPage) hasAny() bool {
	passkeyMu.Lock()
	s, err := loadPasskeys()
	passkeyMu.Unlock()
	if err != nil || len(s.Passkeys) == 0 {
		return false
	}
	u := keyUnder(s.Salt, p.key)
	for _, c := range s.Passkeys {
		if u != "" && c.Under == u {
			return true
		}
	}
	return false
}
