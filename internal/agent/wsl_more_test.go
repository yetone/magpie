package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

// wslMore is each CLI #515 brings into WSL distros: the file under the
// distro's home that carries magpie's provider once one of magpie's models
// is picked, and what of the gateway's address it names (its /v1, or the
// bare address for one spoken to as Anthropic messages).
var wslMore = []struct {
	id, file, suffix string
}{
	{"opencode", ".config/opencode/opencode.json", "/v1"},
	{"mimocode", ".config/mimocode/mimocode.json", "/v1"},
	{"kimi", ".kimi-code/config.toml", "/v1"},
	{"omp", ".omp/agent/models.yml", "/v1"},
	{"crush", ".config/crush/crush.json", "/v1"},
	{"hermes", ".hermes/config.yaml", "/v1"},
	{"morph", ".morph/config.yaml", "/v1"},
	{"grok", ".grok/config.toml", "/v1"},
	{"droid", ".factory/settings.json", "/v1"},
	{"fx", ".fx/settings.json", "/v1"},
	{"commandcode", ".commandcode/providers.json", "/v1"},
	{"minimax-code", ".minimax/config.yaml", ""},
	{"dsh", ".dsh/config.yaml", "/v1"},
	{"muse", ".config/muse/settings.json", "/v1"},
	{"qoder", ".qoder/settings.json", "/v1"},
	{"qoder-cn", ".qoder-cn/settings.json", "/v1"},
}

// moveHostDirs points the variables that move these agents' folders on
// this machine at a folder of its own, which a distro's agent must leave
// alone: a distro's variables are its own, not Windows'.
func moveHostDirs(t *testing.T) string {
	t.Helper()
	moved := t.TempDir()
	for _, v := range []string{"OPENCODE_CONFIG_DIR", "MIMOCODE_HOME", "KIMI_CODE_HOME", "KIMI_SHARE_DIR", "PI_CODING_AGENT_DIR",
		"HERMES_HOME", "GROK_HOME", "FACTORY_HOME_OVERRIDE", "MINIMAX_DATA_DIR", "QODER_CONFIG_DIR", "QODERCN_CONFIG_DIR", "DSH_HOME", "MISTER_MORPH_CONFIG"} {
		t.Setenv(v, filepath.Join(moved, v))
	}
	return moved
}

// The probe asks after each of them by its folder and, but for grok and
// fx, whose names other tools have too, its command; and finds each by
// either.
func TestWSLProbeFindsMoreAgents(t *testing.T) {
	for _, c := range wslMore {
		k := wslKindOf(c.id)
		if !strings.Contains(wslProbeScript, `[ -d "$HOME/`+k.dir+`" ] && echo dir:`+k.dir+`; `) {
			t.Errorf("%s: probe lacks its folder %s", c.id, k.dir)
		}
		if !strings.HasPrefix(c.file, k.dir+"/") {
			t.Errorf("%s: its file %s isn't under %s", c.id, c.file, k.dir)
		}
		if k.bin != "" && !strings.Contains(wslProbeScript, `command -v `+k.bin+` `) {
			t.Errorf("%s: probe lacks its command %s", c.id, k.bin)
		}
		if d := parseProbe("U", "home:/home/me\ndir:"+k.dir+"\n"); d == nil || !k.found(*d) {
			t.Errorf("%s: not found by its folder", c.id)
		}
		if k.bin != "" {
			if d := parseProbe("U", "home:/home/me\nbin:"+k.bin+" /usr/bin/"+k.bin+"\n"); d == nil || !k.found(*d) {
				t.Errorf("%s: not found by its command", c.id)
			}
		}
	}
	for _, bin := range []string{"fx", "grok"} {
		if strings.Contains(wslProbeScript, `command -v `+bin+` `) {
			t.Errorf("probe takes any %s command for the agent", bin)
		}
	}
	// Kimi Code's old kimi-cli, in ~/.kimi, is found by its command
	if d := parseProbe("U", "home:/home/me\nbin:kimi /home/me/.local/bin/kimi\n"); d == nil || !wslKindOf("kimi").found(*d) {
		t.Error("kimi by its command")
	}
}

