package davsync

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/provider"
)

// apacheDAV answers as InfiniCLOUD's (TeraCloud's) WebDAV plausibly does
// (#1362). Its server is Apache with mod_dav_fs (OPTIONS on
// https://domi.teracloud.jp/dav/ says "Server: Apache" and
// "DAV: <http://apache.org/dav/propset/fs/1>"), and mod_deflate runs with
// its default DeflateAlterETag AddSuffix: a GET of its own page asked with
// Accept-Encoding: gzip comes back gzip with ETag "fb-5d88268abcf08-gzip",
// asked without it comes back plain with "fb-5d88268abcf08", and a GET with
// If-Match: "fb-5d88268abcf08-gzip" is answered 412 (all seen 2026-10-09,
// without an account). So, as httpd 2.4's source does:
//   - a PUT answers 201 or 204 with no ETag (mod_dav.c's dav_created: "###
//     insert an ETag header?");
//   - If-Match and If-None-Match are compared with the file's own ETag;
//   - a GET that may be gzip is sent gzip, its ETag with "-gzip" added
//     inside the quotes;
//   - a HEAD says the file's own ETag and length.
//
// Whether TeraCloud's mod_deflate compresses files under /dav/ wasn't seen:
// that needs an account. It is what "works once, then always fails" needs.
type apacheDAV struct {
	mu    sync.Mutex
	files map[string][]byte
	etags map[string]string
	dirs  map[string]bool
	n     int
	// gzipped are the GETs answered compressed; refused the PUTs answered
	// 412; between, when set, runs before the next PUT with If-Match is
	// looked at — another computer's write landing in between
	gzipped, refused int
	between          func()
}

func newApacheDAV() *apacheDAV {
	return &apacheDAV{files: map[string][]byte{}, etags: map[string]string{}, dirs: map[string]bool{"/dav": true}}
}

// write is a write to p as mod_dav_fs makes it: a new ETag (Apache's is
// the size and the time, in hex).
func (a *apacheDAV) write(p string, b []byte) {
	a.n++
	a.files[p], a.etags[p] = b, fmt.Sprintf(`"%x-%x"`, len(b), 0x5d88268abcf08+a.n)
}

