//go:build !windows

package cli

import (
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// `magpie serve` runs on as the new version once an update is put in where
// it runs from (acezan on Discord: the service's serve ran on from
// "magpie.real (deleted)" after `magpie web` updated the program they
// share; #4335 on a Mac). Here the test binary, copied to magpie.real, is
// a released serve on a port of the test's own; the update renames a new
// program over it, a script that notes its pid and arguments. serve runs
// it in its own place: the same pid, with serve's arguments.
func TestServeRestartsIntoAnUpdate(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "magpie.real")
	if err := os.WriteFile(bin, b, 0o755); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	cmd := exec.Command(bin, "serve")
	cmd.Env = append(os.Environ(), "MAGPIE_CLI_TEST_SERVE=1", "MAGPIE_ADDR="+addr)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ended := make(chan struct{})
	go func() { cmd.Wait(); close(ended) }()
	t.Cleanup(func() { cmd.Process.Kill(); <-ended })
	c := &http.Client{Timeout: 2 * time.Second}
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		if res, err := c.Get("http://" + addr + "/v1/models"); err == nil {
			res.Body.Close()
			break
		}
		select {
		case <-ended:
			t.Fatal("serve ended before it served")
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("serve didn't answer")
		}
	}

	// the update: a new program renamed over the old, as an update puts
	// it in; the running serve's file is now gone
	marker := filepath.Join(dir, "ran")
	next := filepath.Join(dir, "magpie.real.new")
	script := "#!/bin/sh\necho \"$$ $*\" > '" + marker + "'\nexec sleep 60\n"
	if err := os.WriteFile(next, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(next, bin); err != nil {
		t.Fatal(err)
	}

	for deadline := time.Now().Add(40 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		if got, err := os.ReadFile(marker); err == nil && strings.HasSuffix(string(got), "\n") {
			want := strconv.Itoa(cmd.Process.Pid) + " serve\n"
			if string(got) != want {
				t.Fatalf("the new program ran as %q, want %q: serve's own process, with its arguments", got, want)
			}
			break
		}
		select {
		case <-ended:
			t.Fatal("serve ended instead of running the update in its place")
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("serve ran on as the old version: the update put in where it runs from was never run")
		}
	}
	// and it let the gateway's port go for the new one
	if res, err := c.Get("http://" + addr + "/v1/models"); err == nil {
		res.Body.Close()
		t.Fatal("the old serve still answers on the gateway's port")
	}
}
