package davsync

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// pan123DAV answers as 123pan's WebDAV plausibly does (#4993). The
// reporter saw, with curl (2026-11-03): a plain GET of the file answers
// 200 with its 1,910,440 bytes, a HEAD answers 200 with a valid ETag, and
// a GET sent with If-None-Match — the ETag the HEAD said — answers HTTP
// 500, about half a minute later. 304 never comes: the conditions aren't
// understood, they break the read. Writes weren't seen (the read fails
// first), so If-Match is taken to work.
type pan123DAV struct {
	mu    sync.Mutex
	files map[string][]byte
	etags map[string]string
	dirs  map[string]bool
	n     int
	// condGets are the GETs that came with a condition, plainGets those
	// that came without; puts the writes
	condGets, plainGets, puts int
	// ifMatch is the If-Match the last write came with
	ifMatch string
}

func newPan123DAV() *pan123DAV {
	return &pan123DAV{files: map[string][]byte{}, etags: map[string]string{}, dirs: map[string]bool{"/dav": true}}
}

func (p *pan123DAV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if u, pw, ok := r.BasicAuth(); !ok || u != "me" || pw != "pw" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "" {
			p.condGets++
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		p.plainGets++
		b, ok := p.files[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("ETag", p.etags[r.URL.Path])
		w.Write(b)
	case http.MethodPut:
		if !p.dirs[urlDir(r.URL.Path)] {
			w.WriteHeader(http.StatusConflict)
			return
		}
		b, _ := io.ReadAll(r.Body)
		p.ifMatch = r.Header.Get("If-Match")
		p.puts++
		p.n++
		p.files[r.URL.Path], p.etags[r.URL.Path] = b, `"`+strings.Repeat("e", p.n)+`"`
		w.Header().Set("ETag", p.etags[r.URL.Path])
		w.WriteHeader(http.StatusCreated)
	case "MKCOL":
		dir := strings.TrimSuffix(r.URL.Path, "/")
		if p.dirs[dir] {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		p.dirs[dir] = true
		w.WriteHeader(http.StatusCreated)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// A read sent with the version last seen is answered 500 by 123pan, not
// 304: the read is asked again without the conditions, and the version is
// marked so no condition is sent to that server again — the ETag stays,
// for the write to go over (#4993).
func TestDAV123panConditionalRead(t *testing.T) {
	fake := newPan123DAV()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	d, err := newDAV(Config{URL: srv.URL + "/dav/", User: "me", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := d.put(ctx, []byte("v1"), ""); err != nil {
		t.Fatal(err)
	}
	etag := fake.etags["/dav/magpie/magpie.magpie-backup"]

	// the read with the version last seen: answered 500, asked again
	// without the conditions, and said so in the version it gives back
	data, v, err := d.get(ctx, version{ETag: etag})
	if err != nil {
		t.Fatalf("a read 123pan answers 500: %v", err)
	}
	if string(data) != "v1" {
		t.Fatalf("read %q, want v1", data)
	}
	if !v.NoCondRead || v.ETag != etag {
		t.Fatalf("version %+v, want the ETag kept and no conditional reads from here on", v)
	}
	if fake.condGets != 1 || fake.plainGets != 1 {
		t.Fatalf("%d conditional GETs, %d plain: want one of each — the 500, then the read without the conditions", fake.condGets, fake.plainGets)
	}

	// the version marked: no condition is sent again
	fake.condGets, fake.plainGets = 0, 0
	if _, _, err := d.get(ctx, v); err != nil {
		t.Fatal(err)
	}
	if fake.condGets != 0 || fake.plainGets != 1 {
		t.Fatalf("%d conditional GETs, %d plain after the marking: want no conditions sent", fake.condGets, fake.plainGets)
	}

	// the write after the read goes over the ETag, and the version it
	// leaves keeps the marking
	v2, err := d.put(ctx, []byte("v2"), v.ETag)
	if err != nil {
		t.Fatal(err)
	}
	if !v2.NoCondRead {
		t.Fatal("the version after the write lost the marking: the next sync would ask with the conditions again")
	}
}

// 123pan breaks a sync that reads with the version last seen: the first
// sync reads the file and writes, and every sync after reads with
// If-None-Match, is answered 500, and fails. The read is asked again
// without the conditions, once, and the syncs go through (#4993).
func TestSync123panConditionalRead(t *testing.T) {
	fake := newPan123DAV()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ctx := context.Background()
	a := newComputer(t)
	a.use(t)
	provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k1"})
	if err := Configure(Config{URL: srv.URL + "/dav/", User: "me", Password: "pw", Passphrase: "correct horse", Keys: true}); err != nil {
		t.Fatal(err)
	}
	if err := Now(ctx); err != nil {
		t.Fatal(err)
	}

	// the first sync read without the conditions; the next reads with the
	// ETag, is answered 500, and asks again without them
	if err := Now(ctx); err != nil {
		t.Fatalf("the sync after the first: %v", err)
	}
	if fake.condGets != 1 {
		t.Fatalf("%d conditional GETs after the second sync, want 1: the one answered 500", fake.condGets)
	}

	// from here the conditions are not sent: the sync reads plainly
	condGets, plainGets := fake.condGets, fake.plainGets
	if err := Now(ctx); err != nil {
		t.Fatal(err)
	}
	if fake.condGets != condGets || fake.plainGets != plainGets+1 {
		t.Fatalf("%d conditional GETs, %d plain after the third sync: want the conditions gone for good", fake.condGets, fake.plainGets)
	}

	// changed here, the merge still goes up, over the ETag
	provider.Save(provider.Provider{ID: "kimi", Name: "Kimi", Chat: "https://api.moonshot.cn/v1", Key: "k2"})
	if err := Now(ctx); err != nil {
		t.Fatal(err)
	}
	if fake.puts != 2 {
		t.Fatalf("%d writes, want 2: the first sync's and the merged one", fake.puts)
	}
	if fake.ifMatch != `"e"` {
		t.Fatalf("the merged write came with If-Match %q, want the version read plainly: another computer's write would be gone over", fake.ifMatch)
	}
	// and every sync after, changed or not, still reads plainly: the
	// marking rides on each version a plain read or a write leaves
	for range 3 {
		if err := Now(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if fake.condGets != condGets {
		t.Fatalf("%d conditional GETs after more syncs, want %d: the marking was lost and 123pan asked with a condition again", fake.condGets, condGets)
	}
	b := newComputer(t)
	b.use(t)
	if err := Configure(Config{URL: srv.URL + "/dav/", User: "me", Password: "pw", Passphrase: "correct horse", Keys: true}); err != nil {
		t.Fatal(err)
	}
	if err := Now(ctx); err != nil {
		t.Fatal(err)
	}
	if got := ids(); !slices.Equal(got, []string{"deepseek=k1", "kimi=k2"}) {
		t.Fatalf("b after joining: %v", got)
	}
}
