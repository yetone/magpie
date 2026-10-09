package davsync

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// "not a magpie backup" was all a sync said whatever the server sent where
// the backup should be (JasonLeeForOnly on Discord, over several versions),
// and nothing in magpie could get past it. Each case below is a server
// behaving as a real one does: one that compresses though asked not to, a
// web page in front of WebDAV, a file left empty or cut short, another
// app's file at the path. What magpie can read it reads; the rest is said
// as what it is, and a file that is really there and not a backup can be
// replaced from this computer, the server's copy kept first.

// served is a fake WebDAV whose reads of the backup file answer as get
// says, when it says; writes and everything else go to the fake.
type served struct {
	fake *fakeDAV
	get  func(w http.ResponseWriter, r *http.Request) bool
}

func (s *served) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.get != nil && r.Method == http.MethodGet && s.get(w, r) {
		return
	}
	s.fake.ServeHTTP(w, r)
}

// syncedServer is a fake WebDAV that computer a has synced its setup to:
// the server's backup is a's, sealed with "horse".
func syncedServer(t *testing.T) (*served, *httptest.Server, Config, []byte) {
	t.Helper()
	fake := &fakeDAV{files: map[string][]byte{}, etags: map[string]string{}, dirs: map[string]bool{"/dav": true}}
	s := &served{fake: fake}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	cfg := Config{URL: srv.URL + "/dav/", User: "me", Password: "pw", Passphrase: "horse", Keys: true, Agents: true}
	a := newComputer(t)
	a.use(t)
	provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k1"})
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if err := Now(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s, srv, cfg, onServer(fake, backupFile)
}

func gz(b []byte) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write(b)
	w.Close()
	return buf.Bytes()
}

// A server that compresses the file though asked for it as it is: with the
// header said, with it dropped by a proxy, in zstd and deflate, and one
// that says gzip and sends the file as it is. Each reads as the backup.
func TestReadCompressedBackup(t *testing.T) {
	zs := func(b []byte) []byte {
		e, _ := zstd.NewWriter(nil)
		return e.EncodeAll(b, nil)
	}
	zl := func(b []byte) []byte {
		var buf bytes.Buffer
		w := zlib.NewWriter(&buf)
		w.Write(b)
		w.Close()
		return buf.Bytes()
	}
	cases := map[string]struct {
		enc  string
		pack func([]byte) []byte
	}{
		"gzip said":          {"gzip", gz},
		"gzip unsaid":        {"", gz},
		"x-gzip":             {"x-gzip", gz},
		"zstd":               {"zstd", zs},
		"zstd unsaid":        {"", zs},
		"deflate":            {"deflate", zl},
		"gzip said not done": {"gzip", func(b []byte) []byte { return b }},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s, _, cfg, good := syncedServer(t)
			s.get = func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path != backupFile {
					return false
				}
				if tc.enc != "" {
					w.Header().Set("Content-Encoding", tc.enc)
				}
				w.Header().Set("ETag", `"x"`)
				w.Write(tc.pack(good))
				return true
			}
			b := newComputer(t)
			b.use(t)
			if err := Configure(cfg); err != nil {
				t.Fatal(err)
			}
			if err := Now(context.Background()); err != nil {
				t.Fatalf("a compressed backup didn't read: %v", err)
			}
			if got := ids(); len(got) != 1 || got[0] != "deepseek=k1" {
				t.Fatalf("b didn't take a's setup: %v", got)
			}
		})
	}
}

// Brotli, which magpie can't unpack, is said as itself.
func TestReadBrotliSaid(t *testing.T) {
	s, _, _, _ := syncedServer(t)
	s.get = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Encoding", "br")
		w.Write([]byte{0x1b, 0x2a, 0x00, 0xf8, 0x8d, 0x94})
		return true
	}
	err := Now(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Brotli") {
		t.Fatalf("a Brotli body should be said as one: %v", err)
	}
}

