package artifact

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeSite is usemagpie.ai's /api/shares as site/shares.js answers it.
type fakeSite struct {
	*httptest.Server
	mu     sync.Mutex
	shares map[string]*fakeShare
	fail   int // answer every ask with this status
	asks   int
}

type fakeShare struct {
	token   string
	version int
	title   string
	files   map[string]string // path → contents, of the version shown
}

func newFakeSite(t *testing.T) *fakeSite {
	t.Helper()
	f := &fakeSite{shares: map[string]*fakeShare{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	t.Setenv("MAGPIE_SHARE_SITE", f.URL)
	return f
}

func (f *fakeSite) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asks++
	if f.fail != 0 {
		http.Error(w, `{"error":"down"}`, f.fail)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/shares/")
	sh := f.shares[id]
	if r.Method != http.MethodPost {
		if sh == nil {
			http.Error(w, `{"error":"no such share"}`, http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+sh.token {
			http.Error(w, `{"error":"wrong token"}`, http.StatusForbidden)
			return
		}
	}
	read := func() (string, map[string]string, bool) {
		if err := r.ParseMultipartForm(64 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return "", nil, false
		}
		var meta struct {
			Title string
			Files []string
		}
		if err := json.Unmarshal([]byte(r.FormValue("meta")), &meta); err != nil || len(meta.Files) == 0 || meta.Files[0] != Page {
			http.Error(w, "bad meta", http.StatusBadRequest)
			return "", nil, false
		}
		files := map[string]string{}
		for i, p := range meta.Files {
			fh := r.MultipartForm.File[strconv.Itoa(i)]
			if len(fh) != 1 {
				http.Error(w, "missing part", http.StatusBadRequest)
				return "", nil, false
			}
			rd, _ := fh[0].Open()
			b, _ := io.ReadAll(rd)
			rd.Close()
			files[p] = string(b)
		}
		return meta.Title, files, true
	}
	switch r.Method {
	case http.MethodPost:
		title, files, ok := read()
		if !ok {
			return
		}
		id := strings.Repeat("a", 15) + string(rune('b'+len(f.shares)))
		f.shares[id] = &fakeShare{token: "tok-" + id, version: 1, title: title, files: files}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"id": id, "url": "https://usemagpie.ai/a/" + id, "token": "tok-" + id, "version": 1})
	case http.MethodPut:
		title, files, ok := read()
		if !ok {
			return
		}
		sh.version++
		sh.title, sh.files = title, files
		json.NewEncoder(w).Encode(map[string]any{"id": id, "url": "https://usemagpie.ai/a/" + id, "version": sh.version})
	case http.MethodDelete:
		delete(f.shares, id)
		json.NewEncoder(w).Encode(map[string]bool{"deleted": true})
	}
}

// the test's own reads and writes of the site's state
func (f *fakeSite) count() (shares, asks int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.shares), f.asks
}

func (f *fakeSite) drop(id string) { f.mu.Lock(); delete(f.shares, id); f.mu.Unlock() }

func (f *fakeSite) failWith(status int) { f.mu.Lock(); f.fail = status; f.mu.Unlock() }

func (f *fakeSite) only(t *testing.T) (string, *fakeShare) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.shares) != 1 {
		t.Fatalf("the site holds %d shares", len(f.shares))
	}
	for id, sh := range f.shares {
		return id, sh
	}
	return "", nil
}

