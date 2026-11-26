package gui

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// cborEnc writes v (int, int64, []byte, string, bool, map[any]any) as CBOR,
// the little a software authenticator says.
func cborEnc(v any) []byte {
	head := func(major byte, n uint64) []byte {
		switch {
		case n < 24:
			return []byte{major<<5 | byte(n)}
		case n < 1<<8:
			return []byte{major<<5 | 24, byte(n)}
		case n < 1<<16:
			return binary.BigEndian.AppendUint16([]byte{major<<5 | 25}, uint16(n))
		case n < 1<<32:
			return binary.BigEndian.AppendUint32([]byte{major<<5 | 26}, uint32(n))
		}
		return binary.BigEndian.AppendUint64([]byte{major<<5 | 27}, n)
	}
	switch v := v.(type) {
	case int:
		return cborEnc(int64(v))
	case int64:
		if v < 0 {
			return head(1, uint64(-1-v))
		}
		return head(0, uint64(v))
	case []byte:
		return append(head(2, uint64(len(v))), v...)
	case string:
		return append(head(3, uint64(len(v))), v...)
	case bool:
		if v {
			return []byte{0xf5}
		}
		return []byte{0xf4}
	case map[any]any:
		keys := make([][]byte, 0, len(v))
		for k := range v {
			keys = append(keys, cborEnc(k))
		}
		sort.Slice(keys, func(i, j int) bool { return bytes.Compare(keys[i], keys[j]) < 0 })
		out := head(5, uint64(len(v)))
		for _, kb := range keys {
			out = append(out, kb...)
			for k, val := range v {
				if bytes.Equal(cborEnc(k), kb) {
					out = append(out, cborEnc(val)...)
				}
			}
		}
		return out
	}
	panic("cborEnc: no such type")
}

// softAuth is a software authenticator: one passkey, of alg, made at
// origin, which counts its signatures when counts.
type softAuth struct {
	alg    int
	signer crypto.Signer
	id     []byte
	count  uint32
	counts bool
}

func newSoftAuth(t *testing.T, alg int) *softAuth {
	t.Helper()
	a := &softAuth{alg: alg, id: make([]byte, 16)}
	rand.Read(a.id)
	var err error
	switch alg {
	case coseES256:
		a.signer, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	case coseEdDSA:
		_, a.signer, err = ed25519.GenerateKey(rand.Reader)
	case coseRS256:
		a.signer, err = rsa.GenerateKey(rand.Reader, 2048)
	}
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (a *softAuth) cose() []byte {
	switch k := a.signer.Public().(type) {
	case *ecdsa.PublicKey:
		b, _ := k.Bytes()
		return cborEnc(map[any]any{1: 2, 3: coseES256, -1: 1, -2: b[1:33], -3: b[33:]})
	case ed25519.PublicKey:
		return cborEnc(map[any]any{1: 1, 3: coseEdDSA, -1: 6, -2: []byte(k)})
	case *rsa.PublicKey:
		return cborEnc(map[any]any{1: 3, 3: coseRS256, -1: k.N.Bytes(), -2: big.NewInt(int64(k.E)).Bytes()})
	}
	panic("no key")
}

func (a *softAuth) authData(rpID string, flags byte, attested bool) []byte {
	h := sha256.Sum256([]byte(rpID))
	out := append(h[:], flags)
	if a.counts {
		a.count++
	}
	out = binary.BigEndian.AppendUint32(out, a.count)
	if attested {
		out = append(out, make([]byte, 16)...) // AAGUID
		out = binary.BigEndian.AppendUint16(out, uint16(len(a.id)))
		out = append(out, a.id...)
		out = append(out, a.cose()...)
	}
	return out
}

func clientDataFor(typ string, challenge, origin string) []byte {
	b, _ := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": origin, "crossOrigin": false})
	return b
}

func (a *softAuth) sign(authData, cdj []byte) []byte {
	h := sha256.Sum256(cdj)
	msg := append(append([]byte{}, authData...), h[:]...)
	var sig []byte
	var err error
	switch a.alg {
	case coseEdDSA:
		sig, err = a.signer.Sign(rand.Reader, msg, crypto.Hash(0))
	default:
		d := sha256.Sum256(msg)
		sig, err = a.signer.Sign(rand.Reader, d[:], crypto.SHA256)
	}
	if err != nil {
		panic(err)
	}
	return sig
}

var b64 = base64.RawURLEncoding.EncodeToString

// passkeyHome starts a test with no passkeys.json, and leaves none.
func passkeyHome(t *testing.T) {
	t.Helper()
	os.Remove(passkeysFile())
	passkeyUnders.Clear()
	t.Cleanup(func() { os.Remove(passkeysFile()) })
}

// pkReq is a request the page at origin sends, with the cookie when not "".
func pkReq(method, origin, path, cookie string, body any) *http.Request {
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	r := httptest.NewRequest(method, origin+path, rd)
	r.Header.Set("Origin", origin)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: "magpie_web_3430", Value: cookie})
	}
	return r
}

