package plugin

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/steady"
)

func TestHostScriptConcurrent(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	start := make(chan struct{})
	var writers sync.WaitGroup
	for range 16 {
		writers.Go(func() {
			<-start
			p, err := hostScript("host", hostJS)
			if err != nil {
				t.Errorf("write host script: %v", err)
				return
			}
			if b, err := steady.ReadThere(p); err != nil || !bytes.Equal(b, hostJS) {
				t.Errorf("read host script: got %d bytes, want %d, error %v", len(b), len(hostJS), err)
			}
		})
	}
	close(start)
	writers.Wait()

	p, err := hostScript("host", hostJS)
	if err != nil {
		t.Fatal(err)
	}
	assertOnlyHostScript(t, p)
}

func TestHostScriptIgnoresStaleTemp(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	p, err := hostScript("host", hostJS)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	// A directory at the old temp path makes it unusable on every OS.
	if err := os.Mkdir(p+".tmp", 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := hostScript("host", hostJS)
	if err != nil || got != p {
		t.Fatalf("write beside stale temp: got %q, %v, want %q", got, err, p)
	}
	if b, err := steady.ReadThere(p); err != nil || !bytes.Equal(b, hostJS) {
		t.Fatalf("read host script: got %d bytes, want %d, error %v", len(b), len(hostJS), err)
	}
}

func assertOnlyHostScript(t *testing.T, p string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(p))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(p) {
		t.Fatalf("cache holds %v, want only %s", entries, filepath.Base(p))
	}
}
