package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// memBucket is a bucket in memory, or one that refuses every write.
type memBucket struct {
	mu   sync.Mutex
	objs map[string][]byte
	err  error
}

func (b *memBucket) Put(_ context.Context, name string, data []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return b.err
	}
	b.objs[name] = data
	return nil
}

func archiveTo(t *testing.T, b *memBucket) {
	t.Helper()
	was := ArchiveBucket
	ArchiveBucket = func() (Putter, bool) { return b, true }
	t.Cleanup(func() { ArchiveBucket = was })
}

// archiveVendor answers a chat request, with a cookie of its own, and
// keeps nothing of what it was sent.
type archiveVendor struct{}

func (archiveVendor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Set-Cookie", "vendor_session=abc123; HttpOnly")
	io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
}

const archiveKey = "sk-proj-abcdefghijklmnopqrstuvwxyz0123456789"

func archivePost(t *testing.T, s *Server) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"model":"fake/m1","messages":[{"role":"user","content":"deploy with ` + archiveKey + `"}],"api_key":"named-secret"}`
	req := httptest.NewRequest("POST", "/v1/chat/completions?key=query-secret&beta=true", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer magpie-gateway-key")
	req.Header.Set("x-api-key", "another-secret")
	req.Header.Set("Cookie", "sid=cookie-secret")
	req.Header.Set("User-Agent", "claude-cli/2.1.0")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	archivePending.Wait()
	return rec
}

// Off, as it is unless turned on, nothing is archived: no upload, no id on
// the call or its response — even with a bucket there to take it.
func TestArchiveOff(t *testing.T) {
	fresh(t)
	serveOn(t, "fake", "k", []string{"m1"}, archiveVendor{})
	b := &memBucket{objs: map[string][]byte{}}
	archiveTo(t, b)
	s := New()
	rec := archivePost(t, s)
	if len(b.objs) != 0 || rec.Header().Get(ArchiveHeader) != "" || s.Recent()[0].Archive != "" {
		t.Fatalf("archived while off: %v %q %+v", keys(b.objs), rec.Header().Get(ArchiveHeader), s.Recent()[0].Archive)
	}
	// on, with no bucket (sync isn't to S3): nothing either
	settings.Save(settings.Settings{RequestArchive: true})
	ArchiveBucket = func() (Putter, bool) { return nil, false }
	rec = archivePost(t, s)
	if len(b.objs) != 0 || rec.Header().Get(ArchiveHeader) != "" || s.Recent()[0].Archive != "" {
		t.Fatalf("archived with no bucket: %v", keys(b.objs))
	}
}

// On, a call goes to archive/<date>/<id>.json, the date its own in UTC and
// the id on the response and the call; the headers and bodies both ways
// are there with every secret taken out.
func TestArchiveOn(t *testing.T) {
	fresh(t)
	serveOn(t, "fake", "k", []string{"m1"}, archiveVendor{})
	settings.Save(settings.Settings{RequestArchive: true})
	b := &memBucket{objs: map[string][]byte{}}
	archiveTo(t, b)
	s := New()
	before := time.Now().UTC()
	rec := archivePost(t, s)
	id := rec.Header().Get(ArchiveHeader)
	if !regexp.MustCompile(`^\d{6}-[0-9a-f]{16}$`).MatchString(id) {
		t.Fatalf("id %q", id)
	}
	name := before.Format("2006-01-02") + "/" + id
	c := s.Recent()[0]
	if c.Archive != name || c.wire != nil {
		t.Fatalf("call archive %q, want %q", c.Archive, name)
	}
	if p, ok := ArchiveName(before.Format("2006-01-02"), id); !ok || p != "archive/"+name+".json" {
		t.Fatalf("ArchiveName %q %v", p, ok)
	}
	data, ok := b.objs["archive/"+name+".json"]
	if !ok || len(b.objs) != 1 {
		t.Fatalf("uploaded %v", keys(b.objs))
	}
	for _, secret := range []string{archiveKey, "named-secret", "magpie-gateway-key", "another-secret", "cookie-secret", "abc123", "query-secret"} {
		if strings.Contains(string(data), secret) {
			t.Errorf("%s in the archive:\n%s", secret, data)
		}
	}
	var a Archived
	if err := json.Unmarshal(data, &a); err != nil {
		t.Fatal(err)
	}
	if a.ID != name || a.Call.Model != "fake/m1" || a.Call.Status != 200 || a.Call.RequestBody != "" || a.Call.Archive != name {
		t.Errorf("call %+v", a.Call)
	}
	if a.Request.Method != "POST" || a.Request.Path != "/v1/chat/completions?beta=true&key=%5BREDACTED%5D" ||
		a.Request.Headers["Authorization"][0] != "[REDACTED]" || a.Request.Headers["User-Agent"][0] != "claude-cli/2.1.0" ||
		!strings.Contains(a.Request.Body, "deploy with [REDACTED:API_KEY]") || !strings.Contains(a.Request.Body, `"api_key":"[REDACTED]"`) {
		t.Errorf("request %+v", a.Request)
	}
	if a.Response.Status != 200 || a.Response.Headers[ArchiveHeader][0] != id || !strings.Contains(a.Response.Body, `"content":"done"`) {
		t.Errorf("response %+v", a.Response)
	}

	// a bucket that refuses: the request is served as ever, the failure
	// said, and the next upload that goes clears it
	b.err = errors.New("HTTP 403 AccessDenied")
	archivePost(t, s)
	if e, _ := ArchiveError(); !strings.Contains(e, "AccessDenied") {
		t.Errorf("failure not kept: %q", e)
	}
	b.err = nil
	archivePost(t, s)
	if e, _ := ArchiveError(); e != "" || len(b.objs) != 2 {
		t.Errorf("after a good upload: %q, %d uploads", e, len(b.objs))
	}
}

// The archive's names are only ever a date and an id of magpie's shape,
// never a path out of the archive folder.
func TestArchiveName(t *testing.T) {
	for _, c := range [][2]string{{"2026-10-01", "../backup"}, {"../x", "a"}, {"2026-13-01", "a"}, {"2026-10-01", ""}, {"2026-10-01", "A/b"}} {
		if n, ok := ArchiveName(c[0], c[1]); ok {
			t.Errorf("%v named %q", c, n)
		}
	}
}

func keys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
