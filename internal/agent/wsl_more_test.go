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
	// lgtm on Discord: a CodeBuddy Code in WSL wasn't found, nor were the
	// other CLIs magpie set up only on this machine
	{"codebuddy", ".codebuddy/models.json", "/v1/chat/completions"},
	{"cline", ".cline/data/settings/providers.json", "/v1"},
	{"atomcode", ".atomcode/config.toml", "/v1"},
	{"goose", ".config/goose/custom_providers/magpie.json", "/v1"},
	{"reasonix", ".reasonix/config.toml", "/v1"},
}

// wslFinds is what a probe of a distro with the agent of kind k prints,
// and the distro it makes: its folder, or for one found by its command
// alone, the command and the version it gives.
func wslFinds(k wslKind) (probe string, has map[string]bool, versions map[string]string) {
	if k.dir != "" {
		return "dir:" + k.dir + "\n", map[string]bool{"dir:" + k.dir: true}, nil
	}
	probe = "bin:" + k.bin + " /home/me/.local/bin/" + k.bin + "\n"
	has = map[string]bool{"bin:" + k.bin: true}
	if k.version {
		probe += "ver:" + k.id + " " + k.bin + " v2.1.0\n"
		versions = map[string]string{k.id: "2.1.0"}
	}
	return probe, has, versions
}

