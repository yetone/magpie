package plugin

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The Bun magpie downloads is kept as magpie-bun, the name a proxy app's
// PROCESS-NAME rule sees (#1048).
func TestBunDownloadedAsMagpieBun(t *testing.T) {
	bunHome(t, BunVersion, time.Now())
	asked := bunReleases(t, []byte("bun"))
	exe, err := Bun(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(exe) != magpieBun() || asked.Load() != 1 {
		t.Fatalf("Bun = %s after %d downloads, want one named %s", exe, asked.Load(), magpieBun())
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(exe), bunExe())); !os.IsNotExist(err) {
		t.Fatalf("a bun beside magpie-bun: %v", err)
	}
}

// A Bun downloaded before is renamed magpie-bun, not downloaded again.
func TestBunDownloadedBeforeTakesTheName(t *testing.T) {
	bunHome(t, BunVersion, time.Now())
	asked := bunReleases(t, []byte("new download"))
	old := filepath.Join(bunDirOf(BunVersion), bunExe())
	if err := os.MkdirAll(filepath.Dir(old), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("downloaded before"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !HasBun() {
		t.Fatal("HasBun is false with a bun downloaded before")
	}
	exe, err := Bun(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); filepath.Base(exe) != magpieBun() || string(b) != "downloaded before" || asked.Load() != 0 {
		t.Fatalf("Bun = %s holding %q after %d downloads", exe, b, asked.Load())
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("the old bun is still there: %v", err)
	}
}

// The plugin host runs as magpie-bun: the process name the system reports
// for it is magpie-bun, not bun.
func TestHostRunsAsMagpieBun(t *testing.T) {
	real, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("no bun on PATH")
	}
	bunHome(t, BunVersion, time.Now())
	t.Cleanup(Settle)
	// a real Bun as magpie downloaded it before: a copy (not a link, whose
	// process would take the target's name) at the old name
	old := filepath.Join(bunDirOf(BunVersion), bunExe())
	if err := os.MkdirAll(filepath.Dir(old), 0o755); err != nil {
		t.Fatal(err)
	}
	copyExe(t, real, old)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("testdata/fake/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if ps, err := Providers(ctx); err != nil || len(ps) != 1 {
		t.Fatalf("Providers = %+v, %v", ps, err)
	}
	hostMu.Lock()
	h := current
	hostMu.Unlock()
	if h == nil || h.cmd.Process == nil {
		t.Fatal("no host running")
	}
	if got := filepath.Base(h.cmd.Path); got != magpieBun() {
		t.Fatalf("the host runs %s", h.cmd.Path)
	}
	if runtime.GOOS == "windows" {
		return
	}
	out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(h.cmd.Process.Pid)).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := filepath.Base(strings.TrimSpace(string(out))); got != "magpie-bun" {
		t.Fatalf("ps says the host is %q", got)
	}
}

func copyExe(t *testing.T, from, to string) {
	t.Helper()
	in, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}
