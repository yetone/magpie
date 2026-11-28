package gateway

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// skillsGetAt is a GET of path from addr, with hdr's pairs as headers.
func skillsGetAt(h http.Handler, path, addr, host string, hdr ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.RemoteAddr, r.Host = addr, host
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// untar is a .tar.gz's files by name, with their modes.
func untar(t *testing.T, b []byte) (map[string]string, map[string]int64) {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	files, modes := map[string]string{}, map[string]int64{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		c, _ := io.ReadAll(tr)
		files[h.Name], modes[h.Name] = string(c), h.Mode
	}
	return files, modes
}

// The pull script and the skills it pulls go to another computer with a
// gateway key of this magpie, and nothing else, as get_skill does: not
// without a key (no Origin, a null one, a foreign site, a rebound Host,
// through a proxy here), not to a page or a browser with the key, another
// magpie's router, this computer, or a held key. The router's switch
// isn't theirs: they are given with it off.
func TestSkillsPullOnlyWithAKeyFromThere(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	t.Setenv("MAGPIE_TRUST_PROXY", "")
	f := newSkillsHub()
	f.on = false
	withHub(t, f)
	h := lanGuard(New().Handler())
	const there = "192.0.2.7:4000"
	const host = "nas.local:3425"
	given := func(w *httptest.ResponseRecorder) bool {
		return w.Code == http.StatusOK || strings.Contains(w.Body.String(), "Run scripts/ship.sh") || strings.Contains(w.Body.String(), "magpie-skills-pull")
	}
	paths := []string{SkillsArchivePath, SkillsPullPath}
	for _, p := range paths {
		if w := skillsGetAt(h, p, there, host); given(w) {
			t.Errorf("%s not shared: %d", p, w.Code)
		}
	}
	keys, secrets := newCaller(t, "Laptop", "Held", "Narrow")
	key := []string{"Authorization", "Bearer " + secrets[0]}
	for _, p := range paths {
		refused := func(what string, w *httptest.ResponseRecorder) {
			t.Helper()
			if given(w) || w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
				t.Errorf("%s %s: %d %.200s", p, what, w.Code, w.Body)
			}
		}
		refused("no key, no Origin", skillsGetAt(h, p, there, host))
		refused("a wrong key", skillsGetAt(h, p, there, host, "Authorization", "Bearer sk-magpie-wrong"))
		for _, origin := range []string{"null", "https://evil.example", "chrome-extension://abcdefghijklmnop", "file://"} {
			hdr := []string{"Origin", origin, "Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "cors"}
			refused("no key, Origin "+origin, skillsGetAt(h, p, there, host, hdr...))
			refused("Origin "+origin+" from here", skillsGetAt(h, p, here, "127.0.0.1:3425", hdr...))
			refused("Origin "+origin+" with the key", skillsGetAt(h, p, there, host, append(hdr, key...)...))
		}
		for _, addr := range []string{here, there} {
			for _, marks := range [][]string{
				{"Origin", "http://rebind.attacker.example:3425", "Sec-Fetch-Site", "same-origin"},
				{"Sec-Fetch-Site", "same-origin"},
				{"User-Agent", "Mozilla/5.0 (Macintosh) AppleWebKit/605.1.15 Safari/605.1.15"},
				{},
			} {
				refused("a rebound Host from "+addr+" "+strings.Join(marks, " "), skillsGetAt(h, p, addr, "rebind.attacker.example:3425", marks...))
			}
		}
		refused("through a proxy here, no key", skillsGetAt(h, p, here, "127.0.0.1:3425", "X-Forwarded-For", "203.0.113.9"))
		refused("this computer", skillsGetAt(h, p, here, "127.0.0.1:3425"))
		refused("this computer with a key", skillsGetAt(h, p, here, "127.0.0.1:3425", key...))
		refused("a page with the key", skillsGetAt(h, p, there, host, append([]string{"Origin", "http://192.0.2.7:3430", "Sec-Fetch-Site", "same-origin"}, key...)...))
		refused("a browser with the key", skillsGetAt(h, p, there, host, append([]string{"User-Agent", "Mozilla/5.0"}, key...)...))
		refused("another magpie's router", skillsGetAt(h, p, there, host, append([]string{RouterHeader, "1"}, key...)...))
	}
	if _, err := access.Update("models-key", access.Change{Key: keys[1].ID, Models: []string{"openai/gpt-5"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := access.Update("mcp-key", access.Change{Key: keys[2].ID, MCP: []string{"notes"}}); err != nil {
		t.Fatal(err)
	}
	for i, who := range []string{"a key held to models", "a key held to some MCP servers"} {
		w := skillsGetAt(h, SkillsArchivePath, there, host, "Authorization", "Bearer "+secrets[i+1])
		if given(w) || w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "is held to") {
			t.Errorf("%s: %d %.200s", who, w.Code, w.Body)
		}
	}
	// the control: the key from there, the router off, and through a
	// proxy here
	for _, addr := range []string{there, here} {
		hdr := key
		if addr == here {
			hdr = append([]string{"X-Forwarded-For", "203.0.113.9"}, key...)
		}
		w := skillsGetAt(h, SkillsArchivePath, addr, host, hdr...)
		if w.Code != http.StatusOK {
			t.Fatalf("not given from %s: %d %s", addr, w.Code, w.Body)
		}
		if fs, _ := untar(t, w.Body.Bytes()); fs["deploy/SKILL.md"] == "" {
			t.Fatalf("no deploy from %s: %v", addr, fs)
		}
		w = skillsGetAt(h, SkillsPullPath, addr, host, hdr...)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "base='http://nas.local:3425'") {
			t.Fatalf("no script from %s: %d %.300s", addr, w.Code, w.Body)
		}
	}
	// a Host a shell's quotes would end on isn't written into it
	if w := skillsGetAt(h, SkillsPullPath, there, "nas.local:3425'$(id)", key...); w.Code == http.StatusOK && strings.Contains(w.Body.String(), "$(id)") {
		t.Fatalf("the Host went into the script: %.300s", w.Body)
	}
	// a Library it can't read is an error, never no skills: the script
	// would take that as every skill gone
	f.err = errors.New("library.json: unexpected end of JSON input")
	if w := skillsGetAt(h, SkillsArchivePath, there, host, key...); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("a Library it couldn't read: %d %s", w.Code, w.Body)
	}
}

// The archive holds each skill's files by its folder, those that run
// still running, beside the list of every skill; one past its size is
// named, not sent, and a path out of its folder is never written.
func TestSkillsArchive(t *testing.T) {
	f := newSkillsHub()
	f.files["release-notes"] = RemoteSkillFiles{Files: map[string][]byte{
		"SKILL.md":       []byte("---\nname: release-notes\n---\nNotes.\n"),
		"../escape.sh":   []byte("x"),
		"/etc/passwd":    []byte("x"),
		"a/../../b":      []byte("x"),
		".magpie-pulled": []byte("from=http://elsewhere\n"),
	}}
	ks, _ := f.RemoteSkills("")
	b, left, err := skillsTar(f, ks)
	if err != nil || len(left) != 0 {
		t.Fatal(err, left)
	}
	fs, modes := untar(t, b)
	if fs[skillsListFile] != "deploy\nrelease-notes\nwipe\n" {
		t.Fatalf("list: %q", fs[skillsListFile])
	}
	if fs["deploy/scripts/ship.sh"] != "#!/bin/sh\necho shipped\n" || modes["deploy/scripts/ship.sh"] != 0o755 || modes["deploy/SKILL.md"] != 0o644 {
		t.Fatalf("deploy: %v %v", fs["deploy/scripts/ship.sh"], modes)
	}
	if fs["deploy/assets/logo.png"] != "\x89PNG\x00\x00" || len(fs["deploy/docs/big.txt"]) != hubSkillFileMax+500 {
		t.Fatal("a file wasn't sent whole")
	}
	for n := range fs {
		if n != skillsListFile && !strings.HasPrefix(n, "deploy/") && !strings.HasPrefix(n, "release-notes/") || strings.Contains(n, "..") || strings.HasSuffix(n, ".magpie-pulled") {
			t.Errorf("written: %s", n)
		}
	}
	old := skillsArchiveMax
	skillsArchiveMax = 1000
	t.Cleanup(func() { skillsArchiveMax = old })
	b, left, err = skillsTar(f, ks)
	if err != nil || !slices.Equal(left, []string{"deploy"}) {
		t.Fatal(err, left)
	}
	if fs, _ := untar(t, b); fs["deploy/SKILL.md"] != "" || fs["release-notes/SKILL.md"] == "" || !strings.Contains(fs[skillsListFile], "deploy\n") {
		t.Fatalf("past the size: %v", fs)
	}
	// a skill listed that can't be read now is an error, not left out
	ks = append(ks, RemoteSkill{Name: "vanished"})
	if _, _, err := skillsTar(f, ks); err == nil {
		t.Fatal("a skill it couldn't read was left out")
	}
}

// pull.sh, run for real with sh, curl and tar against a magpie's archive:
// it pulls every skill into ~/.agents/skills and Claude Code's folder,
// keeps a folder of the user's own and one the user changed after it was
// pulled, follows a skill changed in the Library, removes one gone from
// it only as it was pulled, keeps one too big to send this time, and
// changes nothing when the magpie fails or sends no list.
func TestSkillsPullScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pull.sh is for macOS and Linux (Git Bash and WSL run it on Windows)")
	}
	for _, c := range []string{"sh", "curl", "tar", "cksum"} {
		if _, err := exec.LookPath(c); err != nil {
			t.Skip("no " + c)
		}
	}
	f := newSkillsHub()
	f.files["release-notes"] = RemoteSkillFiles{Files: map[string][]byte{"SKILL.md": []byte("---\nname: release-notes\n---\nNotes v1.\n")}}
	failing, bare := false, false
	var mu sync.Mutex // the Library's state, read by the server's goroutine
	change := func(fn func()) {
		mu.Lock()
		defer mu.Unlock()
		fn()
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer sk-magpie-test" || r.URL.Path != SkillsArchivePath {
			http.Error(w, "no", http.StatusForbidden)
			return
		}
		if failing {
			writeError(w, provider.Chat, http.StatusServiceUnavailable, "magpie couldn't read its Library: disk")
			return
		}
		if bare { // an archive with no list of the skills
			gz := gzip.NewWriter(w)
			tar.NewWriter(gz).Close()
			gz.Close()
			return
		}
		ks, _ := f.RemoteSkills("")
		b, left, err := skillsTar(f, ks)
		if err != nil {
			t.Error(err)
		}
		if len(left) > 0 {
			w.Header().Set("X-Magpie-Skills-Left", strings.Join(left, ","))
		}
		w.Write(b)
	}))
	defer srv.Close()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	agents, claude := filepath.Join(home, ".agents/skills"), filepath.Join(home, ".claude/skills")
	write := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	read := func(p string) string {
		b, _ := os.ReadFile(p)
		return string(b)
	}
	// the user's own release-notes, before any pull
	write(filepath.Join(agents, "release-notes/SKILL.md"), "mine\n")
	script := strings.ReplaceAll(skillsPullScript, "@BASE@", srv.URL)
	pull := func(env ...string) (string, error) {
		t.Helper()
		cmd := exec.Command("sh", "-s")
		cmd.Stdin = strings.NewReader(script)
		cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "TMPDIR=" + t.TempDir(), "MAGPIE_KEY=sk-magpie-test"}, env...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := pull()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, d := range []string{agents, claude} {
		if read(filepath.Join(d, "deploy/SKILL.md")) == "" || !strings.Contains(read(filepath.Join(d, "deploy/.magpie-pulled")), "from="+srv.URL) {
			t.Fatalf("deploy not pulled into %s:\n%s", d, out)
		}
		st, err := os.Stat(filepath.Join(d, "deploy/scripts/ship.sh"))
		if err != nil || st.Mode()&0o111 == 0 {
			t.Fatalf("ship.sh doesn't run in %s: %v", d, err)
		}
	}
	if read(filepath.Join(agents, "release-notes/SKILL.md")) != "mine\n" || !strings.Contains(out, "yours, not pulled") {
		t.Fatalf("the user's own release-notes written over:\n%s", out)
	}
	if read(filepath.Join(claude, "release-notes/SKILL.md")) != "---\nname: release-notes\n---\nNotes v1.\n" {
		t.Fatalf("release-notes not pulled into Claude Code's folder:\n%s", out)
	}
	// the user edits a pulled skill, then the Library changes it: the
	// edit stays, and the copy as it was pulled follows the Library
	write(filepath.Join(claude, "deploy/SKILL.md"), "my own steps\n")
	change(func() {
		f.files["deploy"].Files["SKILL.md"] = []byte("---\nname: deploy\ndescription: Ships the site to production\n---\nRun scripts/ship.sh twice.\n")
	})
	if out, err = pull(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if read(filepath.Join(claude, "deploy/SKILL.md")) != "my own steps\n" || !strings.Contains(out, "changed here since it was pulled") {
		t.Fatalf("the user's edit written over:\n%s", out)
	}
	if !strings.Contains(read(filepath.Join(agents, "deploy/SKILL.md")), "twice") {
		t.Fatalf("the copy as pulled didn't follow the Library:\n%s", out)
	}
	// a pull with nothing new changes nothing
	if out, err = pull(); err != nil || !strings.Contains(out, "0 pulled, 0 removed") {
		t.Fatalf("%v\n%s", err, out)
	}
	// the magpie fails: nothing is changed
	change(func() { failing = true })
	if out, err = pull(); err == nil || read(filepath.Join(agents, "deploy/SKILL.md")) == "" || !strings.Contains(out, "answered 503: magpie couldn't read its Library: disk; nothing was changed") {
		t.Fatalf("a failed pull: %v\n%s", err, out)
	}
	change(func() { failing = false })
	// an archive with no list: nothing is taken as gone
	change(func() { bare = true })
	if out, err = pull(); err == nil || read(filepath.Join(claude, "deploy/SKILL.md")) == "" || read(filepath.Join(agents, "deploy/SKILL.md")) == "" || !strings.Contains(out, "sent no list") {
		t.Fatalf("an archive with no list: %v\n%s", err, out)
	}
	change(func() { bare = false })
	// no key: it says so, and changes nothing
	if out, err := pull("MAGPIE_KEY="); err == nil || !strings.Contains(out, "set MAGPIE_KEY") {
		t.Fatalf("no key: %v\n%s", err, out)
	}
	// release-notes is gone from the Library and deploy too big to send:
	// the copy of release-notes as pulled is removed, deploy is kept
	old := skillsArchiveMax
	change(func() {
		f.skills[""] = slices.DeleteFunc(f.skills[""], func(k RemoteSkill) bool { return k.Name == "release-notes" })
		skillsArchiveMax = 1000
	})
	t.Cleanup(func() { skillsArchiveMax = old })
	if out, err = pull(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(claude, "release-notes")); !os.IsNotExist(err) {
		t.Fatalf("release-notes, gone from the Library, kept:\n%s", out)
	}
	if read(filepath.Join(agents, "release-notes/SKILL.md")) != "mine\n" {
		t.Fatalf("the user's own release-notes removed:\n%s", out)
	}
	if read(filepath.Join(agents, "deploy/SKILL.md")) == "" || !strings.Contains(out, "too big to pull: deploy") {
		t.Fatalf("deploy, too big this time, removed:\n%s", out)
	}
	// one pulled from another magpie is that one's
	change(func() { skillsArchiveMax = old })
	write(filepath.Join(agents, "wipe/SKILL.md"), "from the NAS\n")
	write(filepath.Join(agents, "wipe/.magpie-pulled"), "from=http://nas.local:3425\nsum=1 1\n")
	if out, err = pull(); err != nil || read(filepath.Join(agents, "wipe/SKILL.md")) != "from the NAS\n" || !strings.Contains(out, "pulled from http://nas.local:3425") {
		t.Fatalf("another magpie's skill: %v\n%s", err, out)
	}
	// MAGPIE_SKILLS_DIRS says where
	other := filepath.Join(home, "elsewhere")
	if out, err = pull("MAGPIE_SKILLS_DIRS=" + other); err != nil || read(filepath.Join(other, "deploy/SKILL.md")) == "" {
		t.Fatalf("MAGPIE_SKILLS_DIRS: %v\n%s", err, out)
	}
	// deploy, edited there, is gone from the Library: the edit is kept
	write(filepath.Join(other, "deploy/notes.md"), "mine\n")
	change(func() {
		f.skills[""] = slices.DeleteFunc(f.skills[""], func(k RemoteSkill) bool { return k.Name == "deploy" })
	})
	if out, err = pull("MAGPIE_SKILLS_DIRS=" + other); err != nil || read(filepath.Join(other, "deploy/notes.md")) != "mine\n" || !strings.Contains(out, "gone from magpie's Library, but changed here") {
		t.Fatalf("an edited skill gone from the Library: %v\n%s", err, out)
	}
	// and one as it was pulled is removed
	if out, err = pull(); err != nil || !strings.Contains(out, "removed "+filepath.Join(agents, "deploy")) {
		t.Fatalf("deploy, gone from the Library, kept: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(agents, "deploy")); !os.IsNotExist(err) {
		t.Fatalf("deploy, gone from the Library, kept:\n%s", out)
	}
}
