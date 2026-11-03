package library

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

// The tests run in a home of their own, never the user's real files, with
// a temp dir of their own: what they leave in it is theirs, and a skill's
// download left there fails the run (#2321).
func TestMain(m *testing.M) {
	// started as magpie's own image or browser server (Args mcp image,
	// mcp browser): the fake server, for the hub to take it for that one
	if len(os.Args) >= 3 && os.Args[1] == "mcp" && (os.Args[2] == "image" || os.Args[2] == "browser") {
		os.Args = []string{os.Args[0], "-test.run=^TestFakeMCPServer$"}
	}
	if os.Getenv(testenv.Marker) != "" {
		testenv.Main(m) // started again by a test: in its temp dir already
	}
	tmp, err := os.MkdirTemp("", "magpie-library-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, k := range []string{"TMPDIR", "TMP", "TEMP"} { // TMP, TEMP: Windows'
		os.Setenv(k, tmp)
	}
	// Codex given its servers by their commands, as the tests expect;
	// mcp_run_test.go turns it on
	runOn = false
	code := testenv.Run(m)
	forgetProbes()
	if left := leftDownloads(tmp); len(left) > 0 && code == 0 {
		fmt.Fprintf(os.Stderr, "library: the tests left %d skill downloads in the temp dir: %s\n", len(left), strings.Join(left, ", "))
		code = 1
	}
	os.RemoveAll(tmp)
	os.Exit(code)
}

// forgetProbes ends every cached probe, as its expiry would.
func forgetProbes() {
	probes.Lock()
	m := probes.m
	probes.m = map[string]*probeEntry{}
	probes.Unlock()
	for _, e := range m {
		e.timer.Stop()
		os.RemoveAll(e.tmp)
	}
}

func leftDownloads(dir string) []string {
	got, _ := filepath.Glob(filepath.Join(dir, skillTemp+"*"))
	for i, p := range got {
		got[i] = filepath.Base(p)
	}
	return got
}
