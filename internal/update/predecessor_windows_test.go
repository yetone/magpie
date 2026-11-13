package update

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The magpie before is gone but its WebView2 isn't yet: the start waits
// for the msedgewebview2 processes it left (vsoullai on X, 0.1.4096: the
// new magpie's WebView2 joined the old browser going down and hung). The
// old magpie here is this test binary; its "WebView2" is a copy of it named
// msedgewebview2.exe that ends 3s after it.
const testPredecessorEnv = "MAGPIE_TEST_PREDECESSOR"

func init() {
	v := os.Getenv(testPredecessorEnv)
	if v == "" {
		return
	}
	role, arg, _ := strings.Cut(v, ",")
	switch role {
	case "old": // arg: the web copy, then its done file
		exe, done, _ := strings.Cut(arg, ",")
		web := exec.Command(exe, "-test.run=^$")
		web.Env = append(os.Environ(), testPredecessorEnv+"=web,"+done)
		if err := web.Start(); err != nil {
			os.Exit(2)
		}
		os.Stdout.WriteString("ready\n")
		io.Copy(io.Discard, os.Stdin) // the test lets it go
		os.Exit(0)
	case "web": // arg: the file it writes as it ends
		time.Sleep(3 * time.Second)
		os.WriteFile(arg, []byte("done"), 0o644)
		os.Exit(0)
	}
}

func TestAwaitGoneWaitsForTheOldWebView(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	web := filepath.Join(dir, "msedgewebview2.exe")
	b, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(web, b, 0o755); err != nil {
		t.Fatal(err)
	}
	done := filepath.Join(dir, "web-done")
	old := exec.Command(self, "-test.run=^$")
	old.Env = append(os.Environ(), testPredecessorEnv+"=old,"+web+","+done)
	in, _ := old.StdinPipe()
	out, _ := old.StdoutPipe()
	if err := old.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		in.Close()
		old.Wait()
		// the web copy ends by itself within 3s; the folder goes after it
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
			if _, err := os.Stat(done); err == nil {
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	})
	if line, _ := bufio.NewReader(out).ReadString('\n'); line != "ready\n" {
		t.Fatalf("old: %q", line)
	}
	if got := webViewProcs(uint32(old.Process.Pid)); len(got) != 1 {
		t.Fatalf("the old one's WebView2 processes: %v, want its one", got)
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		in.Close() // the old magpie quits; its WebView2 runs on 3s
	}()
	t.Setenv("MAGPIE_REPLACES", strconv.Itoa(old.Process.Pid))
	start := time.Now()
	AwaitPredecessor()
	if _, err := os.Stat(done); err != nil {
		t.Fatalf("the start went on after %s, while the old magpie's WebView2 still ran", time.Since(start).Round(time.Millisecond))
	}
}
