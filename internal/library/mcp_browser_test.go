package library

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// inBox has magpie run in a container, on Linux, with Chrome there or not.
func inBox(t *testing.T, box, chrome bool) {
	t.Helper()
	wb, wg, wc := runsInBox, browserGOOS, chromeExists
	runsInBox = func() bool { return box }
	browserGOOS = "linux"
	chromeExists = func(string) bool { return chrome }
	t.Cleanup(func() { runsInBox, browserGOOS, chromeExists = wb, wg, wc })
}

// Passings_z on X: Chrome DevTools' server in magpie's image listed its
// tools, and each failed with "Could not find Google Chrome executable".
func TestNoBrowser(t *testing.T) {
	npx := func(args ...string) *Server { return &Server{Transport: "stdio", Command: "npx", Args: args} }
	for _, c := range []struct {
		name string
		s    *Server
		want string
	}{
		{"chrome-devtools, the Library's", npx("-y", "chrome-devtools-mcp@latest"), "--browserUrl"},
		{"chrome-devtools by path", &Server{Transport: "stdio", Command: "/usr/local/bin/npx", Args: []string{"chrome-devtools-mcp"}}, "--browserUrl"},
		{"playwright", npx("@playwright/mcp@latest"), "--cdp-endpoint"},
		{"playwright headless", npx("-y", "@playwright/mcp", "--headless"), "--cdp-endpoint"},
		{"bunx", &Server{Transport: "stdio", Command: "bunx", Args: []string{"chrome-devtools-mcp@latest"}}, "--browserUrl"},
		{"at a running Chrome", npx("-y", "chrome-devtools-mcp@latest", "--browserUrl=http://host.docker.internal:9222"), ""},
		{"at a running Chrome, split", npx("chrome-devtools-mcp", "--browser-url", "http://h:9222"), ""},
		{"short option", npx("chrome-devtools-mcp", "-u", "http://h:9222"), ""},
		{"its own executable", npx("chrome-devtools-mcp", "--executablePath", "/usr/bin/chromium"), ""},
		{"playwright at a CDP endpoint", npx("@playwright/mcp", "--cdp-endpoint", "ws://h:9222"), ""},
		{"playwright's own Firefox", npx("@playwright/mcp", "--browser=firefox"), ""},
		{"another package", npx("-y", "@modelcontextprotocol/server-memory"), ""},
		{"a package named after it", npx("-y", "chrome-devtools-mcp-helper"), ""},
		{"not npx", &Server{Transport: "stdio", Command: "node", Args: []string{"chrome-devtools-mcp"}}, ""},
		{"remote", &Server{Transport: "http", URL: "http://h/mcp", Command: "npx", Args: []string{"chrome-devtools-mcp"}}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			inBox(t, true, false)
			if got := noBrowser(c.s); got != c.want {
				t.Fatalf("noBrowser = %q, want %q", got, c.want)
			}
		})
	}
	s := npx("-y", "chrome-devtools-mcp@latest")
	t.Run("not in a container", func(t *testing.T) {
		inBox(t, false, false)
		if got := noBrowser(s); got != "" {
			t.Fatalf("noBrowser = %q on a desktop", got)
		}
	})
	t.Run("an image with Chrome", func(t *testing.T) {
		inBox(t, true, true)
		if got := noBrowser(s); got != "" {
			t.Fatalf("noBrowser = %q with Chrome there", got)
		}
	})
	t.Run("not Linux", func(t *testing.T) {
		inBox(t, true, false)
		browserGOOS = "darwin"
		if got := noBrowser(s); got != "" {
			t.Fatalf("noBrowser = %q on macOS", got)
		}
	})
}

// The check says so: the server answers, with its tools, and the row says
// it has no Chrome to start, with its option for one it can reach.
func TestCheckSaysNoBrowser(t *testing.T) {
	s := fakeStdio(t, "ok", nil)
	// the fake server, as npx running Chrome DevTools' package; the
	// leading -test.run keeps npxDirect from installing it
	name := "npx"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	b, err := os.ReadFile(s.Command)
	if err != nil {
		t.Fatal(err)
	}
	s.Command = filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(s.Command, b, 0o755); err != nil {
		t.Fatal(err)
	}
	s.Args = append(s.Args, "chrome-devtools-mcp@latest")

	inBox(t, true, false)
	h := CheckServer(context.Background(), s)
	if h.State != "error" || h.Why != "nobrowser" || h.Detail != "--browserUrl" || h.Tools != 3 {
		t.Fatalf("got %+v, want nobrowser with --browserUrl and its 3 tools", h)
	}
	if err := healthErr(h); err == nil || err.Error() != "it starts a Chrome, and there is none where magpie runs: use Magpie Browser, or give it --browserUrl for a Chrome it can reach" {
		t.Errorf("healthErr = %v", err)
	}

	inBox(t, false, false)
	if h := CheckServer(context.Background(), s); h.State != "ok" || h.Tools != 3 {
		t.Fatalf("on a desktop: got %+v, want ok", h)
	}
}
