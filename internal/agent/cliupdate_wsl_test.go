package agent

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// An agent installed inside a WSL distro has its version read and is
// updated in the distro (@mewcoder on X: "可以支持wsl里的agent版本检测和更新吗"),
// the way the host's is: by the npm that owns it, run through wsl.exe in the
// distro. A distro that isn't running is never asked.
func TestWSLAgentCLIVersionAndUpdate(t *testing.T) {
	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"version":"0.2.0"}`))
	}))
	oldReg, oldRun := npmRegistry, wslRunUpdate
	npmRegistry = reg.URL + "/"
	t.Cleanup(func() {
		reg.Close()
		npmRegistry, wslRunUpdate = oldReg, oldRun
		versions, latests, wslProbes = memo{}, memo{}, memo{}
	})
	versions, latests, wslProbes = memo{}, memo{}, memo{}

	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "home", "me", ".codex"), 0o755)
	const prefix = "/home/me/.nvm/versions/node/v22"
	const bin = prefix + "/bin/codex"
	const real = prefix + "/lib/node_modules/@openai/codex/bin/codex.js"
	fakeWSL(t, "Ubuntu\r\nStopped\r\n", "Ubuntu\r\n", map[string]string{
		"Ubuntu": "home:/home/me\ndir:.codex\nbin:codex " + bin + "\nroute:default via 172.20.0.1 dev eth0\n",
	}, map[string]string{"Ubuntu": root})

	var mu sync.Mutex
	version, asked := "0.1.0", map[string]int{}
	probe := wslRun
	wslRun = func(d time.Duration, args ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		if len(args) > 4 && args[0] == "-d" && args[2] == "-e" {
			asked[args[1]]++
			switch args[3] {
			case "test":
				return nil, nil // writable, and npm is there
			case "sh":
				if args[4] == "-c" && strings.Contains(args[5], "readlink") {
					return []byte("bin:" + bin + "\nreal:" + real + "\ncode:0\nver:codex-cli " + version + " \n"), nil
				}
			}
		}
		return probe(d, args...)
	}
	var ran []string
	wslRunUpdate = func(_ time.Duration, u *updater) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		ran = append([]string{u.wsl, u.path}, u.cmd...)
		version = "0.2.0"
		return nil, nil
	}

	ds := wslDistros()
	var d distro
	for _, x := range ds {
		if x.Name == "Ubuntu" {
			d = x
		}
	}
	a := wslAgent(wslKindOf("codex"), d)
	c, ok := a.CLI()
	if !ok || c.Version != "0.1.0" || c.Via != "npm" || c.Latest != "0.2.0" || !c.Update || c.Path != bin {
		t.Fatalf("CLI = %+v, %v", c, ok)
	}
	if !strings.Contains(c.Command, "npm install -g --prefix "+prefix) {
		t.Fatalf("command %q", c.Command)
	}
	if got, _ := CLIs(5 * time.Second); got[a.ID].Version != "0.1.0" {
		t.Fatalf("CLIs has no %s: %+v", a.ID, got)
	}

	c, err := a.UpdateCLI()
	if err != nil {
		t.Fatal(err)
	}
	if c.Version != "0.2.0" || c.Update {
		t.Fatalf("after the update: %+v", c)
	}
	want := []string{"Ubuntu", prefix + "/bin", prefix + "/bin/npm", "install", "-g", "--prefix", prefix, "@openai/codex@latest"}
	if strings.Join(ran, " ") != strings.Join(want, " ") {
		t.Fatalf("ran %q, want %q", ran, want)
	}

	// a distro that isn't running is never asked
	stopped := wslAgent(wslKindOf("codex"), distro{Name: "Stopped", Home: "/home/s", Has: map[string]bool{"dir:.codex": true}})
	before := asked["Stopped"]
	if _, ok := stopped.CLI(); ok {
		t.Fatal("a stopped distro's CLI was read")
	}
	if _, err := stopped.UpdateCLI(); err == nil {
		t.Fatal("a stopped distro was updated")
	}
	if asked["Stopped"] != before {
		t.Fatalf("a stopped distro was asked %d times", asked["Stopped"]-before)
	}
}
