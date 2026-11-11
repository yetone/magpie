package agent

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

// mimi001234 on Discord: magpie's Agents page didn't find Antigravity CLI
// installed in WSL. The probe asks after its folder and its command, and
// finds it by either; a distro with neither has no agy row.
func TestWSLAgyFound(t *testing.T) {
	k := wslKindOf("agy")
	if !strings.Contains(wslProbeScript(), `[ -d "$HOME/.gemini/antigravity-cli" ] && echo dir:.gemini/antigravity-cli; `) ||
		!strings.Contains(wslProbeScript(), `p=$(wslcmd agy) `) {
		t.Fatal("the probe doesn't ask after agy")
	}
	for _, p := range []string{"dir:.gemini/antigravity-cli\n", "bin:agy /home/me/.local/bin/agy\n"} {
		if d := parseProbe("U", "home:/home/me\n"+p); d == nil || !k.found(*d) {
			t.Errorf("not found by %q", p)
		}
	}
	// Gemini CLI's ~/.gemini, or Windows' agy on the PATH WSL inherits, is not it
	for _, p := range []string{"dir:.gemini\n", "bin:gemini /usr/bin/gemini\n", "bin:agy /mnt/c/Users/me/AppData/Local/Programs/agy/agy\n"} {
		if d := parseProbe("U", "home:/home/me\n"+p); d != nil && k.found(*d) {
			t.Errorf("found by %q", p)
		}
	}

	syncHome(t)
	with, without := t.TempDir(), t.TempDir()
	fakeWSL(t, "Ubuntu\r\nDebian\r\n", "Ubuntu\r\nDebian\r\n", map[string]string{
		"Ubuntu": "home:/home/me\nbin:agy /home/me/.local/bin/agy\ndir:.gemini/antigravity-cli\nroute:default via 172.20.0.1 dev eth0\n",
		"Debian": "home:/home/me\ndir:.codex\nroute:default via 172.20.0.1 dev eth0\n",
	}, map[string]string{"Ubuntu": with, "Debian": without})
	var ids []string
	for _, a := range wslAgentsOf(wslDistros()) {
		ids = append(ids, a.ID)
		if a.ID == "agy@wsl:Ubuntu" && (a.Name != "Antigravity CLI · WSL Ubuntu" || a.Bin != "" ||
			a.Path != filepath.Join(with, "home", "me", ".gemini", "antigravity-cli", "settings.json")) {
			t.Errorf("%+v", a)
		}
	}
	if got := strings.Join(ids, ","); got != "agy@wsl:Ubuntu,codex@wsl:Debian" {
		t.Fatalf("agents %s", got)
	}
}

// agyDistro is a running distro under NAT whose user logs in to zsh and has
// a .bashrc of their own, with agy found by its command.
func agyDistro(t *testing.T, mine string) (d distro, dhome string) {
	t.Helper()
	root := t.TempDir()
	dhome = filepath.Join(root, "home", "me")
	writeFile(t, filepath.Join(root, "etc", "passwd"), "root:x:0:0:root:/root:/bin/bash\nme:x:1000:1000:,,,:/home/me:/usr/bin/zsh\n")
	writeFile(t, filepath.Join(dhome, ".bashrc"), mine)
	return distro{Name: "Ubuntu", Root: root, Home: "/home/me", Has: map[string]bool{"bin:agy": true},
		Gateway: "172.20.0.1", Running: true}, dhome
}