func (a *apacheDAV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if u, p, ok := r.BasicAuth(); !ok || u != "me" || p != "pw" {
		w.Header().Set("WWW-Authenticate", `Basic realm="InfiniCLOUD Basic Authentication dav"`)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	w.Header().Set("Server", "Apache")
	p := r.URL.Path
	body, there := a.files[p]
	etag := a.etags[p]
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		if !there {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if m := r.Header.Get("If-None-Match"); m != "" && m == etag {
			w.Header().Set("ETag", etag)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		gz := r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip")
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Vary", "Accept-Encoding")
		if !gz {
			w.Header().Set("ETag", etag)
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			w.WriteHeader(http.StatusOK)
			if r.Method == http.MethodGet {
				w.Write(body)
			}
			return
		}
		a.gzipped++
		w.Header().Set("ETag", strings.TrimSuffix(etag, `"`)+`-gzip"`)
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusOK)
		zw := gzip.NewWriter(w)
		zw.Write(body)
		zw.Close()
	case http.MethodPut:
		if !a.dirs[urlDir(p)] {
			w.WriteHeader(http.StatusConflict)
			return
		}
		if m := r.Header.Get("If-Match"); m != "" {
			if a.between != nil {
				a.between()
				a.between = nil
				etag = a.etags[p]
			}
			if m != etag || strings.HasPrefix(m, "W/") {
				a.refused++
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
		}
		b, _ := io.ReadAll(r.Body)
		a.write(p, b)
		if there {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Location", "http://"+r.Host+p)
		w.WriteHeader(http.StatusCreated)
	case "MKCOL":
		d := strings.TrimSuffix(p, "/")
		if a.dirs[d] {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		a.dirs[d] = true
		w.WriteHeader(http.StatusCreated)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// Sync with TeraCloud worked the first time and failed every time after
// with "the file on the server changed meanwhile" (#1362): its PUT says no
// ETag, so the next sync read the file in full, Go asked for it gzip, and
// the ETag that came back was the gzip copy's — which no If-Match matches.
// The file is read as it is now, and every sync after the first writes.
func TestSyncTeraCloudGzipETag(t *testing.T) {
	fake := newApacheDAV()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ctx := context.Background()
	cfg := Config{URL: srv.URL + "/dav/", User: "me", Password: "pw", Passphrase: "correct horse", Keys: true, Agents: true}

	a, b := newComputer(t), newComputer(t)
	a.use(t)
	provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k1"})
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if err := Now(ctx); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	for i, id := range []string{"kimi", "three", "four"} {
		provider.Save(provider.Provider{ID: id, Name: id, Chat: "https://" + id + "/v1", Key: "k" + id})
		if err := Now(ctx); err != nil {
			t.Fatalf("sync %d after the first: %v (%d writes refused, %d reads sent gzip)", i+2, err, fake.refused, fake.gzipped)
		}
	}
	if fake.refused != 0 || fake.gzipped != 0 {
		t.Fatalf("%d writes refused, %d reads sent gzip", fake.refused, fake.gzipped)
	}
	remote, err := backup.Open(fake.files["/dav/magpie/magpie.magpie-backup"], "correct horse")
	if err != nil || len(remote.Providers) != 4 {
		t.Fatalf("on the server: %v %+v", err, remote.Providers)
	}

	// another computer joins, adds one, and a gets it
	b.use(t)
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if err := Now(ctx); err != nil {
		t.Fatalf("b's first sync: %v", err)
	}
	provider.Save(provider.Provider{ID: "five", Name: "Five", Chat: "https://five/v1", Key: "k5"})
	if err := Now(ctx); err != nil {
		t.Fatalf("b's sync: %v", err)
	}
	a.use(t)
	if err := Now(ctx); err != nil {
		t.Fatalf("a after b: %v", err)
	}
	if got := ids(); !slices.Equal(got, []string{"deepseek=k1", "five=k5", "four=kfour", "kimi=kkimi", "three=kthree"}) {
		t.Fatalf("a after b added: %v", got)
	}
}

// A server that sends the file gzip though it was asked for as it is: the
// file is still read whole.
func TestDAVGetGzipAnyway(t *testing.T) {
	want := []byte(`{"app":"magpie"}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"1-gzip"`)
		w.Header().Set("Content-Encoding", "gzip")
		zw := gzip.NewWriter(w)
		zw.Write(want)
		zw.Close()
	}))
	defer srv.Close()
	d, err := newDAV(Config{URL: srv.URL + "/dav/"})
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := d.get(context.Background(), version{})
	if err != nil || string(got) != string(want) {
		t.Fatalf("read %q, %v", got, err)
	}
}

// Reading the file as it is doesn't loosen the check that another
// computer wrote in between: on the same server, a write over the version
// read is refused once another write landed after the read, and the sync
// then merges over the other computer's version, losing none of it.
func TestSyncTeraCloudStillSeesOtherWrites(t *testing.T) {
	fake := newApacheDAV()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ctx := context.Background()
	const name = "/dav/magpie/magpie.magpie-backup"

	// the remote alone: a write over a version another write replaced
	d, err := newDAV(Config{URL: srv.URL + "/dav/", User: "me", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.put(ctx, []byte("one"), ""); err != nil {
		t.Fatal(err)
	}
	_, v, err := d.get(ctx, version{})
	if err != nil || v.ETag == "" {
		t.Fatalf("read: %+v %v", v, err)
	}
	fake.mu.Lock()
	fake.write(name, []byte("other computer"))
	fake.mu.Unlock()
	if _, err := d.put(ctx, []byte("two"), v.ETag); !errors.Is(err, errChanged) {
		t.Fatalf("write over a replaced version: %v, want errChanged", err)
	}
	if got := string(fake.files[name]); got != "other computer" {
		t.Fatalf("the other computer's write was overwritten: %q", got)
	}
	delete(fake.files, name)
	delete(fake.etags, name)

	// the whole sync: b's write lands between a's read and a's write
	cfg := Config{URL: srv.URL + "/dav/", User: "me", Password: "pw", Passphrase: "correct horse", Keys: true, Agents: true}
	a, b := newComputer(t), newComputer(t)
	a.use(t)
	provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k1"})
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if err := Now(ctx); err != nil {
		t.Fatal(err)
	}
	b.use(t)
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if err := Now(ctx); err != nil {
		t.Fatal(err)
	}
	provider.Save(provider.Provider{ID: "kimi", Name: "Kimi", Chat: "https://api.moonshot.cn/v1", Key: "k2"})
	// b's sealed file, made by a sync whose result is then taken back off
	// the server, to land in the middle of a's
	fake.mu.Lock()
	before, beforeTag := fake.files[name], fake.etags[name]
	fake.mu.Unlock()
	if err := Now(ctx); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	bs := fake.files[name]
	fake.files[name], fake.etags[name] = before, beforeTag
	fake.between = func() { fake.write(name, bs) }
	refused := fake.refused
	fake.mu.Unlock()

	a.use(t)
	provider.Save(provider.Provider{ID: "three", Name: "Three", Chat: "https://z/v1", Key: "k3"})
	if err := Now(ctx); err != nil {
		t.Fatal(err)
	}
	if fake.refused != refused+1 {
		t.Fatalf("a's write over b's: %d refused, want 1", fake.refused-refused)
	}
	// both changed the providers: the newer, a's, stays, and b's is kept
	// aside and told — as any sync that saw the other's write does
	n := Status().Notice
	if n == nil || !slices.Contains(n.There, "providers") || n.Saved == "" {
		t.Fatalf("a wrote over b's providers without seeing them: %+v", n)
	}
	saved, _ := filepath.Glob(filepath.Join(n.Saved, "*-server"+backup.Ext))
	if len(saved) != 1 {
		t.Fatalf("b's file kept aside: %v", saved)
	}
	kept, err := os.ReadFile(saved[0])
	if err != nil || string(kept) != string(bs) {
		t.Fatalf("the copy kept aside isn't b's file: %v", err)
	}
}