// A running distro with them lists each as <name> · WSL <distro>, with no
// command, User-Agent or aliases of its own (those are its Windows twin's).
func TestWSLMoreAgentsDiscovered(t *testing.T) {
	syncHome(t)
	root := t.TempDir()
	probe := "home:/home/me\nroute:default via 172.20.0.1 dev eth0\n"
	var want []string
	for _, c := range wslMore {
		probe += "dir:" + wslKindOf(c.id).dir + "\n"
		want = append(want, c.id+"@wsl:Ubuntu")
	}
	_, opened := fakeWSL(t, "Ubuntu\r\n", "Ubuntu\r\n", map[string]string{"Ubuntu": probe}, map[string]string{"Ubuntu": root})
	ds := wslDistros()
	if len(ds) != 1 || strings.Join(*opened, ",") != "Ubuntu" {
		t.Fatalf("%+v %v", ds, *opened)
	}
	var ids []string
	for _, a := range wslAgentsOf(ds) {
		ids = append(ids, a.ID)
		k := wslKindOf(strings.TrimSuffix(a.ID, "@wsl:Ubuntu"))
		if a.Name != k.name+" · WSL Ubuntu" || a.WSL != "Ubuntu" || a.Bin != "" || a.UA != nil || a.Aliases != nil ||
			!strings.HasPrefix(a.Path, filepath.Join(root, "home", "me")+string(filepath.Separator)) {
			t.Errorf("%+v", a)
		}
	}
	if got := strings.Join(ids, ","); got != strings.Join(want, ",") {
		t.Fatalf("agents %s\nwant %s", got, strings.Join(want, ","))
	}
}

// Picking one of magpie's models writes each one's own file in the
// distro's home, as on this machine but naming the gateway as the distro
// reaches it: Windows' address under NAT, 127.0.0.1 when mirrored. Its
// wiring checks out against that address, a sync keeps it, and nothing of
// this machine's — its home, or the folders its variables name — is
// touched.
func TestWSLMoreAgentsPick(t *testing.T) {
	for _, c := range wslMore {
		for _, mirrored := range []bool{false, true} {
			home := syncHome(t)
			moved := moveHostDirs(t)
			root := t.TempDir()
			dhome := filepath.Join(root, "home", "me")
			k := wslKindOf(c.id)
			os.MkdirAll(filepath.Join(dhome, filepath.FromSlash(k.dir)), 0o755)
			d := distro{Name: "Ubuntu", Root: root, Home: "/home/me", Has: map[string]bool{"dir:" + k.dir: true},
				Gateway: "172.20.0.1", Mirrored: mirrored, Running: true}
			a := wslAgent(k, d)
			if err := a.Field("model").Set("magpie/relay/glm-4.6"); err != nil {
				t.Fatalf("%s: %v", c.id, err)
			}
			path := filepath.Join(dhome, filepath.FromSlash(c.file))
			gw := gateway.URL()
			if !mirrored {
				gw = "http://172.20.0.1:" + gateway.Port()
			}
			body := readFile(path)
			if !strings.Contains(body, gw+c.suffix) || !strings.Contains(body, "glm-4.6") {
				t.Fatalf("%s mirrored %v: %s lacks %s:\n%s", c.id, mirrored, c.file, gw+c.suffix, body)
			}
			if !mirrored && strings.Contains(body, "127.0.0.1") {
				t.Fatalf("%s: this machine's address under NAT:\n%s", c.id, body)
			}
			if v := a.Field("model").Get(); !usesMagpie(v) {
				t.Fatalf("%s: model %q", c.id, v)
			}
			if msg := a.Check(); msg != "" {
				t.Fatalf("%s mirrored %v: %s", c.id, mirrored, msg)
			}
			if a.Sync != nil {
				if err := a.Sync(); err != nil || !strings.Contains(readFile(path), gw+c.suffix) {
					t.Fatalf("%s sync %v:\n%s", c.id, err, readFile(path))
				}
			}
			if !mirrored && !strings.Contains(a.Notice(), "mirrored") {
				t.Errorf("%s: %s", c.id, a.Notice())
			}
			if mirrored && !strings.HasPrefix(a.Notice(), k.name+" in WSL Ubuntu ") {
				t.Errorf("%s: %s", c.id, a.Notice())
			}
			for _, dir := range []string{filepath.Join(home, filepath.FromSlash(k.dir)), moved} {
				if es, _ := os.ReadDir(dir); len(es) > 0 {
					t.Fatalf("%s: this machine's %s was touched", c.id, dir)
				}
			}
		}
	}
}

