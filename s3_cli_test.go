package main

import (
	"crypto/md5"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/davsync"
	"github.com/yetone/magpie/internal/provider"
)

// s3Server is an S3 server with buckets of any name, path-style, keeping
// objects in memory and taking the access keys given; signatures are
// davsync's to check (its fake S3 does).
type s3Server struct {
	mu      sync.Mutex
	ids     []string
	objects map[string][]byte
	url     string
}

func newS3Server(t *testing.T, ids ...string) *s3Server {
	t.Helper()
	s := &s3Server{ids: ids, objects: map[string][]byte{}}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	s.url = srv.URL
	return s
}

func etagOf(b []byte) string {
	h := md5.Sum(b)
	return `"` + hex.EncodeToString(h[:]) + `"`
}

func (s *s3Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	auth := r.Header.Get("Authorization")
	id, _, _ := strings.Cut(strings.TrimPrefix(auth, "AWS4-HMAC-SHA256 Credential="), "/")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") || !slices.Contains(s.ids, id) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, "<Error><Code>InvalidAccessKeyId</Code></Error>")
		return
	}
	b, there := s.objects[r.URL.Path]
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		if !there {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, "<Error><Code>NoSuchKey</Code></Error>")
			return
		}
		w.Header().Set("ETag", etagOf(b))
		if r.Method == http.MethodGet {
			w.Write(b)
		}
	case http.MethodPut:
		if m := r.Header.Get("If-Match"); m != "" && (!there || m != etagOf(b)) || r.Header.Get("If-None-Match") == "*" && there {
			w.WriteHeader(http.StatusPreconditionFailed)
			io.WriteString(w, "<Error><Code>PreconditionFailed</Code></Error>")
			return
		}
		s.objects[r.URL.Path], _ = io.ReadAll(r.Body)
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func (s *s3Server) object(p string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.objects[p]
}

