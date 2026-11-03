package library

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// The npx lines read as a package and its arguments, and those left to npx.
func TestReadNpx(t *testing.T) {
	for _, c := range []struct {
		cmd  string
		args []string
		spec string
		rest []string
	}{
		{"npx", []string{"-y", "@playwright/mcp@latest", "--headless"}, "@playwright/mcp@latest", []string{"--headless"}},
		{`C:\Program Files\nodejs\npx.cmd`, []string{"--yes", "tavily-mcp@0.2.1"}, "tavily-mcp@0.2.1", []string{}},
		{"/opt/homebrew/bin/npx", []string{"-y", "firecrawl-mcp"}, "firecrawl-mcp", []string{}},
		{"npx", []string{"firecrawl-mcp"}, "", nil},                     // no -y: npx asks
		{"npx", []string{"-y", "-p", "a", "b"}, "", nil},                // -p
		{"npx", []string{"-y", "github:me/srv"}, "", nil},               // not the registry's
		{"npx", []string{"-y", "./srv"}, "", nil},                       // a path
		{"bunx", []string{"-y", "srv"}, "", nil},                        // not npx
		{"npx", []string{"-y", "--registry=https://x", "srv"}, "", nil}, // a flag of npm's
		{"node", []string{"-y", "srv"}, "", nil},                        // node
		{"npx", []string{"-y", "mcp-remote", "https://hf.co/mcp"}, "mcp-remote", []string{"https://hf.co/mcp"}},
	} {
		r, ok := readNpx(&Server{Transport: "stdio", Command: c.cmd, Args: c.args})
		if ok != (c.spec != "") || r.spec != c.spec || len(r.args)+len(c.rest) > 0 && !slices.Equal(r.args, c.rest) {
			t.Errorf("%s %q: read %q %q %v, want %q %q", c.cmd, c.args, r.spec, r.args, ok, c.spec, c.rest)
		}
	}
	for spec, pinned := range map[string]bool{"a@1.2.3": true, "@s/a@0.1.0-beta.2": true, "a": false, "@s/a": false, "a@latest": false, "a@^1.2.0": false} {
		if (npxRun{spec: spec}).pinned() != pinned {
			t.Errorf("%s pinned = %v", spec, !pinned)
		}
	}
}

// An agent is given an npx server as node running the package's program
// from magpie's install of it, with the server's arguments after: no npx
// left between the agent and the server, which held a node and two
// cmd.exe a server on Windows (#3885). Before the install is done the
// agent has npx, as the library does; the install is asked for once.
func TestNpxServerRunsByNode(t *testing.T) {
	h := sandbox(t)
	cfg := filepath.Join(h, ".codex/config.toml")
	installs := make(chan string, 4)
	release := make(chan struct{})
	npxInstall = func(spec, npx, dir string) error {
		installs <- spec
		<-release
		pkg := filepath.Join(dir, "node_modules", "@acme", "mcp")
		if err := os.MkdirAll(filepath.Join(pkg, "dist"), 0o755); err != nil {
			return err
		}
		write(t, filepath.Join(pkg, "package.json"), `{"name":"@acme/mcp","bin":{"acme-mcp":"dist/cli.js"}}`)
		write(t, filepath.Join(pkg, "dist", "cli.js"), "#!/usr/bin/env node\n")
		return nil
	}
	synced := make(chan struct{}, 1)
	npxSynced = func() { synced <- struct{}{} }
	t.Cleanup(func() { npxInstall, npxSynced = nil, nil })

	srv := Server{Name: "acme", Transport: "stdio", Command: "npx", Args: []string{"-y", "@acme/mcp@1.0.0", "--port", "0"}, Agents: []string{"codex"}}
	ok(t)(SaveServer("", srv))
	if e := codexEntry(t, cfg, "acme"); e["command"] != "npx" {
		t.Fatalf("before the install, Codex has %v, want npx:\n%s", e["command"], read(t, cfg))
	}
	select {
	case got := <-installs:
		if got != "@acme/mcp@1.0.0" {
			t.Fatalf("installed %q", got)
		}
	case <-time.After(30 * time.Second):
		close(release)
		t.Fatal("the package was never installed")
	}
	ok(t)(Sync()) // while installing: asked for no second time
	close(release)
	select {
	case <-synced:
	case <-time.After(30 * time.Second):
		t.Fatal("the install never asked for a sync")
	}
	if len(installs) > 0 {
		t.Fatalf("the package was installed again: %s", <-installs)
	}
	ok(t)(Sync())
	e := codexEntry(t, cfg, "acme")
	args, _ := e["args"].([]any)
	if e["command"] != "node" || len(args) != 3 || !strings.HasSuffix(filepath.ToSlash(args[0].(string)), "/node_modules/@acme/mcp/dist/cli.js") || args[1] != "--port" || args[2] != "0" {
		t.Fatalf("Codex runs %v %v, want node …/@acme/mcp/dist/cli.js --port 0:\n%s", e["command"], args, read(t, cfg))
	}
	before := read(t, cfg)
	ok(t)(Sync())
	if read(t, cfg) != before {
		t.Fatalf("a sync wrote the entry again:\n%s", read(t, cfg))
	}
	if l, _ := load(); l.server("acme").Command != "npx" {
		t.Fatalf("the library's own server became %s", l.server("acme").Command)
	}

	// taken off, the processes running the program are the ones ended
	var ended []*Server
	ending = func(s []*Server) { ended = append(ended, s...) }
	t.Cleanup(func() { ending = func([]*Server) {} })
	ok(t)(ServerAgents("acme", nil))
	if len(ended) != 1 || !launchedBy(ended[0])(`"node" "`+args[0].(string)+`" --port 0`) {
		t.Fatalf("ended %v, want node's run of the program", ended)
	}

	// out of the library, the install goes a day after its last
	dir := npxDir("@acme/mcp@1.0.0")
	write(t, filepath.Join(dir, ".installed"), "@acme/mcp@1.0.0\n")
	ok(t)(Sync())
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("the install of a server the library has went: %v", err)
	}
	ok(t)(RemoveServer("acme"))
	ok(t)(Sync())
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("an install under a day old went at once: %v", err)
	}
	old := time.Now().Add(-25 * time.Hour)
	os.Chtimes(filepath.Join(dir, ".installed"), old, old)
	ok(t)(Sync())
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("the install of a server taken out of the library stays")
	}
}

