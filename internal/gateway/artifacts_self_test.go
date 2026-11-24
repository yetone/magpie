package gateway

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/artifact"
)

// pageCall is a page's call into <page>/_api/<what>, as runtime.js makes
// it: a simple POST from its opaque origin.
func pageCall(h http.Handler, ref, what string, call any) (int, map[string]any) {
	b, _ := json.Marshal(call)
	r := httptest.NewRequest(http.MethodPost, ArtifactPagePath+ref+"/_api/"+what, strings.NewReader(string(b)))
	r.RemoteAddr, r.Host = "127.0.0.1:50001", "127.0.0.1:3425"
	r.Header.Set("Origin", "null")
	r.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	r.Header.Set("Sec-Fetch-Mode", "cors")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func selfPage(t *testing.T, caps string, files map[string][]byte) *artifact.Artifact {
	t.Helper()
	var c artifact.Capabilities
	if caps != "" {
		var err error
		if c, err = artifact.ParseCapabilities(json.RawMessage(caps)); err != nil {
			t.Fatal(err)
		}
	}
	a, err := artifact.PublishPage(artifact.Publish{Title: "List", Page: []byte("<!doctype html><title>List</title><p>0"), Files: files, Capabilities: c})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// A page that declares artifact publishes itself: its whole page becomes
// the newest version, saved as the page, with its capabilities kept, and a
// publish from a view of an older version is refused with the live one.
func TestArtifactPagePublishesItself(t *testing.T) {
	h := artifacts(t)
	a := selfPage(t, `{"artifact": {}, "db": {}}`, nil)
	page := ArtifactPagePath + a.ID + "/"
	if body := pageGet(h, page).Body.String(); !strings.Contains(body, `"version":1`) || strings.Contains(body, `"pinned"`) || !strings.Contains(body, `"home":"`+page+`"`) {
		t.Fatalf("the runtime's config: %.600s", body)
	}
	if body := pageGet(h, ArtifactPagePath+a.ID+"@1/").Body.String(); !strings.Contains(body, `"pinned":true`) {
		t.Fatalf("a pinned view's config: %.600s", body)
	}
	tok := pageToken(t, h, page)
	code, out := pageCall(h, a.ID, "publish", map[string]any{"t": tok, "base": 1, "html": "<!doctype html><title>List</title><p>1"})
	if code != 200 || out["version"] != "2" || out["shas"] != nil || out["changed"] != nil {
		t.Fatalf("publish: %d %v", code, out)
	}
	b, _ := artifact.Get(a.ID)
	v := b.Latest()
	if v.N != 2 || v.Agent != artifact.PageAgent || !v.Capabilities.Has("db") || !v.Capabilities.Has("artifact") {
		t.Fatalf("version 2: %+v", v)
	}
	if body := pageGet(h, page).Body.String(); !strings.Contains(body, "<p>1") || !strings.Contains(body, `"version":2`) {
		t.Fatalf("the newest page: %.600s", body)
	}
	// a view still on version 1 loses to version 2
	code, out = pageCall(h, a.ID, "publish", map[string]any{"t": tok, "base": 1, "html": "<!doctype html><p>late"})
	if e, _ := out["error"].(map[string]any); code != 409 || errCode(out) != "conflict" || e["live"] != "2" {
		t.Fatalf("a stale publish: %d %v", code, out)
	}
	if b, _ := artifact.Get(a.ID); b.Latest().N != 2 {
		t.Fatalf("the stale publish was written: %+v", b.Latest())
	}
	// self, the former name, publishes the same way
	s := selfPage(t, `{"self": {}}`, nil)
	stok := pageToken(t, h, ArtifactPagePath+s.ID+"/")
	if code, out := pageCall(h, s.ID, "publish", map[string]any{"t": stok, "base": 1, "html": "<!doctype html><p>s"}); code != 200 || out["version"] != "2" {
		t.Fatalf("self: %d %v", code, out)
	}
}

// What a publish may not be, and who may not publish, each with the code
// claude.ai's runtime gives it.
func TestArtifactPagePublishRefused(t *testing.T) {
	h := artifacts(t)
	a := selfPage(t, `{"artifact": {}}`, nil)
	tok := pageToken(t, h, ArtifactPagePath+a.ID+"/")
	for _, c := range []struct {
		name   string
		call   map[string]any
		status int
		code   string
	}{
		{"no doctype", map[string]any{"t": tok, "base": 1, "html": "<p>no doctype"}, 400, "invalid_content"},
		{"nothing", map[string]any{"t": tok, "base": 1}, 400, "invalid_content"},
		{"both", map[string]any{"t": tok, "base": 1, "html": "<!doctype html>", "files": map[string]any{"a.txt": map[string]any{"text": "a"}}}, 400, "invalid_content"},
		{"a bad path", map[string]any{"t": tok, "base": 1, "files": map[string]any{"../x": map[string]any{"text": "a"}}}, 400, "invalid_content"},
		{"the runtime's folder", map[string]any{"t": tok, "base": 1, "files": map[string]any{"_api/x": map[string]any{"text": "a"}}}, 400, "invalid_content"},
		{"the page deleted", map[string]any{"t": tok, "base": 1, "files": map[string]any{"index.html": nil}}, 400, "invalid_content"},
		{"a page without doctype", map[string]any{"t": tok, "base": 1, "files": map[string]any{"index.html": map[string]any{"text": "<p>x"}}}, 400, "invalid_content"},
		{"text as bytes' type", map[string]any{"t": tok, "base": 1, "files": map[string]any{"a.png": map[string]any{"text": "a", "contentType": "image/png"}}}, 400, "invalid_content"},
		{"a type with params", map[string]any{"t": tok, "base": 1, "files": map[string]any{"a.txt": map[string]any{"text": "a", "contentType": "text/plain; charset=utf-8"}}}, 400, "invalid_content"},
		{"bad base64", map[string]any{"t": tok, "base": 1, "files": map[string]any{"a.bin": map[string]any{"base64": "!!"}}}, 400, "invalid_content"},
		{"too large", map[string]any{"t": tok, "base": 1, "html": "<!doctype html>" + strings.Repeat("x", artifact.MaxPage)}, 413, "too_large"},
		{"another page's token", map[string]any{"t": "nope", "base": 1, "html": "<!doctype html>"}, 403, "not_granted"},
	} {
		code, out := pageCall(h, a.ID, "publish", c.call)
		if code != c.status || errCode(out) != c.code {
			t.Errorf("%s: %d %v, want %d %s", c.name, code, out, c.status, c.code)
		}
	}
	if b, _ := artifact.Get(a.ID); b.Latest().N != 1 {
		t.Fatalf("a refused publish was written: %+v", b.Latest())
	}

	// a page that doesn't declare it, or declares publish: false
	for caps, want := range map[string]string{`{"db": {}}`: "not_granted", `{"artifact": {"publish": false}}`: "not_granted", ``: "not_granted"} {
		b := selfPage(t, caps, nil)
		btok := ptok(t, b.ID)
		if code, out := pageCall(h, b.ID, "publish", map[string]any{"t": btok, "base": 1, "html": "<!doctype html>"}); code != 403 || errCode(out) != want {
			t.Errorf("caps %s: %d %v", caps, code, out)
		}
	}
	// the newest version dropped it: a view of an older one is told so
	d := selfPage(t, `{"artifact": {}}`, nil)
	if _, err := artifact.PublishPage(artifact.Publish{ID: d.ID, Page: []byte("<!doctype html><p>2"), Capabilities: artifact.Capabilities{}}); err != nil {
		t.Fatal(err)
	}
	if code, out := pageCall(h, d.ID+"@1", "publish", map[string]any{"t": ptok(t, d.ID), "base": 1, "html": "<!doctype html>"}); code != 403 || errCode(out) != "not_declared" {
		t.Errorf("dropped: %d %v", code, out)
	}
	// a page publishes at most artifactPublishMost times a minute
	r := selfPage(t, `{"artifact": {}}`, nil)
	rtok := ptok(t, r.ID)
	for i := 1; i <= artifactPublishMost+1; i++ {
		code, out := pageCall(h, r.ID, "publish", map[string]any{"t": rtok, "base": i, "html": "<!doctype html><p>" + strings.Repeat("x", i)})
		if i <= artifactPublishMost && code != 200 || i > artifactPublishMost && (code != 429 || errCode(out) != "rate_limited") {
			t.Fatalf("publish %d: %d %v", i, code, out)
		}
	}
}

// The files form: the page keeps its page and saves the files that
// changed. With ifMatch on every file, another writer's save to other
// files lands between, and the answer says what they changed; a file that
// no longer matches refuses it with the paths.
func TestArtifactPagePublishesFiles(t *testing.T) {
	h := artifacts(t)
	a := selfPage(t, `{"artifact": {}}`, map[string][]byte{"data/a.json": []byte(`{"a":1}`), "data/b.json": []byte(`{"b":1}`)})
	tok := pageToken(t, h, ArtifactPagePath+a.ID+"/")

	// whole-version check: text, bytes, a delete
	code, out := pageCall(h, a.ID, "publish", map[string]any{"t": tok, "base": 1, "files": map[string]any{
		"data/a.json": map[string]any{"text": `{"a":2}`},
		"img/dot.png": map[string]any{"base64": base64.StdEncoding.EncodeToString([]byte{0x89, 'P', 'N', 'G'}), "contentType": "image/png"},
		"data/b.json": nil,
	}})
	if code != 200 || out["version"] != "2" || out["shas"] != nil {
		t.Fatalf("files: %d %v", code, out)
	}
	b, _ := artifact.Get(a.ID)
	if v := b.Latest(); strings.Join(v.Files, ",") != "data/a.json,img/dot.png" || v.Agent != artifact.PageAgent {
		t.Fatalf("version 2: %+v", v)
	}
	where, _, _ := artifact.File(a.ID, 2, "img/dot.png")
	if got, _ := os.ReadFile(where); string(got) != "\x89PNG" {
		t.Fatalf("the bytes: %q", got)
	}
	// another writer saves data/c.json (version 3) while this view is on 2
	if _, err := artifact.PublishPage(artifact.Publish{ID: a.ID, Files: map[string][]byte{"data/c.json": []byte(`{"c":1}`)}}); err != nil {
		t.Fatal(err)
	}
	// without ifMatch on every file, the whole version is checked
	code, out = pageCall(h, a.ID, "publish", map[string]any{"t": tok, "base": 2, "files": map[string]any{"data/a.json": map[string]any{"text": `{"a":3}`}}})
	if e, _ := out["error"].(map[string]any); code != 409 || errCode(out) != "conflict" || e["live"] != "3" || e["paths"] != nil {
		t.Fatalf("a whole-version conflict: %d %v", code, out)
	}
	// with it, this save lands over theirs, and says what they changed
	code, out = pageCall(h, a.ID, "publish", map[string]any{"t": tok, "base": 2, "files": map[string]any{
		"data/a.json": map[string]any{"text": `{"a":3}`, "ifMatch": sha(`{"a":2}`)},
		"data/n.json": map[string]any{"text": `{"n":1}`, "ifMatch": nil},
	}})
	if code != 200 || out["version"] != "4" {
		t.Fatalf("an ifMatch save: %d %v", code, out)
	}
	shas, _ := out["shas"].(map[string]any)
	if len(shas) != 2 || shas["data/a.json"] != sha(`{"a":3}`) || shas["data/n.json"] != sha(`{"n":1}`) {
		t.Fatalf("shas: %v", out["shas"])
	}
	changed, _ := out["changed"].([]any)
	if c, _ := changed[0].(map[string]any); len(changed) != 1 || c["path"] != "data/c.json" || c["sha256"] != sha(`{"c":1}`) {
		t.Fatalf("changed: %v", out["changed"])
	}
	// a file that changed since: nothing is published, and the paths say so
	code, out = pageCall(h, a.ID, "publish", map[string]any{"t": tok, "base": 2, "files": map[string]any{
		"data/a.json": map[string]any{"text": `{"a":9}`, "ifMatch": sha(`{"a":2}`)},
		"data/c.json": map[string]any{"delete": true, "ifMatch": sha(`{"c":1}`)},
		"data/n.json": map[string]any{"text": `{}`, "ifMatch": nil},
	}})
	e, _ := out["error"].(map[string]any)
	paths, _ := e["paths"].([]any)
	if code != 409 || errCode(out) != "conflict" || e["live"] != "4" || len(paths) != 2 {
		t.Fatalf("a file conflict: %d %v", code, out)
	}
	p0, _ := paths[0].(map[string]any)
	p1, _ := paths[1].(map[string]any)
	if p0["path"] != "data/a.json" || p0["expected"] != sha(`{"a":2}`) || p0["actual"] != sha(`{"a":3}`) || p1["path"] != "data/n.json" || p1["expected"] != nil || p1["actual"] != sha(`{"n":1}`) {
		t.Fatalf("the paths: %v", paths)
	}
	if b, _ := artifact.Get(a.ID); b.Latest().N != 4 {
		t.Fatalf("the conflicting save was written: %+v", b.Latest())
	}
	// the page itself, in the files form
	code, out = pageCall(h, a.ID, "publish", map[string]any{"t": tok, "base": 4, "files": map[string]any{"index.html": map[string]any{"text": "<!doctype html><p>new", "ifMatch": sha("<!doctype html><title>List</title><p>0")}}})
	if shas, _ := out["shas"].(map[string]any); code != 200 || shas["index.html"] != sha("<!doctype html><p>new") {
		t.Fatalf("the page as a file: %d %v", code, out)
	}
}

// A view that publishes itself hears each newer version on its events
// stream, a version published while it loaded too; a page with db alone
// hears none.
func TestArtifactPageEventsSayVersions(t *testing.T) {
	h := artifacts(t)
	a := selfPage(t, `{"artifact": {}}`, nil)
	tok := pageToken(t, h, ArtifactPagePath+a.ID+"/")
	srv := httptest.NewServer(h)
	defer srv.Close()
	open := func(id, q string) (chan string, func()) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+ArtifactPagePath+id+"/_api/events?t="+ptok(t, id)+q, nil)
		req.Header.Set("Origin", "null")
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != 200 {
			t.Fatalf("events: %d", res.StatusCode)
		}
		lines := make(chan string, 64)
		go func() {
			sc := bufio.NewScanner(res.Body)
			for sc.Scan() {
				lines <- sc.Text()
			}
			close(lines)
		}()
		return lines, func() { res.Body.Close() }
	}
	wait := func(lines chan string, want string) {
		t.Helper()
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatalf("the stream ended before %s", want)
				}
				if l == want {
					return
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("no %s", want)
			}
		}
	}
	lines, stop := open(a.ID, "&v=1")
	defer stop()
	wait(lines, ": magpie")
	if code, out := pageCall(h, a.ID, "publish", map[string]any{"t": tok, "base": 1, "html": "<!doctype html><p>2"}); code != 200 {
		t.Fatalf("publish: %d %v", code, out)
	}
	wait(lines, `data: {"version":2}`)
	// an agent's publish too
	if said, isErr := artifactsCall(t, h, artifactPublishTool, map[string]any{"id": a.ID, "html": "<!doctype html><p>3"}); isErr {
		t.Fatal(said)
	}
	wait(lines, `data: {"version":3}`)

	// a view of version 1 that connects now hears 3 at once
	late, stop2 := open(a.ID, "&v=1")
	defer stop2()
	wait(late, `data: {"version":3}`)

	// db alone: the page doesn't follow versions
	d := selfPage(t, `{"db": {}}`, nil)
	dl, stop3 := open(d.ID, "&v=1")
	defer stop3()
	wait(dl, ": magpie")
	if _, err := artifact.PublishPage(artifact.Publish{ID: d.ID, Page: []byte("<!doctype html><p>2")}); err != nil {
		t.Fatal(err)
	}
	if _, err := artifact.PublishPage(artifact.Publish{ID: d.ID, Files: map[string][]byte{"x.txt": []byte("x")}}); err != nil {
		t.Fatal(err)
	}
	// a write after both is told, and no version came before it
	artifactsCall(t, h, artifactDataTool, map[string]any{"id": d.ID, "action": "set", "path": "n/1", "data": map[string]any{"a": 1}})
	for {
		select {
		case l := <-dl:
			if strings.Contains(l, "version") {
				t.Fatalf("a db page was told a version: %s", l)
			}
			if l == `data: {"paths":["n/1"]}` {
				return
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the write wasn't told")
		}
	}
}

