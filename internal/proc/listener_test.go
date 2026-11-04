package proc

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"testing"
)

// The process listening on a port is found, with the program it runs: here
// the test's own, on a port of its own.
func TestListeningOn(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	pids, err := ListeningOn(context.Background(), ln.Addr().(*net.TCPAddr).Port)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(pids, os.Getpid()) {
		t.Fatalf("listening on the port: %v, not this process (%d)", pids, os.Getpid())
	}
	got, err := Executable(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	want, _ := os.Executable()
	if a, b := evalPath(got), evalPath(want); a != b {
		t.Fatalf("program %q, want %q", a, b)
	}
}

// A port nothing listens on is no listener and no error, on every system:
// the take-over reads exactly that to know an older magpie let the port go
// (#2231, where Windows answered "exit status 1").
func TestNothingListeningIsNoError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	pids, err := ListeningOn(context.Background(), port)
	if err != nil {
		t.Fatalf("nothing listens on %d: %v", port, err)
	}
	if len(pids) != 0 {
		t.Fatalf("nothing listens on %d, read %v", port, pids)
	}
}

func evalPath(p string) string {
	if q, err := filepath.EvalSymlinks(p); err == nil {
		return q
	}
	return p
}

// Only magpie's own programs are taken for magpie. An update moves a running
// exe aside to exe.old, and to exe.old-2, -3 when that one is held too, so
// every name it can take while it still runs is magpie's.
func TestIsMagpie(t *testing.T) {
	for path, want := range map[string]bool{
		"/Applications/Magpie.app/Contents/MacOS/magpie": true,
		"/usr/local/bin/magpie":                          true,
		`C:\Users\u\AppData\Local\magpie\magpie.exe`:     true,
		`C:\Users\u\AppData\Local\magpie\magpie.exe.old`: true,
		// an update that finds .old still held leaves the running exe at
		// .old-2, the name it holds the gateway port under
		`C:\Users\u\AppData\Local\magpie\magpie.exe.old-2`: true,
		`C:\Users\u\AppData\Local\magpie\magpie.exe.old-3`: true,
		"/home/u/magpie.old-2":                             true,
		"/home/u/dial.old-4":                               true,
		// the numbered suffix is an update's, not another program's
		`C:\Users\u\other-server.old-2`: false,
		`C:\Users\u\magpie.old-backup`:  false,
		"/home/u/.local/bin/dial":       true,
		// the program a wrapper script named magpie runs (acezan)
		"/root/.local/bin/magpie.real":    true,
		"/opt/magpie-dev":                 true,
		"/usr/bin/python3":                false,
		"/usr/bin/magpies":                false,
		"/tmp/other-server":               false,
		`C:\Windows\System32\svchost.exe`: false,
		"":                                false,
	} {
		if IsMagpie(path) != want {
			t.Errorf("IsMagpie(%q) = %v", path, !want)
		}
	}
}

// The programs holding a port for a container are told apart from others:
// a magpie answering there runs inside it, out of this one's reach.
func TestIsForwarder(t *testing.T) {
	for path, want := range map[string]bool{
		"/Applications/OrbStack.app/Contents/Frameworks/OrbStack Helper.app/Contents/MacOS/OrbStack Helper": true,
		"/Applications/Docker.app/Contents/MacOS/com.docker.backend":                                        true,
		"/usr/bin/docker-proxy": true,
		`C:\Program Files\Docker\Docker\resources\com.docker.backend.exe`: true,
		`C:\Windows\System32\wslrelay.exe`:                                true,
		"/opt/homebrew/bin/limactl":                                       true,
		"/usr/libexec/podman/gvproxy":                                     true,
		"/Applications/Magpie.app/Contents/MacOS/magpie":                  false,
		"/usr/bin/python3":                                                false,
		"/tmp/other-server":                                               false,
		"":                                                                false,
	} {
		if IsForwarder(path) != want {
			t.Errorf("IsForwarder(%q) = %v", path, !want)
		}
	}
}

// A magpie is told by what its program is, not only its name: Go's build
// information names magpie's main package. On Linux that is read from the
// running process (/proc/<pid>/exe), so one whose file an update replaced
// still reads as magpie (acezan on Discord). Here the test binary, copied
// under another name and its file removed once it runs, stands in, with
// magpieMain the test binary's own package.
func TestRunsMagpieByItsBuild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a running program's file can't be removed on Windows")
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		t.Fatal("no build information in the test binary")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "server.real")
	if err := os.WriteFile(bin, b, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-test.run=^$")
	cmd.Env = append(os.Environ(), "MAGPIE_PROC_SLEEP=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stdin.Close(); cmd.Process.Kill(); cmd.Wait() })
	pid := cmd.Process.Pid

	was := magpieMain
	defer func() { magpieMain = was }()
	magpieMain = bi.Path
	if !RunsMagpie(pid, bin) {
		t.Fatalf("%s, built from %s, not told as magpie", bin, bi.Path)
	}
	magpieMain = "github.com/yetone/magpie"
	if RunsMagpie(pid, bin) {
		t.Fatal("another program's build told as magpie")
	}
	if runtime.GOOS != "linux" {
		return
	}
	// the update's rename: the file is gone, the process runs on
	if err := os.Remove(bin); err != nil {
		t.Fatal(err)
	}
	path, err := Executable(pid)
	if err != nil || filepath.Base(path) != "server.real" {
		t.Fatalf("Executable of a replaced program: %q, %v", path, err)
	}
	magpieMain = bi.Path
	if !RunsMagpie(pid, path) {
		t.Fatal("a magpie whose file was replaced isn't told as magpie")
	}
}