// What a server answers instead of the file, as real ones do: each is said
// as what it is, with what to do; a page is never offered for replacing,
// and nothing is written to the server for it.
func TestServerAnswerSaid(t *testing.T) {
	page := func(title, body string) []byte {
		return []byte("<!DOCTYPE html>\n<html lang=\"en\"><head><meta charset=\"utf-8\"><title>" + title + "</title></head><body>" + body + "</body></html>")
	}
	type answer struct {
		status int
		ctype  string
		body   []byte
		// to: the read is redirected there first
		to string
	}
	cases := map[string]struct {
		a       answer
		want    []string
		what    string
		replace bool
	}{
		// a Nextcloud's address given without /remote.php/dav: its web
		// app sends the read to its sign-in page
		"nextcloud login": {answer{to: "/login?redirect_url=/dav/magpie", ctype: "text/html; charset=UTF-8", body: page("Nextcloud", `<form method="post" name="login">`)},
			[]string{"web page", `"Nextcloud"`, "redirect to 127.0.0.1", "/login", "WebDAV address"}, "page", false},
		// Cloudflare Access in front of a tunnel
		"cloudflare access": {answer{to: "/cdn-cgi/access/login/dav.example.com?kid=abc", ctype: "text/html", body: page("Sign in ・ Cloudflare Access", "")},
			[]string{"Cloudflare Access", "redirect to"}, "page", false},
		// Alist's and OpenList's web app answers any path that isn't
		// /dav/ with its page
		"alist web app": {answer{status: 200, ctype: "text/html", body: page("AList", `<div id="root"></div>`)},
			[]string{`"AList"`, "WebDAV address"}, "page", false},
		// rclone serve webdav (and a Synology's web station) list a
		// folder as a page
		"directory listing": {answer{status: 200, ctype: "text/html; charset=utf-8", body: []byte("<html><head><title>Directory listing of /magpie/magpie.magpie-backup/</title></head><body><a href=\"../\">../</a></body></html>")},
			[]string{"Directory listing of /magpie/magpie.magpie-backup/"}, "page", false},
		// a captive portal's page with no <html> to begin with
		"captive portal": {answer{status: 200, ctype: "text/html", body: []byte("<head><title>Hotel WiFi – Sign in</title></head><body><h1>Welcome</h1></body>")},
			[]string{"Hotel WiFi – Sign in"}, "page", false},
		// a WebDAV error document answered 200 by a broken proxy
		"xml": {answer{status: 200, ctype: "application/xml", body: []byte(`<?xml version="1.0" encoding="utf-8"?><d:error xmlns:d="DAV:" xmlns:s="http://sabredav.org/ns"><s:exception>Sabre\DAV\Exception\NotAuthenticated</s:exception></d:error>`)},
			[]string{"XML document", "<d:error>"}, "xml", false},
		// a write that never landed left an empty file
		"empty": {answer{status: 200, ctype: "application/octet-stream", body: []byte{}},
			[]string{"is empty (0 bytes)", "magpie webdav upload"}, "empty", true},
		// another app's file at the same path
		"other app": {answer{status: 200, ctype: "application/octet-stream", body: []byte(`{"format":"cc-switch-backup","version":3,"data":"..."}`)},
			[]string{`"cc-switch-backup" file`, "another app", "upload"}, "format", true},
		"json": {answer{status: 200, ctype: "application/json", body: []byte(`[1,2,3]`)},
			[]string{"JSON file that isn't a magpie backup (7 bytes)"}, "json", true},
		"zip": {answer{status: 200, ctype: "application/octet-stream", body: append([]byte("PK\x03\x04"), make([]byte, 40)...)},
			[]string{"application/zip (44 bytes)"}, "other", true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s, _, cfg, good := syncedServer(t)
			// a file really at the path is the server's own; a page is
			// answered in front of it
			if tc.replace {
				s.fake.mu.Lock()
				s.fake.n++
				s.fake.files[backupFile], s.fake.etags[backupFile] = tc.a.body, `"other"`
				s.fake.mu.Unlock()
			} else {
				s.get = answering(tc.a.to, tc.a.ctype, tc.a.body)
			}
			// a fresh computer: nothing of its own to rebuild with
			b := newComputer(t)
			b.use(t)
			if err := Configure(cfg); err != nil {
				t.Fatal(err)
			}
			puts := s.fake.puts
			err := Now(context.Background())
			if err == nil {
				t.Fatal("the sync went on over what isn't a backup")
			}
			t.Log(err)
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("the error doesn't say %q: %v", w, err)
				}
			}
			if strings.Contains(err.Error(), "data\":") || strings.Contains(err.Error(), "<form") {
				t.Errorf("the error carries the body: %v", err)
			}
			v := Status()
			if v.File == nil || v.File.What != tc.what || v.File.Replace != tc.replace {
				t.Fatalf("the page isn't told what the server holds: %+v", v.File)
			}
			if s.fake.puts != puts {
				t.Fatal("a sync wrote over it")
			}
			// Upload writes over only what is a file there
			err = Upload(context.Background())
			if !tc.replace {
				if err == nil || s.fake.puts != puts {
					t.Fatalf("Upload wrote over a page the server answered with: %v", err)
				}
				if got := onServer(s.fake, backupFile); !bytes.Equal(got, good) {
					t.Fatal("the server's backup changed")
				}
				return
			}
			if err != nil {
				t.Fatalf("Upload: %v", err)
			}
			s.get = nil
			if _, err := backup.Open(onServer(s.fake, backupFile), "horse"); err != nil {
				t.Fatalf("the uploaded file doesn't open: %v", err)
			}
			if v := Status(); v.Error != "" || v.File != nil {
				t.Fatalf("the error stayed after Upload: %+v", v)
			}
			kept, _ := filepath.Glob(filepath.Join(settings.Dir(), "sync", "*-server-replaced"+backup.Ext))
			if len(tc.a.body) == 0 {
				if len(kept) != 0 {
					t.Fatalf("an empty file was kept: %v", kept)
				}
			} else if len(kept) != 1 {
				t.Fatalf("the server's file wasn't kept before it was replaced: %v", kept)
			} else if b, _ := os.ReadFile(kept[0]); !bytes.Equal(b, tc.a.body) {
				t.Fatal("what was kept isn't what the server held")
			}
			if err := Now(context.Background()); err != nil {
				t.Fatalf("the sync after Upload: %v", err)
			}
		})
	}
}