func pkDo(h http.Handler, r *http.Request) (*httptest.ResponseRecorder, map[string]any) {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var m map[string]any
	json.Unmarshal(w.Body.Bytes(), &m)
	return w, m
}

// passkeyServed is the page with key, as magpie web serves it.
func passkeyServed(key string) http.Handler {
	return webHandler("3430", key, "", nil, http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { rw.WriteHeader(http.StatusTeapot) }))
}

// register has a adds its passkey at origin to the page h, signed in with
// key, through the page's own routes; it says the reply.
func (a *softAuth) register(t *testing.T, h http.Handler, origin, key, name string, edit func(cdj, att *[]byte)) (int, map[string]any) {
	t.Helper()
	w, o := pkDo(h, pkReq("POST", origin, "/api/passkeys/options", key, map[string]any{}))
	if w.Code != 200 {
		return w.Code, o
	}
	rpID := o["rp"].(map[string]any)["id"].(string)
	cdj := clientDataFor("webauthn.create", o["challenge"].(string), origin)
	att := cborEnc(map[any]any{"fmt": "none", "attStmt": map[any]any{}, "authData": a.authData(rpID, flagUP|flagUV|flagAT, true)})
	if edit != nil {
		edit(&cdj, &att)
	}
	w, o = pkDo(h, pkReq("POST", origin, "/api/passkeys/add", key, map[string]any{
		"name": name, "id": b64(a.id), "clientDataJSON": b64(cdj), "attestationObject": b64(att),
	}))
	return w.Code, o
}

// assertion is a's answer to a sign-in at origin: what tamper leaves of it.
type assertion struct {
	rpID, origin, typ   string
	flags               byte
	cdj, ad, sig, id    []byte
	challenge, sentFrom string
}

// signIn has a sign in at origin to h; tamper changes the answer before
// it is signed (sign) or after (send). It says the reply and the cookie.
func (a *softAuth) signIn(t *testing.T, h http.Handler, origin string, tamper func(*assertion), after func(*assertion)) (*httptest.ResponseRecorder, string) {
	t.Helper()
	w, o := pkDo(h, pkReq("POST", origin, passkeyOptionsPath, "", nil))
	if w.Code != 200 {
		return w, ""
	}
	as := &assertion{rpID: strings.Split(strings.TrimPrefix(strings.TrimPrefix(origin, "https://"), "http://"), ":")[0], origin: origin, typ: "webauthn.get", flags: flagUP | flagUV, challenge: o["challenge"].(string), sentFrom: origin, id: a.id}
	if tamper != nil {
		tamper(as)
	}
	as.cdj = clientDataFor(as.typ, as.challenge, as.origin)
	as.ad = a.authData(as.rpID, as.flags, false)
	as.sig = a.sign(as.ad, as.cdj)
	if after != nil {
		after(as)
	}
	w, _ = pkDo(h, pkReq("POST", as.sentFrom, passkeySignInPath, "", map[string]any{
		"id": b64(as.id), "clientDataJSON": b64(as.cdj), "authenticatorData": b64(as.ad), "signature": b64(as.sig), "back": "/settings?x=1",
	}))
	cookie := ""
	for _, c := range w.Result().Cookies() {
		if c.Name == "magpie_web_3430" {
			cookie = c.Value
		}
	}
	return w, cookie
}

const pkOrigin = "https://magpie.example.test"

