package davsync

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeDigest is a WebDAV server that takes Digest only (RFC 7616, qop=auth)
// and answers a Basic header with a Digest challenge, as Apache's
// mod_auth_digest and nginx's auth modules do.
type fakeDigest struct {
	mu       sync.Mutex
	alg      string
	newHash  func() hash.Hash
	basic    int // requests that came with Basic
	digested int // requests that passed
	seenNC   map[string]bool
}

func (f *fakeDigest) h(s string) string {
	x := f.newHash()
	x.Write([]byte(s))
	return hex.EncodeToString(x.Sum(nil))
}

func (f *fakeDigest) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Basic ") {
		f.basic++
	}
	if strings.HasPrefix(auth, "Digest ") {
		p := authParams(auth[7:])
		ha1 := f.h("me:fixture realm:pw")
		ha2 := f.h(r.Method + ":" + r.URL.RequestURI())
		want := f.h(ha1 + ":fixturenonce:" + p["nc"] + ":" + p["cnonce"] + ":auth:" + ha2)
		if p["response"] == want && p["username"] == "me" && p["uri"] == r.URL.RequestURI() &&
			p["qop"] == "auth" && p["algorithm"] == f.alg && p["opaque"] == "op" && !f.seenNC[p["nc"]] {
			f.seenNC[p["nc"]] = true
			f.digested++
			body := make([]byte, 16)
			n, _ := r.Body.Read(body)
			if r.Method == http.MethodPut && string(body[:n]) != "payload" {
				w.WriteHeader(http.StatusBadRequest) // the body must survive the second send
				return
			}
			w.WriteHeader(http.StatusNotFound)
			return
		}
	}
	w.Header().Add("WWW-Authenticate", `Digest realm="fixture realm", nonce="fixturenonce", opaque="op", algorithm=`+f.alg+`, qop="auth"`)
	w.WriteHeader(http.StatusUnauthorized)
}

// A server that takes Digest only is signed in to with the same user name
// and password, in MD5 and in SHA-256, and a PUT's body is sent again.
func TestDAVDigest(t *testing.T) {
	for _, c := range []struct {
		alg string
		h   func() hash.Hash
	}{{"MD5", md5.New}, {"SHA-256", sha256.New}} {
		t.Run(c.alg, func(t *testing.T) {
			f := &fakeDigest{alg: c.alg, newHash: c.h, seenNC: map[string]bool{}}
			srv := httptest.NewServer(f)
			defer srv.Close()
			d, err := newDAV(Config{URL: srv.URL, User: "me", Password: "pw"})
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if _, _, err := d.get(ctx, version{}); err != nil {
				t.Fatalf("get: %v", err)
			}
			res, err := d.send(ctx, http.MethodPut, d.url("a"), []byte("payload"), nil)
			if err != nil {
				t.Fatalf("put: %v", err)
			}
			res.Body.Close()
			if res.StatusCode == http.StatusBadRequest {
				t.Fatalf("the PUT's body was lost on the second send")
			}
			if f.basic != 1 || f.digested < 2 {
				t.Errorf("basic requests %d (want 1, the first), digest passes %d (want >= 2)", f.basic, f.digested)
			}
		})
	}
}

// A wrong password still reads as one, and a server asking for Basic is
// left alone.
func TestDAVDigestWrongPassword(t *testing.T) {
	f := &fakeDigest{alg: "MD5", newHash: md5.New, seenNC: map[string]bool{}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	d, _ := newDAV(Config{URL: srv.URL, User: "me", Password: "bad"})
	if _, _, err := d.get(context.Background(), version{}); err != errLogin {
		t.Errorf("err = %v, want errLogin", err)
	}
	var basic int
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); ok && u == "me" && p == "pw" {
			basic++
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="x"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer b.Close()
	d, _ = newDAV(Config{URL: b.URL, User: "me", Password: "pw"})
	if _, _, err := d.get(context.Background(), version{}); err != nil || basic == 0 {
		t.Errorf("basic server: err %v, passes %d", err, basic)
	}
}