func TestShareFollowsNewVersions(t *testing.T) {
	sandbox(t)
	site := newFakeSite(t)
	a, err := PublishPage(Publish{Page: []byte("<!doctype html><title>Plan</title><p>one"), Files: map[string][]byte{"css/s.css": []byte("p{}")}})
	if err != nil {
		t.Fatal(err)
	}
	if sh, err := Shared(a.ID); sh != nil || err != nil {
		t.Fatalf("shared before the user shared it: %+v %v", sh, err)
	}
	sh, err := ShareLatest(context.Background(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	id, got := site.only(t)
	if sh.ID != id || sh.URL != "https://usemagpie.ai/a/"+id || sh.Version != 1 || got.title != "Plan" ||
		got.files[Page] != "<!doctype html><title>Plan</title><p>one" || got.files["css/s.css"] != "p{}" {
		t.Fatalf("shared %+v as %+v", sh, got)
	}
	// share.json is the user's alone: it holds the token
	if fi, err := os.Stat(filepath.Join(folder(a.ID), shareName)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("share.json: %v %v", fi.Mode(), err)
	}
	// sharing it again asks nothing
	_, asks := site.count()
	if again, err := ShareLatest(context.Background(), a.ID); err != nil || again.URL != sh.URL {
		t.Fatalf("shared again: %+v %v", again, err)
	}
	if _, now := site.count(); now != asks {
		t.Fatalf("sharing it again asked the site %d times", now-asks)
	}
	// a new version goes to the same link
	if _, err := PublishPage(Publish{ID: a.ID, Page: []byte("<!doctype html><p>two")}); err != nil {
		t.Fatal(err)
	}
	sh2, err := ShareLatest(context.Background(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	id2, got := site.only(t)
	if id2 != id || sh2.URL != sh.URL || sh2.Version != 2 || got.version != 2 || got.files[Page] != "<!doctype html><p>two" || got.files["css/s.css"] != "p{}" {
		t.Fatalf("version 2 shared as %+v %+v", sh2, got)
	}
	if err := Unshare(context.Background(), a.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := site.count(); n != 0 {
		t.Fatal("the link is still up")
	}
	if sh, err := Shared(a.ID); sh != nil || err != nil {
		t.Fatalf("still shared: %+v %v", sh, err)
	}
	if _, err := Get(a.ID); err != nil {
		t.Fatalf("unsharing took the artifact: %v", err)
	}
}

// a link taken down on usemagpie.ai (a page reported) is never shared
// again behind the user's back by the next publish
func TestShareTakenDownStaysDown(t *testing.T) {
	sandbox(t)
	site := newFakeSite(t)
	a, _ := PublishPage(Publish{Page: []byte("<p>one")})
	if _, err := ShareLatest(context.Background(), a.ID); err != nil {
		t.Fatal(err)
	}
	id, _ := site.only(t)
	site.drop(id)
	PublishPage(Publish{ID: a.ID, Page: []byte("<p>two")})
	if _, err := ShareLatest(context.Background(), a.ID); !errors.Is(err, ErrSharedGone) {
		t.Fatalf("a taken-down link: %v", err)
	}
	if n, _ := site.count(); n != 0 {
		t.Fatal("shared again at a new link")
	}
	if sh, err := Shared(a.ID); sh != nil || err != nil {
		t.Fatalf("still says shared: %+v %v", sh, err)
	}
}

// the token is in share.json alone: a delete that can't take the link
// down keeps the artifact and its token
func TestDeleteTakesTheLinkDownFirst(t *testing.T) {
	sandbox(t)
	site := newFakeSite(t)
	a, _ := PublishPage(Publish{Page: []byte("<p>one")})
	if _, err := ShareLatest(context.Background(), a.ID); err != nil {
		t.Fatal(err)
	}
	site.failWith(http.StatusInternalServerError)
	if err := Delete(a.ID); err == nil || !strings.Contains(err.Error(), "down") {
		t.Fatalf("deleted with its link up: %v", err)
	}
	if sh, err := Shared(a.ID); sh == nil || err != nil {
		t.Fatalf("the token went: %v", err)
	}
	site.failWith(0)
	if err := Delete(a.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := site.count(); n != 0 {
		t.Fatal("the link outlived its artifact")
	}
	if _, err := Get(a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the artifact is still here: %v", err)
	}
	// one taken down on the site already deletes too
	b, _ := PublishPage(Publish{Page: []byte("<p>b")})
	ShareLatest(context.Background(), b.ID)
	id, _ := site.only(t)
	site.drop(id)
	if err := Delete(b.ID); err != nil {
		t.Fatalf("a link gone already held the delete: %v", err)
	}
}

// an unreadable share.json is unknown, never "not shared": sharing then
// would make a second link and lose the first one's token
func TestUnreadableShareIsNotShareAgain(t *testing.T) {
	sandbox(t)
	site := newFakeSite(t)
	a, _ := PublishPage(Publish{Page: []byte("<p>one")})
	os.WriteFile(filepath.Join(folder(a.ID), shareName), []byte(`{"url":`), 0o600)
	if _, err := Shared(a.ID); err == nil {
		t.Fatal("a half-written share.json read as not shared")
	}
	if _, err := ShareLatest(context.Background(), a.ID); err == nil {
		t.Fatal("shared again")
	}
	if _, asks := site.count(); asks != 0 {
		t.Fatalf("asked the site %d times", asks)
	}
	if err := Delete(a.ID); err == nil {
		t.Fatal("deleted, losing whatever link the file named")
	}
}

func TestShareOffInTests(t *testing.T) {
	sandbox(t)
	t.Setenv("MAGPIE_SHARE_SITE", "")
	a, _ := PublishPage(Publish{Page: []byte("<p>one")})
	if _, err := ShareLatest(context.Background(), a.ID); err == nil {
		t.Fatal("a test shared on usemagpie.ai")
	}
}

func TestShareRefusesWhatTheSiteCantHold(t *testing.T) {
	sandbox(t)
	site := newFakeSite(t)
	big := make([]byte, 12<<20)
	a, err := PublishPage(Publish{Page: []byte("<p>big"), Files: map[string][]byte{"a.bin": big, "b.bin": big, "c.bin": big}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ShareLatest(context.Background(), a.ID); err == nil || !strings.Contains(err.Error(), "32 MB") {
		t.Fatalf("a %d-byte version: %v", a.Latest().Size, err)
	}
	if _, asks := site.count(); asks != 0 {
		t.Fatalf("asked the site %d times", asks)
	}
}