// answering is a server that answers every read of the backup with body,
// after a redirect to to when there is one.
func answering(to, ctype string, body []byte) func(w http.ResponseWriter, r *http.Request) bool {
	return func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == backupFile && to != "" {
			http.Redirect(w, r, to, http.StatusFound)
			return true
		}
		if r.URL.Path != backupFile && (to == "" || !strings.HasPrefix(to, r.URL.Path)) {
			return false
		}
		w.Header().Set("Content-Type", ctype)
		w.Write(body)
		return true
	}
}

// A backup cut short that this computer can't rebuild (it never read the
// version that broke) is said as cut short, and Upload replaces it.
func TestCutBackupSaidAndUploaded(t *testing.T) {
	s, _, cfg, good := syncedServer(t)
	cut(s.fake, backupFile, len(good)/2)
	b := newComputer(t)
	b.use(t)
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	err := Now(context.Background())
	if err == nil || !strings.Contains(err.Error(), "cut short") || !strings.Contains(err.Error(), "upload") {
		t.Fatalf("a cut backup should be said as one: %v", err)
	}
	if _, rerr := Restore(context.Background()); rerr == nil || !strings.Contains(rerr.Error(), "cut short") {
		t.Fatalf("Restore should say it too: %v", rerr)
	}
	if v := Status(); v.File == nil || v.File.What != "cut" || !v.File.Replace {
		t.Fatalf("%+v", v.File)
	}
	if err := Upload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.Open(onServer(s.fake, backupFile), "horse"); err != nil {
		t.Fatalf("the uploaded file doesn't open: %v", err)
	}
}

// Upload never writes over a backup: one this computer opens (a sync
// merges with it) or one sealed with another passphrase.
func TestUploadKeepsABackup(t *testing.T) {
	s, _, cfg, good := syncedServer(t)
	puts := s.fake.puts
	if err := Upload(context.Background()); err == nil || !strings.Contains(err.Error(), "Sync now") {
		t.Fatalf("Upload over a good backup: %v", err)
	}
	b := newComputer(t)
	b.use(t)
	cfg.Passphrase = "other"
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if err := Upload(context.Background()); err == nil || !strings.Contains(err.Error(), "passphrase") {
		t.Fatalf("Upload over another passphrase's backup: %v", err)
	}
	if s.fake.puts != puts || !bytes.Equal(onServer(s.fake, backupFile), good) {
		t.Fatal("a backup was written over")
	}
}

// S3: an object stored compressed without saying so reads as the backup.
func TestS3ReadCompressedObject(t *testing.T) {
	f, srv := newFakeS3(t)
	cfg := f.config(srv)
	const key = "team x+y/magpie/magpie.magpie-backup"
	a := newComputer(t)
	a.use(t)
	provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k1"})
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if err := Now(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.store(key, gz(f.objects[key]))
	f.mu.Unlock()
	b := newComputer(t)
	b.use(t)
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if err := Now(context.Background()); err != nil {
		t.Fatalf("a gzipped object didn't read: %v", err)
	}
	if got := ids(); len(got) != 1 || got[0] != "deepseek=k1" {
		t.Fatalf("b didn't take a's setup: %v", got)
	}
}