// Picking one of magpie's models for agy in a distro puts the function agy
// in the distro's shells' profiles — its .bashrc, and the .zshrc of the
// zsh it logs in to, made — naming the gateway as the distro reaches it
// under NAT, with the WSL key; this machine's profiles are left alone. A
// sync after WSL moved the address names the new one, and stepping out
// takes the function out, the user's lines as they were.
func TestWSLAgyPick(t *testing.T) {
	home := syncHome(t)
	fakeWSLNet(t, "172.20.0.1/20")
	mine := "alias ll='ls -l'\n"
	d, dhome := agyDistro(t, mine)
	bashrc, zshrc := filepath.Join(dhome, ".bashrc"), filepath.Join(dhome, ".zshrc")
	a := wslAgent(wslKindOf("agy"), d)
	if a.Typed() != "" || a.Launch() != "" {
		t.Fatalf("before: %q %q", a.Typed(), a.Launch())
	}
	if err := a.Field("model").Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	gw := "http://172.20.0.1:" + gateway.Port()
	key := agentKeyAt("agy", gw)
	if !strings.HasPrefix(key, "wsl-") || !strings.HasSuffix(key, "-agy") {
		t.Fatalf("key %q", key)
	}
	over := filepath.Join(dhome, ".gemini", "antigravity-cli", "magpie-settings.json")
	if m, _ := edit.GetJSON(over, "model"); m != "magpie/relay/glm-4.6" {
		t.Fatalf("magpie-settings.json:\n%s", readFile(over))
	}
	// agy's own settings.json is left as it is (remote control needs agy
	// off an API key)
	if m, _ := edit.GetJSON(filepath.Join(dhome, ".gemini", "antigravity-cli", "settings.json"), "modelProvider"); m != "" {
		t.Fatalf("settings.json has modelProvider %q", m)
	}
	for _, p := range []string{bashrc, zshrc} {
		body := readFile(p)
		if !strings.Contains(body, "# >>> magpie agy >>>\n"+agyPosixHead) || !strings.Contains(body, "GOOGLE_GEMINI_BASE_URL='"+gw+"'") ||
			!strings.Contains(body, "GEMINI_API_KEY='"+key+"'") || strings.Contains(body, "127.0.0.1") {
			t.Fatalf("%s:\n%s", p, body)
		}
	}
	if !strings.HasPrefix(readFile(bashrc), mine+"\n# >>> magpie agy >>>") {
		t.Fatalf(".bashrc:\n%s", readFile(bashrc))
	}
	if _, err := os.Stat(filepath.Join(dhome, ".config", "fish")); !os.IsNotExist(err) {
		t.Fatal("a fish config for a distro without fish")
	}
	if a.Typed() != "agy" || !strings.Contains(a.Launch(), "GOOGLE_GEMINI_BASE_URL="+gw+" ") {
		t.Fatalf("typed %q, launch %q", a.Typed(), a.Launch())
	}
	if msg := a.Check(); msg != "" {
		t.Fatal(msg)
	}
	if !strings.Contains(a.Notice(), "mirrored") {
		t.Errorf("notice: %s", a.Notice())
	}
	for _, n := range []string{".zshrc", ".bashrc", ".bash_profile", ".config/fish", ".gemini"} {
		if _, err := os.Stat(filepath.Join(home, n)); !os.IsNotExist(err) {
			t.Fatalf("this machine's %s was touched", n)
		}
	}

	// WSL restarted on another address: Check says so, and a sync follows
	d.Gateway = "172.20.5.1"
	a = wslAgent(wslKindOf("agy"), d)
	if a.Check() == "" {
		t.Fatal("the old address passes")
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	moved := "http://172.20.5.1:" + gateway.Port()
	for _, p := range []string{bashrc, zshrc} {
		if body := readFile(p); !strings.Contains(body, "GOOGLE_GEMINI_BASE_URL='"+moved+"'") || strings.Contains(body, "172.20.0.1") {
			t.Fatalf("%s after the move:\n%s", p, body)
		}
	}
	if msg := a.Check(); msg != "" {
		t.Fatal(msg)
	}

	// default: out of every profile
	if err := a.Field("model").Set(""); err != nil {
		t.Fatal(err)
	}
	if got := readFile(bashrc); got != mine {
		t.Fatalf(".bashrc:\n%s", got)
	}
	if got := readFile(zshrc); strings.Contains(got, "magpie agy") {
		t.Fatalf(".zshrc:\n%s", got)
	}
	if a.Typed() != "" || a.Launch() != "" {
		t.Fatalf("after: %q %q", a.Typed(), a.Launch())
	}
}

// The function in a distro reads settings.json's model itself, there being
// no magpie there to ask: on magpie's, agy starts with the gateway, key and
// --model (none when one is given); off it, as it is. Run in bash and zsh.
func TestWSLAgyFunctionRuns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the distro's shells")
	}
	syncHome(t)
	d, dhome := agyDistro(t, "")
	a := wslAgent(wslKindOf("agy"), d)
	if err := a.Field("model").Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	gw := "http://172.20.0.1:" + gateway.Port()
	over := filepath.Join(dhome, ".gemini", "antigravity-cli", "magpie-settings.json")
	bin := t.TempDir()
	writeFile(t, filepath.Join(bin, "agy"), "#!/bin/sh\necho \"key=$GEMINI_API_KEY url=$GOOGLE_GEMINI_BASE_URL args=$*\"\n")
	os.Chmod(filepath.Join(bin, "agy"), 0o755)
	on := "key=" + agentKeyAt("agy", gw) + " url=" + gw
	for _, c := range []struct{ sh, rc string }{{"bash", ".bashrc"}, {"zsh", ".zshrc"}} {
		sh, err := exec.LookPath(c.sh)
		if err != nil {
			t.Logf("no %s", c.sh)
			continue
		}
		run := func(args string) string {
			cmd := exec.Command(sh, "-c", `. "$HOME/`+c.rc+`"; agy `+args)
			cmd.Env = []string{"HOME=" + dhome, "PATH=" + bin + ":/usr/bin:/bin"}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%s: %v\n%s", c.sh, err, out)
			}
			return strings.TrimSpace(string(out))
		}
		if got := run("-p 'hi there'"); got != on+" args=--settings "+over+" --model magpie/relay/glm-4.6 -p hi there" {
			t.Errorf("%s: %s", c.sh, got)
		}
		if got := run("--model=own -p hi"); got != on+" args=--settings "+over+" --model=own -p hi" {
			t.Errorf("%s, a model given: %s", c.sh, got)
		}
	}
	// remote control needs the Google sign-in: plain, whatever the model
	rc := exec.Command("bash", "-c", `. "$HOME/.bashrc"; agy remote-control start; agy --remote-control`)
	rc.Env = []string{"HOME=" + dhome, "PATH=" + bin + ":/usr/bin:/bin"}
	if out, err := rc.CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "key= url= args=remote-control start\nkey= url= args=--remote-control" {
		t.Fatalf("remote control: %v %s", err, out)
	}
	// the user picked their own model in agy: it starts as it is
	if err := a.Field("model").Set("gemini-3-pro"); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", `. "$HOME/.bashrc"; agy -p hi`)
	cmd.Env = []string{"HOME=" + dhome, "PATH=" + bin + ":/usr/bin:/bin"}
	if out, err := cmd.CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "key= url= args=-p hi" {
		t.Fatalf("off magpie: %v %s", err, out)
	}
}