func TestS3Cmd(t *testing.T) {
	webdavHome(t)
	bucket := newS3Server(t, "AKID", "AKID2")
	provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k1"})

	if err := s3Cmd([]string{"set", "region=auto"}); err == nil || !strings.Contains(err.Error(), "S3 sync is off") {
		t.Fatalf("set while off: %v", err)
	}
	if err := s3Cmd(nil); err != nil {
		t.Fatal(err)
	}
	// the wrong kind of address, and no access key, said before any secret
	// is asked for
	if err := s3Cmd([]string{"on", "https://dav.example.com/dav/"}); err == nil || !strings.Contains(err.Error(), "not an S3 address") {
		t.Fatalf("a WebDAV address to magpie s3: %v", err)
	}
	if err := webdavCmd([]string{"on", "s3://bkt"}); err == nil || !strings.Contains(err.Error(), "magpie s3 on s3://bkt") {
		t.Fatalf("an S3 address to magpie webdav: %v", err)
	}
	if err := s3Cmd([]string{"on", "s3://bkt", "endpoint=" + bucket.url}); err == nil || !strings.Contains(err.Error(), "no access key") {
		t.Fatalf("no access key: %v", err)
	}
	if err := s3Cmd([]string{"on", "s3://bkt", "secret=on-the-command-line"}); err == nil || !strings.Contains(err.Error(), "asked for") {
		t.Fatalf("a secret on the command line: %v", err)
	}

	// on: the secret and the passphrase asked for, and a sync at once
	piped(t, "s3cr3t", "correct horse")
	if err := s3Cmd([]string{"on", "s3://bkt/team", "endpoint=" + bucket.url, "access-key-id=AKID", "region=auto", "path-style=yes"}); err != nil {
		t.Fatal(err)
	}
	c, ok := davsync.Load()
	if !ok || c.URL != "s3://bkt/team" || c.User != "AKID" || c.Password != "s3cr3t" || c.Passphrase != "correct horse" ||
		c.Endpoint != bucket.url || c.Region != "auto" || !c.PathStyle || !c.Keys || !c.Agents {
		t.Fatalf("on: %v %+v", ok, c)
	}
	if up := bucket.object("/bkt/team/magpie/magpie.magpie-backup"); len(up) == 0 || strings.Contains(string(up), "deepseek") {
		t.Fatalf("in the bucket: %q", up)
	}
	if v := davsync.Status(); v.Error != "" || v.Last.IsZero() || v.Kind != "s3" || v.Region != "auto" {
		t.Fatalf("status: %+v", v)
	}
	if err := s3Cmd(nil); err != nil {
		t.Fatal(err)
	}

	// set: the region and the prefix change, the secret stays
	if err := s3Cmd([]string{"set", "region=us-east-1", "prefix=/other/", "keys=no"}); err != nil {
		t.Fatal(err)
	}
	if c, _ := davsync.Load(); c.URL != "s3://bkt/other" || c.Region != "us-east-1" || c.Keys || c.Password != "s3cr3t" {
		t.Fatalf("set: %+v", c)
	}
	if len(bucket.object("/bkt/other/magpie/magpie.magpie-backup")) == 0 {
		t.Fatal("nothing under the new prefix")
	}
	// magpie webdav doesn't change or turn off S3 sync
	if err := webdavCmd([]string{"set", "keys=yes"}); err == nil || !strings.Contains(err.Error(), "S3 sync is on") {
		t.Fatalf("webdav set while S3 is on: %v", err)
	}
	if err := webdavCmd([]string{"off"}); err == nil || !strings.Contains(err.Error(), "magpie s3 off") {
		t.Fatalf("webdav off while S3 is on: %v", err)
	}
	if _, ok := davsync.Load(); !ok {
		t.Fatal("webdav off turned S3 sync off")
	}

	// another access key: the secret saved is not used with it, one is
	// asked for
	piped(t, "s3cr3t2")
	if err := s3Cmd([]string{"set", "access-key-id=AKID2"}); err != nil {
		t.Fatal(err)
	}
	if c, _ := davsync.Load(); c.User != "AKID2" || c.Password != "s3cr3t2" || c.Passphrase != "correct horse" {
		t.Fatalf("another key: %+v", c)
	}

	// magpie webdav on moves sync to a WebDAV folder, keeping the
	// passphrase and what is synced, leaving no S3 field behind
	dav := newDAVServer(t, "pw")
	piped(t, "pw")
	if err := webdavCmd([]string{"on", dav.url, "user=me"}); err != nil {
		t.Fatal(err)
	}
	if c, _ := davsync.Load(); c.S3() || c.URL != dav.url || c.Password != "pw" || c.Passphrase != "correct horse" || c.Keys ||
		c.Endpoint != "" || c.Region != "" || c.PathStyle {
		t.Fatalf("to WebDAV: %+v", c)
	}
	// and back, with nothing typed: the bucket, key and secret kept when
	// sync moved from them (ARNO on Discord: S3 wiped the WebDAV setup)
	if err := s3Cmd([]string{"on"}); err != nil {
		t.Fatal(err)
	}
	if c, _ := davsync.Load(); !c.S3() || c.URL != "s3://bkt/other" || c.User != "AKID2" || c.Password != "s3cr3t2" || c.Endpoint != bucket.url ||
		c.Other == nil || c.Other.URL != dav.url || c.Other.Password != "pw" {
		t.Fatalf("back to S3: %+v", c)
	}
	if err := webdavCmd([]string{"on"}); err != nil {
		t.Fatal(err)
	}
	if c, _ := davsync.Load(); c.S3() || c.URL != dav.url || c.User != "me" || c.Password != "pw" || c.Other == nil || c.Other.Password != "s3cr3t2" {
		t.Fatalf("back to WebDAV: %+v", c)
	}
	if err := s3Cmd([]string{"off"}); err == nil || !strings.Contains(err.Error(), "magpie webdav off") {
		t.Fatalf("s3 off while WebDAV is on: %v", err)
	}
	if err := webdavCmd([]string{"off"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := davsync.Load(); ok {
		t.Fatal("still on after off")
	}
}
