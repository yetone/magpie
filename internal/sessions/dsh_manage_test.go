package sessions

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

// dsh 0.2's own layout (dsh-session-persistence-jsonl, dsh-base's
// cordis.patch.yml; as dsh 0.2.0-rc.2's `dsh headless` wrote it): $DSH_HOME/sessions/--<cwd>--/<escaped id>/ holds a
// session's generations, zstd frames by default (the header's, then one a
// batch), and its session.lock; a subagent's session is a folder of its own
// whose header names its parent; the listing's cache keeps a row a session
// at storages/session_projcache/sessions/<id>.json.
const (
	dshP     = "session-4f1c2a9e-6b3d-4e8a-9c71-2d5e8f0a1b3c" // the session deleted
	dshC     = "session-a7e2c4d1-0f9b-4c3e-8d26-5b1a9e7f3c40" // its subagent's
	dshO     = "session-c93b1e5f-2a4d-4b7c-9e18-6f0d3a2c5b97" // another, in the same project
	dshCwd   = "/Users/me/work/app"
	dshProjD = "--Users-me-work-app--"
)

func dshFrames(t *testing.T, lines ...string) []byte {
	t.Helper()
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderCRC(true))
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	// the header in a frame of its own, then the events a batch at a time
	out := enc.EncodeAll([]byte(lines[0]+"\n"), nil)
	return enc.EncodeAll([]byte(strings.Join(lines[1:], "\n")+"\n"), out)
}

func dshSession(t *testing.T, home, id, head, prompt string) string {
	t.Helper()
	dir := filepath.Join(home, "sessions", dshProjD, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b := dshFrames(t, head,
		`{"type":"user/message","seq":0,"time":1790503201000,"data":{"content":[{"type":"text","text":"`+prompt+`"}],"source":{"kind":"user"},"role":"user"}}`,
		`{"type":"assistant/message","seq":1,"time":1790503260000,"data":{"message":{"source":{"model":"deepseek-v4-pro"}},"usage":{"inputTokens":100,"outputTokens":20,"cacheReadTokens":1000}}}`)
	os.WriteFile(filepath.Join(dir, "session.v4.jsonl.zstd"), b, 0o600)
	os.WriteFile(filepath.Join(dir, "session.lock"), nil, 0o600)
	cache := filepath.Join(home, "storages", "session_projcache", "sessions")
	os.MkdirAll(cache, 0o700)
	os.WriteFile(filepath.Join(cache, id+".json"), []byte(`{"version":7,"record":{}}`), 0o600)
	return dir
}

func dshManageSetup(t *testing.T) (home string) {
	manageSetup(t)
	home = filepath.Join(t.TempDir(), "dsh")
	t.Setenv("DSH_HOME", home)
	p := dshSession(t, home, dshP, `{"type":"session","version":4,"id":"`+dshP+`","createdAt":1790503200000,"cwd":"`+dshCwd+`","isSeeded":false,"delegationDepth":0}`, "Tidy the build script")
	// the generation it was carried over from stays behind in its folder
	os.WriteFile(filepath.Join(p, "session.v3.jsonl.zstd"), dshFrames(t, `{"type":"session","version":3,"id":"`+dshP+`","createdAt":1790503200000,"cwd":"`+dshCwd+`","isSeeded":false,"delegationDepth":0}`, `{"type":"session/title","seq":0,"time":1790503200100,"data":{"title":"x"}}`), 0o600)
	dshSession(t, home, dshC, `{"type":"session","version":4,"id":"`+dshC+`","createdAt":1790503230000,"cwd":"`+dshCwd+`","isSeeded":false,"delegationDepth":1,"parentSession":"`+dshP+`","origin":"subagent"}`, "Find the flaky test")
	dshSession(t, home, dshO, `{"type":"session","version":4,"id":"`+dshO+`","createdAt":1790503300000,"cwd":"`+dshCwd+`","isSeeded":false,"delegationDepth":0}`, "Write the release notes")
	ageDsh(home)
	Reset()
	return home
}

func ageDsh(home string) {
	old := time.Now().Add(-24 * time.Hour)
	filepath.WalkDir(home, func(p string, d os.DirEntry, err error) error {
		if err == nil {
			os.Chtimes(p, old, old)
		}
		return nil
	})
}

// lc on Discord: "deepseek harness无法删除会话". A dsh session goes to the
// trash whole — its folder, its subagent's, their cached rows — and
// nothing of another session's goes with it.
func TestDshDeleteRestore(t *testing.T) {
	home := dshManageSetup(t)
	list := ListAgent("dsh")
	if len(list) != 2 {
		t.Fatalf("dsh sessions %d, want 2 (the subagent in its parent)", len(list))
	}
	m, ok := findManaged(list, dshP)
	if !ok || !m.Deletable || m.Files != 2 || m.Cwd != dshCwd {
		t.Fatalf("listed as %+v", m)
	}
	for _, a := range Agents() {
		if a.Agent == "dsh" && !a.Deletable {
			t.Fatal("dsh not deletable")
		}
	}
	proj := filepath.Join(home, "sessions", dshProjD)
	cache := filepath.Join(home, "storages", "session_projcache", "sessions")
	pDir, cDir, oDir := filepath.Join(proj, dshP), filepath.Join(proj, dshC), filepath.Join(proj, dshO)

	// one being written to is left alone
	now := time.Now()
	os.Chtimes(filepath.Join(cDir, "session.v4.jsonl.zstd"), now, now)
	if _, err := Delete("dsh", dshP); !errors.Is(err, ErrActive) {
		t.Fatalf("a session just written: %v", err)
	}
	ageDsh(home)

	tr, err := Delete("dsh", dshP)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Items) != 4 || tr.Cwd != dshCwd || tr.Title != "Tidy the build script" {
		t.Fatalf("trashed %+v, want its folder, its subagent's and their two cached rows", tr)
	}
	for _, p := range []string{pDir, cDir, filepath.Join(cache, dshP+".json"), filepath.Join(cache, dshC+".json")} {
		if _, err := os.Lstat(p); err == nil {
			t.Fatalf("%s is still there", p)
		}
	}
	// the other session, its row and the project folder are as they were
	for _, p := range []string{filepath.Join(oDir, "session.v4.jsonl.zstd"), filepath.Join(oDir, "session.lock"), filepath.Join(cache, dshO+".json")} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("another session's %s: %v", p, err)
		}
	}
	if l := ListAgent("dsh"); len(l) != 1 || l[0].ID != dshO {
		t.Fatalf("after the delete: %+v", l)
	}

	if _, err := Restore(tr.Key); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(pDir, "session.v4.jsonl.zstd"), filepath.Join(pDir, "session.v3.jsonl.zstd"), filepath.Join(pDir, "session.lock"), filepath.Join(cDir, "session.v4.jsonl.zstd"), filepath.Join(cache, dshP+".json"), filepath.Join(cache, dshC+".json")} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s isn't back: %v", p, err)
		}
	}
	Reset()
	if s, ok := findSession(List(0), "dsh", dshP); !ok || s.Title != "Tidy the build script" {
		t.Fatalf("the restored session: %+v", s)
	}
}