// moveHostDirs points the variables that move these agents' folders on
// this machine at a folder of its own, which a distro's agent must leave
// alone: a distro's variables are its own, not Windows'.
func moveHostDirs(t *testing.T) string {
	t.Helper()
	moved := t.TempDir()
	for _, v := range []string{"OPENCODE_CONFIG_DIR", "MIMOCODE_HOME", "KIMI_CODE_HOME", "KIMI_SHARE_DIR", "PI_CODING_AGENT_DIR",
		"HERMES_HOME", "GROK_HOME", "FACTORY_HOME_OVERRIDE", "MINIMAX_DATA_DIR", "QODER_CONFIG_DIR", "QODERCN_CONFIG_DIR", "DSH_HOME", "MISTER_MORPH_CONFIG",
		"CODEBUDDY_CONFIG_DIR", "CLINE_DIR", "CLINE_DATA_DIR", "ATOMCODE_HOME", "REASONIX_HOME", "APPDATA"} {
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
		if k.dir != "" && !strings.Contains(wslProbeScript, `[ -d "$HOME/`+k.dir+`" ] && echo dir:`+k.dir+`; `) {
			t.Errorf("%s: probe lacks its folder %s", c.id, k.dir)
		}
		if k.dir != "" && !strings.HasPrefix(c.file, k.dir+"/") {
			t.Errorf("%s: its file %s isn't under %s", c.id, c.file, k.dir)
		}
		if k.bin != "" && !strings.Contains(wslProbeScript, `command -v `+k.bin+` `) {
			t.Errorf("%s: probe lacks its command %s", c.id, k.bin)
		}
		if p, _, _ := wslFinds(k); k.dir != "" || k.version {
			if d := parseProbe("U", "home:/home/me\n"+p); d == nil || !k.found(*d) {
				t.Errorf("%s: not found by %q", c.id, p)
			}
		}
		if k.bin != "" && !k.version {
			if d := parseProbe("U", "home:/home/me\nbin:"+k.bin+" /usr/bin/"+k.bin+"\n"); d == nil || !k.found(*d) {
				t.Errorf("%s: not found by its command", c.id)
			}
		}
	}
	for _, bin := range []string{"fx", "grok", "goose"} {
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
		p, _, _ := wslFinds(wslKindOf(c.id))
		probe += p
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
			if k.dir != "" {
				os.MkdirAll(filepath.Join(dhome, filepath.FromSlash(k.dir)), 0o755)
			}
			_, has, versions := wslFinds(k)
			d := distro{Name: "Ubuntu", Root: root, Home: "/home/me", Has: has, Versions: versions,
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
			if a.Check != nil {
				if msg := a.Check(); msg != "" {
					t.Fatalf("%s mirrored %v: %s", c.id, mirrored, msg)
				}
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
			own := k.dir
			if own == "" {
				own, _, _ = strings.Cut(c.file, "/")
			}
			for _, dir := range []string{filepath.Join(home, filepath.FromSlash(own)), moved} {
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
		_, has, versions := wslFinds(k)
		b, _ := json.Marshal(map[string]*distro{
			"Stopped": {Name: "Stopped", Home: "/home/me", Root: root, Has: has, Versions: versions, Probe: wslProbeVersion,
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

// Gemini CLI in a distro is found by its command alone (Antigravity keeps
// ~/.gemini too), and a magpie model points the distro's ~/.gemini/.env at
// the gateway as the distro reaches it, with the key it takes from there;
// a model of its own puts back what that replaced.
func TestWSLGemini(t *testing.T) {
	home := syncHome(t)
	k := wslKindOf("gemini")
	if strings.Contains(wslProbeScript, `"$HOME/" ]`) {
		t.Fatal("the probe asks after $HOME itself")
	}
	if d := parseProbe("U", "home:/home/me\ndir:.gemini\n"); d != nil && k.found(*d) {
		t.Fatal("found by a ~/.gemini Antigravity keeps too")
	}
	if d := parseProbe("U", "home:/home/me\nbin:gemini /home/me/.npm-global/bin/gemini\n"); d == nil || !k.found(*d) {
		t.Fatal("not found by its command")
	}
	root := t.TempDir()
	dhome := filepath.Join(root, "home", "me")
	env := filepath.Join(dhome, ".gemini", ".env")
	os.MkdirAll(filepath.Dir(env), 0o755)
	os.WriteFile(env, []byte("GEMINI_API_KEY=mine\n"), 0o600)
	d := distro{Name: "Ubuntu", Root: root, Home: "/home/me", Has: map[string]bool{"bin:gemini": true},
		Gateway: "172.20.0.1", Running: true}
	a := wslAgent(k, d)
	if !a.Detected() {
		t.Fatal("not detected")
	}
	if err := a.Field("model").Set("relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	gw := "http://172.20.0.1:" + gateway.Port()
	if v, _ := edit.GetEnvFile(env, "GOOGLE_GEMINI_BASE_URL"); v != gw {
		t.Fatalf(".env:\n%s", readFile(env))
	}
	if v, _ := edit.GetJSON(filepath.Join(dhome, ".gemini", "settings.json"), "model.name"); v != "relay/glm-4.6" {
		t.Fatalf("model %q", v)
	}
	if msg := a.Check(); msg != "" {
		t.Fatal(msg)
	}
	if v := a.Field("provider").Get(); v != magpieID {
		t.Fatalf("auth %q", v)
	}
	if err := a.Field("model").Set("gemini-2.5-pro"); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetEnvFile(env, "GEMINI_API_KEY"); v != "mine" {
		t.Fatalf("the key of its own isn't back:\n%s", readFile(env))
	}
	if _, set := edit.GetEnvFile(env, "GOOGLE_GEMINI_BASE_URL"); set {
		t.Fatalf(".env:\n%s", readFile(env))
	}
	if es, _ := os.ReadDir(filepath.Join(home, ".gemini")); len(es) > 0 {
		t.Fatal("this machine's ~/.gemini was touched")
	}
}

// A distro's address under NAT changes when WSL restarts: the next sync
// names the gateway at the new one, and the old is gone from the file.
func TestWSLMoreAgentsFollowTheAddress(t *testing.T) {
	for _, c := range wslMore {
		syncHome(t)
		moveHostDirs(t)
		root := t.TempDir()
		k := wslKindOf(c.id)
		if k.dir != "" {
			os.MkdirAll(filepath.Join(root, "home", "me", filepath.FromSlash(k.dir)), 0o755)
		}
		_, has, versions := wslFinds(k)
		d := distro{Name: "Ubuntu", Root: root, Home: "/home/me", Has: has, Versions: versions, Gateway: "172.20.0.1", Running: true}
		if err := wslAgent(k, d).Field("model").Set("magpie/relay/glm-4.6"); err != nil {
			t.Fatalf("%s: %v", c.id, err)
		}
		d.Gateway = "172.29.0.1"
		a := wslAgent(k, d)
		if a.Sync == nil {
			continue
		}
		if err := a.Sync(); err != nil {
			t.Fatalf("%s: %v", c.id, err)
		}
		body := readFile(filepath.Join(root, "home", "me", filepath.FromSlash(c.file)))
		if !strings.Contains(body, "http://172.29.0.1:"+gateway.Port()+c.suffix) || strings.Contains(body, "172.20.0.1") {
			t.Errorf("%s: %s after the address moved:\n%s", c.id, c.file, body)
		}
	}
}

// Gemini CLI in a distro whose address moved is on magpie still, and
// picking its model again names the new address without taking the old
// one for the user's own: a model of its own then puts back their key.
func TestWSLGeminiAddressMoved(t *testing.T) {
	syncHome(t)
	k := wslKindOf("gemini")
	root := t.TempDir()
	env := filepath.Join(root, "home", "me", ".gemini", ".env")
	writeFile(t, env, "GEMINI_API_KEY=mine\n")
	d := distro{Name: "Ubuntu", Root: root, Home: "/home/me", Has: map[string]bool{"bin:gemini": true},
		Gateway: "172.20.0.1", Running: true}
	if err := wslAgent(k, d).Field("model").Set("relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	d.Gateway = "172.29.0.1"
	a := wslAgent(k, d)
	if v := a.Field("provider").Get(); v != magpieID {
		t.Fatalf("auth %q", v)
	}
	if a.Check() == "" {
		t.Fatal("the old address passes")
	}
	if err := a.Field("model").Set("relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetEnvFile(env, "GOOGLE_GEMINI_BASE_URL"); v != "http://172.29.0.1:"+gateway.Port() {
		t.Fatalf(".env:\n%s", readFile(env))
	}
	if msg := a.Check(); msg != "" {
		t.Fatal(msg)
	}
	if err := a.Field("model").Set("gemini-2.5-pro"); err != nil {
		t.Fatal(err)
	}
	if body := readFile(env); body != "GEMINI_API_KEY=mine\n" {
		t.Fatalf(".env:\n%s", body)
	}
}