// A stopped distro's agents are never asked anything nor opened: each is
// listed as magpie last saw it, its options come from magpie alone, and a
// pick starts the distro, then writes its file there.
func TestWSLMoreAgentsStopped(t *testing.T) {
	for _, c := range wslMore {
		syncHome(t)
		root := t.TempDir()
		k := wslKindOf(c.id)
		b, _ := json.Marshal(map[string]*distro{
			"Stopped": {Name: "Stopped", Home: "/home/me", Root: root, Has: map[string]bool{"dir:" + k.dir: true},
				Values: map[string]string{k.memo("model"): "own/last-seen"}},
		})
		os.MkdirAll(filepath.Dir(wslStatePath()), 0o755)
		os.WriteFile(wslStatePath(), b, 0o600)
		asked, opened := fakeWSL(t, "Stopped\r\n", "", nil, nil)
		var a *Agent
		for _, x := range wslAgentsOf(wslDistros()) {
			if x.ID == c.id+"@wsl:Stopped" {
				a = x
			}
		}
		if a == nil || a.Path != "" || a.Dir != "" || a.Check != nil || a.Sync != nil {
			t.Fatalf("%s: %+v", c.id, a)
		}
		if v := a.Values(); v["model"] != "own/last-seen" {
			t.Fatalf("%s: values %v", c.id, v)
		}
		for _, f := range a.Fields {
			if f.Options != nil {
				f.Options(a.Values())
			}
		}
		if !strings.Contains(a.Notice(), "isn't running") || len(*asked) != 0 || len(*opened) != 0 {
			t.Fatalf("%s: %s, asked %v opened %v", c.id, a.Notice(), *asked, *opened)
		}
		if es, _ := os.ReadDir(root); len(es) > 0 {
			t.Fatalf("%s: a stopped distro was opened", c.id)
		}
		if err := a.Field("model").Set("magpie/relay/glm-4.6"); err != nil {
			t.Fatalf("%s: %v", c.id, err)
		}
		if strings.Join(*asked, ",") != "Stopped" {
			t.Fatalf("%s: not started first: %v", c.id, *asked)
		}
		if body := readFile(filepath.Join(root, "home", "me", filepath.FromSlash(c.file))); !strings.Contains(body, "glm-4.6") {
			t.Fatalf("%s: nothing written to %s", c.id, c.file)
		}
		if v := a.Field("model").Get(); !usesMagpie(v) {
			t.Fatalf("%s: last seen %q", c.id, v)
		}
	}
}

