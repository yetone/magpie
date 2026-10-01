package davsync

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/provider"
)

// fakeS3 is an S3 server with one bucket and one access key, keeping
// objects in memory. It checks every request's signature as S3 does —
// signed again from what arrived, so a header or path that changed on the
// way fails — and does conditional PUTs, or, noCond, refuses them as a
// server without them does.
type fakeS3 struct {
	mu                 sync.Mutex
	id, secret, region string
	bucket             string
	objects            map[string][]byte
	etags              map[string]string
	noCond             bool
	log                []string // each request: method, and the condition sent
	onWrite            func()   // runs once, at the next PUT or HEAD (a write's look first), before it is answered
}

func newFakeS3(t *testing.T) (*fakeS3, *httptest.Server) {
	f := &fakeS3{id: "AKIDTEST", secret: "s3cr3t/+key", region: "us-east-1", bucket: "bkt",
		objects: map[string][]byte{}, etags: map[string]string{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeS3) fail(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>%s</Message></Error>`, code, code)
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	if !f.signed(r, body) {
		f.fail(w, http.StatusForbidden, "SignatureDoesNotMatch")
		return
	}
	bucket, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if bucket != f.bucket {
		f.fail(w, http.StatusNotFound, "NoSuchBucket")
		return
	}
	cond := r.Header.Get("If-Match")
	if v := r.Header.Get("If-None-Match"); v != "" {
		cond = "none:" + v
	}
	f.log = append(f.log, strings.TrimSpace(r.Method+" "+cond))
	if f.onWrite != nil && (r.Method == http.MethodPut || r.Method == http.MethodHead) {
		f.onWrite()
		f.onWrite = nil
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		b, ok := f.objects[key]
		if !ok {
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			f.fail(w, http.StatusNotFound, "NoSuchKey")
			return
		}
		w.Header().Set("ETag", f.etags[key])
		if r.Method == http.MethodGet {
			w.Write(b)
		}
	case http.MethodPut:
		m, none := r.Header.Get("If-Match"), r.Header.Get("If-None-Match")
		if f.noCond && (m != "" || none != "") {
			f.fail(w, http.StatusNotImplemented, "NotImplemented")
			return
		}
		_, there := f.objects[key]
		if none == "*" && there || m != "" && m != f.etags[key] {
			f.fail(w, http.StatusPreconditionFailed, "PreconditionFailed")
			return
		}
		f.store(key, body)
		w.Header().Set("ETag", f.etags[key])
	default:
		f.fail(w, http.StatusNotImplemented, "NotImplemented")
	}
}

func (f *fakeS3) store(key string, b []byte) {
	h := md5.Sum(b)
	f.objects[key], f.etags[key] = b, `"`+hex.EncodeToString(h[:])+`"`
}

// signed is whether r carries this key's signature over what arrived.
func (f *fakeS3) signed(r *http.Request, body []byte) bool {
	auth := r.Header.Get("Authorization")
	var cred, names string
	for _, part := range strings.Split(strings.TrimPrefix(auth, "AWS4-HMAC-SHA256 "), ", ") {
		k, v, _ := strings.Cut(part, "=")
		switch k {
		case "Credential":
			cred = v
		case "SignedHeaders":
			names = v
		}
	}
	at, err := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
	if err != nil || !strings.HasPrefix(cred, f.id+"/"+at.Format("20060102")+"/"+f.region+"/s3/") || r.Header.Get("X-Amz-Content-Sha256") != sum(body) {
		return false
	}
	u, err := url.Parse("http://" + r.Host + r.RequestURI)
	if err != nil {
		return false
	}
	// the path as S3 has it: decoded, then encoded again as SigV4 says —
	// a + sent bare is signed as %2B
	u.RawPath = awsEscape(u.Path, true)
	again := &http.Request{Method: r.Method, URL: u, Host: r.Host, Header: http.Header{}}
	for _, n := range strings.Split(names, ";") {
		if n != "host" {
			again.Header[http.CanonicalHeaderKey(n)] = r.Header.Values(n)
		}
	}
	signer{id: f.id, secret: f.secret, region: f.region, service: "s3"}.sign(again, sum(body), at)
	return again.Header.Get("Authorization") == auth
}

func (f *fakeS3) config(srv *httptest.Server) Config {
	return Config{URL: "s3://bkt/team x+y", Endpoint: srv.URL, User: f.id, Password: f.secret, Passphrase: "correct horse", Keys: true, Agents: true}
}

// The object read with its ETag, written with If-None-Match: * the first
// time and If-Match after: a write over a version since changed is
// refused, as another computer's first one is over one already there.
func TestS3Put(t *testing.T) {
	f, srv := newFakeS3(t)
	ctx := context.Background()
	s, err := newS3(f.config(srv))
	if err != nil {
		t.Fatal(err)
	}
	if data, etag, err := s.get(ctx); data != nil || etag != "" || err != nil {
		t.Fatalf("nothing there: %q %q %v", data, etag, err)
	}
	if err := s.put(ctx, []byte("one"), ""); err != nil {
		t.Fatal(err)
	}
	if got := string(f.objects["team x+y/magpie/magpie.magpie-backup"]); got != "one" {
		t.Fatalf("stored %q", got)
	}
	if err := s.put(ctx, []byte("another first"), ""); !errors.Is(err, errChanged) {
		t.Fatalf("a first write over one there: %v", err)
	}
	data, etag, err := s.get(ctx)
	if string(data) != "one" || etag == "" || err != nil {
		t.Fatalf("read: %q %q %v", data, etag, err)
	}
	if err := s.put(ctx, []byte("two"), etag); err != nil {
		t.Fatal(err)
	}
	if err := s.put(ctx, []byte("three"), etag); !errors.Is(err, errChanged) {
		t.Fatalf("a write over a version since changed: %v", err)
	}
	if got := string(f.objects["team x+y/magpie/magpie.magpie-backup"]); got != "two" {
		t.Fatalf("stored %q", got)
	}
	want := []string{"GET", "PUT none:*", "PUT none:*", "GET", "PUT " + etag, "PUT " + etag}
	if !slices.Equal(f.log, want) {
		t.Fatalf("requests %q, want %q", f.log, want)
	}

	// what went wrong, said
	for _, c := range []struct {
		change func(*Config)
		want   string
	}{
		{func(c *Config) { c.Password = "wrong" }, "refused the access key ID or secret"},
		{func(c *Config) { c.User = "AKIDOTHER" }, "refused the access key ID or secret"},
		{func(c *Config) { c.URL = "s3://nope" }, "there is no bucket nope"},
	} {
		cfg := f.config(srv)
		c.change(&cfg)
		s, _ := newS3(cfg)
		if _, _, err := s.get(ctx); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v, want %q", cfg, err, c.want)
		}
	}
}

// A server that answers a conditional PUT 501 NotImplemented: the ETag is
// looked at with a HEAD before a plain PUT, which still refuses a write
// over another computer's, and the server is not asked for one again.
func TestS3NoConditions(t *testing.T) {
	f, srv := newFakeS3(t)
	f.noCond = true
	ctx := context.Background()
	s, err := newS3(f.config(srv))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.put(ctx, []byte("one"), ""); err != nil {
		t.Fatal(err)
	}
	if err := s.put(ctx, []byte("another first"), ""); !errors.Is(err, errChanged) {
		t.Fatalf("a first write over one there: %v", err)
	}
	_, etag, _ := s.get(ctx)
	f.mu.Lock()
	f.store("team x+y/magpie/magpie.magpie-backup", []byte("theirs"))
	f.mu.Unlock()
	if err := s.put(ctx, []byte("two"), etag); !errors.Is(err, errChanged) {
		t.Fatalf("a write over another computer's: %v", err)
	}
	_, etag, _ = s.get(ctx)
	if err := s.put(ctx, []byte("two"), etag); err != nil {
		t.Fatal(err)
	}
	if got := string(f.objects["team x+y/magpie/magpie.magpie-backup"]); got != "two" {
		t.Fatalf("stored %q", got)
	}
	want := []string{"PUT none:*", "HEAD", "PUT", "HEAD", "GET", "HEAD", "GET", "HEAD", "PUT"}
	if !slices.Equal(f.log, want) {
		t.Fatalf("requests %q, want %q", f.log, want)
	}
	for _, e := range []s3Error{{Status: 400, Code: "InvalidArgument", Message: "If-None-Match is not supported"}, {Status: 501}, {Status: 400, Code: "NotImplemented"}} {
		if !noConditions(e) {
			t.Errorf("%+v: not taken for no conditional writes", e)
		}
	}
	if noConditions(s3Error{Status: 400, Code: "InvalidArgument", Message: "Invalid storage class"}) {
		t.Error("any 400 taken for no conditional writes")
	}
}

// Where the object is asked for: AWS's endpoint for the region with the
// bucket in the host name, R2 signing as "auto", the bucket in the path
// when asked, and always for an address or a bucket with a dot.
func TestS3Address(t *testing.T) {
	for _, c := range []struct {
		cfg         Config
		url, region string
	}{
		{Config{URL: "s3://bkt/a b/"}, "https://bkt.s3.us-east-1.amazonaws.com/a%20b/magpie/magpie.magpie-backup", "us-east-1"},
		{Config{URL: "s3://bkt", Region: "eu-west-2"}, "https://bkt.s3.eu-west-2.amazonaws.com/magpie/magpie.magpie-backup", "eu-west-2"},
		{Config{URL: "s3://bkt/p", Endpoint: "0123abc.r2.cloudflarestorage.com"}, "https://bkt.0123abc.r2.cloudflarestorage.com/p/magpie/magpie.magpie-backup", "auto"},
		{Config{URL: "s3://bkt/p", Endpoint: "https://s3.example.com/", PathStyle: true}, "https://s3.example.com/bkt/p/magpie/magpie.magpie-backup", "us-east-1"},
		{Config{URL: "s3://bkt", Endpoint: "http://192.168.1.5:9000"}, "http://192.168.1.5:9000/bkt/magpie/magpie.magpie-backup", "us-east-1"},
		{Config{URL: "s3://my.bkt", Endpoint: "https://s3.example.com"}, "https://s3.example.com/my.bkt/magpie/magpie.magpie-backup", "us-east-1"},
	} {
		s, err := newS3(c.cfg)
		if err != nil {
			t.Fatal(err)
		}
		if got := s.objectURL().String(); got != c.url || s.sig.region != c.region {
			t.Errorf("%+v: %s in %s, want %s in %s", c.cfg, got, s.sig.region, c.url, c.region)
		}
	}
	for _, bad := range []Config{{URL: "s3://"}, {URL: "s3:///prefix"}, {URL: "s3://bkt", Endpoint: "ftp://x"}, {URL: "s3://bkt", Endpoint: "https://x?y=z"}} {
		if err := Check(bad); err == nil {
			t.Errorf("%+v: no error", bad)
		}
	}
}

// Sync through S3 as through WebDAV: the bucket only ever holds the sealed
// file, another computer's setup comes in from it, and a write between one
// computer's read and write is caught and synced again over — with
// conditional writes and without.
func TestS3Sync(t *testing.T) {
	for _, noCond := range []bool{false, true} {
		t.Run(fmt.Sprintf("noCond=%v", noCond), func(t *testing.T) {
			f, srv := newFakeS3(t)
			f.noCond = noCond
			ctx := context.Background()
			cfg := f.config(srv)
			const key = "team x+y/magpie/magpie.magpie-backup"

			a, b := newComputer(t), newComputer(t)
			a.use(t)
			provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "sk-plain-key-1"})
			if err := Configure(cfg); err != nil {
				t.Fatal(err)
			}
			if err := Now(ctx); err != nil {
				t.Fatal(err)
			}
			if v := Status(); v.Error != "" || v.Last.IsZero() || v.Kind != "s3" || v.Endpoint != srv.URL {
				t.Fatalf("a: %+v", v)
			}
			up := f.objects[key]
			if len(up) == 0 || strings.Contains(string(up), "deepseek") || strings.Contains(string(up), "sk-plain-key-1") {
				t.Fatalf("in the bucket: %q", up)
			}
			if in, err := backup.Open(up, "correct horse"); err != nil || in.Providers[0].Key != "sk-plain-key-1" {
				t.Fatalf("opened: %v %+v", err, in.Providers)
			}

			b.use(t)
			provider.Save(provider.Provider{ID: "mine", Name: "Mine", Chat: "https://x/v1", Key: "kb"})
			if err := Configure(cfg); err != nil {
				t.Fatal(err)
			}
			if err := Now(ctx); err != nil {
				t.Fatal(err)
			}
			if got := ids(); !slices.Equal(got, []string{"deepseek=sk-plain-key-1"}) {
				t.Fatalf("b's providers: %v", got)
			}

			// another computer writes between b's read and its write
			other, _ := backup.Collect(true, "")
			other.Providers = append(other.Providers, provider.Provider{ID: "two", Name: "Two", Chat: "https://y/v1", Key: "k"})
			other.Created = time.Now().Add(-time.Minute)
			raced, _ := backup.Seal(other, "correct horse")
			f.onWrite = func() { f.store(key, raced) }
			provider.Save(provider.Provider{ID: "three", Name: "Three", Chat: "https://z/v1", Key: "k"})
			if err := Now(ctx); err != nil {
				t.Fatal(err)
			}
			remote, err := backup.Open(f.objects[key], "correct horse")
			if err != nil || !slices.ContainsFunc(remote.Providers, func(p provider.Provider) bool { return p.ID == "three" }) {
				t.Fatalf("after the race: %v %+v", err, remote.Providers)
			}
			if n := Status().Notice; n == nil || !slices.Equal(n.There, []string{"providers"}) {
				t.Fatalf("the other's write went unseen: %+v", n)
			}
		})
	}
}

// The secret goes with the endpoint and access key it was given for: kept
// for another bucket there, asked for with another key or endpoint, never
// taken from a WebDAV password; an access key is needed.
func TestConfigureS3Secret(t *testing.T) {
	newComputer(t).use(t)
	r2 := Config{URL: "s3://one", Endpoint: "https://acct.r2.cloudflarestorage.com", User: "AKID", Password: "secret", Passphrase: "correct horse"}
	dav := Config{URL: "https://dav.example.com/dav/", User: "AKID", Password: "pw", Passphrase: "correct horse"}
	for _, tc := range []struct {
		from  Config
		to    Config
		want  string // the secret saved after
		asked bool
	}{
		{r2, Config{URL: "s3://two/p", Endpoint: "acct.r2.cloudflarestorage.com", User: "AKID"}, "secret", false},
		{r2, Config{URL: "s3://one", Endpoint: "https://other.r2.cloudflarestorage.com", User: "AKID"}, "", true},
		{r2, Config{URL: "s3://one", Endpoint: r2.Endpoint, User: "AKOTHER"}, "", true},
		{r2, Config{URL: "s3://one", User: "AKID"}, "", true}, // AWS
		{dav, Config{URL: "s3://one", Endpoint: "https://dav.example.com", User: "AKID"}, "", true},
		{r2, Config{URL: "https://acct.r2.cloudflarestorage.com/", User: "AKID"}, "", true},
	} {
		if err := Configure(tc.from); err != nil {
			t.Fatal(err)
		}
		if kept, _ := SavedPassword(tc.to); kept != (tc.want != "") {
			t.Errorf("%+v: kept %v", tc.to, kept)
		}
		err := Configure(tc.to)
		if tc.asked {
			if err == nil || !strings.Contains(err.Error(), "type the") {
				t.Errorf("%+v: %v, want the secret asked for", tc.to, err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if c, _ := Load(); c.Password != tc.want {
			t.Errorf("%+v: secret %q", tc.to, c.Password)
		}
	}
	if err := Configure(Config{URL: "s3://one", Password: "x", Passphrase: "p"}); err == nil || !strings.Contains(err.Error(), "access key ID") {
		t.Errorf("no access key: %v", err)
	}
	if err := Configure(Config{URL: "s3://one", User: "AKID", Password: "same", Passphrase: "same"}); err == nil || !strings.Contains(err.Error(), "of its own") {
		t.Errorf("the secret as the passphrase: %v", err)
	}
	// back to WebDAV: nothing of S3's stays
	Configure(r2)
	if err := Configure(Config{URL: "https://dav.example.com/dav/", Password: "pw", Passphrase: "correct horse", Endpoint: "x", Region: "y", PathStyle: true}); err != nil {
		t.Fatal(err)
	}
	if c, _ := Load(); c.Endpoint != "" || c.Region != "" || c.PathStyle {
		t.Errorf("WebDAV with S3's left: %+v", c)
	}
}

// Moving sync between a WebDAV folder and an S3 bucket keeps the server
// moved from, its password too, and moving back finds it (ARNO on
// Discord: trying S3 wiped the WebDAV address, user and password). A
// sync.json from before, with no other server in it, reads as it did.
func TestConfigureKeepsTheOtherKind(t *testing.T) {
	newComputer(t).use(t)
	old := `{"url":"https://dav.example.com/dav/","user":"me","password":"pw","passphrase":"correct horse","keys":true,"agents":true}`
	os.MkdirAll(path(""), 0o755)
	if err := os.WriteFile(path("sync.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, ok := Load(); !ok || c.URL != "https://dav.example.com/dav/" || c.Password != "pw" || c.Other != nil {
		t.Fatalf("a setup from before: %v %+v", ok, c)
	}
	if v := Status(); v.Kind != "webdav" || !v.PasswordSet || v.Other != nil {
		t.Fatalf("its view: %+v", v)
	}

	r2 := Config{URL: "s3://bkt/p", Endpoint: "https://acct.r2.cloudflarestorage.com", Region: "auto", PathStyle: true, User: "AKID", Password: "secret", Keys: true}
	if err := Configure(r2); err != nil {
		t.Fatal(err)
	}
	c, _ := Load()
	if !c.S3() || c.Password != "secret" || c.Passphrase != "correct horse" ||
		c.Other == nil || *c.Other != (Server{URL: "https://dav.example.com/dav/", User: "me", Password: "pw"}) {
		t.Fatalf("to S3: %+v %+v", c, c.Other)
	}
	v := Status()
	if v.Kind != "s3" || v.Other == nil || v.Other.Kind != "webdav" || v.Other.URL != "https://dav.example.com/dav/" || v.Other.User != "me" || !v.Other.PasswordSet {
		t.Fatalf("its view: %+v %+v", v, v.Other)
	}
	// a change to the bucket keeps it too
	if err := Configure(Config{URL: "s3://bkt/q", Endpoint: r2.Endpoint, Region: "auto", PathStyle: true, User: "AKID"}); err != nil {
		t.Fatal(err)
	}
	if c, _ := Load(); c.Password != "secret" || c.Other == nil || c.Other.Password != "pw" {
		t.Fatalf("S3 changed: %+v", c)
	}

	// back to WebDAV, the password not typed again: the one kept, and the
	// bucket kept in turn
	back := Config{URL: "https://dav.example.com/dav/", User: "me"}
	if kept, needed := SavedPassword(back); !kept || needed {
		t.Fatalf("SavedPassword kept %v, needed %v", kept, needed)
	}
	if err := Configure(back); err != nil {
		t.Fatal(err)
	}
	c, _ = Load()
	if c.S3() || c.Password != "pw" || c.Endpoint != "" || c.Other == nil ||
		*c.Other != (Server{URL: "s3://bkt/q", User: "AKID", Password: "secret", Endpoint: r2.Endpoint, Region: "auto", PathStyle: true}) {
		t.Fatalf("back to WebDAV: %+v %+v", c, c.Other)
	}
	// and to S3 again: its secret kept for that key at that endpoint alone
	if err := Configure(Config{URL: "s3://bkt/q", Endpoint: r2.Endpoint, User: "AKID"}); err != nil {
		t.Fatal(err)
	}
	if c, _ := Load(); c.Password != "secret" || c.Other == nil || c.Other.Password != "pw" {
		t.Fatalf("to S3 again: %+v", c)
	}
	Configure(back)
	if err := Configure(Config{URL: "s3://bkt/q", Endpoint: "https://other.example.com", User: "AKID"}); err == nil || !strings.Contains(err.Error(), "type the secret") {
		t.Fatalf("another endpoint: %v", err)
	}
	// the server given is never taken as the one kept
	if err := Configure(Config{URL: "https://dav.example.com/dav/", User: "me", Other: &Server{URL: "s3://evil", User: "x", Password: "y"}}); err != nil {
		t.Fatal(err)
	}
	if c, _ := Load(); c.Other == nil || c.Other.URL != "s3://bkt/q" {
		t.Fatalf("the kept one replaced: %+v", c.Other)
	}

	// off is off: nothing kept
	if err := Off(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path("sync.json")); !os.IsNotExist(err) {
		t.Fatalf("after off: %v", err)
	}
}

// A real S3 server, when one is given: MAGPIE_S3_TEST=endpoint,bucket,key
// id,secret[,region] (a MinIO: docker run -p 9000:9000 minio/minio server
// /data, and a bucket made there). The same writes as TestS3Put, then a sync.
func TestS3Live(t *testing.T) {
	spec := os.Getenv("MAGPIE_S3_TEST")
	if spec == "" {
		t.Skip("MAGPIE_S3_TEST is not set")
	}
	p := strings.Split(spec, ",")
	if len(p) < 4 {
		t.Fatal("MAGPIE_S3_TEST=endpoint,bucket,key id,secret[,region]")
	}
	cfg := Config{URL: fmt.Sprintf("s3://%s/magpie-test-%d x+y", p[1], time.Now().UnixNano()), Endpoint: p[0], User: p[2], Password: p[3], Passphrase: "correct horse", PathStyle: true, Keys: true}
	if len(p) > 4 {
		cfg.Region = p[4]
	}
	ctx := context.Background()
	s, err := newS3(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if data, _, err := s.get(ctx); data != nil || err != nil {
		t.Fatalf("nothing there: %q %v", data, err)
	}
	if err := s.put(ctx, []byte("one"), ""); err != nil {
		t.Fatal(err)
	}
	if err := s.put(ctx, []byte("another first"), ""); !errors.Is(err, errChanged) {
		t.Fatalf("a first write over one there: %v", err)
	}
	data, etag, err := s.get(ctx)
	if string(data) != "one" || etag == "" || err != nil {
		t.Fatalf("read: %q %q %v", data, etag, err)
	}
	if err := s.put(ctx, []byte("two"), etag); err != nil {
		t.Fatal(err)
	}
	if err := s.put(ctx, []byte("three"), etag); !errors.Is(err, errChanged) {
		t.Fatalf("a write over a version since changed: %v", err)
	}
	if data, _, _ := s.get(ctx); string(data) != "two" {
		t.Fatalf("read: %q", data)
	}
	_, fellBack := unconditional.Load(s.endpoint.String() + " " + s.bucket)
	t.Logf("conditional PUTs: %v", !fellBack)

	bad := cfg
	bad.Password += "x"
	if s, _ := newS3(bad); s != nil {
		if _, _, err := s.get(ctx); err == nil || !strings.Contains(err.Error(), "refused the access key") {
			t.Errorf("a wrong secret: %v", err)
		}
	}

	// a sync through it, and another computer's setup in from it
	cfg.URL += "/sync"
	a, b := newComputer(t), newComputer(t)
	a.use(t)
	provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k1"})
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if err := Now(ctx); err != nil {
		t.Fatal(err)
	}
	s, _ = newS3(cfg)
	up, _, err := s.get(ctx)
	if err != nil || len(up) == 0 || strings.Contains(string(up), "deepseek") {
		t.Fatalf("in the bucket: %v %q", err, up)
	}
	b.use(t)
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if err := Now(ctx); err != nil {
		t.Fatal(err)
	}
	if got := ids(); !slices.Equal(got, []string{"deepseek=k1"}) {
		t.Fatalf("b's providers: %v", got)
	}
}
