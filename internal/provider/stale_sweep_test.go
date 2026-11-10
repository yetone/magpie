package provider

import (
	"path/filepath"
	"testing"
	"time"
)

// What the last request held and kept goes once its time is past, with no
// request after it: the catalog was dropped only when the next one looked,
// so a magpie nobody asked again kept the last catalog for good.
func TestHeldAndKeptGoWithNoRequestAfter(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	defer func(d, k time.Duration) { heldFor, keptFor = d, k }(heldFor, keptFor)
	heldFor, keptFor = 50*time.Millisecond, 100*time.Millisecond
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	func() {
		defer Hold()()
		if _, _, ok := Resolve("relay/m1"); !ok {
			t.Fatal("relay/m1 not resolved")
		}
	}()
	left := func() (int, int) {
		held.Lock()
		h := len(held.built)
		held.Unlock()
		kept.Lock()
		defer kept.Unlock()
		return h, len(kept.of)
	}
	if h, k := left(); h == 0 || k == 0 {
		t.Fatalf("after a request: %d held, %d kept; the test looks at nothing", h, k)
	}
	deadline := time.Now().Add(10 * time.Second) // room for a loaded machine
	for {
		h, k := left()
		if h == 0 && k == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("10s after the last request, %d held and %d kept, past %v and %v", h, k, heldFor, keptFor)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// and a request after the let-go resolves as before
	func() {
		defer Hold()()
		if _, _, ok := Resolve("relay/m1"); !ok {
			t.Fatal("relay/m1 not resolved after the let-go")
		}
	}()
}
