package bridgesid

import (
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
	"time"
)

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func sandbox(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	return filepath.Join(dir, "config", "magpie")
}

// An id New makes is a version 4 UUID Claude Code takes, and Is knows it;
// a random UUID and junk it doesn't.
func TestNewIsKnown(t *testing.T) {
	sandbox(t)
	for range 50 {
		id := New()
		if !uuidV4.MatchString(id) {
			t.Fatalf("%q is not a version 4 UUID", id)
		}
		if !Is(id) {
			t.Fatalf("Is(%q) = false for an id New made", id)
		}
	}
	for _, no := range []string{
		"0b6f2c8e-3a41-4c2d-9e7f-5a1b2c3d4e5f", // a session of the user's
		"", "not-a-uuid", "0b6f2c8e3a414c2d9e7f5a1b2c3d4e5f",
		"0b6f2c8e-3a41-4c2d-9e7f-5a1b2c3d4e5g",
	} {
		if Is(no) {
			t.Errorf("Is(%q) = true", no)
		}
	}
	// one bit changed anywhere is no longer the bridge's
	id := []byte(New())
	id[0] ^= 1
	if Is(string(id)) {
		t.Errorf("Is(%q) = true with its first byte changed", id)
	}
}

// The key is kept in magpie's config folder: an id made before is known
// after a restart (the key read again), and another computer's (another
// key) is not.
func TestKeyKept(t *testing.T) {
	cfg := sandbox(t)
	id := New()
	b, err := os.ReadFile(filepath.Join(cfg, keyFile))
	if err != nil || len(b) != 65 {
		t.Fatalf("the key file: %q %v", b, err)
	}
	forget() // as a new process has no key
	if !Is(id) {
		t.Fatalf("an id made before the key was read again isn't known")
	}
	sandbox(t)
	if Is(id) {
		t.Fatalf("another config folder's key knew the id")
	}
	if New(); Is(id) {
		t.Fatalf("a new key knew the old one's id")
	}
}

// With no key, Is knows nothing and makes no key; New makes one. Two that
// make it at once end with the same key.
func TestKeyMadeOnce(t *testing.T) {
	cfg := sandbox(t)
	if Is("0b6f2c8e-3a41-4c2d-9e7f-5a1b2c3d4e5f") {
		t.Fatal("Is said yes with no key")
	}
	if _, err := os.Stat(filepath.Join(cfg, keyFile)); err == nil {
		t.Fatal("Is made a key")
	}
	keys := make([][]byte, 8)
	var wg sync.WaitGroup
	for i := range keys {
		wg.Go(func() { keys[i], _ = makeKey(filepath.Join(cfg, keyFile)) })
	}
	wg.Wait()
	for _, k := range keys {
		if len(k) != 32 || string(k) != string(keys[0]) {
			t.Fatalf("keys made at once differ: %x", keys)
		}
	}
	forget()
	if id := New(); !Is(id) {
		t.Fatalf("an id made with the shared key isn't known")
	}
}

// forget drops the key read, as a new process starts with none.
func forget() {
	key.Lock()
	key.dir, key.k, key.missing = "", nil, time.Time{}
	key.Unlock()
}
