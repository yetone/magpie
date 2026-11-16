package davsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// The gateway's request archive goes to the bucket sync keeps its backup
// in, signed as the backup is, at <prefix>/magpie/archive/<date>/<id>.json,
// and reads back from there — and while it's off, nothing is sent at all.
func TestRequestArchiveToS3(t *testing.T) {
	newComputer(t).use(t)
	f, srv := newFakeS3(t)
	if err := Configure(f.config(srv)); err != nil {
		t.Fatal(err)
	}
	was := gateway.ArchiveBucket
	gateway.ArchiveBucket = func() (gateway.Putter, bool) { // as main.go sets it
		if b, ok := S3Bucket(); ok {
			return b, true
		}
		return nil, false
	}
	t.Cleanup(func() { gateway.ArchiveBucket = was })

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "vendor-key", Models: []string{"m1"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	s := gateway.New()
	post := func() string {
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"fake/m1","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Authorization", "Bearer gw-secret-123")
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		return rec.Header().Get(gateway.ArchiveHeader)
	}
	archived := func() (n int) {
		f.mu.Lock()
		defer f.mu.Unlock()
		for k := range f.objects {
			if strings.HasPrefix(k, "team x+y/magpie/archive/") {
				n++
			}
		}
		return n
	}

	// off
	if id := post(); id != "" {
		t.Fatalf("an archive id while off: %q", id)
	}
	time.Sleep(200 * time.Millisecond)
	if n := archived(); n != 0 {
		t.Fatalf("%d uploaded while off", n)
	}

	// on
	settings.Save(settings.Settings{RequestArchive: true})
	id := post()
	if id == "" {
		t.Fatal("no archive id")
	}
	// the date is the one the call carries, in UTC, not the clock read
	// again here, which a UTC midnight can fall between
	date := s.Recent()[0].Time.UTC().Format("2006-01-02")
	key := "team x+y/magpie/archive/" + date + "/" + id + ".json"
	deadline := time.Now().Add(5 * time.Second)
	for archived() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	f.mu.Lock()
	data, ok := f.objects[key]
	f.mu.Unlock()
	if !ok {
		t.Fatalf("nothing at %q: %v", key, f.log)
	}
	if strings.Contains(string(data), "gw-secret-123") || strings.Contains(string(data), "vendor-key") {
		t.Fatalf("a secret in the bucket: %s", data)
	}

	b, ok := S3Bucket()
	if !ok {
		t.Fatal("no bucket")
	}
	name, _ := gateway.ArchiveName(date, id)
	got, err := b.Get(context.Background(), name)
	if err != nil || string(got) != string(data) {
		t.Fatalf("read back %v: %s", err, got)
	}
	var a gateway.Archived
	if err := json.Unmarshal(got, &a); err != nil || a.Request.Headers["Authorization"][0] != "[REDACTED]" || !strings.Contains(a.Response.Body, "done") {
		t.Fatalf("archived %+v %v", a, err)
	}
	if _, err := b.Get(context.Background(), "archive/"+date+"/000000-0000000000000000.json"); !errors.Is(err, ErrNoObject) {
		t.Fatalf("a missing one: %v", err)
	}
}

