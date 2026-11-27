package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pageFilesCall posts one call to the artifact's files route from this
// computer, as the shell does. hdr are RemoteAddr or Host to set over
// the shell's.
func pageFilesCall(h http.Handler, ref string, body map[string]any, hdr ...string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, ArtifactPagePath+ref+"/_api/files", bytes.NewReader(b))
	r.RemoteAddr, r.Host = "127.0.0.1:50001", "127.0.0.1:3425"
	r.Header.Set("Content-Type", "text/plain")
	for i := 0; i+1 < len(hdr); i += 2 {
		switch hdr[i] {
		case "RemoteAddr":
			r.RemoteAddr = hdr[i+1]
		case "Host":
			r.Host = hdr[i+1]
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func filesOut(t *testing.T, w *httptest.ResponseRecorder) (map[string]any, map[string]any) {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("%d %q: %v", w.Code, w.Body.String(), err)
	}
	e, _ := out["error"].(map[string]any)
	return out, e
}

// filesPage is a page that declares files on a project folder of
// README.md, src/main.go, src/lib/util.go, run.sh and a .git.
func filesPage(t *testing.T) (http.Handler, string, string, string) {
	t.Helper()
	h := artifacts(t)
	dir := t.TempDir()
	for name, body := range map[string]string{
		"README.md":       "# Plan\nhello",
		"src/main.go":     "package main",
		"src/lib/util.go": "package lib",
		"run.sh":          "echo hi",
		".git/config":     "[core]",
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	id := publishFor(t, h, map[string]any{"files": map[string]any{"project": dir}})
	return h, id, shellTokenOf(t, h, id), dir
}

func paths(t *testing.T, out map[string]any) []string {
	t.Helper()
	var ps []string
	for _, e := range out["entries"].([]any) {
		m := e.(map[string]any)
		p := m["path"].(string)
		if m["isDirectory"] == true {
			p += "/"
		}
		ps = append(ps, p)
	}
	return ps
}

// The shell lists the folder, a level or all of it, without .git, and
// tells where it is.
func TestArtifactFilesLists(t *testing.T) {
	h, id, tok, dir := filesPage(t)
	out, e := filesOut(t, pageFilesCall(h, id, map[string]any{"t": tok, "op": "where"}))
	if e != nil || out["folder"] != dir || out["name"] != filepath.Base(dir) {
		t.Fatalf("where: %v %v", out, e)
	}
	out, e = filesOut(t, pageFilesCall(h, id, map[string]any{"t": tok, "op": "list"}))
	if got := strings.Join(paths(t, out), " "); e != nil || got != "README.md run.sh src/" || out["cursor"] != nil {
		t.Fatalf("list: %q %v", got, e)
	}
	out, e = filesOut(t, pageFilesCall(h, id, map[string]any{"t": tok, "op": "list", "path": "src", "recursive": true}))
	if got := strings.Join(paths(t, out), " "); e != nil || got != "src/lib/ src/lib/util.go src/main.go" {
		t.Fatalf("list src: %q %v", got, e)
	}
	for _, m := range out["entries"].([]any) {
		if e := m.(map[string]any); e["path"] == "src/main.go" && e["size"] != float64(len("package main")) {
			t.Fatalf("size %v", e["size"])
		}
	}
	for _, p := range []string{".git", "src/main.go", "nope"} {
		if _, e := filesOut(t, pageFilesCall(h, id, map[string]any{"t": tok, "op": "list", "path": p})); e == nil {
			t.Errorf("listed %s", p)
		}
	}
}

// A recursive list of more than a page goes on from its cursor, each
// entry once and in order, into the folders that hold the cursor.
func TestArtifactFilesListPages(t *testing.T) {
	h, id, tok, dir := filesPage(t)
	want := map[string]bool{}
	for i := range 3 {
		for j := range 300 {
			p := fmt.Sprintf("many/d%d/f%03d.txt", i, j)
			os.MkdirAll(filepath.Join(dir, "many", fmt.Sprintf("d%d", i)), 0o755)
			os.WriteFile(filepath.Join(dir, filepath.FromSlash(p)), nil, 0o644)
			want[p] = true
		}
		want[fmt.Sprintf("many/d%d/", i)] = true
	}
	seen, cursor, pages, last := map[string]bool{}, "", 0, ""
	for {
		out, e := filesOut(t, pageFilesCall(h, id, map[string]any{"t": tok, "op": "list", "path": "many", "recursive": true, "cursor": cursor}))
		if e != nil {
			t.Fatalf("page %d: %v", pages, e)
		}
		ps := paths(t, out)
		if len(ps) > 500 {
			t.Fatalf("a page of %d", len(ps))
		}
		for _, p := range ps {
			if seen[p] || last != "" && !filesBefore(strings.TrimSuffix(last, "/"), strings.TrimSuffix(p, "/")) {
				t.Fatalf("%s again or out of order after %s", p, last)
			}
			seen[p], last = true, p
		}
		pages++
		c, _ := out["cursor"].(string)
		if c == "" {
			break
		}
		cursor = c
	}
	if pages != 2 || len(seen) != len(want) {
		t.Fatalf("%d pages, %d of %d entries", pages, len(seen), len(want))
	}
	for _, c := range []string{"elsewhere/x", "../x", "src/main.go"} {
		if w := pageFilesCall(h, id, map[string]any{"t": tok, "op": "list", "path": "many", "recursive": true, "cursor": c}); w.Code != 400 {
			t.Errorf("cursor %s: %d", c, w.Code)
		}
	}
}

// A read gives the bytes asked for, with the file's size and revision; a
// download's stat and save give the file whole, and not after it changed.
func TestArtifactFilesReads(t *testing.T) {
	h, id, tok, dir := filesPage(t)
	w := pageFilesCall(h, id, map[string]any{"t": tok, "op": "read", "path": "README.md", "offset": 2, "length": 4})
	if w.Code != 200 || w.Body.String() != "Plan" || w.Header().Get("X-Magpie-Size") != "12" || w.Header().Get("X-Magpie-Revision") == "" {
		t.Fatalf("read: %d %q %v", w.Code, w.Body.String(), w.Header())
	}
	if w := pageFilesCall(h, id, map[string]any{"t": tok, "op": "read", "path": "README.md", "offset": 99}); w.Code != 200 || w.Body.Len() != 0 {
		t.Fatalf("read past the end: %d %q", w.Code, w.Body.String())
	}
	out, e := filesOut(t, pageFilesCall(h, id, map[string]any{"t": tok, "op": "stat", "path": "src/main.go"}))
	rev, _ := out["revision"].(string)
	if e != nil || out["name"] != "main.go" || out["size"] != float64(12) || rev == "" {
		t.Fatalf("stat: %v %v", out, e)
	}
	w = pageFilesCall(h, id, map[string]any{"t": tok, "op": "save", "path": "src/main.go", "revision": rev})
	if w.Code != 200 || w.Body.String() != "package main" || w.Header().Get("Content-Disposition") != "attachment; filename*=UTF-8''main.go" {
		t.Fatalf("save: %d %q %v", w.Code, w.Body.String(), w.Header())
	}
	os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package main // changed"), 0o644)
	later := time.Now().Add(time.Second)
	os.Chtimes(filepath.Join(dir, "src", "main.go"), later, later)
	if _, e := filesOut(t, pageFilesCall(h, id, map[string]any{"t": tok, "op": "save", "path": "src/main.go", "revision": rev})); e == nil || e["code"] != "changed" {
		t.Fatalf("saved a changed file: %v", e)
	}
	for _, c := range []struct {
		why  string
		body map[string]any
		code int
		ec   string
	}{
		{"a folder", map[string]any{"op": "read", "path": "src"}, 400, "bad_request"},
		{"no file", map[string]any{"op": "read", "path": "gone.txt"}, 404, "not_found"},
		{"up and out", map[string]any{"op": "read", "path": "../x"}, 400, "bad_request"},
		{"absolute", map[string]any{"op": "read", "path": "/etc/hosts"}, 400, "bad_request"},
		{"backslash", map[string]any{"op": "read", "path": `src\main.go`}, 400, "bad_request"},
		{".git", map[string]any{"op": "read", "path": ".git/config"}, 400, "bad_request"},
		{"too long a read", map[string]any{"op": "read", "path": "README.md", "length": 25<<20 + 1}, 400, "bad_request"},
		{"a negative offset", map[string]any{"op": "read", "path": "README.md", "offset": -1}, 400, "bad_request"},
		{"a script saved", map[string]any{"op": "save", "path": "run.sh"}, 400, "bad_request"},
		{"a script asked about", map[string]any{"op": "stat", "path": "RUN.SH"}, 400, "bad_request"},
		{"the folder saved", map[string]any{"op": "save", "path": ""}, 400, "bad_request"},
		{"an op that isn't one", map[string]any{"op": "write", "path": "README.md"}, 400, "bad_request"},
	} {
		c.body["t"] = tok
		w := pageFilesCall(h, id, c.body)
		if _, e := filesOut(t, w); w.Code != c.code || e == nil || e["code"] != c.ec {
			t.Errorf("%s: %d %v", c.why, w.Code, e)
		}
	}
}

// A link inside the folder that leads out of it reads nothing.
func TestArtifactFilesStaysInTheFolder(t *testing.T) {
	h, id, tok, dir := filesPage(t)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	os.WriteFile(outside, []byte("secret"), 0o644)
	if err := os.Symlink(outside, filepath.Join(dir, "link.txt")); err != nil {
		t.Skip("no symlinks here:", err)
	}
	os.Symlink(filepath.Dir(outside), filepath.Join(dir, "linkdir"))
	for _, p := range []string{"link.txt", "linkdir/secret.txt"} {
		w := pageFilesCall(h, id, map[string]any{"t": tok, "op": "read", "path": p})
		if w.Code == 200 || w.Body.String() == "secret" {
			t.Errorf("%s read: %d %q", p, w.Code, w.Body.String())
		}
	}
	if w := pageFilesCall(h, id, map[string]any{"t": tok, "op": "list", "path": "linkdir"}); w.Code == 200 {
		t.Errorf("listed a folder outside: %q", w.Body.String())
	}
}

// .git is never read or listed however its name is spelled: in another
// case, which macOS's and Windows' folders open as .git, with the dots
// and spaces Windows drops from a name, by its short name, or through a
// link in the folder that leads into it.
func TestArtifactFilesNoGitAnySpelling(t *testing.T) {
	h, id, tok, dir := filesPage(t)
	for _, p := range []string{".GIT", ".Git/config", ".git./config", ".git /config", "GIT~1/config", "src/.GIT"} {
		for _, op := range []string{"read", "list", "stat"} {
			w := pageFilesCall(h, id, map[string]any{"t": tok, "op": op, "path": p})
			if w.Code == 200 || strings.Contains(w.Body.String(), "[core]") || strings.Contains(w.Body.String(), "config") {
				t.Errorf("%s %s: %d %q", op, p, w.Code, w.Body.String())
			}
		}
	}
	if err := os.Symlink(filepath.Join(dir, ".git"), filepath.Join(dir, "meta")); err != nil {
		t.Skip("no symlinks here:", err)
	}
	os.Symlink(filepath.Join(dir, ".git", "config"), filepath.Join(dir, "src", "conf"))
	for _, c := range []struct{ op, p string }{{"read", "meta/config"}, {"list", "meta"}, {"read", "src/conf"}, {"stat", "src/conf"}} {
		w := pageFilesCall(h, id, map[string]any{"t": tok, "op": c.op, "path": c.p})
		if w.Code == 200 || strings.Contains(w.Body.String(), "[core]") {
			t.Errorf("%s %s through a link: %d %q", c.op, c.p, w.Code, w.Body.String())
		}
	}
	// the folder itself still lists, and a file beside the link still reads
	if w := pageFilesCall(h, id, map[string]any{"t": tok, "op": "read", "path": "README.md"}); w.Code != 200 {
		t.Errorf("README.md: %d %q", w.Code, w.Body.String())
	}
	if w := pageFilesCall(h, id, map[string]any{"t": tok, "op": "list", "path": "", "recursive": true}); w.Code != 200 || strings.Contains(w.Body.String(), "config") {
		t.Errorf("list: %d %q", w.Code, w.Body.String())
	}
}

// A script is never saved by a name Windows would open as the script.
func TestArtifactFilesNoScriptByTrailingDot(t *testing.T) {
	h, id, tok, _ := filesPage(t)
	for _, p := range []string{"run.sh.", "run.sh ", "run.sh. ."} {
		w := pageFilesCall(h, id, map[string]any{"t": tok, "op": "stat", "path": p})
		if _, e := filesOut(t, w); w.Code != 400 || e == nil || e["code"] != "bad_request" {
			t.Errorf("stat %q: %d %q", p, w.Code, w.Body.String())
		}
	}
}

// Nothing is read but by the shell on this computer, for a version that
// declares files, and no more often than a page needs.
func TestArtifactFilesRefuses(t *testing.T) {
	h, id, tok, _ := filesPage(t)
	plain := publishFor(t, h, map[string]any{"tts": map[string]any{}})
	pageTok := tokenRe.FindStringSubmatch(pageGet(h, ArtifactPagePath+id+"/", "Sec-Fetch-Dest", "iframe").Body.String())
	if pageTok == nil {
		t.Fatal("no page token")
	}
	read := func(t string) map[string]any {
		return map[string]any{"t": t, "op": "read", "path": "README.md"}
	}
	for _, c := range []struct {
		why  string
		ref  string
		body map[string]any
		hdr  []string
		code int
	}{
		{"the page's own token", id, read(pageTok[1]), nil, 403},
		{"no token", id, read(""), nil, 403},
		{"another artifact's shell", plain, read(tok), nil, 403},
		{"a version without files", plain, read(shellTokenOf(t, h, plain)), nil, 403},
		{"a version that isn't there", id, map[string]any{"t": tok, "op": "read", "path": "README.md", "version": 9}, nil, 403},
		{"another computer", id, read(tok), []string{"RemoteAddr", "192.168.1.9:50001"}, 403},
		{"a rebound name", id, read(tok), []string{"Host", "evil.example:3425"}, 403},
	} {
		w := pageFilesCall(h, c.ref, c.body, c.hdr...)
		if w.Code != c.code || strings.Contains(w.Body.String(), "Plan") {
			t.Errorf("%s: %d %q", c.why, w.Code, w.Body.String())
		}
	}

	was := artifactFilesCalls
	artifactFilesCalls = newMinuteLimit(2)
	t.Cleanup(func() { artifactFilesCalls = was })
	for i := 1; i <= 3; i++ {
		w := pageFilesCall(h, id, read(tok))
		if i <= 2 && w.Code != 200 {
			t.Fatalf("call %d: %d", i, w.Code)
		}
		if i == 3 {
			if _, e := filesOut(t, w); w.Code != 429 || e["code"] != "rate_limited" || e["retryAfterMs"] == nil {
				t.Fatalf("call 3: %d %v", w.Code, e)
			}
		}
	}
	// where isn't counted: the shell asks it before the viewer says yes
	if w := pageFilesCall(h, id, map[string]any{"t": tok, "op": "where"}); w.Code != 200 {
		t.Fatalf("where, limited: %d", w.Code)
	}
}

// An agent can't publish files on the home folder, nor without a folder.
func TestArtifactFilesPublishRefused(t *testing.T) {
	h := artifacts(t)
	home, _ := os.UserHomeDir()
	for _, caps := range []map[string]any{
		{"files": map[string]any{}},
		{"files": map[string]any{"project": home}},
		{"files": map[string]any{"project": filepath.Join(home, "not-there")}},
	} {
		said, isErr := artifactsCall(t, h, artifactPublishTool, map[string]any{"html": "<!doctype html><p>x", "capabilities": caps})
		if !isErr || !strings.Contains(said, "files") {
			t.Errorf("%v: %v %s", caps, isErr, said)
		}
	}
}
