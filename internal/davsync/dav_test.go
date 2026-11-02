package davsync

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// A 403 is told apart: a folder in the address that isn't on the server, one
// the account may only read, and a server that won't say — not all of them a
// wrong password, as they read before.
func TestDAVForbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "me" || p != "pw" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		path := strings.TrimSuffix(r.URL.Path, "/")
		switch {
		case r.Method == "PROPFIND" && (path == "" || path == "/ro" || path == "/ro/magpie"):
			w.WriteHeader(http.StatusMultiStatus)
		case r.Method == "PROPFIND" && path == "/missing":
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/ro/"):
			w.WriteHeader(http.StatusNotFound)
		default: // a Synology: anything else, 403
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	for _, c := range []struct{ dir, pass, want string }{
		{"/missing", "pw", "there is no folder /missing"},
		{"/ro", "pw", "doesn't let this account change files in /ro/magpie"},
		{"/secret", "pw", "doesn't let this account use /secret"},
		// the server's root in the address: its folder is /magpie, not //magpie
		{"", "pw", "doesn't let magpie make its folder /magpie ("},
		{"/missing", "bad", "user name or password"},
	} {
		d, err := newDAV(Config{URL: srv.URL + c.dir, User: "me", Password: c.pass})
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = d.get(ctx, version{})
		if err == nil {
			_, err = d.put(ctx, []byte("x"), "")
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.dir, err, c.want)
		}
	}
}

