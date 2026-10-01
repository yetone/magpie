package gui

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/davsync"
	"github.com/yetone/magpie/internal/gateway"
)

// fakeArchive is a bucket of archived calls in memory; size, when set,
// is what it says an object's size is.
type fakeArchive struct {
	objs map[string][]byte
	size int64
}

func (f fakeArchive) Open(_ context.Context, name string) (io.ReadCloser, int64, error) {
	b, ok := f.objs[name]
	if !ok {
		return nil, 0, davsync.ErrNoObject
	}
	if f.size != 0 {
		return io.NopCloser(bytes.NewReader(b)), f.size, nil
	}
	return io.NopCloser(bytes.NewReader(b)), int64(len(b)), nil
}

func archiveHome(t *testing.T, f fakeArchive) http.Handler {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	os.MkdirAll(filepath.Join(h, "Downloads"), 0o700)
	was := archiveBucket
	archiveBucket = func() (archiveStore, bool) { return f, true }
	t.Cleanup(func() { archiveBucket = was })
	return Handler(nil, nil)
}

func archivedCall(t *testing.T, req, res string) []byte {
	t.Helper()
	b, err := json.Marshal(gateway.Archived{ID: "2026-10-02/101010-00000000000000aa", Call: gateway.Call{Model: "p/m", Status: 200},
		Request:  gateway.ArchivePart{Method: "POST", Path: "/v1/messages", Body: req, Size: int64(len(req))},
		Response: gateway.ArchivePart{Status: 200, Body: res, Size: int64(len(res))}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

const archiveQuery = "?date=2026-10-02&id=101010-00000000000000aa"
const archiveObject = "archive/2026-10-02/101010-00000000000000aa.json"

// The page is sent a body to show only when it is 256 KB or less: a longer
// one is told by its size alone, and the file downloads whole, however
// large (#447) — to the browser, or, in the app, to Downloads.
func TestArchivePreviewAndDownload(t *testing.T) {
	long := strings.Repeat("a", 20<<20) + "TAIL" // past what Get reads, 16 MiB
	obj := archivedCall(t, `{"q":"short"}`, long)
	h := archiveHome(t, fakeArchive{objs: map[string][]byte{archiveObject: obj}})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/archive"+archiveQuery, nil))
	if rec.Code != 200 || rec.Body.Len() > 64<<10 {
		t.Fatalf("%d, %d bytes sent to the page", rec.Code, rec.Body.Len())
	}
	var p archivePreview
	json.Unmarshal(rec.Body.Bytes(), &p)
	if p.Request == nil || p.Request.Body != `{"q":"short"}` || p.Request.Omitted || p.Response == nil || !p.Response.Omitted || p.Response.Body != "" ||
		p.Response.Size != int64(len(long)) || p.Bytes != int64(len(obj)) || p.Call == nil || p.Call.Model != "p/m" {
		t.Fatalf("preview %.400s", rec.Body)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/archive/file"+archiveQuery, nil))
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), obj) ||
		rec.Header().Get("Content-Disposition") != `attachment; filename="magpie-request-2026-10-02-101010-00000000000000aa.json"` {
		t.Fatalf("download %d %q, %d of %d bytes", rec.Code, rec.Header().Get("Content-Disposition"), rec.Body.Len(), len(obj))
	}

	for _, want := range []string{"magpie-request-2026-10-02-101010-00000000000000aa.json", "magpie-request-2026-10-02-101010-00000000000000aa-2.json"} {
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/archive/export"+archiveQuery, strings.NewReader("{}")))
		var out struct{ Path string }
		json.Unmarshal(rec.Body.Bytes(), &out)
		if rec.Code != 200 || filepath.Base(out.Path) != want {
			t.Fatalf("export %d %s", rec.Code, rec.Body)
		}
		got, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), "Downloads", want))
		if err != nil || !bytes.Equal(got, obj) {
			t.Fatalf("saved %d of %d bytes: %v", len(got), len(obj), err)
		}
	}
	if left, _ := filepath.Glob(filepath.Join(os.Getenv("HOME"), "Downloads", ".*part")); len(left) != 0 {
		t.Fatalf("left %v", left)
	}

	// one not there yet says so; a name not of the archive's shape is refused
	for _, q := range []string{"?date=2026-10-02&id=101010-00000000000000bb", "?date=2026-10-02&id=../../backup"} {
		for _, path := range []string{"GET /api/archive", "GET /api/archive/file", "POST /api/archive/export"} {
			m, u, _ := strings.Cut(path, " ")
			rec = httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(m, u+q, strings.NewReader("{}")))
			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s%s: %d %s", path, q, rec.Code, rec.Body)
			}
		}
	}
}

// An archive too large to read for a preview is only said to be, and
// downloaded.
func TestArchiveTooLargeToPreview(t *testing.T) {
	h := archiveHome(t, fakeArchive{objs: map[string][]byte{archiveObject: archivedCall(t, "{}", "{}")}, size: archiveReadMost + 1})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/archive"+archiveQuery, nil))
	var p archivePreview
	json.Unmarshal(rec.Body.Bytes(), &p)
	if rec.Code != 200 || !p.Large || p.Bytes != archiveReadMost+1 || p.Request != nil {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// An archive from before #447, its bodies cut at 256 KB with no size, is
// shown as it was, its size the body's.
func TestArchiveOldPreview(t *testing.T) {
	old := `{"id":"2026-10-02/101010-00000000000000aa","call":{"model":"p/m"},"request":{"method":"POST","headers":{},"body":"{}","truncated":true},"response":{"status":200,"headers":{},"body":"ok"}}`
	h := archiveHome(t, fakeArchive{objs: map[string][]byte{archiveObject: []byte(old)}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/archive"+archiveQuery, nil))
	var p archivePreview
	json.Unmarshal(rec.Body.Bytes(), &p)
	if rec.Code != 200 || p.Request == nil || p.Request.Body != "{}" || !p.Request.Truncated || p.Request.Size != 2 || p.Response.Body != "ok" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