// A package whose program isn't a script node runs, or with no bin to
// pick, stays with npx.
func TestNpxBinPicks(t *testing.T) {
	dir := t.TempDir()
	put := func(name, pkgJSON string, files map[string]string) {
		pkg := filepath.Join(dir, "node_modules", filepath.FromSlash(name))
		os.MkdirAll(pkg, 0o755)
		write(t, filepath.Join(pkg, "package.json"), pkgJSON)
		for f, b := range files {
			os.MkdirAll(filepath.Dir(filepath.Join(pkg, f)), 0o755)
			write(t, filepath.Join(pkg, f), b)
		}
	}
	put("one", `{"bin":"cli.mjs"}`, map[string]string{"cli.mjs": "import x from 'y'"})
	put("@s/two", `{"bin":{"other":"o.js","two":"bin/two"}}`, map[string]string{"bin/two": "#!/usr/bin/env node\n", "o.js": ""})
	put("native", `{"bin":{"native":"bin/native"}}`, map[string]string{"bin/native": "\x7fELF"})
	put("many", `{"bin":{"a":"a.js","b":"b.js"}}`, map[string]string{"a.js": "", "b.js": ""})
	put("out", `{"bin":"../one/cli.mjs"}`, nil)
	for name, want := range map[string]string{"one": "cli.mjs", "@s/two": "bin/two", "native": "", "many": "", "out": "", "none": ""} {
		got := npxBin(dir, name)
		if want == "" && got != "" || want != "" && !strings.HasSuffix(filepath.ToSlash(got), name+"/"+want) {
			t.Errorf("%s: bin %q, want %q", name, got, want)
		}
	}
}

// No package's tests install a server's package or end the machine's
// own processes: the library's defaults are off in any test binary, not
// only under this package's TestMain. internal/cli's TestLibraryMCPHub
// had npm install `npx -y fs` under its temp HOME after it ended, and
// ended the user's own running fs server (#4140).
func TestTestsLeaveTheMachineAlone(t *testing.T) {
	if npxInstall != nil {
		t.Error("npxInstall is set in a test binary: a test adding an npx server runs npm install")
	}
	if reflect.ValueOf(ending).Pointer() == reflect.ValueOf(endDropped).Pointer() {
		t.Error("ending is endDropped in a test binary: a test taking a server out ends the machine's processes of it")
	}
}
