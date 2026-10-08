package davsync

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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
