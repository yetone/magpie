package plugin

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A plugin host is reaped when it ends, while a program the plugin started
// still holds its stdout. The host was waited for only once its stdout
// reached EOF, which such a program put off for its whole life, so every
// host stopped (a Restart, a proxy change) left a <defunct> magpie-bun
// under magpie until magpie quit (acezan on Discord: 57 after 47 hours as
// a systemd service on Linux).
func TestHostReapedWhileItsChildHoldsStdout(t *testing.T) {
	sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// a sleep of a length no other test uses, to find and end it after
	const hold = "61.25"
	t.Cleanup(func() { killSleeps(hold) })

	b, err := os.ReadFile("testdata/fake/index.js")
	if err != nil {
		t.Fatal(err)
	}
	js := filepath.Join(t.TempDir(), "index.js")
	start := `Bun.spawn({ cmd: ["sleep", "` + hold + `"], stdout: Bun.stdout })` + "\n"
	if err := os.WriteFile(js, append([]byte(start), b...), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Add(ctx, js); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if _, err := Providers(ctx); err != nil {
			t.Fatalf("round %d: %v", i, err)
		}
		hostMu.Lock()
		h := current
		hostMu.Unlock()
		if h == nil {
			t.Fatalf("round %d: no host", i)
		}
		Restart()
		select {
		case <-h.dead:
		case <-time.After(15 * time.Second):
			t.Fatalf("round %d: the host isn't marked ended 15s after Restart", i)
		}
		// the host is waited for as soon as it ends, not when the sleep
		// lets go of its stdout
		until := time.Now().Add(15 * time.Second)
		for {
			z := defunctChildren()
			if len(z) == 0 {
				break
			}
			if time.Now().After(until) {
				t.Fatalf("round %d: %d defunct children 15s after Restart: %v", i, len(z), z)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
}

// defunctChildren lists this process's children that have ended and not
// been waited for.
func defunctChildren() []string {
	me := strconv.Itoa(os.Getpid())
	ents, _ := os.ReadDir("/proc")
	var z []string
	for _, e := range ents {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		s := string(b)
		i := strings.LastIndexByte(s, ')')
		if i < 0 || i+2 > len(s) {
			continue
		}
		f := strings.Fields(s[i+2:])
		if len(f) > 1 && f[0] == "Z" && f[1] == me {
			z = append(z, e.Name()+" "+s[strings.IndexByte(s, '(')+1:i])
		}
	}
	return z
}

// killSleeps ends every `sleep secs` running.
func killSleeps(secs string) {
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if err != nil {
			continue
		}
		args := bytes.Split(bytes.TrimRight(b, "\x00"), []byte{0})
		if len(args) == 2 && filepath.Base(string(args[0])) == "sleep" && string(args[1]) == secs {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}
