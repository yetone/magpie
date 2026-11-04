package agent

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

// An agent in a WSL distro under NAT reaches the gateway at Windows'
// address, where gateway.Token is turned away (whqtian on Discord: OpenCode
// in WSL was written "apiKey": "magpie" and refused). Every agent's config
// carries the LAN sharing key there while sharing is on, and gateway.Token on
// loopback, or while nothing is shared.
func TestGatewayKeyBeyondLoopback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	wsl := "http://172.23.80.1:3425"
	local := "http://127.0.0.1:3425"

	keys := func(gw string) map[string]string {
		oc := magpieProviderJSONAt("opencode", "opencode", gw).(map[string]any)["options"].(map[string]any)
		cr := magpieProviderJSONAt("crush", "crush", gw).(map[string]any)
		pi := magpieProviderJSONAt("pi", "pi", gw).(map[string]any)
		out := map[string]string{
			"opencode": oc["apiKey"].(string),
			"crush":    cr["api_key"].(string),
			"pi":       pi["apiKey"].(string),
			"hermes":   hermesProviderAt(gw).APIKey,
		}
		for _, e := range droidEntriesAt(gw) {
			out["droid"] = e.APIKey
		}
		return out
	}
	want := func(gw, key string) {
		t.Helper()
		for agent, k := range keys(gw) {
			if k != key {
				t.Errorf("%s at %s: key %q, want %q", agent, gw, k, key)
			}
		}
	}

	want(wsl, gateway.Token) // nothing shared: nothing else to give
	if err := access.ConfigureLAN(true, false); err != nil {
		t.Fatal(err)
	}
	lan := access.LANSecret()
	if !strings.HasPrefix(lan, access.Prefix) {
		t.Fatalf("LAN key %q", lan)
	}
	want(wsl, lan)
	want(local, gateway.Token)
	want("http://localhost:3425", gateway.Token)
	if k := qoderProviderAt("qoder", "", wsl+"/v1")["apiKey"]; k != lan {
		t.Errorf("Qoder in WSL: key %q, want the LAN key", k)
	}
	if k := qoderProviderAt("qoder", "", local+"/v1")["apiKey"]; k != gateway.TokenFor("qoder") {
		t.Errorf("Qoder on loopback: key %q", k)
	}
	if !ourKey(lan) || !ourKey(gateway.Token) || ourKey("sk-someone-else") || ourKey("") {
		t.Error("ourKey tells magpie's keys from others wrongly")
	}
	if !qoderKeyed(lan, "qoder") {
		t.Error("a Qoder given the LAN key isn't seen as on magpie")
	}

	if err := access.ConfigureLAN(false, false); err != nil {
		t.Fatal(err)
	}
	want(wsl, gateway.Token)
}

// T3 Code's magpie instance is Claude Code routed with the gateway's
// address and the token Claude Code is sent with: beyond loopback that is
// the LAN sharing key, as at.gwKey() routes Claude Code itself.
// gateway.Token is turned away there, so T3's every call was answered 401
// (#1647, ggbdpq).
func TestT3CodeKeyBeyondLoopback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	path := filepath.Join(home, ".t3", "userdata", "settings.json")
	wsl := "http://172.23.80.1:3425"

	env := func(gw string) map[string]string {
		t.Helper()
		b, err := json.Marshal(t3InstanceJSON(path, gw))
		if err != nil {
			t.Fatal(err)
		}
		e := t3Format(t, b)
		out := map[string]string{}
		for _, v := range e.Environment {
			out[v.Name] = v.Value
		}
		return out
	}

	if err := access.ConfigureLAN(true, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { access.ConfigureLAN(false, false) })
	lan := access.LANSecret()
	if !strings.HasPrefix(lan, access.Prefix) {
		t.Fatalf("LAN key %q", lan)
	}
	if e := env(wsl); e["ANTHROPIC_AUTH_TOKEN"] != lan {
		t.Errorf("T3 Code at %s: token %q, want the LAN sharing key", wsl, e["ANTHROPIC_AUTH_TOKEN"])
	}
	if e := env("http://127.0.0.1:3425"); e["ANTHROPIC_AUTH_TOKEN"] != gateway.Token {
		t.Errorf("T3 Code on loopback: token %q, want %q", e["ANTHROPIC_AUTH_TOKEN"], gateway.Token)
	}
	if err := access.ConfigureLAN(false, false); err != nil {
		t.Fatal(err)
	}
	if e := env(wsl); e["ANTHROPIC_AUTH_TOKEN"] != gateway.Token {
		t.Errorf("T3 Code at %s with nothing shared: token %q, want %q", wsl, e["ANTHROPIC_AUTH_TOKEN"], gateway.Token)
	}
}

