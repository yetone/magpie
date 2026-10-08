package davsync

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/usage"
)

// nutstoreDAV answers as #1259's error says 坚果云 (Nutstore) does: a HEAD
// of a file it holds says Content-Length: 0, whatever the file's size —
// the kept 0 could only have come from there — while PROPFIND (Depth 0),
// where WebDAV clients read sizes from, gives its real getcontentlength.
// A read in a folder that isn't there is a 409, as 坚果云 answers, and the
// rest is the ordinary test server. Not checked against a real account.
// cut keeps half of every usage write, as a tunnel dropping the rest would.
type nutstoreDAV struct {
	*usageDAV
	cut               bool
	puts, heads, lens int // guarded by fakeDAV.mu; puts are the PUTs into a folder there
	sent, kept        int
}

func (d *nutstoreDAV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if u, p, ok := r.BasicAuth(); !ok || u != "me" || p != "pw" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	d.mu.Lock()
	body, there := d.files[r.URL.Path]
	etag := d.etags[r.URL.Path]
	d.mu.Unlock()
	switch {
	case r.Method == http.MethodHead:
		d.mu.Lock()
		d.heads++
		d.mu.Unlock()
		if !there {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)
	case r.Method == "PROPFIND" && r.Header.Get("Depth") == "0" && there:
		d.mu.Lock()
		d.lens++
		d.mu.Unlock()
		w.Header().Set("Content-Type", "text/xml; charset=UTF-8")
		w.WriteHeader(http.StatusMultiStatus)
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8" standalone="no"?><d:multistatus xmlns:d="DAV:" xmlns:s="http://ns.jianguoyun.com"><d:response><d:href>%s</d:href><d:propstat><d:prop><d:getlastmodified>Thu, 08 Oct 2026 06:00:00 GMT</d:getlastmodified><d:getcontentlength>%d</d:getcontentlength><d:owner>me</d:owner><d:current-user-privilege-set><d:privilege><d:read/></d:privilege><d:privilege><d:write/></d:privilege></d:current-user-privilege-set><d:getcontenttype>application/octet-stream</d:getcontenttype><d:displayname>%s</d:displayname><d:resourcetype/></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`,
			r.URL.Path, len(body), r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:])
	case r.Method == http.MethodPut && (r.ContentLength < 0 || len(r.TransferEncoding) > 0):
		// a body sent without its length (chunked) is turned away here, so
		// the test also holds that magpie's writes say how long they are
		w.WriteHeader(http.StatusLengthRequired)
	case r.Method == http.MethodPut:
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		d.mu.Lock()
		if d.dirs[urlDir(r.URL.Path)] { // a write, not the 409 before the folder is made
			d.puts++
		}
		if d.cut && strings.HasPrefix(r.URL.Path, "/dav/magpie/usage/") {
			d.sent = len(b)
			b = b[:len(b)/2]
			d.kept = len(b)
		}
		d.mu.Unlock()
		r.Body = io.NopCloser(bytes.NewReader(b))
		r.ContentLength = int64(len(b))
		d.usageDAV.ServeHTTP(w, r)
	default:
		d.usageDAV.ServeHTTP(w, r)
	}
}

func nutstoreServer(t *testing.T, cut bool) (*nutstoreDAV, Config) {
	t.Helper()
	f := &nutstoreDAV{usageDAV: &usageDAV{fakeDAV: &fakeDAV{
		files: map[string][]byte{}, etags: map[string]string{},
		dirs: map[string]bool{"/dav": true}, nutstore: true, putETag: true,
	}}, cut: cut}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, Config{URL: srv.URL + "/dav/", User: "me", Password: "pw", Passphrase: "correct horse", Keys: true, Agents: true, Usage: true}
}

// #1259: on 坚果云 every usage write came back "the WebDAV server kept 0 of
// 6869 bytes … the write was cut short" — its HEAD says Content-Length: 0
// for a file it holds whole. A write is now taken as kept when PROPFIND
// gives its whole length, once, so the backup and the usage day go up in
// one write each and the sync says nothing failed; a usage write a tunnel
// really cut is still told as cut, with the bytes the server kept.
func TestNutstoreHeadSaysNoLength(t *testing.T) {
	old := usageEvery
	usageEvery = 0
	t.Cleanup(func() { usageEvery = old })

	t.Run("whole writes", func(t *testing.T) {
		newComputer(t).use(t)
		f, cfg := nutstoreServer(t, false)
		if err := Configure(cfg); err != nil {
			t.Fatal(err)
		}
		usage.Append(call(time.Now().UTC(), "deepseek-chat", 100))
		if err := SyncNow(context.Background()); err != nil {
			t.Fatalf("sync: %v", err)
		}
		v := Status()
		if v.Error != "" || v.UsageError != "" || v.Last.IsZero() {
			t.Fatalf("sync status: error %q, usage error %q, last %v — want synced with usage shared", v.Error, v.UsageError, v.Last)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		var days int
		for p, b := range f.files {
			if strings.HasPrefix(p, "/dav/magpie/usage/") && strings.HasSuffix(p, usageExt) && len(b) > 0 {
				days++
			}
		}
		if days != 1 {
			t.Errorf("usage days on the server: %d, want 1", days)
		}
		// the backup and the day, each written once and checked once
		if f.puts != 2 {
			t.Errorf("writes: %d, want one backup and one usage day, none tried again", f.puts)
		}
		if f.heads != 2 || f.lens != 2 {
			t.Errorf("checks: %d HEAD, %d PROPFIND Depth 0, want one of each per write", f.heads, f.lens)
		}
	})

	t.Run("cut short", func(t *testing.T) {
		newComputer(t).use(t)
		f, cfg := nutstoreServer(t, true)
		if err := Configure(cfg); err != nil {
			t.Fatal(err)
		}
		usage.Append(call(time.Now().UTC(), "deepseek-chat", 100))
		if err := SyncNow(context.Background()); err != nil {
			t.Fatalf("the backup's sync: %v", err)
		}
		v := Status()
		f.mu.Lock()
		kept, sent := f.kept, f.sent
		f.mu.Unlock()
		if v.Error != "" || !strings.Contains(v.UsageError, "cut short") || !strings.Contains(v.UsageError, fmt.Sprintf("kept %d of %d bytes", kept, sent)) {
			t.Fatalf("sync status: error %q, usage error %q — want the usage write told as cut, %d of %d bytes kept", v.Error, v.UsageError, kept, sent)
		}
	})
}

// The backup's own write on 坚果云: its HEAD's 0 isn't taken as a cut write
// either, so the write isn't tried again and its ETag stands.
func TestNutstoreBackupWrite(t *testing.T) {
	f, cfg := nutstoreServer(t, false)
	f.mu.Lock()
	f.dirs["/dav/magpie"] = true
	f.mu.Unlock()
	d, err := newDAV(cfg)
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("x"), 6869)
	v, err := d.put(context.Background(), data, "")
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.puts != 1 || v.ETag == "" || len(f.files["/dav/magpie/"+file]) != len(data) {
		t.Errorf("puts %d, ETag %q, kept %d: want one whole write and its ETag", f.puts, v.ETag, len(f.files["/dav/magpie/"+file]))
	}
}