// A 403 on the write itself, with everything listing fine, is told apart by
// magpie's own folder: there, the server refuses to change files in it —
// 坚果云 (Nutstore)'s free tier refuses writes with 403 once the account's
// upload quota is used up (#4927) — and the error names the folder the write
// went to, not just the address's, and says the quota. Missing, the server
// refused to have magpie make it, and the error says to make it by hand.
func TestDAVWriteRefused(t *testing.T) {
	ctx := context.Background()
	refuse := func(statusOf func(r *http.Request) int) *dav {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(statusOf(r))
		}))
		t.Cleanup(srv.Close)
		d, err := newDAV(Config{URL: srv.URL + "/dav/"})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}

	// the folder is there (an earlier sync made it), the write is refused
	d := refuse(func(r *http.Request) int {
		switch {
		case r.Method == "PROPFIND" && (r.URL.Path == "/dav/" || r.URL.Path == "/dav/magpie/"):
			return http.StatusMultiStatus
		default:
			return http.StatusForbidden
		}
	})
	if _, err := d.put(ctx, []byte("x"), ""); err == nil || !strings.Contains(err.Error(), "change files in /dav/magpie") || !strings.Contains(err.Error(), "quota") {
		t.Fatalf("a refused write into the folder there: %v", err)
	}

	// the folder isn't there, and the server refuses to have magpie make it
	d = refuse(func(r *http.Request) int {
		switch {
		case r.Method == "PROPFIND" && r.URL.Path == "/dav/":
			return http.StatusMultiStatus
		case r.Method == "MKCOL":
			return http.StatusForbidden
		default:
			return http.StatusConflict
		}
	})
	if _, err := d.put(ctx, []byte("x"), ""); err == nil || !strings.Contains(err.Error(), "doesn't let magpie make its folder /dav/magpie") || !strings.Contains(err.Error(), "make the folder magpie there first") {
		t.Fatalf("a refused folder: %v", err)
	}

	// an address with no folder of its own: magpie's is at the root,
	// named once, not "//magpie"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PROPFIND" && (r.URL.Path == "/" || r.URL.Path == "/magpie/") {
			w.WriteHeader(http.StatusMultiStatus)
			return
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	d, err := newDAV(Config{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.put(ctx, []byte("x"), ""); err == nil || !strings.Contains(err.Error(), "change files in /magpie ") || strings.Contains(err.Error(), "//") {
		t.Fatalf("a refused write under an address with no folder: %v", err)
	}
}

// OpenList's and Alist's /dav/ lists their storages: a folder can't be made
// there (MKCOL 405, as if it were) and a file can't be put (404). The error
// says to put a storage in the address, naming them.
func TestDAVStorageRoot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "PROPFIND" && r.URL.Path == "/dav/" && r.Header.Get("Depth") == "1":
			w.WriteHeader(http.StatusMultiStatus)
			io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><D:multistatus xmlns:D="DAV:">`+
				`<D:response><D:href>/dav/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop></D:propstat></D:response>`+
				`<D:response><D:href>/dav/local/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop></D:propstat></D:response>`+
				`<D:response><D:href>/dav/aliyun/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop></D:propstat></D:response>`+
				`<D:response><D:href>/dav/readme.txt</D:href><D:propstat><D:prop><D:resourcetype/></D:prop></D:propstat></D:response>`+
				`</D:multistatus>`)
		case r.Method == "MKCOL":
			w.WriteHeader(http.StatusMethodNotAllowed)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	d, err := newDAV(Config{URL: srv.URL + "/dav/"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.put(context.Background(), []byte("x"), "")
	if err == nil || !strings.Contains(err.Error(), "(local, aliyun), like "+srv.URL+"/dav/local") {
		t.Fatalf("%v", err)
	}
}

// A Synology answers 403 for anything under a folder that isn't there yet,
// PROPFIND too: magpie's own folder, before the first sync, is made rather
// than taken for a folder the account can't write in.
func TestDAVForbiddenUntilMade(t *testing.T) {
	made, stored := false, ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimSuffix(r.URL.Path, "/")
		switch {
		case path == "/data" && r.Method == "PROPFIND":
			w.WriteHeader(http.StatusMultiStatus)
		case path == "/data/magpie" && r.Method == "MKCOL":
			made = true
			w.WriteHeader(http.StatusCreated)
		case strings.HasPrefix(path, "/data/magpie") && made:
			switch r.Method {
			case "PROPFIND":
				w.WriteHeader(http.StatusMultiStatus)
			case http.MethodPut:
				b, _ := io.ReadAll(r.Body)
				stored = string(b)
				w.WriteHeader(http.StatusCreated)
			case http.MethodGet:
				if stored == "" {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				io.WriteString(w, stored)
			}
		default:
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	d, err := newDAV(Config{URL: srv.URL + "/data"})
	if err != nil {
		t.Fatal(err)
	}
	if data, _, err := d.get(ctx, version{}); err != nil || data != nil {
		t.Fatalf("before the first sync: %q %v", data, err)
	}
	if _, err := d.put(ctx, []byte("x"), ""); err != nil {
		t.Fatal(err)
	}
	if data, _, err := d.get(ctx, version{}); err != nil || string(data) != "x" {
		t.Fatalf("after: %q %v", data, err)
	}
}

// A relay or tunnel can cut a long write short: the server keeps half the
// file and answers 2xx anyway, and every later read trips over the half as
// "not a magpie backup". A write that kept less than was sent is read back,
// tried again over the version the short one made, and told as itself in
// the end. A HEAD that can't be done leaves the write unchecked: it landed.
func TestDAVShortWrite(t *testing.T) {
	data := []byte(strings.Repeat("x", 8192))
	// cut is a small WebDAV with one file and, when etags, an ETag it
	// changes on every write and If-Match it holds writes to — so a retry
	// on the version read before the short one would be refused. The cut
	// write keeps half: the writes-th one, or every one at -1. headfail
	// answers the HEAD a check needs with 503.
	cut := func(cut int, etags, headfail bool) (*dav, *int) {
		var kept []byte
		var etag, writes int
		etagOf := func() string {
			if !etags {
				return ""
			}
			return strconv.Quote("v" + strconv.Itoa(etag))
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if u, p, ok := r.BasicAuth(); !ok || u != "me" || p != "pw" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			switch r.Method {
			case http.MethodPut:
				if im := r.Header.Get("If-Match"); im != "" && im != etagOf() {
					w.WriteHeader(http.StatusPreconditionFailed)
					return
				}
				body, _ := io.ReadAll(r.Body)
				writes++
				if cut == -1 || cut == writes {
					kept = body[:len(body)/2] // the tunnel drops the rest
				} else {
					kept = body
				}
				etag++
				if e := etagOf(); e != "" {
					w.Header().Set("ETag", e)
				}
				w.WriteHeader(http.StatusCreated)
			case http.MethodGet:
				if e := etagOf(); e != "" {
					w.Header().Set("ETag", e)
				}
				w.Write(kept)
			case http.MethodHead:
				if headfail {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				w.Header().Set("Content-Length", strconv.Itoa(len(kept)))
				if e := etagOf(); e != "" {
					w.Header().Set("ETag", e)
				}
			default:
				w.WriteHeader(http.StatusForbidden)
			}
		}))
		t.Cleanup(srv.Close)
		d, err := newDAV(Config{URL: srv.URL, User: "me", Password: "pw"})
		if err != nil {
			t.Fatal(err)
		}
		return d, &writes
	}
	ctx := context.Background()
	kept := func(d *dav) []byte {
		got, _, err := d.get(ctx, version{})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	// the write after a cut one is kept whole, so the sync goes through,
	// and the server ends with the file in full
	d, writes := cut(1, true, false)
	if _, err := d.put(ctx, data, ""); err != nil {
		t.Fatalf("the write after a short one: %v", err)
	}
	if *writes != 2 {
		t.Fatalf("writes: %d, want 2: the short one was to be tried again", *writes)
	}
	if got := kept(d); len(got) != len(data) {
		t.Fatalf("the server kept %d bytes, want the whole %d", len(got), len(data))
	}

	// the same over a version read before: the short write changed it, so
	// the retry matches what the short write left, not that one
	d, writes = cut(2, true, false)
	if _, err := d.put(ctx, data, ""); err != nil {
		t.Fatalf("the seed write: %v", err)
	}
	_, v, err := d.get(ctx, version{})
	if err != nil {
		t.Fatal(err)
	}
	if v.ETag == "" {
		t.Fatal("no ETag to write over")
	}
	if _, err := d.put(ctx, data, v.ETag); err != nil {
		t.Fatalf("over a version read: %v", err)
	}
	if *writes != 3 {
		t.Fatalf("writes: %d, want 3: the short one was to be tried again", *writes)
	}
	if got := kept(d); len(got) != len(data) {
		t.Fatalf("the server kept %d bytes, want the whole %d", len(got), len(data))
	}

	// a server that keeps cutting: three tries, then the error says what
	// was kept
	d, writes = cut(-1, true, false)
	_, err = d.put(ctx, data, "")
	if err == nil || !strings.Contains(err.Error(), "cut short") || !strings.Contains(err.Error(), "4096 of 8192") {
		t.Fatalf("always short: %v, want a kept-of-sent count", err)
	}
	if *writes != 3 {
		t.Fatalf("writes: %d, want 3 before giving up", *writes)
	}

	// a HEAD that can't be done (here 503) leaves the write unchecked:
	// it landed, and it isn't failed over the check
	d, writes = cut(0, true, true)
	if _, err := d.put(ctx, data, ""); err != nil {
		t.Fatalf("a write the HEAD couldn't be done for: %v", err)
	}
	if *writes != 1 {
		t.Fatalf("writes: %d, want 1: the write landed", *writes)
	}
}