// Air's and OpenChamber's OpenCode provider block carries the key for the
// gateway's address, and each row's Check compares against it: beyond
// loopback, the LAN sharing key. Against gateway.Token there, the rows said
// "magpie's gateway won't take its requests" of the config Sync had just
// written and the gateway does take (#1647, ggbdpq).
func TestAirAndOpenChamberChecksTakeTheKeyForTheGateway(t *testing.T) {
	home, _ := codexHome(t, "", "")
	t.Setenv("MAGPIE_ADDR", "172.23.80.1:3425")
	if err := access.ConfigureLAN(true, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { access.ConfigureLAN(false, false) })
	lan := access.LANSecret()
	if !strings.HasPrefix(lan, access.Prefix) {
		t.Fatalf("LAN key %q", lan)
	}
	if gateway.URL() != "http://172.23.80.1:3425" {
		t.Fatalf("gateway URL %q", gateway.URL())
	}

	airDir := airDir(home, filepath.Join(home, ".config"))
	was := airOpenCode
	airOpenCode = func(string) string { return "/opt/bin/opencode" }
	t.Cleanup(func() { airOpenCode = was })
	a := air(home, filepath.Join(home, ".config"))
	if err := a.Apply("model", "magpie/fake/m1"); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(airDir, "magpie-opencode.json")
	if v, _ := edit.GetJSON(cfg, "provider.magpie.options.apiKey"); v != lan {
		t.Fatalf("Air's config has key %q, want the LAN sharing key", v)
	}
	if d := a.Check(); d != "" {
		t.Errorf("Air's check: %s", d)
	}

	t.Setenv("OPENCODE_CONFIG_DIR", filepath.Join(home, "oc"))
	dir := filepath.Join(home, ".config", "openchamber")
	writeFile(t, filepath.Join(dir, "preferences.json"), `{"version":1,"fields":{"defaultModel":{"value":"anthropic/claude-sonnet-5","updatedAt":7}}}`)
	writeFile(t, filepath.Join(dir, "settings.json"), `{"defaultModel":"anthropic/claude-sonnet-5"}`)
	ch := openChamber(home, filepath.Join(home, ".config"))
	if err := ch.Apply("model", "magpie/fake/m1"); err != nil {
		t.Fatal(err)
	}
	ocCfg := filepath.Join(home, "oc", "opencode.json")
	if v, _ := edit.GetJSON(ocCfg, "provider.magpie.options.apiKey"); v != lan {
		t.Fatalf("OpenChamber's OpenCode config has key %q, want the LAN sharing key", v)
	}
	if d := ch.Check(); d != "" {
		t.Errorf("OpenChamber's check: %s", d)
	}
}

// Every agent given the gateway's own address, gateway.URL(), with the key
// that names it (gateway.TokenFor) or gateway.Token, is given the LAN
// sharing key instead while MAGPIE_ADDR pins that address beyond loopback
// and sharing is on: the gateway takes neither from there (#1647's class).
func TestPinnedGatewayKeyForEveryWriter(t *testing.T) {
	home, _ := codexHome(t, "", "")
	t.Setenv("MAGPIE_ADDR", "172.23.80.1:3425")
	gw := gateway.URL()
	if gw != "http://172.23.80.1:3425" {
		t.Fatalf("gateway URL %q", gw)
	}
	keys := func() map[string]string {
		var cindy struct {
			Auth struct {
				APIKey string `json:"apiKey"`
			} `json:"auth"`
		}
		data := CindyLink(gw)[strings.Index(CindyLink(gw), "data=")+len("data="):]
		b, err := base64.RawURLEncoding.DecodeString(data)
		if err != nil || json.Unmarshal(b, &cindy) != nil {
			t.Fatalf("Cindy's link: %v %s", err, b)
		}
		jb := filepath.Join(home, "jb", "copilot.json")
		if err := copilotJBWrite(jb); err != nil {
			t.Fatal(err)
		}
		jbKey, _ := edit.GetJSON(jb, copilotJBKey)
		_, zc := zcodeAddress(gw, gateway.Token)
		agy := ""
		if m := regexp.MustCompile(`GEMINI_API_KEY="?([^" ;]+)`).FindStringSubmatch(AgyLaunch("m")); m != nil {
			agy = m[1]
		}
		return map[string]string{
			"agy":        agy,
			"aionui":     aionuiKey(AionUiLink(gw)),
			"alma":       almaKey(),
			"cindy":      cindy.Auth.APIKey,
			"copilotjb":  jbKey,
			"cursor":     cursorLocalEnv(gw)[cursorLocalVars[1]],
			"hanako":     hanakoProvider().APIKey,
			"liveagent":  liveAgentEntryFor(gw).APIKey,
			"newmax":     newMaxEntryFor(gw).APIKey,
			"openclaw":   openClawProviderAt(gw)["apiKey"],
			"openpencil": openPencilEntry(gw).APIKey,
			"pencil":     pencilProviderJSON(gw)["apiKey"].(string),
			"vscode":     vscodeStable.token(),
			"vscodium":   vscodiumKind.token(),
			"workbuddy":  buddyModel("codebuddy", "m", "m", 0, 0, false, nil)["apiKey"].(string),
			"zcode":      zc,
		}
	}
	plain := keys()
	if err := access.ConfigureLAN(true, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { access.ConfigureLAN(false, false) })
	lan := access.LANSecret()
	if !strings.HasPrefix(lan, access.Prefix) {
		t.Fatalf("LAN key %q", lan)
	}
	for agent, k := range keys() {
		if k != lan {
			t.Errorf("%s at %s: key %q, want the LAN sharing key", agent, gw, k)
		}
		if plain[agent] == lan || !strings.HasPrefix(plain[agent], gateway.Token) {
			t.Errorf("%s with nothing shared: key %q, want magpie's own", agent, plain[agent])
		}
	}
}

// aionuiKey is the api_key AionUi's link hands it.
func aionuiKey(link string) string {
	v, _ := url.ParseQuery(strings.TrimPrefix(link, AionUiScheme))
	return v.Get("api_key")
}
