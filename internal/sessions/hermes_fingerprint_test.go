package sessions

import (
	"database/sql"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// A write to one Hermes session moves the whole store's revision. Only that
// session is parsed again (#2776): every session was, each poll while
// Hermes ran. A message rewritten in place, with no row added, still is.
func TestHermesWriteParsesOnlyItsSession(t *testing.T) {
	setup(t)
	root := filepath.Join(t.TempDir(), "hermes")
	t.Setenv("HERMES_HOME", root)
	path := hermesFixture(t, root, "", "busy", true)
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	base := float64(time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC).Unix())
	for _, sid := range []string{"idle1", "idle2", "idle3"} {
		if _, err := db.Exec(`INSERT INTO sessions (id, title, started_at) VALUES (?, NULL, ?)`, sid, base); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO messages (session_id, role, content, tool_calls, timestamp) VALUES (?, 'user', 'first words', '[]', ?)`, sid, base+1); err != nil {
			t.Fatal(err)
		}
	}
	var parsed atomic.Int32
	parseHook = func() { parsed.Add(1) }
	t.Cleanup(func() { parseHook = nil })

	List(0)
	parsed.Store(0)
	List(0)
	if n := parsed.Load(); n != 0 {
		t.Fatalf("nothing changed, yet %d parsed again", n)
	}

	if _, err := db.Exec(`INSERT INTO messages (session_id, role, content, tool_calls, timestamp) VALUES ('busy', 'user', 'one more', '[]', ?)`, base+20); err != nil {
		t.Fatal(err)
	}
	List(0)
	if n := parsed.Load(); n != 1 {
		t.Fatalf("one session written, %d parsed again", n)
	}

	// in place: the row count, ids and times all stay
	parsed.Store(0)
	if _, err := db.Exec(`UPDATE messages SET content = 'rewritten first words' WHERE session_id = 'idle2'`); err != nil {
		t.Fatal(err)
	}
	if got := hermesSessionByID(t, hermesID(path, "idle2")).Title; got != "rewritten first words" {
		t.Errorf("in-place rewrite not read: title %q", got)
	}
	if n := parsed.Load(); n != 1 {
		t.Fatalf("one message rewritten in place, %d parsed again", n)
	}
}

// Fingerprints that can't be read are unknown: no session's parse is kept.
func TestHermesUnreadFingerprintsParseAgain(t *testing.T) {
	setup(t)
	root := filepath.Join(t.TempDir(), "hermes")
	path := hermesFixture(t, root, "", "s", true)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	h := newHermesDB(db)
	h.revision = hermesRevision(path)
	if p := hermesPrintsFor(path, h); p["s"] == "" {
		t.Fatalf("fingerprints of a readable store: %v", p)
	}
	db.Close()
	h.revision = "moved"
	if p := hermesPrintsFor(path, h); p != nil {
		t.Fatalf("a closed store's fingerprints read as %v", p)
	}
	// the same size and time as what was parsed: only the fingerprint can
	// tell it changed, and it is unknown
	f := file{agent: "hermes", path: path + "#s", more: &fileMore{hermes: h}, mod: modOf(time.Unix(1790000000, 0))}
	s := state{Mod: f.mod.UnixNano()}
	cache = map[string]*state{f.path: &s}
	t.Cleanup(func() { cache = nil })
	parsed := 0
	parseHook = func() { parsed++ }
	t.Cleanup(func() { parseHook = nil })
	mu.Lock()
	refresh([]file{f}, nil)
	mu.Unlock()
	if parsed != 1 {
		t.Fatalf("an unknown fingerprint reused a parse (%d parsed)", parsed)
	}
}

// A store's path is resolved (EvalSymlinks walks every folder in it) once
// per listing, not once per session: 2000 sessions spent ~30ms on it per
// poll (#2776).
func TestHermesStoreResolvedOncePerListing(t *testing.T) {
	setup(t)
	root := filepath.Join(t.TempDir(), "hermes")
	t.Setenv("HERMES_HOME", root)
	path := hermesFixture(t, root, "", "s0", true)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := 1; i < 10; i++ {
		sid := "s" + string(rune('0'+i))
		if _, err := db.Exec(`INSERT INTO sessions (id, title, started_at) VALUES (?, 'x', 1790000000)`, sid); err != nil {
			t.Fatal(err)
		}
	}
	var n atomic.Int32
	canonicalHook = func() { n.Add(1) }
	t.Cleanup(func() { canonicalHook = nil })
	mu.Lock()
	files := hermesFiles()
	closeDBs()
	mu.Unlock()
	if len(files) != 10 {
		t.Fatalf("%d files, want 10", len(files))
	}
	if got := int(n.Load()); got >= len(files) {
		t.Fatalf("the store's path was resolved %d times for %d sessions", got, len(files))
	}
	if want := "hermes:" + hermesID(path, files[0].m().sid); files[0].key != want {
		t.Fatalf("key %q, want %q", files[0].key, want)
	}
}