// A session file that isn't in the folder dsh names by its id is no folder
// of its own to take: nothing is moved.
func TestDshDeleteOnlyItsOwnFolder(t *testing.T) {
	home := dshManageSetup(t)
	proj := filepath.Join(home, "sessions", dshProjD)
	// the other session's file, in a folder named as dsh wouldn't
	odd := filepath.Join(proj, "shared")
	os.Rename(filepath.Join(proj, dshO), odd)
	os.WriteFile(filepath.Join(odd, "notes.txt"), []byte("someone's"), 0o600)
	ageDsh(home)
	Reset()
	if _, ok := findManaged(ListAgent("dsh"), dshO); !ok {
		t.Fatal("the session isn't listed")
	}
	if _, err := Delete("dsh", dshO); err == nil {
		t.Fatal("deleted a folder that isn't the session's")
	}
	if _, err := os.Stat(filepath.Join(odd, "notes.txt")); err != nil {
		t.Fatal("the folder was touched")
	}
	if len(Trash()) != 0 {
		t.Fatal("something went to the trash")
	}
}

func TestDshSegment(t *testing.T) {
	for in, want := range map[string]string{
		dshP:    dshP,
		"a~b":   "a~007Eb",
		"a/b":   "a~002Fb",
		".":     "~002E",
		"..":    "~002E~002E",
		"x.y":   "x.y",
		"会话":    "~4F1A~8BDD",
		"😀":     "~D83D~DE00",
		"a b_c": "a~0020b_c",
	} {
		if got := dshSegment(in); got != want {
			t.Errorf("dshSegment(%q) = %q, want %q", in, got, want)
		}
	}
}
