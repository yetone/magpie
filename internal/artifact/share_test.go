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
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSite is usemagpie.ai's /api/shares as site/shares.js answers it.
type fakeSite struct {
	*httptest.Server
	mu     sync.Mutex
	shares map[string]*fakeShare
	fail   int // answer every ask with this status
	asks   int
	// dataFail answers GET …/data with this status
	dataFail int
	// blobs holds which share holds each asset, as shares.js's table;
	// maxBlobs is a share's budget in files (0: shares.js's)
	blobs    map[string]string
	maxBlobs int
	// said is each ask, "METHOD sub"
	said []string
	// noBlobs answers the asset calls as a site without them does
	noBlobs bool
}

type fakeShare struct {
	token   string
	version int
	title   string
	files   map[string]string // path → contents, of the version shown
	caps    map[string]any    // meta's capabilities
	owner   string            // meta's owner
	// docs is the share's data: the data part it was first sent with,
	// then what its viewers wrote (a test sets it)
	docs   []SharedDoc
	seeded bool
	// assets is what the share holds of the artifact's: id → type, body
	assets map[string][2]string
}

func newFakeSite(t *testing.T) *fakeSite {
	t.Helper()
	f := &fakeSite{shares: map[string]*fakeShare{}, blobs: map[string]string{}}
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
	// "/api/shares" (a new share), "/api/shares/<id>" or "/api/shares/<id>/<sub>"
	id, sub, _ := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/api/shares"), "/"), "/")
	f.said = append(f.said, r.Method+" "+sub)
	sh := f.shares[id]
	if r.Method != http.MethodPost || sub != "" {
		if sh == nil {
			http.Error(w, `{"error":"no such shared page: it was taken down"}`, http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+sh.token {
			http.Error(w, `{"error":"wrong token"}`, http.StatusForbidden)
			return
		}
	}
	switch {
	case sub == "data" && r.Method == http.MethodGet:
		if f.dataFail != 0 {
			http.Error(w, `{"error":"down"}`, f.dataFail)
			return
		}
		// as live.js answers it: held says the page's data was seeded
		docs := sh.docs
		if docs == nil {
			docs = []SharedDoc{}
		}
		json.NewEncoder(w).Encode(map[string]any{"held": sh.seeded, "docs": docs})
		return
	case f.noBlobs && (sub == "blobs" || strings.HasPrefix(sub, "blobs/")):
		http.Error(w, `{"error":"no such call"}`, http.StatusNotFound)
		return
	case sub == "blobs" && r.Method == http.MethodGet:
		list := []map[string]any{}
		for aid, x := range sh.assets {
			list = append(list, map[string]any{"id": aid, "type": x[0], "bytes": len(x[1])})
		}
		json.NewEncoder(w).Encode(map[string]any{"blobs": list})
		return
	case strings.HasPrefix(sub, "blobs/"):
		aid := strings.TrimPrefix(sub, "blobs/")
		if other, ok := f.blobs[aid]; ok && other != id {
			http.Error(w, `{"error":"another shared page holds that asset"}`, http.StatusConflict)
			return
		}
		if r.Method == http.MethodDelete {
			_, had := sh.assets[aid]
			delete(sh.assets, aid)
			delete(f.blobs, aid)
			json.NewEncoder(w).Encode(map[string]bool{"deleted": had})
			return
		}
		most := f.maxBlobs
		if most == 0 {
			most = 256
		}
		if _, had := sh.assets[aid]; !had && len(sh.assets)+1 > most {
			http.Error(w, `{"error":"a shared page's assets are at most `+strconv.Itoa(most)+` files"}`, http.StatusRequestEntityTooLarge)
			return
		}
		b, _ := io.ReadAll(r.Body)
		if sh.assets == nil {
			sh.assets = map[string][2]string{}
		}
		sh.assets[aid] = [2]string{r.Header.Get("Content-Type"), string(b)}
		f.blobs[aid] = id
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"id": aid})
		return
	case sub == "owner" && r.Method == http.MethodPost:
		json.NewEncoder(w).Encode(map[string]string{"code": "c0de", "url": "https://usemagpie.ai/a/" + id + "#owner=c0de"})
		return
	case sub != "":
		http.Error(w, `{"error":"no such route"}`, http.StatusNotFound)
		return
	}
	// meta and data: what shares.js keeps of them
	keep := func(sh *fakeShare, caps map[string]any, owner string) bool {
		sh.caps, sh.owner = caps, owner
		if raw := r.FormValue("data"); raw != "" && !sh.seeded {
			var docs []SharedDoc
			if err := json.Unmarshal([]byte(raw), &docs); err != nil {
				http.Error(w, "bad data", http.StatusBadRequest)
				return false
			}
			sh.docs, sh.seeded = docs, true
		}
		return true
	}
	var meta struct {
		Title        string
		Files        []string
		Capabilities map[string]any
		Owner        string
	}
	read := func() (string, map[string]string, bool) {
		if err := r.ParseMultipartForm(64 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return "", nil, false
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
		sh := &fakeShare{token: "tok-" + id, version: 1, title: title, files: files}
		if !keep(sh, meta.Capabilities, meta.Owner) {
			return
		}
		f.shares[id] = sh
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"id": id, "url": "https://usemagpie.ai/a/" + id, "token": "tok-" + id, "version": 1})
	case http.MethodPut:
		title, files, ok := read()
		if !ok {
			return
		}
		if !keep(sh, meta.Capabilities, meta.Owner) {
			return
		}
		sh.version++
		sh.title, sh.files = title, files
		json.NewEncoder(w).Encode(map[string]any{"id": id, "url": "https://usemagpie.ai/a/" + id, "version": sh.version})
	case http.MethodDelete:
		delete(f.shares, id)
		for aid, holder := range f.blobs {
			if holder == id {
				delete(f.blobs, aid)
			}
		}
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
		got.files[Page] != sharedPage("<!doctype html><title>Plan</title><p>one") || got.files["css/s.css"] != "p{}" {
		t.Fatalf("shared %+v as %+v", sh, got)
	}
	// share.json is the user's alone: it holds the token (Windows has no
	// such bits: a file there is its owner's by its folder's ACL)
	if fi, err := os.Stat(filepath.Join(folder(a.ID), shareName)); err != nil || runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("share.json: %v %v", fi.Mode(), err)
	}
	// sharing it again sends no version, only asks after its assets
	_, asks := site.count()
	if again, err := ShareLatest(context.Background(), a.ID); err != nil || again.URL != sh.URL {
		t.Fatalf("shared again: %+v %v", again, err)
	}
	if _, now := site.count(); now != asks+1 || got.version != 1 {
		t.Fatalf("sharing it again asked the site %d times, version %d", now-asks, got.version)
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
	if id2 != id || sh2.URL != sh.URL || sh2.Version != 2 || got.version != 2 || got.files[Page] != sharedPage("<!doctype html><p>two") || got.files["css/s.css"] != "p{}" {
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

// sharedPage is a page as its shared copy carries it: with the runtime
// that resolves every capability null.
func sharedPage(page string) string {
	return string(WithRuntime(Served([]byte(page)), SharedRuntime(nil)))
}

func (f *fakeSite) setDocs(id string, docs []SharedDoc) {
	f.mu.Lock()
	f.shares[id].docs = docs
	f.mu.Unlock()
}

func (f *fakeSite) failData(status int) { f.mu.Lock(); f.dataFail = status; f.mu.Unlock() }

// livePage is an artifact that declares what a live share serves, and
// sample, which it doesn't; its store holds two documents.
func livePage(t *testing.T) (*Artifact, *DB) {
	t.Helper()
	a, err := PublishPage(Publish{Page: []byte("<p>board"), Capabilities: Capabilities{
		"db":     json.RawMessage(`{}`),
		"user":   json.RawMessage(`{}`),
		"room":   json.RawMessage(`{"topics":{"move":"interact"}}`),
		"sample": json.RawMessage(`{}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	db, err := OpenDB(a.ID, 0, Viewer{Agent: true})
	if err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string]map[string]any{"cards/c1": {"title": "one"}, "cards/c2": {"title": "two"}} {
		if err := db.Set(path, data); err != nil {
			t.Fatal(err)
		}
	}
	return a, db
}

// A page that keeps data and has a room is shared with what usemagpie.ai
// serves it (db, room, user; never sample), its owner's id, and its
// data; a page that declares none of those sends none of it.
func TestShareCarriesWhatTheSiteServes(t *testing.T) {
	sandbox(t)
	site := newFakeSite(t)
	a, _ := livePage(t)
	if _, err := ShareLatest(context.Background(), a.ID); err != nil {
		t.Fatal(err)
	}
	_, got := site.only(t)
	if len(got.caps) != 3 || got.caps["db"] == nil || got.caps["user"] == nil || got.caps["sample"] != nil {
		t.Fatalf("capabilities: %v", got.caps)
	}
	if topics := got.caps["room"].(map[string]any)["topics"].(map[string]any); topics["move"] != "interact" {
		t.Fatalf("room's declaration: %v", got.caps["room"])
	}
	me, _ := viewerID()
	if got.owner != me {
		t.Fatalf("owner %q, want %q", got.owner, me)
	}
	if len(got.docs) != 2 || got.docs[0].Path != "cards/c1" || got.docs[0].Data["title"] != "one" || got.docs[1].Version != 1 || got.docs[1].Updated.IsZero() {
		t.Fatalf("data: %+v", got.docs)
	}

	plain, _ := PublishPage(Publish{Page: []byte("<p>plain")})
	sh, err := ShareLatest(context.Background(), plain.ID)
	if err != nil {
		t.Fatal(err)
	}
	site.mu.Lock()
	p := site.shares[sh.ID]
	site.mu.Unlock()
	if p.caps != nil || p.owner != "" || p.seeded {
		t.Fatalf("a plain page sent %v %q %v", p.caps, p.owner, p.seeded)
	}
}

// Unsharing takes what the viewers wrote back into the store first, in
// place of what it held; a failed read keeps the page shared, and its
// store as it was.
func TestUnshareTakesTheDataBack(t *testing.T) {
	sandbox(t)
	site := newFakeSite(t)
	a, db := livePage(t)
	sh, err := ShareLatest(context.Background(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	at := time.UnixMilli(1_790_000_000_000)
	site.setDocs(sh.ID, []SharedDoc{
		{Path: "cards/c1", Data: map[string]any{"title": "one, edited"}, Version: 5, Updated: at},
		{Path: "votes/u_1", Data: map[string]any{"up": true}, Version: 1, Updated: at},
	})

	site.failData(http.StatusInternalServerError)
	if err := Unshare(context.Background(), a.ID); err == nil {
		t.Fatal("unshared with its data unread")
	}
	if s, _ := Shared(a.ID); s == nil {
		t.Fatal("not shared any more")
	}
	if n, _ := site.count(); n != 1 {
		t.Fatal("the link went down")
	}
	if d, _ := db.Get("cards/c1"); d == nil || d.Data["title"] != "one" {
		t.Fatalf("the store changed: %+v", d)
	}

	site.failData(0)
	if err := Unshare(context.Background(), a.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := site.count(); n != 0 {
		t.Fatal("the link is still up")
	}
	d, _ := db.Get("cards/c1")
	if d == nil || d.Data["title"] != "one, edited" || d.Version != 5 || !d.Updated.Equal(at) {
		t.Fatalf("cards/c1: %+v", d)
	}
	if d, _ := db.Get("cards/c2"); d != nil {
		t.Fatalf("a document the share deleted came back: %+v", d)
	}
	if d, _ := db.Get("votes/u_1"); d == nil {
		t.Fatal("a viewer's vote was lost")
	}
	if n, _, _ := Stats(a.ID); n != 2 {
		t.Fatalf("%d documents", n)
	}
}

// What the share holds goes back even when the version shown last
// declares no db (one before did); a share whose versions never
// declared db leaves the store alone, and a site that says it never
// held the data writes nothing over it.
func TestUnshareTakesBackWhatTheShareHeld(t *testing.T) {
	sandbox(t)
	site := newFakeSite(t)
	a, db := livePage(t)
	sh, err := ShareLatest(context.Background(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PublishPage(Publish{ID: a.ID, Page: []byte("<p>room only"), Capabilities: Capabilities{"room": json.RawMessage(`{}`)}}); err != nil {
		t.Fatal(err)
	}
	if s, err := ShareLatest(context.Background(), a.ID); err != nil || !s.Data {
		t.Fatalf("the share forgot it holds data: %+v %v", s, err)
	}
	site.setDocs(sh.ID, []SharedDoc{{Path: "cards/c1", Data: map[string]any{"title": "kept"}, Version: 3}})
	if err := Unshare(context.Background(), a.ID); err != nil {
		t.Fatal(err)
	}
	if d, _ := db.Get("cards/c1"); d == nil || d.Data["title"] != "kept" {
		t.Fatalf("cards/c1: %+v", d)
	}

	// shared with a room alone, then given db: the site seeds it, so the
	// share holds it; but one whose site says it never held any is left
	room, _ := PublishPage(Publish{Page: []byte("<p>room"), Capabilities: Capabilities{"room": json.RawMessage(`{}`)}})
	rdb, err := OpenDB(room.ID, 0, Viewer{Agent: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := rdb.Set("notes/n1", map[string]any{"t": "mine"}); err != nil {
		t.Fatal(err)
	}
	rs, err := ShareLatest(context.Background(), room.ID)
	if err != nil || rs.Data {
		t.Fatalf("a room alone holds data: %+v %v", rs, err)
	}
	site.failData(http.StatusInternalServerError)
	if err := Unshare(context.Background(), room.ID); err != nil {
		t.Fatalf("a share with no data asked for it: %v", err)
	}
	site.failData(0)
	if d, _ := rdb.Get("notes/n1"); d == nil || d.Data["t"] != "mine" {
		t.Fatalf("notes/n1: %+v", d)
	}
}

// Deleting a shared artifact takes its link down without reading its
// data, which has nowhere to go.
func TestDeleteDoesntTakeBack(t *testing.T) {
	sandbox(t)
	site := newFakeSite(t)
	a, _ := livePage(t)
	if _, err := ShareLatest(context.Background(), a.ID); err != nil {
		t.Fatal(err)
	}
	site.failData(http.StatusInternalServerError)
	if err := Delete(a.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := site.count(); n != 0 {
		t.Fatal("the link is still up")
	}
}

// The owner link is the share's own link with a code; an artifact not
// shared, or taken down there, has none.
func TestOwnerLink(t *testing.T) {
	sandbox(t)
	site := newFakeSite(t)
	a, _ := livePage(t)
	if _, err := OwnerLink(context.Background(), a.ID); err == nil {
		t.Fatal("an owner link for a page not shared")
	}
	sh, err := ShareLatest(context.Background(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	u, err := OwnerLink(context.Background(), a.ID)
	if err != nil || u != sh.URL+"#owner=c0de" {
		t.Fatalf("%q %v", u, err)
	}
	site.drop(sh.ID)
	if _, err := OwnerLink(context.Background(), a.ID); !errors.Is(err, ErrSharedGone) {
		t.Fatalf("taken down: %v", err)
	}
}

// DumpDocs and RestoreDocs carry a store whole; a document the store
// wouldn't take refuses all of them, and the store stays as it was.
func TestRestoreDocsAllOrNone(t *testing.T) {
	sandbox(t)
	a, db := livePage(t)
	docs, err := DumpDocs(a.ID)
	if err != nil || len(docs) != 2 {
		t.Fatalf("%+v %v", docs, err)
	}
	for _, bad := range [][]SharedDoc{
		{{Path: "cards/x", Data: map[string]any{}}, {Path: "cards/x", Data: map[string]any{}}},
		{{Path: "cards", Data: map[string]any{}}},
		{{Path: "../x", Data: map[string]any{}}},
		{{Path: "cards/x", Data: nil}},
		{{Path: "cards/big", Data: map[string]any{"s": strings.Repeat("x", 257<<10)}}},
	} {
		if err := RestoreDocs(a.ID, append(append([]SharedDoc{}, docs...), bad...)); err == nil {
			t.Fatalf("took %+v", bad)
		}
		if n, _, _ := Stats(a.ID); n != 2 {
			t.Fatalf("after %+v: %d documents", bad, n)
		}
	}
	docs[0].Data = map[string]any{"title": "back"}
	if err := RestoreDocs(a.ID, docs); err != nil {
		t.Fatal(err)
	}
	if d, _ := db.Get("cards/c1"); d.Data["title"] != "back" {
		t.Fatalf("%+v", d)
	}
	if err := RestoreDocs(a.ID, nil); err != nil {
		t.Fatal(err)
	}
	if n, b, _ := Stats(a.ID); n != 0 || b != 0 {
		t.Fatalf("emptied: %d %d", n, b)
	}
}