// metadata: the titles of the artifacts a page names, by id or link, an
// empty entry for what names none here; a page without artifact gets
// nothing.
func TestArtifactPageMetadata(t *testing.T) {
	h := artifacts(t)
	a := selfPage(t, `{"artifact": {"publish": false}}`, nil)
	other, _ := artifact.PublishPage(artifact.Publish{Title: "Other", Page: []byte("<p>o")})
	refs := []string{other.ID, "http://127.0.0.1:3425/a/" + other.ID + "@1/", "https://claude.ai/artifact/Other-" + other.ID, "https://evil.example/x?" + other.ID, "nope", "aaaaaaaaaaaaaaaaaaaaaaaaaa"}
	code, out := pageCall(h, a.ID, "meta", map[string]any{"t": ptok(t, a.ID), "refs": refs})
	got, _ := out["artifacts"].(map[string]any)
	if code != 200 || len(got) != len(refs) {
		t.Fatalf("meta: %d %v", code, out)
	}
	for i, r := range refs {
		e, _ := got[r].(map[string]any)
		named := i < 3
		if named && (e["id"] != other.ID || e["title"] != "Other") || !named && (e["id"] != "" || e["title"] != "") || e["type"] != nil {
			t.Errorf("%s: %v", r, e)
		}
	}
	d := selfPage(t, `{"db": {}}`, nil)
	if code, out := pageCall(h, d.ID, "meta", map[string]any{"t": ptok(t, d.ID), "refs": refs}); code != 403 || errCode(out) != "not_granted" {
		t.Fatalf("a db page's meta: %d %v", code, out)
	}
}

func ptok(t *testing.T, id string) string {
	t.Helper()
	tok, err := artifact.PageToken(id)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}