// A stopped distro's agy is never opened: its model is shown as last seen,
// its options come from magpie, and a pick starts the distro and writes
// the function there.
func TestWSLAgyStopped(t *testing.T) {
	syncHome(t)
	root := t.TempDir()
	b, _ := json.Marshal(map[string]*distro{
		"Stopped": {Name: "Stopped", Home: "/home/me", Root: root, Has: map[string]bool{"bin:agy": true}, Probe: wslProbeVersion,
			Values: map[string]string{"agy.model": "gemini-3-pro"}},
	})
	os.MkdirAll(filepath.Dir(wslStatePath()), 0o755)
	os.WriteFile(wslStatePath(), b, 0o600)
	asked, opened := fakeWSL(t, "Stopped\r\n", "", nil, nil)
	var a *Agent
	for _, x := range wslAgentsOf(wslDistros()) {
		if x.ID == "agy@wsl:Stopped" {
			a = x
		}
	}
	if a == nil || a.Path != "" || a.Sync != nil {
		t.Fatalf("%+v", a)
	}
	opts := a.Field("model").Options(a.Values())
	var mine, ours bool
	for _, o := range opts {
		mine = mine || o.Value == "gemini-3-pro"
		ours = ours || usesMagpie(o.Value)
	}
	if !mine || !ours || len(*asked) != 0 || len(*opened) != 0 {
		t.Fatalf("options %v, asked %v, opened %v", opts, *asked, *opened)
	}
	if es, _ := os.ReadDir(root); len(es) > 0 {
		t.Fatal("a stopped distro was opened")
	}
	if err := a.Field("model").Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(*asked, ",") != "Stopped" {
		t.Fatalf("not started first: %v", *asked)
	}
	if body := readFile(filepath.Join(root, "home", "me", ".bashrc")); !strings.Contains(body, agyPosixHead) {
		t.Fatalf(".bashrc:\n%s", body)
	}
}