// The archive in the bucket is kept under its size (RequestArchiveMB):
// a bucket grown before, with calls months old, loses its oldest at the
// first upload, and day after day of calls never takes it past the size,
// listed once per tenth of the size uploaded, not once per call. Nothing
// but the archive's own calls is ever removed: the backup, shared usage,
// a file of the user's in the archive's folder, another prefix's archive.
// Before, the bucket grew by every call's whole bodies for good (18 GB in
// a month for an R2 user on X).
func TestRequestArchiveKeptUnderItsSize(t *testing.T) {
	newComputer(t).use(t)
	f, srv := newFakeS3(t)
	if err := Configure(f.config(srv)); err != nil {
		t.Fatal(err)
	}
	was := gateway.ArchiveBucket
	gateway.ArchiveBucket = func() (gateway.Putter, bool) { // as main.go sets it
		if b, ok := S3Bucket(); ok {
			return b, true
		}
		return nil, false
	}
	t.Cleanup(func() { gateway.ArchiveBucket = was })
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "vendor-key", Models: []string{"m1"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	settings.Save(settings.Settings{RequestArchive: true, RequestArchiveMB: 1})
	const most = 1 << 20
	const archive = "team x+y/magpie/archive/"

	// what the bucket had before: 3 MiB of calls from 2020, and what
	// isn't the archive's, a MiB each
	mib := strings.Repeat("o", most)
	var old []string
	f.mu.Lock()
	for i := range 48 {
		k := fmt.Sprintf("%s2020-01-%02d/%06d-%016x.json", archive, 1+i/8, 100000+i, i)
		f.store(k, []byte(mib[:64<<10]))
		old = append(old, k)
	}
	foreign := []string{
		"team x+y/magpie/magpie.magpie-backup",
		"team x+y/magpie/usage/a.magpie-usage",
		archive + "notes.txt",
		archive + "2020-01-01/README.md",
		archive + "2020-01-01/UPPER-0000000000000000.json",
		"team x+y/other/archive/2020-01-01/000000-0000000000000001.json",
		"elsewhere/magpie/archive/2020-01-01/000000-0000000000000002.json",
	}
	for _, k := range foreign {
		f.store(k, []byte(mib))
	}
	f.mu.Unlock()

	s := gateway.New()
	// 200 calls of about 20 KB each: 4 MB, four times the size
	content := strings.Repeat("y", 20<<10)
	const calls = 200
	for range calls {
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"fake/m1","messages":[{"role":"user","content":"`+content+`"}]}`))
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
	}
	// the archive's own calls only: what the bucket holds of them
	ours := func() (n, bytes int) {
		f.mu.Lock()
		defer f.mu.Unlock()
		for k, b := range f.objects {
			if strings.HasPrefix(k, archive) && strings.HasSuffix(k, ".json") && !strings.Contains(k, "UPPER") {
				n, bytes = n+1, bytes+len(b)
			}
		}
		return n, bytes
	}
	oldLeft := func() (n int) {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, k := range old {
			if _, ok := f.objects[k]; ok {
				n++
			}
		}
		return n
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		n, b := ours()
		if b <= most && oldLeft() == 0 && n > 0 {
			break
		}
		if time.Now().After(deadline) {
			left := oldLeft()
			f.mu.Lock()
			put, lists, deletes := f.put, f.lists, f.deletes
			f.mu.Unlock() // the server's handlers wait on it as the test ends
			t.Fatalf("after %d calls the archive holds %d calls, %d bytes, over its %d (%d from 2020 left); %d bytes uploaded, %d LISTs, %d DELETEs",
				calls, n, b, most, left, put, lists, deletes)
		}
		time.Sleep(20 * time.Millisecond)
	}
	n, b := ours()
	f.mu.Lock()
	put, lists, deletes := f.put, f.lists, f.deletes
	objects := maps.Clone(f.objects)
	f.mu.Unlock()
	t.Logf("%d calls: %d kept, %d bytes; %d bytes uploaded, %d LISTs, %d DELETEs", calls, n, b, put, lists, deletes)
	// at least the tenth under the size is kept, not everything removed
	if b < most/2 {
		t.Errorf("only %d bytes kept of %d", b, most)
	}
	// a LIST per tenth of the size uploaded, about 100 KB here, and the
	// first: never one per call
	if lists > 1+put/(most/10)+2 || lists*4 > calls {
		t.Errorf("%d LISTs for %d calls of %d bytes", lists, calls, put)
	}
	for _, k := range foreign {
		if string(objects[k]) != mib {
			t.Errorf("%s touched", k)
		}
	}
}