// A stopped distro's agent is built on its default files, as they can't
// be looked at; a pick, once it has started the distro, writes the ones
// the agent reads: an opencode.jsonc already there, the old kimi-cli's
// ~/.kimi where there is no ~/.kimi-code.
func TestWSLStoppedPickFindsItsFiles(t *testing.T) {
	syncHome(t)
	root := t.TempDir()
	dhome := filepath.Join(root, "home", "me")
	jsonc := filepath.Join(dhome, ".config", "opencode", "opencode.jsonc")
	legacy := filepath.Join(dhome, ".kimi", "config.toml")
	os.MkdirAll(filepath.Dir(jsonc), 0o755)
	os.MkdirAll(filepath.Dir(legacy), 0o755)
	os.WriteFile(jsonc, []byte("{\n  // mine\n  \"model\": \"anthropic/claude-sonnet-5\"\n}\n"), 0o644)
	os.WriteFile(legacy, []byte("default_model = \"kimi-k3\"\n"), 0o644)
	b, _ := json.Marshal(map[string]*distro{
		"Stopped": {Name: "Stopped", Home: "/home/me", Root: root, Has: map[string]bool{"dir:.config/opencode": true, "bin:kimi": true}, Probe: wslProbeVersion},
	})
	os.MkdirAll(filepath.Dir(wslStatePath()), 0o755)
	os.WriteFile(wslStatePath(), b, 0o600)
	fakeWSL(t, "Stopped\r\n", "", nil, nil)
	byID := map[string]*Agent{}
	for _, a := range wslAgentsOf(wslDistros()) {
		byID[a.ID] = a
	}
	oc, ki := byID["opencode@wsl:Stopped"], byID["kimi@wsl:Stopped"]
	if oc == nil || ki == nil {
		t.Fatalf("%v", byID)
	}
	if err := oc.Field("model").Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	if m, _ := edit.GetJSON(jsonc, "model"); m != "magpie/relay/glm-4.6" || !strings.Contains(readFile(jsonc), "// mine") {
		t.Fatalf("opencode.jsonc:\n%s", readFile(jsonc))
	}
	if _, err := os.Stat(filepath.Join(dhome, ".config", "opencode", "opencode.json")); !os.IsNotExist(err) {
		t.Fatal("a second opencode.json beside the .jsonc")
	}
	if err := ki.Field("model").Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetTOMLTop(legacy, "default_model"); v != "magpie/relay/glm-4.6" {
		t.Fatalf("~/.kimi/config.toml:\n%s", readFile(legacy))
	}
	if _, err := os.Stat(filepath.Join(dhome, ".kimi-code")); !os.IsNotExist(err) {
		t.Fatal("a ~/.kimi-code the old kimi-cli never reads")
	}
}

// lgtm on Discord: wsl中安装的dsh，目前magpie还是扫描不到. A dsh of today's
// in a distro keeps its profiles' patch lists in the distro's ~/.dsh: a pick
// puts magpie's route in them naming the gateway as the distro reaches it,
// and neither a sync nor this machine's 30-second round puts it back at
// this machine's address.
func TestWSLDshProfiles(t *testing.T) {
	home := syncHome(t)
	moved := moveHostDirs(t)
	root := t.TempDir()
	web := filepath.Join(root, "home", "me", ".dsh", "profiles", "web", "cordis.patch.yml")
	os.MkdirAll(filepath.Dir(web), 0o755)
	os.WriteFile(web, []byte("# Your patch layer for this dsh profile.\n[]\n"), 0o644)
	d := distro{Name: "Ubuntu", Root: root, Home: "/home/me", Has: map[string]bool{"bin:dsh": true},
		Gateway: "172.20.0.1", Running: true}
	a := wslAgent(wslKindOf("dsh"), d)
	if a.ID != "dsh@wsl:Ubuntu" || a.Path != web {
		t.Fatalf("%+v", a)
	}
	if err := a.Field("model").Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	gw := "http://172.20.0.1:" + gateway.Port() + "/v1"
	body := readFile(web)
	if !strings.Contains(body, "baseURL: "+gw) || !strings.Contains(body, `model: "relay/glm-4.6"`) || strings.Contains(body, "127.0.0.1") {
		t.Fatalf("lacks %s:\n%s", gw, body)
	}
	if v := a.Field("model").Get(); v != "magpie/relay/glm-4.6" {
		t.Fatalf("model %q", v)
	}
	if msg := a.Check(); msg != "" {
		t.Fatal(msg)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	dshWiredOnce()
	if got := readFile(web); got != body {
		t.Fatalf("written again:\n%s", got)
	}
	for _, dir := range []string{filepath.Join(home, ".dsh"), moved} {
		if es, _ := os.ReadDir(dir); len(es) > 0 {
			t.Fatalf("this machine's %s was touched", dir)
		}
	}
}