// A passkey added by a browser signed in with the key signs a browser in
// as pasting the key does: the same cookie, holding the key, and the
// page behind it. Each algorithm a browser offers is taken.
func TestPasskeySignsInAsItsKey(t *testing.T) {
	passkeyHome(t)
	key := "0123456789abcdef-passkey"
	h := passkeyServed(key)
	for _, alg := range []int{coseES256, coseEdDSA, coseRS256} {
		a := newSoftAuth(t, alg)
		if code, o := a.register(t, h, pkOrigin, key, "  My   laptop ", nil); code != 200 {
			t.Fatalf("alg %d: add: %d %v", alg, code, o)
		}
		w, cookie := a.signIn(t, h, pkOrigin, nil, nil)
		if w.Code != 200 || cookie != key {
			t.Fatalf("alg %d: sign-in: %d %s, cookie %q", alg, w.Code, w.Body, cookie)
		}
		var got struct{ Back string }
		json.Unmarshal(w.Body.Bytes(), &got)
		if got.Back != "/settings?x=1" {
			t.Errorf("back: %q", got.Back)
		}
		r := httptest.NewRequest("GET", pkOrigin+"/", nil)
		r.AddCookie(&http.Cookie{Name: "magpie_web_3430", Value: cookie})
		if w, _ := pkDo(h, r); w.Code != http.StatusTeapot {
			t.Errorf("the page with the passkey's cookie: %d", w.Code)
		}
	}
	w, o := pkDo(h, pkReq("GET", pkOrigin, "/api/passkeys", key, nil))
	list, _ := o["passkeys"].([]any)
	if w.Code != 200 || len(list) != 3 || list[0].(map[string]any)["name"] != "My laptop" || list[0].(map[string]any)["used"] == nil {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	// only public keys are kept, and never the key
	b, _ := os.ReadFile(passkeysFile())
	if bytes.Contains(b, []byte(key)) {
		t.Errorf("passkeys.json holds the key: %s", b)
	}
	var s passkeyStore
	json.Unmarshal(b, &s)
	for _, c := range s.Passkeys {
		der, _ := base64.StdEncoding.DecodeString(c.PublicKey)
		if _, err := x509.ParsePKIXPublicKey(der); err != nil {
			t.Errorf("%s's key is no public key: %v", c.Name, err)
		}
	}
	if fi, err := os.Stat(passkeysFile()); err != nil {
		t.Error(err)
	} else if fi.Mode().Perm() != 0o600 && os.PathSeparator == '/' {
		t.Errorf("passkeys.json is %v", fi.Mode())
	}
}

// A passkey stands for the key it was made under: once the page's key
// changes, it signs no one in, and the new key lists none of the old's.
// Removed, it signs no one in either.
func TestPasskeyGoesWithItsKey(t *testing.T) {
	passkeyHome(t)
	oldKey, newKey := "0123456789abcdef-old", "0123456789abcdef-new"
	a := newSoftAuth(t, coseES256)
	if code, o := a.register(t, passkeyServed(oldKey), pkOrigin, oldKey, "phone", nil); code != 200 {
		t.Fatalf("add: %d %v", code, o)
	}
	h := passkeyServed(newKey)
	if w, cookie := a.signIn(t, h, pkOrigin, nil, nil); w.Code != http.StatusUnauthorized || cookie != "" || !strings.Contains(w.Body.String(), "no longer has") {
		t.Fatalf("under a new key: %d %s %q", w.Code, w.Body, cookie)
	}
	if _, o := pkDo(h, pkReq("GET", pkOrigin, "/api/passkeys", newKey, nil)); len(o["passkeys"].([]any)) != 0 {
		t.Errorf("the new key lists the old's: %v", o)
	}
	// the old key's own browser can't remove it through the new key's page
	if w, _ := pkDo(h, pkReq("POST", pkOrigin, "/api/passkeys/remove", newKey, map[string]string{"id": b64(a.id)})); w.Code == 200 {
		t.Errorf("removed another key's passkey")
	}
	old := passkeyServed(oldKey)
	if w, _ := a.signIn(t, old, pkOrigin, nil, nil); w.Code != 200 {
		t.Fatalf("under its own key: %d %s", w.Code, w.Body)
	}
	if w, o := pkDo(old, pkReq("POST", pkOrigin, "/api/passkeys/remove", oldKey, map[string]string{"id": b64(a.id)})); w.Code != 200 || len(o["passkeys"].([]any)) != 0 {
		t.Fatalf("remove: %d %s", w.Code, w.Body)
	}
	if w, _ := a.signIn(t, old, pkOrigin, nil, nil); w.Code != http.StatusUnauthorized {
		t.Errorf("a removed passkey signed in: %d", w.Code)
	}
}

// Browser access's New key takes the old key's passkeys away with it.
func TestAppPageNewKeyDropsItsPasskeys(t *testing.T) {
	passkeyHome(t)
	t.Cleanup(func() { os.Remove(appPageFile()) })
	if code, s := appPagePost(t, `{"mode":""}`); code != 200 {
		t.Fatalf("browser access: %d %+v", code, s)
	}
	c, err := loadAppPage()
	if err != nil || CheckWebKey(c.Key) != nil {
		t.Fatalf("no key: %+v %v", c, err)
	}
	a, other := newSoftAuth(t, coseES256), newSoftAuth(t, coseES256)
	if code, o := a.register(t, passkeyServed(c.Key), pkOrigin, c.Key, "", nil); code != 200 {
		t.Fatalf("add: %d %v", code, o)
	}
	otherKey := "0123456789abcdef-other"
	if code, o := other.register(t, passkeyServed(otherKey), pkOrigin, otherKey, "", nil); code != 200 {
		t.Fatalf("add: %d %v", code, o)
	}
	if code, _ := appPagePost(t, `{"newKey":true}`); code != 200 {
		t.Fatalf("new key: %d", code)
	}
	if l, err := passkeysUnder(c.Key); err != nil || len(l) != 0 {
		t.Errorf("the old key's passkeys stayed: %v %v", l, err)
	}
	if l, err := passkeysUnder(otherKey); err != nil || len(l) != 1 {
		t.Errorf("another key's passkey went too: %v %v", l, err)
	}
}

// Every part of a sign-in is checked: a challenge this page gave, once and
// for a sign-in; the origin the browser names in the signed data, in the
// request and the one the passkey was made at; the site's rpId hash; the
// user's presence; the signature; the count; a passkey this magpie knows;
// and the page itself sending it.
func TestPasskeySignInChecksEachPart(t *testing.T) {
	passkeyHome(t)
	key := "0123456789abcdef-checks"
	h := passkeyServed(key)
	a := newSoftAuth(t, coseES256)
	a.counts = true
	if code, o := a.register(t, h, pkOrigin, key, "", nil); code != 200 {
		t.Fatalf("add: %d %v", code, o)
	}
	other := "https://evil.example.test"
	var used string
	for _, c := range []struct {
		name          string
		tamper, after func(*assertion)
	}{
		{"a challenge never given", func(as *assertion) { as.challenge = b64(make([]byte, 32)) }, nil},
		{"a challenge used before", func(as *assertion) { as.challenge = used }, nil},
		{"a registration's type", func(as *assertion) { as.typ = "webauthn.create" }, nil},
		{"another origin in the signed data", func(as *assertion) { as.origin = other }, nil},
		{"sent from another origin", nil, func(as *assertion) { as.sentFrom = other }},
		{"another site's rpId", func(as *assertion) { as.rpID = "evil.example.test" }, nil},
		{"no user present", func(as *assertion) { as.flags = flagUV }, nil},
		{"a signature over other data", nil, func(as *assertion) { as.cdj = append(as.cdj[:len(as.cdj)-1], `,"other":1}`...) }},
		{"a broken signature", nil, func(as *assertion) { as.sig[len(as.sig)/2] ^= 1 }},
		{"a count that went back", nil, func(as *assertion) {
			as.ad = a.authData(as.rpID, as.flags, false)
			binary.BigEndian.PutUint32(as.ad[33:], 1)
			as.sig = a.sign(as.ad, as.cdj)
		}},
		{"a passkey this magpie doesn't know", nil, func(as *assertion) { as.id = []byte("someone else") }},
		{"in another site's frame", nil, func(as *assertion) {
			as.cdj = append(bytes.TrimSuffix(as.cdj, []byte(`"crossOrigin":false,"origin":"`+as.origin+`","type":"webauthn.get"}`)), []byte(`"crossOrigin":true,"origin":"`+as.origin+`","type":"webauthn.get"}`)...)
			if !bytes.Contains(as.cdj, []byte(`"crossOrigin":true`)) {
				t.Fatalf("no crossOrigin in %s", as.cdj)
			}
			as.sig = a.sign(as.ad, as.cdj)
		}},
		{"a challenge given to make a passkey", func(as *assertion) {
			_, o := pkDo(h, pkReq("POST", pkOrigin, "/api/passkeys/options", key, map[string]any{}))
			as.challenge = o["challenge"].(string)
		}, nil},
		{"a challenge that expired", func(as *assertion) {
			challengeLife = -time.Second
			defer func() { challengeLife = 5 * time.Minute }()
			_, o := pkDo(h, pkReq("POST", pkOrigin, passkeyOptionsPath, "", nil))
			as.challenge = o["challenge"].(string)
		}, nil},
	} {
		w, cookie := a.signIn(t, h, pkOrigin, c.tamper, c.after)
		if w.Code == 200 || cookie != "" {
			t.Errorf("%s: signed in: %d %s", c.name, w.Code, w.Body)
		}
		if used == "" {
			w, _ := a.signIn(t, h, pkOrigin, func(as *assertion) { used = as.challenge }, nil)
			if w.Code != 200 {
				t.Fatalf("a good sign-in: %d %s", w.Code, w.Body)
			}
		}
	}
	// another page's request is turned away before it is read
	r := pkReq("POST", pkOrigin, passkeyOptionsPath, "", nil)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.Header.Set("Origin", other)
	if w, _ := pkDo(h, r); w.Code != http.StatusForbidden {
		t.Errorf("another page's options: %d", w.Code)
	}
	r = pkReq("GET", pkOrigin, "/api/passkeys", key, nil)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	if w, _ := pkDo(h, r); w.Code != http.StatusForbidden {
		t.Errorf("another page's list: %d", w.Code)
	}
	// and the passkeys' routes need the key's cookie
	if w, _ := pkDo(h, pkReq("GET", pkOrigin, "/api/passkeys", "", nil)); w.Code != http.StatusUnauthorized {
		t.Errorf("the list without the key: %d", w.Code)
	}
	if w, _ := a.signIn(t, h, pkOrigin, nil, nil); w.Code != 200 {
		t.Fatalf("after all that, a good sign-in: %d %s", w.Code, w.Body)
	}
}

// A passkey is made at the origin it signs in at alone, and a
// registration is checked as a sign-in is.
func TestPasskeyRegistrationChecksEachPart(t *testing.T) {
	passkeyHome(t)
	key := "0123456789abcdef-reg"
	h := passkeyServed(key)
	for _, c := range []struct {
		name string
		edit func(cdj, att *[]byte)
	}{
		{"a sign-in's type", func(cdj, att *[]byte) {
			var m map[string]any
			json.Unmarshal(*cdj, &m)
			*cdj = clientDataFor("webauthn.get", m["challenge"].(string), pkOrigin)
		}},
		{"another origin", func(cdj, att *[]byte) {
			var m map[string]any
			json.Unmarshal(*cdj, &m)
			*cdj = clientDataFor("webauthn.create", m["challenge"].(string), "https://evil.example.test")
		}},
		{"a challenge never given", func(cdj, att *[]byte) {
			*cdj = clientDataFor("webauthn.create", b64(make([]byte, 32)), pkOrigin)
		}},
		{"another site's rpId", func(cdj, att *[]byte) {
			a := newSoftAuth(t, coseES256)
			*att = cborEnc(map[any]any{"fmt": "none", "attStmt": map[any]any{}, "authData": a.authData("evil.example.test", flagUP|flagAT, true)})
		}},
		{"no key in it", func(cdj, att *[]byte) {
			a := newSoftAuth(t, coseES256)
			*att = cborEnc(map[any]any{"fmt": "none", "attStmt": map[any]any{}, "authData": a.authData("magpie.example.test", flagUP, false)})
		}},
	} {
		if code, o := newSoftAuth(t, coseES256).register(t, h, pkOrigin, key, "", c.edit); code == 200 {
			t.Errorf("%s: added: %v", c.name, o)
		}
	}
	// made at one origin, it signs in at another of the same host not
	a := newSoftAuth(t, coseES256)
	if code, o := a.register(t, h, pkOrigin, key, "", nil); code != 200 {
		t.Fatalf("add: %d %v", code, o)
	}
	if w, _ := a.signIn(t, h, pkOrigin+":8443", nil, nil); w.Code == 200 {
		t.Errorf("signed in at another port")
	}
	if l, _ := passkeysUnder(key); len(l) != 1 {
		t.Errorf("passkeys after the refused ones: %d", len(l))
	}
}

// Passkeys need a secure context at a name: plain http on a network
// address, or an IP address, makes none; localhost does.
func TestPasskeyNeedsHTTPSAtAName(t *testing.T) {
	passkeyHome(t)
	key := "0123456789abcdef-https"
	h := passkeyServed(key)
	for origin, ok := range map[string]bool{
		"http://192.168.1.5:3430":  false,
		"https://192.168.1.5:3430": false,
		"https://[fd7a::1]:3430":   false,
		"http://magpie.lan:3430":   false,
		"http://localhost:3430":    true,
		"https://magpie.lan":       true,
	} {
		code, o := newSoftAuth(t, coseES256).register(t, h, origin, key, "", nil)
		if (code == 200) != ok {
			t.Errorf("%s: add: %d %v", origin, code, o)
		}
		if w, _ := pkDo(h, pkReq("POST", origin, passkeyOptionsPath, "", nil)); (w.Code == 200) != ok {
			t.Errorf("%s: sign-in's challenge: %d", origin, w.Code)
		}
	}
}

// A passkeys.json that can't be read is not empty: no passkey signs in
// on it, and nothing is written over it.
func TestPasskeyUnreadFileSignsNoOneIn(t *testing.T) {
	passkeyHome(t)
	key := "0123456789abcdef-unread"
	h := passkeyServed(key)
	a := newSoftAuth(t, coseES256)
	if code, o := a.register(t, h, pkOrigin, key, "", nil); code != 200 {
		t.Fatalf("add: %d %v", code, o)
	}
	b, _ := os.ReadFile(passkeysFile())
	bad := append(b[:len(b)/2:len(b)/2], "\n"...)
	os.WriteFile(passkeysFile(), bad, 0o600)
	if w, _ := a.signIn(t, h, pkOrigin, nil, nil); w.Code == 200 {
		t.Errorf("signed in on a broken file")
	}
	if code, _ := newSoftAuth(t, coseES256).register(t, h, pkOrigin, key, "", nil); code == 200 {
		t.Errorf("added over a broken file")
	}
	if w, _ := pkDo(h, pkReq("POST", pkOrigin, "/api/passkeys/remove", key, map[string]string{"id": b64(a.id)})); w.Code == 200 {
		t.Errorf("removed from a broken file")
	}
	if got, _ := os.ReadFile(passkeysFile()); !bytes.Equal(got, bad) {
		t.Errorf("the broken file was written over")
	}
	if w, _ := pkDo(h, pkReq("GET", pkOrigin, "/api/passkeys", key, nil)); w.Code == 200 {
		t.Errorf("listed a broken file as read")
	}
}

// The sign-in page offers a passkey when its key has one, beside the
// key's field, which stays as it was.
func TestTurnedAwayOffersAPasskey(t *testing.T) {
	passkeyHome(t)
	key := "0123456789abcdef-page"
	h := passkeyServed(key)
	page := func() string {
		r := httptest.NewRequest("GET", pkOrigin+"/", nil)
		r.Header.Set("Accept", "text/html")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("turned away: %d", w.Code)
		}
		return w.Body.String()
	}
	if p := page(); strings.Contains(p, "Sign in with passkey") || !strings.Contains(p, `name="key"`) || strings.Contains(p, "%!") {
		t.Fatalf("with no passkey: %s", p)
	}
	if code, o := newSoftAuth(t, coseES256).register(t, h, pkOrigin, key, "", nil); code != 200 {
		t.Fatalf("add: %d %v", code, o)
	}
	p := page()
	for _, want := range []string{"Sign in with passkey", `name="key"`, passkeyOptionsPath, passkeySignInPath, "isSecureContext", "https"} {
		if !strings.Contains(p, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if strings.Contains(p, "%!") || strings.Contains(p, key) {
		t.Errorf("the page: %s", p)
	}
}
