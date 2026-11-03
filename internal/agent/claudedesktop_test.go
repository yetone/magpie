package agent

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/desktopdir"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// desktopSandbox is a home of its own with nothing of this machine's in
// reach: every variable Desktop's folders (and magpie's stash) follow.
func desktopSandbox(t *testing.T) (string, desktopPaths) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	p := desktopPathsOf(desktopDirs(runtime.GOOS, home, os.Getenv))
	return home, p
}

// desktopTree is every file (with its bytes) and folder under Desktop's two.
func desktopTree(t *testing.T, p desktopPaths) map[string]string {
	out := map[string]string{}
	for _, root := range []string{p.dir, p.dir3p} {
		filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				out[path] = "/"
				return nil
			}
			b, _ := os.ReadFile(path)
			out[path] = string(b)
			return nil
		})
	}
	return out
}

func desktopJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	var m map[string]any
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v\n%s", path, err, b)
	}
	return m
}

func desktopWrite(t *testing.T, path, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDesktopDirs(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	home := t.TempDir()

	d, d3 := desktopDirs("darwin", home, env(nil))
	as := filepath.Join(home, "Library", "Application Support")
	if d != filepath.Join(as, "Claude") || d3 != filepath.Join(as, "Claude-3p") {
		t.Errorf("darwin: %s %s", d, d3)
	}

	xdg := filepath.Join(home, "xdg")
	if d, d3 = desktopDirs("linux", home, env(map[string]string{"XDG_CONFIG_HOME": xdg})); d != filepath.Join(xdg, "Claude") || d3 != filepath.Join(xdg, "Claude-3p") {
		t.Errorf("linux, XDG_CONFIG_HOME: %s %s", d, d3)
	}
	for _, x := range []string{"", "relative/dir"} {
		if d, d3 = desktopDirs("linux", home, env(map[string]string{"XDG_CONFIG_HOME": x})); d != filepath.Join(home, ".config", "Claude") || d3 != filepath.Join(home, ".config", "Claude-3p") {
			t.Errorf("linux, XDG_CONFIG_HOME %q: %s %s", x, d, d3)
		}
	}

	// Windows: %LOCALAPPDATA%\Claude and Claude-3p, else ~\AppData\Local
	local := filepath.Join(home, "Local")
	if d, d3 = desktopDirs("windows", home, env(map[string]string{"LOCALAPPDATA": local})); d != filepath.Join(local, "Claude") || d3 != filepath.Join(local, "Claude-3p") {
		t.Errorf("windows: %s %s", d, d3)
	}
	if d, _ = desktopDirs("windows", home, env(nil)); d != filepath.Join(home, "AppData", "Local", "Claude") {
		t.Errorf("windows without LOCALAPPDATA: %s", d)
	}
	// a folder named otherwise (Claude… with or without -3p) is taken when
	// the exact one isn't there, the exact one first when it is
	os.MkdirAll(filepath.Join(local, "ClaudeBeta"), 0o755)
	os.MkdirAll(filepath.Join(local, "ClaudeBeta-3p"), 0o755)
	os.MkdirAll(filepath.Join(local, "claude-cli-nodejs"), 0o755)
	if d, d3 = desktopDirs("windows", home, env(map[string]string{"LOCALAPPDATA": local})); d != filepath.Join(local, "ClaudeBeta") || d3 != filepath.Join(local, "ClaudeBeta-3p") {
		t.Errorf("windows, other names: %s %s", d, d3)
	}
	os.MkdirAll(filepath.Join(local, "Claude"), 0o755)
	if d, _ = desktopDirs("windows", home, env(map[string]string{"LOCALAPPDATA": local})); d != filepath.Join(local, "Claude") {
		t.Errorf("windows, exact: %s", d)
	}
}

// A fresh Claude Desktop: its folder and nothing magpie writes in it yet.
func TestClaudeDesktopFresh(t *testing.T) {
	home, p := desktopSandbox(t)
	a := claudeDesktop(home)
	if a.Detected() {
		t.Fatal("detected without its folder")
	}
	os.MkdirAll(p.dir, 0o755)
	if !a.Detected() {
		t.Fatal("not detected")
	}
	before := desktopTree(t, p)
	f := a.Field("provider")
	if f.Get() != "" {
		t.Fatalf("get: %q", f.Get())
	}

	if err := f.Set("magpie"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{p.config, p.config3p} {
		if m := desktopJSON(t, c); !reflect.DeepEqual(m, map[string]any{"deploymentMode": "3p"}) {
			t.Errorf("%s: %v", c, m)
		}
	}
	want := map[string]any{
		"inferenceProvider": "gateway", "inferenceGatewayBaseUrl": gateway.URL(),
		"inferenceGatewayApiKey": "magpie-claude-desktop", "inferenceGatewayAuthScheme": "bearer",
		"disableDeploymentModeChooser": true, "coworkEgressAllowedHosts": []any{"*"},
		"claudeAiImport":    map[string]any{"enabled": true, "bannerBehavior": "detect"},
		"toolSearchEnabled": true,
	}
	if prof := desktopJSON(t, p.prof); !reflect.DeepEqual(prof, want) {
		t.Errorf("profile: %v", prof)
	}
	if st, _ := os.Stat(p.prof); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Errorf("profile mode %v", st.Mode())
	}
	meta := desktopJSON(t, p.meta)
	if meta["appliedId"] != desktopProfileID || !reflect.DeepEqual(meta["entries"], []any{map[string]any{"id": desktopProfileID, "name": "magpie"}}) {
		t.Errorf("meta: %v", meta)
	}
	if f.Get() != "magpie" || a.Check() != "" {
		t.Fatalf("get %q, check %q", f.Get(), a.Check())
	}
	if n := a.Notice(); n == "" {
		t.Error("no restart notice")
	}

	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if after := desktopTree(t, p); !reflect.DeepEqual(after, before) {
		t.Fatalf("off left:\n%v\nwas:\n%v", after, before)
	}
	if f.Get() != "" {
		t.Fatalf("get after off: %q", f.Get())
	}
	// off again changes nothing
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if after := desktopTree(t, p); !reflect.DeepEqual(after, before) {
		t.Fatalf("second off: %v", after)
	}
}

// A Desktop with settings and third-party profiles of its own: they are all
// kept, and off puts every byte back.
func TestClaudeDesktopKeepsTheUsers(t *testing.T) {
	home, p := desktopSandbox(t)
	desktopWrite(t, p.config, "{\n  \"globalShortcut\": \"Alt+Space\",\n  \"deploymentMode\": \"1p\",\n  \"mcpServers\": {\n    \"fs\": {\n      \"command\": \"npx\"\n    }\n  }\n}\n")
	desktopWrite(t, p.config3p, "{\n  \"preferences\": {\n    \"theme\": \"dark\"\n  }\n}\n")
	desktopWrite(t, p.meta, "{\n  \"appliedId\": \"mine-2\",\n  \"entries\": [\n    {\n      \"id\": \"mine-1\",\n      \"name\": \"Bedrock\"\n    },\n    {\n      \"id\": \"mine-2\",\n      \"name\": \"Corp gateway\"\n    }\n  ]\n}\n")
	desktopWrite(t, filepath.Join(p.library, "mine-2.json"), `{"inferenceProvider":"gateway","inferenceGatewayBaseUrl":"https://corp.example"}`)
	before := desktopTree(t, p)

	a := claudeDesktop(home)
	f := a.Field("provider")
	if err := f.Set("magpie"); err != nil {
		t.Fatal(err)
	}
	cfg := desktopJSON(t, p.config)
	if cfg["deploymentMode"] != "3p" || cfg["globalShortcut"] != "Alt+Space" || cfg["mcpServers"] == nil {
		t.Errorf("config: %v", cfg)
	}
	if cfg := desktopJSON(t, p.config3p); cfg["deploymentMode"] != "3p" || cfg["preferences"] == nil {
		t.Errorf("3p config: %v", cfg)
	}
	meta := desktopJSON(t, p.meta)
	entries, _ := meta["entries"].([]any)
	if meta["appliedId"] != desktopProfileID || len(entries) != 3 ||
		entries[0].(map[string]any)["id"] != "mine-1" || entries[2].(map[string]any)["id"] != desktopProfileID {
		t.Errorf("meta: %v", meta)
	}
	if b, _ := os.ReadFile(filepath.Join(p.library, "mine-2.json")); string(b) != before[filepath.Join(p.library, "mine-2.json")] {
		t.Error("the user's profile was touched")
	}
	on := desktopTree(t, p)

	// on again changes nothing
	if err := f.Set("magpie"); err != nil {
		t.Fatal(err)
	}
	if again := desktopTree(t, p); !reflect.DeepEqual(again, on) {
		t.Fatalf("on twice:\n%v\nonce:\n%v", again, on)
	}

	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	after := desktopTree(t, p)
	for k, v := range before {
		if after[k] != v {
			t.Errorf("%s after off:\n%s\nwas:\n%s", k, after[k], v)
		}
	}
	if len(after) != len(before) {
		t.Errorf("off left %d paths, was %d: %v", len(after), len(before), after)
	}

	// on → off → on is the first on again
	if err := f.Set("magpie"); err != nil {
		t.Fatal(err)
	}
	if again := desktopTree(t, p); !reflect.DeepEqual(again, on) {
		t.Fatalf("on after off:\n%v\nfirst:\n%v", again, on)
	}
}

// dev_quj on X: "Claude Desktop 已不经过 magpie：配置已变更" after a
// restart, with Desktop on magpie. Desktop 4.4212 drops a stored
// deploymentMode itself and never reads the Claude folder's, so the check
// follows Desktop's own rule (desktop3p): off magpie only when Claude-3p
// says "1p" and the profile leaves Desktop's chooser on.
func TestClaudeDesktopModeAsDesktopReadsIt(t *testing.T) {
	home, p := desktopSandbox(t)
	a := claudeDesktop(home)
	if err := a.Field("provider").Set("magpie"); err != nil {
		t.Fatal(err)
	}
	quiet := func(why string) {
		t.Helper()
		if c := a.Check(); c != "" {
			t.Errorf("%s: check %q", why, c)
		}
		if d := a.Drift(); d != nil && d.Kind == "unwired" {
			t.Errorf("%s: drift %+v", why, d)
		}
	}
	said := func(why string) {
		t.Helper()
		if c := a.Check(); !strings.Contains(c, "is 1p now") {
			t.Errorf("%s: check %q", why, c)
		}
		if d := a.Drift(); d == nil || d.Kind != "unwired" {
			t.Errorf("%s: drift %+v", why, d)
		}
	}
	quiet("on")
	// what 4.4212 writes at start ("deploymentMode written { mode: undefined }")
	desktopWrite(t, p.config3p, `{"preferences":{"x":1}}`)
	quiet("Claude-3p's deploymentMode dropped by Desktop")
	desktopWrite(t, p.config, `{}`)
	quiet("the Claude folder's deploymentMode gone")
	desktopWrite(t, p.config3p, `{"deploymentMode":"1p"}`)
	quiet("1p with the chooser off: Desktop starts in 3p")

	if err := edit.SetJSON(p.prof, edit.KV{Path: "disableDeploymentModeChooser", Value: false}); err != nil {
		t.Fatal(err)
	}
	said("1p picked in Desktop's chooser")
	desktopWrite(t, p.config3p, `{}`)
	quiet("none with the chooser on: Desktop starts in 3p")
	desktopWrite(t, p.config3p, `{"deploymentMode":"1p"}`)
	if err := a.Reapply(); err != nil {
		t.Fatal(err)
	}
	quiet("after Reconnect")
	// another gateway applied is still said
	meta := desktopJSON(t, p.meta)
	meta["appliedId"] = "mine"
	b, _ := json.Marshal(meta)
	desktopWrite(t, p.meta, string(b))
	if c := a.Check(); !strings.Contains(c, "another gateway") {
		t.Errorf("another profile applied: check %q", c)
	}
}

// Desktop or the user changing things after magpie: Check says so, off
// leaves what they chose, and the profile the user's own settings in it.
func TestClaudeDesktopChangedSince(t *testing.T) {
	home, p := desktopSandbox(t)
	os.MkdirAll(p.dir, 0o755)
	desktopWrite(t, p.meta, `{"entries":[{"id":"mine","name":"Mine"}],"appliedId":"mine"}`)
	a := claudeDesktop(home)
	f := a.Field("provider")
	if err := f.Set("magpie"); err != nil {
		t.Fatal(err)
	}
	// a setting made in Desktop's window stays through a second on
	desktopWrite(t, p.prof, `{"inferenceProvider":"gateway","inferenceGatewayBaseUrl":"http://elsewhere:1","userAutoMode":true,"coworkEgressAllowedHosts":["corp.example"]}`)
	if c := a.Check(); c == "" {
		t.Error("check: a profile pointing elsewhere is fine")
	}
	if err := f.Set("magpie"); err != nil {
		t.Fatal(err)
	}
	prof := desktopJSON(t, p.prof)
	if prof["userAutoMode"] != true || !reflect.DeepEqual(prof["coworkEgressAllowedHosts"], []any{"corp.example"}) ||
		prof["inferenceGatewayBaseUrl"] != gateway.URL() || prof["disableDeploymentModeChooser"] != true {
		t.Errorf("profile: %v", prof)
	}
	if c := a.Check(); c != "" {
		t.Errorf("check after on: %s", c)
	}
	// the Claude folder's deploymentMode is one Desktop doesn't read
	desktopWrite(t, p.config, `{"deploymentMode":"1p"}`)
	if c := a.Check(); c != "" {
		t.Errorf("check: the Claude folder's 1p is said: %s", c)
	}
	// Claude-3p's is, with the profile's chooser on
	if err := edit.SetJSON(p.prof, edit.KV{Path: "disableDeploymentModeChooser", Value: false}); err != nil {
		t.Fatal(err)
	}
	desktopWrite(t, p.config3p, `{"deploymentMode":"1p"}`)
	if c := a.Check(); c == "" {
		t.Error("check: 1p is fine")
	}
	desktopWrite(t, p.config3p, `{"deploymentMode":"3p"}`)
	// off: the user's 1p stays, their profile is applied again
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if m := desktopJSON(t, p.config); m["deploymentMode"] != "1p" {
		t.Errorf("config: %v", m)
	}
	if _, err := os.Stat(p.config3p); !os.IsNotExist(err) {
		t.Error("the 3p config magpie made is still there")
	}
	if meta := desktopJSON(t, p.meta); meta["appliedId"] != "mine" || len(meta["entries"].([]any)) != 1 {
		t.Errorf("meta: %v", meta)
	}
	if _, err := os.Stat(p.prof); !os.IsNotExist(err) {
		t.Error("magpie's profile is still there")
	}
}

// #2035 (capiton0): settings the user made in Desktop's third-party window
// live in magpie's profile. Switching Desktop off magpie and on again
// brings them back, the gateway's address and key set anew.
func TestClaudeDesktopOffOnKeepsTheUsersSettings(t *testing.T) {
	home, p := desktopSandbox(t)
	os.MkdirAll(p.dir, 0o755)
	a := claudeDesktop(home)
	f := a.Field("provider")
	if err := f.Set("magpie"); err != nil {
		t.Fatal(err)
	}
	desktopWrite(t, p.prof, `{"inferenceProvider":"gateway","inferenceGatewayBaseUrl":"http://old:1","inferenceGatewayApiKey":"old","inferenceGatewayAuthScheme":"bearer",`+
		`"disableDeploymentModeChooser":false,"coworkEgressAllowedHosts":["corp.example"],"inferenceModels":["claude-opus-4-7"],"isClaudeCodeForDesktopEnabled":true}`)
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.prof); !os.IsNotExist(err) {
		t.Fatal("magpie's profile is still there after off")
	}
	if err := f.Set("magpie"); err != nil {
		t.Fatal(err)
	}
	prof := desktopJSON(t, p.prof)
	want := map[string]any{
		"inferenceProvider": "gateway", "inferenceGatewayBaseUrl": gateway.URL(),
		"inferenceGatewayApiKey": "magpie-claude-desktop", "inferenceGatewayAuthScheme": "bearer",
		"disableDeploymentModeChooser": false, "coworkEgressAllowedHosts": []any{"corp.example"},
		"inferenceModels": []any{"claude-opus-4-7"}, "isClaudeCodeForDesktopEnabled": true,
	}
	if !reflect.DeepEqual(prof, want) {
		t.Errorf("profile after off and on:\n%v\nwant\n%v", prof, want)
	}
	if f.Get() != "magpie" || a.Check() != "" {
		t.Errorf("get %q, check %q", f.Get(), a.Check())
	}
}

// #2035 (capiton0): a profile the user made in Desktop's window for
// magpie's gateway, applied when magpie is switched on, starts magpie's
// profile: their settings (as they posted them) are kept, only the
// gateway's address, key and auth scheme set. Their own profile is left
// as it was and applied again when magpie is switched off.
func TestClaudeDesktopOnKeepsTheUsersOwnGatewayProfile(t *testing.T) {
	home, p := desktopSandbox(t)
	const theirs = `{
  "inferenceGatewayBaseUrl": "` + "%s" + `",
  "inferenceGatewayApiKey": "magpie",
  "sshHostAllowlist": ["*"],
  "chatTabEnabled": true,
  "chatAdvancedFileAnalysisEnabled": true,
  "isDesktopExtensionEnabled": true,
  "modelPrefer1mContext": true,
  "defaultModelEffort": "max",
  "autoModeEnabled": true,
  "toolSearchEnabled": true,
  "skipWebFetchPreflight": true,
  "coworkEgressAllowedHosts": ["*"],
  "disableDeploymentModeChooser": true,
  "disableDeepLinkRegistration": true,
  "inferenceProvider": "gateway",
  "inferenceCredentialKind": "static"
}`
	own := filepath.Join(p.library, "5b0d0c6e-1f2a-4c3b-9d4e-0a1b2c3d4e5f.json")
	body := fmt.Sprintf(theirs, gateway.URL())
	desktopWrite(t, own, body)
	desktopWrite(t, p.meta, `{"appliedId":"5b0d0c6e-1f2a-4c3b-9d4e-0a1b2c3d4e5f","entries":[{"id":"5b0d0c6e-1f2a-4c3b-9d4e-0a1b2c3d4e5f","name":"mine"}]}`)
	a := claudeDesktop(home)
	f := a.Field("provider")
	if err := f.Set("magpie"); err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	json.Unmarshal([]byte(body), &want)
	want["inferenceGatewayApiKey"] = "magpie-claude-desktop"
	want["inferenceGatewayAuthScheme"] = "bearer"
	want["claudeAiImport"] = map[string]any{"enabled": true, "bannerBehavior": "detect"}
	if prof := desktopJSON(t, p.prof); !reflect.DeepEqual(prof, want) {
		t.Errorf("magpie's profile:\n%v\nwant\n%v", prof, want)
	}
	if b, _ := os.ReadFile(own); string(b) != body {
		t.Errorf("their profile was changed:\n%s", b)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if id, _ := desktopJSON(t, p.meta)["appliedId"].(string); id != "5b0d0c6e-1f2a-4c3b-9d4e-0a1b2c3d4e5f" {
		t.Errorf("applied after off: %q", id)
	}
	if b, _ := os.ReadFile(own); string(b) != body {
		t.Errorf("their profile after off:\n%s", b)
	}
}

// A profile for somewhere else (another gateway, Bedrock) is not carried
// into magpie's: its models and settings are for that place.
func TestClaudeDesktopOnLeavesOtherProfilesOut(t *testing.T) {
	for name, prof := range map[string]string{
		"other gateway": `{"inferenceProvider":"gateway","inferenceGatewayBaseUrl":"https://llm.example.com","inferenceModels":["corp-model"],"chatTabEnabled":true}`,
		"bedrock":       `{"inferenceProvider":"bedrock","inferenceBedrockRegion":"us-east-1","chatTabEnabled":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			home, p := desktopSandbox(t)
			desktopWrite(t, filepath.Join(p.library, "abc.json"), prof)
			desktopWrite(t, p.meta, `{"appliedId":"abc","entries":[{"id":"abc","name":"mine"}]}`)
			if err := claudeDesktop(home).Field("provider").Set("magpie"); err != nil {
				t.Fatal(err)
			}
			got := desktopJSON(t, p.prof)
			if _, ok := got["chatTabEnabled"]; ok {
				t.Errorf("magpie's profile took another place's settings: %v", got)
			}
		})
	}
}

// A file magpie can't read as Desktop writes it is left alone, and so is
// everything else.
func TestClaudeDesktopRefusesOddFiles(t *testing.T) {
	home, p := desktopSandbox(t)
	desktopWrite(t, p.meta, `[]`)
	if err := claudeDesktop(home).Field("provider").Set("magpie"); err == nil {
		t.Fatal("a meta file that isn't an object was taken")
	}
	if b, _ := os.ReadFile(p.meta); string(b) != "[]" {
		t.Errorf("meta: %s", b)
	}
	for _, f := range []string{p.config, p.config3p, p.prof} {
		if _, err := os.Stat(f); err == nil {
			t.Errorf("%s written", f)
		}
	}
	desktopWrite(t, p.meta, `{"entries":{}}`)
	if err := claudeDesktop(home).Field("provider").Set("magpie"); err == nil {
		t.Fatal("entries that aren't a list were taken")
	}
}

// each of Claude Code's tiers in Desktop's Code tab can have a model of its
// own (WilianWeng): offered once Desktop is on magpie, set by the catalog id
// or by the id Desktop is shown, and taken away with ""
func TestClaudeDesktopTiers(t *testing.T) {
	home, _ := desktopSandbox(t)
	if err := provider.Save(provider.Provider{ID: "v", Name: "V", Chat: "http://127.0.0.1:1/v1", Key: "k", Models: []string{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
	a := claudeDesktop(home)
	f := a.Field("sonnet")
	if f == nil || !f.Quiet {
		t.Fatal("no quiet sonnet field")
	}
	if len(f.Options(nil)) != 0 {
		t.Fatal("tiers offered before Desktop is on magpie")
	}
	if err := a.Field("provider").Set("magpie"); err != nil {
		t.Fatal(err)
	}
	if len(f.Options(nil)) != 2 {
		t.Fatalf("options: %+v", f.Options(nil))
	}
	if err := f.Set("v/a"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("haiku").Set(gateway.DesktopID(provider.Entry{ID: "v/b"})); err != nil {
		t.Fatal(err)
	}
	if f.Get() != "v/a" || a.Field("haiku").Get() != "v/b" || a.Field("opus").Get() != "" {
		t.Fatalf("tiers: %v", gateway.DesktopTiers())
	}
	if err := f.Set("nope/x"); err == nil {
		t.Fatal("a model magpie doesn't serve was taken")
	}
	if err := f.Set(""); err != nil || f.Get() != "" {
		t.Fatalf("unset: %v %q", err, f.Get())
	}
}

// Kilig on Discord: Claude Desktop from its setup.exe on Windows 10 LTSC
// runs as an MSIX package, its data in
// %LOCALAPPDATA%\Packages\Claude_<publisher>\LocalCache\Roaming\Claude and
// nothing in %APPDATA%\Claude. It is detected, and magpie's switch writes
// the package's folders, which the packaged app up to 2.x reads. bg5eau on
// X: 2.31226.1, packaged the same way, reads the real
// %LOCALAPPDATA%\Claude-3p, so the switch writes the real folders too, and
// switching off leaves both as they were.
func TestClaudeDesktopMSIX(t *testing.T) {
	home, _ := desktopSandbox(t)
	old := desktopdir.OS
	desktopdir.OS = "windows"
	t.Cleanup(func() { desktopdir.OS = old })
	local, roaming := filepath.Join(home, "AppData", "Local"), filepath.Join(home, "AppData", "Roaming")
	os.MkdirAll(roaming, 0o755)
	pkg := filepath.Join(local, "Packages", "Claude_pzs8sxrjxfjjc", "LocalCache")
	data := filepath.Join(pkg, "Roaming", "Claude")
	for _, d := range []string{"claude-code-sessions", "local-agent-mode-sessions", "Local Storage", "IndexedDB", "logs"} {
		os.MkdirAll(filepath.Join(data, d), 0o755)
	}
	os.WriteFile(filepath.Join(data, "Local State"), []byte(`{}`), 0o644)
	os.MkdirAll(filepath.Join(pkg, "Local", "Claude", "logs"), 0o755)
	// the newer build's own 3p folder: its settings, which stay
	desktopWrite(t, filepath.Join(local, "Claude-3p", "claude_desktop_config.json"), `{"preferences":{"x":1}}`)
	p := desktopPathsOf(filepath.Join(pkg, "Local", "Claude"), filepath.Join(pkg, "Local", "Claude-3p"))
	r := desktopPathsOf(filepath.Join(local, "Claude"), filepath.Join(local, "Claude-3p"))
	before, beforeReal := desktopTree(t, p), desktopTree(t, r)

	a := claudeDesktop(home)
	if !a.Detected() {
		t.Fatal("an MSIX Claude Desktop isn't detected")
	}
	if want := filepath.Join(pkg, "Local", "Claude", "claude_desktop_config.json"); a.Path != want {
		t.Errorf("path %s, want %s", a.Path, want)
	}
	if err := a.Field("provider").Set("magpie"); err != nil {
		t.Fatal(err)
	}
	if a.Field("provider").Get() != magpieID {
		t.Fatal("not on magpie")
	}
	for _, q := range []desktopPaths{p, r} {
		if !desktopWired(q) {
			t.Fatalf("not wired in %s: %v", q.dir3p, desktopTree(t, q))
		}
		for _, f := range []string{q.config, q.config3p} {
			if m := desktopJSON(t, f); m["deploymentMode"] != "3p" {
				t.Errorf("%s: %v", f, m)
			}
		}
		if m := desktopJSON(t, q.prof); m["inferenceGatewayBaseUrl"] != gateway.URL() || m["inferenceGatewayAuthScheme"] != "bearer" {
			t.Errorf("%s: %v", q.prof, m)
		}
		if m := desktopJSON(t, q.meta); m["appliedId"] != desktopProfileID {
			t.Errorf("%s: %v", q.meta, m)
		}
	}
	if m := desktopJSON(t, r.config3p); m["preferences"] == nil {
		t.Errorf("the real Claude-3p's own settings lost: %v", m)
	}
	if c := a.Check(); c != "" {
		t.Errorf("check: %s", c)
	}
	// Desktop's chooser turns the real one back to 1p: the check says so
	if err := edit.SetJSON(r.prof, edit.KV{Path: "disableDeploymentModeChooser", Value: false}); err != nil {
		t.Fatal(err)
	}
	desktopWrite(t, r.config3p, `{"deploymentMode":"1p","preferences":{"x":1}}`)
	if c := a.Check(); !strings.Contains(c, "is 1p now") || !strings.Contains(c, r.dir3p) {
		t.Errorf("check: %q", c)
	}
	desktopWrite(t, r.config3p, `{"deploymentMode":"3p","preferences":{"x":1}}`)

	if err := a.Field("provider").Set(""); err != nil {
		t.Fatal(err)
	}
	if got := desktopTree(t, p); !reflect.DeepEqual(got, before) {
		t.Errorf("package's folders not as they were:\n got %v\nwant %v", got, before)
	}
	if got := desktopTree(t, r); !reflect.DeepEqual(got, beforeReal) {
		t.Errorf("real folders not as they were:\n got %v\nwant %v", got, beforeReal)
	}
}

// A packaged Desktop switched on by a magpie that wrote only the
// package's folders keeps its stash: switching on now writes the real ones
// too, and off still leaves the package's as they were before magpie.
func TestClaudeDesktopMSIXStashBefore(t *testing.T) {
	home, _ := desktopSandbox(t)
	old := desktopdir.OS
	desktopdir.OS = "windows"
	t.Cleanup(func() { desktopdir.OS = old })
	local := filepath.Join(home, "AppData", "Local")
	pkg := filepath.Join(local, "Packages", "Claude_pzs8sxrjxfjjc", "LocalCache")
	desktopWrite(t, filepath.Join(pkg, "Roaming", "Claude", "Local State"), `{}`)
	p := desktopPathsOf(filepath.Join(pkg, "Local", "Claude"), filepath.Join(pkg, "Local", "Claude-3p"))
	desktopWrite(t, p.config3p, `{"deploymentMode":"1p"}`)
	before := desktopTree(t, p)
	// as the magpie before wrote it: the package's set, the plain keys
	if err := desktopOn(p); err != nil {
		t.Fatal(err)
	}
	r := desktopPathsOf(filepath.Join(local, "Claude"), filepath.Join(local, "Claude-3p"))

	a := claudeDesktop(home)
	if err := a.Field("provider").Set("magpie"); err != nil {
		t.Fatal(err)
	}
	if !desktopWired(r) {
		t.Fatal("real folders not written")
	}
	if err := a.Field("provider").Set(""); err != nil {
		t.Fatal(err)
	}
	if got := desktopTree(t, p); !reflect.DeepEqual(got, before) {
		t.Errorf("package's folders not as they were:\n got %v\nwant %v", got, before)
	}
	for _, d := range []string{r.dir, r.dir3p} {
		if _, err := os.Stat(d); err == nil {
			t.Errorf("%s left behind", d)
		}
	}
}

// dumplings on Discord: Desktop 2.31226.0 from its MSIX on Windows runs in
// the real %LOCALAPPDATA%\Claude-3p (its "Local State" there is the newest),
// and the package's LocalCache copy, which magpie wrote too, is emptied.
// Desktop is on magpie and the card doesn't say it was changed; the copy
// Desktop reads turned back to 1p still does, and Reconnect with a tier on
// one of magpie's models puts the gateway back, not only the tier.
func TestClaudeDesktopMSIXChecksTheFoldersDesktopRuns(t *testing.T) {
	home, _ := desktopSandbox(t)
	old := desktopdir.OS
	desktopdir.OS = "windows"
	t.Cleanup(func() { desktopdir.OS = old })
	if err := provider.Save(provider.Provider{ID: "v", Name: "V", Chat: "http://127.0.0.1:1/v1", Key: "k", Models: []string{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(home, "AppData", "Local")
	os.MkdirAll(filepath.Join(home, "AppData", "Roaming"), 0o755)
	pkg := filepath.Join(local, "Packages", "Claude_pzs8sxrjxfjjc", "LocalCache")
	pkg3p, real3p := filepath.Join(pkg, "Local", "Claude-3p"), filepath.Join(local, "Claude-3p")
	desktopWrite(t, filepath.Join(pkg, "Roaming", "Claude", "Local State"), `{}`)
	desktopWrite(t, filepath.Join(pkg3p, "Local State"), `{}`)
	desktopWrite(t, filepath.Join(real3p, "Local State"), `{}`)
	desktopWrite(t, filepath.Join(real3p, "claude_desktop_config.json"), `{"preferences":{"x":1}}`)
	then := time.Now().Add(-48 * time.Hour)
	for _, f := range []string{filepath.Join(pkg, "Roaming", "Claude", "Local State"), filepath.Join(pkg3p, "Local State")} {
		os.Chtimes(f, then, then)
	}
	a := claudeDesktop(home)
	if err := a.Field("provider").Set("magpie"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("sonnet").Set("v/a"); err != nil {
		t.Fatal(err)
	}
	if c := a.Check(); c != "" {
		t.Fatalf("check: %s", c)
	}
	// the update leaves the package's Claude-3p empty
	if err := os.RemoveAll(pkg3p); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(pkg3p, 0o755)
	if c := a.Check(); c != "" {
		t.Errorf("the package's copy Desktop no longer reads: %s", c)
	}
	if d := a.Drift(); d != nil && d.Kind == "unwired" {
		t.Errorf("drift: %+v", d)
	}
	// the one Desktop runs in, turned back to 1p, is said
	r := desktopPathsOf(filepath.Join(local, "Claude"), real3p)
	if err := edit.SetJSON(r.prof, edit.KV{Path: "disableDeploymentModeChooser", Value: false}); err != nil {
		t.Fatal(err)
	}
	desktopWrite(t, r.config3p, `{"deploymentMode":"1p","preferences":{"x":1}}`)
	if c := a.Check(); !strings.Contains(c, "is 1p now") || !strings.Contains(c, real3p) {
		t.Fatalf("check: %q", c)
	}
	if d := a.Drift(); d == nil || d.Kind != "unwired" {
		t.Fatalf("drift: %+v", d)
	}
	// Reconnect: the gateway back, with the tier
	if err := a.Reapply(); err != nil {
		t.Fatal(err)
	}
	if c := a.Check(); c != "" {
		t.Errorf("after Reconnect: %s", c)
	}
	if m := desktopJSON(t, r.config3p); m["deploymentMode"] != "3p" || m["preferences"] == nil {
		t.Errorf("%s: %v", r.config3p, m)
	}
	if got := a.Field("sonnet").Get(); got != "v/a" {
		t.Errorf("sonnet: %q", got)
	}
}

// Claude-3p is a userData of its own, so Desktop on magpie started in the
// system's language, not the one picked signed in with Anthropic (memory
// and Osal on Discord: Chinese gone). The language is carried into
// Claude-3p's config.json, nothing else of Claude's is, and off takes the
// file magpie made for it away again.
func TestClaudeDesktopCarriesTheLanguage(t *testing.T) {
	home, p := desktopSandbox(t)
	own := "{\n  \"locale\": \"zh-CN\",\n  \"userThemeMode\": \"dark\"\n}\n"
	desktopWrite(t, p.app, own)
	before := desktopTree(t, p)

	f := claudeDesktop(home).Field("provider")
	if err := f.Set("magpie"); err != nil {
		t.Fatal(err)
	}
	if m := desktopJSON(t, p.app3p); !reflect.DeepEqual(m, map[string]any{"locale": "zh-CN"}) {
		t.Errorf("Claude-3p's config.json: %v", m)
	}
	if b, _ := os.ReadFile(p.app); string(b) != own {
		t.Errorf("Claude's config.json changed:\n%s", b)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if after := desktopTree(t, p); !reflect.DeepEqual(after, before) {
		t.Fatalf("off left:\n%v\nwas:\n%v", after, before)
	}
}

// A language already set in 3p mode (Desktop's, or the user's by hand) is
// theirs: no write of magpie's changes Claude-3p's config.json, on, again,
// off and on again, and off leaves it there.
func TestClaudeDesktopKeepsThe3pLanguage(t *testing.T) {
	home, p := desktopSandbox(t)
	desktopWrite(t, p.app, `{"locale":"zh-CN"}`)
	mine := "{\n  \"locale\": \"ja-JP\",\n  \"windowBounds\": {\n    \"width\": 900\n  }\n}\n"
	desktopWrite(t, p.app3p, mine)

	f := claudeDesktop(home).Field("provider")
	for i, v := range []string{"magpie", "magpie", "", "magpie"} {
		if err := f.Set(v); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(p.app3p); string(b) != mine {
			t.Fatalf("step %d (%q): Claude-3p's config.json is now\n%s", i, v, b)
		}
	}
}

// Wired, and Desktop has written its config.json since: the user then
// sets the language there by hand. Applying magpie again keeps every key;
// off keeps the file, which is no longer magpie's alone.
func TestClaudeDesktopUserEditsThenReapply(t *testing.T) {
	home, p := desktopSandbox(t)
	f := claudeDesktop(home).Field("provider")
	if err := f.Set("magpie"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.app3p); err == nil {
		t.Fatal("config.json written with no language to carry")
	}
	desktopWrite(t, p.app, `{"locale":"zh-CN"}`)
	edited := "{\n  \"locale\": \"zh-TW\",\n  \"hasCompletedOnboarding\": true\n}\n"
	desktopWrite(t, p.app3p, edited)
	for _, v := range []string{"magpie", "", "magpie"} {
		if err := f.Set(v); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(p.app3p); string(b) != edited {
			t.Fatalf("%q: the user's edit is gone:\n%s", v, b)
		}
	}
}

// Only a language Desktop can name is carried, and a Claude-3p config.json
// magpie can't read as an object is left as it is, the takeover going on.
func TestClaudeDesktopLanguageOddValues(t *testing.T) {
	for _, own := range []string{`{"locale":"../../x"}`, `{"locale":7}`, `{"locale":""}`, `not json`} {
		home, p := desktopSandbox(t)
		desktopWrite(t, p.app, own)
		if err := claudeDesktop(home).Field("provider").Set("magpie"); err != nil {
			t.Fatal(err)
		}
		if b, err := os.ReadFile(p.app3p); err == nil {
			t.Errorf("%s: carried %s", own, b)
		}
	}
	home, p := desktopSandbox(t)
	desktopWrite(t, p.app, `{"locale":"zh-CN"}`)
	desktopWrite(t, p.app3p, `[1]`)
	if err := claudeDesktop(home).Field("provider").Set("magpie"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p.app3p); string(b) != `[1]` {
		t.Errorf("odd config.json written: %s", b)
	}
	if m := desktopJSON(t, p.config3p); m["deploymentMode"] != "3p" {
		t.Errorf("takeover stopped: %v", m)
	}
}

// bigcat1800 on Discord: Desktop 4.4212 in 3p mode on magpie, 13 MCP
// servers, and every tool's schema went whole on each request (~162K
// tokens). Desktop starts Claude Code with experimental betas off unless
// its toolSearchEnabled policy is on, and Claude Code then keeps tool
// search off whatever ENABLE_TOOL_SEARCH says. magpie's profile gets the
// policy once, on a switch on and, for a Desktop already on magpie, from
// Sync; a value the user set, false too, or took away since, stays theirs.
func TestClaudeDesktopTurnsOnToolSearch(t *testing.T) {
	home, p := desktopSandbox(t)
	a := claudeDesktop(home)
	if err := a.Field("provider").Set("magpie"); err != nil {
		t.Fatal(err)
	}
	if got := desktopJSON(t, p.prof)["toolSearchEnabled"]; got != true {
		t.Fatalf("switch on: toolSearchEnabled %v", got)
	}

	// on magpie before this change: no toolSearchEnabled, no mark
	prof := desktopJSON(t, p.prof)
	delete(prof, "toolSearchEnabled")
	b, _ := json.Marshal(prof)
	desktopWrite(t, p.prof, string(b))
	forget(desktopSearch + p.key)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if got := desktopJSON(t, p.prof)["toolSearchEnabled"]; got != true {
		t.Fatalf("Sync on magpie: toolSearchEnabled %v", got)
	}

	// the user takes it away in Desktop's window: not written back
	desktopWrite(t, p.prof, string(b))
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("provider").Set("magpie"); err != nil {
		t.Fatal(err)
	}
	if _, has := desktopJSON(t, p.prof)["toolSearchEnabled"]; has {
		t.Fatal("seeded again over the user's choice")
	}

	// the user's own false, seen first: kept
	if err := edit.SetJSON(p.prof, edit.KV{Path: "toolSearchEnabled", Value: false}); err != nil {
		t.Fatal(err)
	}
	forget(desktopSearch + p.key)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if got := desktopJSON(t, p.prof)["toolSearchEnabled"]; got != false {
		t.Fatalf("the user's toolSearchEnabled: %v", got)
	}
	if a.Check() != "" {
		t.Fatalf("check %q", a.Check())
	}

	// off magpie: the profile goes, and Sync writes nothing
	if err := a.Field("provider").Set(""); err != nil {
		t.Fatal(err)
	}
	forget(desktopSearch + p.key)
	before := desktopTree(t, p)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if after := desktopTree(t, p); !reflect.DeepEqual(after, before) {
		t.Fatalf("Sync off magpie wrote:\n%v", after)
	}
}

// lambdua on X: Desktop on magpie runs in Claude-3p, a userData of its own,
// so the Code and Cowork sessions it had signed in with Anthropic were out
// of sight. Desktop imports them itself (Settings → Import) once its
// claudeAiImport policy is on: magpie's profile gets it, once, on a switch
// on and, for a Desktop already on magpie, from Sync at the next start.
// A claudeAiImport the user has, or took away since, stays theirs, and
// Desktop's own sessions are never touched.
func TestClaudeDesktopOffersItsSessionImport(t *testing.T) {
	home, p := desktopSandbox(t)
	on := map[string]any{"enabled": true, "bannerBehavior": "detect"}
	// a session Desktop wrote signed in with Anthropic
	session := filepath.Join(p.dir, "claude-code-sessions", "acct", "org", "local_1.json")
	desktopWrite(t, session, `{"sessionId":"local_1","title":"mine"}`)
	a := claudeDesktop(home)
	if a.Sync == nil {
		t.Fatal("no Sync: a Desktop already on magpie never gets the import")
	}
	// not on magpie: Sync writes nothing
	before := desktopTree(t, p)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if after := desktopTree(t, p); !reflect.DeepEqual(after, before) {
		t.Fatalf("Sync off magpie wrote:\n%v", after)
	}

	// on magpie before this change: a profile with no claudeAiImport and
	// nothing in the stash; Sync seeds it
	if err := a.Field("provider").Set("magpie"); err != nil {
		t.Fatal(err)
	}
	if got := desktopJSON(t, p.prof)["claudeAiImport"]; !reflect.DeepEqual(got, on) {
		t.Fatalf("switch on: claudeAiImport %v", got)
	}
	prof := desktopJSON(t, p.prof)
	delete(prof, "claudeAiImport")
	b, _ := json.Marshal(prof)
	desktopWrite(t, p.prof, string(b))
	forget(desktopImport + p.key)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if got := desktopJSON(t, p.prof)["claudeAiImport"]; !reflect.DeepEqual(got, on) {
		t.Fatalf("Sync on magpie: claudeAiImport %v", got)
	}

	// the user turns it off in Desktop's window (the key gone): stays off
	desktopWrite(t, p.prof, string(b))
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("provider").Set("magpie"); err != nil {
		t.Fatal(err)
	}
	if _, has := desktopJSON(t, p.prof)["claudeAiImport"]; has {
		t.Fatal("seeded again over the user's choice")
	}

	// a claudeAiImport of the user's own, seen first: kept as it is
	if err := a.Field("provider").Set(""); err != nil {
		t.Fatal(err)
	}
	forget(desktopImport+p.key, desktopProfile+p.key)
	theirs := map[string]any{"enabled": false}
	if err := a.Field("provider").Set("magpie"); err != nil {
		t.Fatal(err)
	}
	if err := edit.SetJSON(p.prof, edit.KV{Path: "claudeAiImport", Value: theirs}); err != nil {
		t.Fatal(err)
	}
	forget(desktopImport + p.key)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if got := desktopJSON(t, p.prof)["claudeAiImport"]; !reflect.DeepEqual(got, theirs) {
		t.Fatalf("the user's claudeAiImport: %v", got)
	}

	// Desktop's own sessions are where they were, as they were
	if got, _ := os.ReadFile(session); string(got) != `{"sessionId":"local_1","title":"mine"}` {
		t.Fatalf("session: %q", got)
	}
}

// aa86222 on Discord: Desktop on magpie had none of the user's connectors.
// 3p mode reads its MCP servers from Claude-3p's claude_desktop_config.json,
// so the ones added signed in with Anthropic (Claude's file) were out of
// sight. Switching on copies each one Claude-3p doesn't have; one of the
// same name there stays theirs; Claude's file is never written; off takes
// the copies back and leaves the tree as it was.
func TestClaudeDesktopCarriesTheMCPServers(t *testing.T) {
	home, p := desktopSandbox(t)
	own := `{"mcpServers":{"fs":{"command":"npx","args":["-y","@modelcontextprotocol/server-filesystem","/tmp"]},"notes":{"command":"notes-mcp"}}}`
	desktopWrite(t, p.config, own)
	before := desktopTree(t, p)

	a := claudeDesktop(home)
	f := a.Field("provider")
	if err := f.Set("magpie"); err != nil {
		t.Fatal(err)
	}
	m := desktopJSON(t, p.config3p)
	want := desktopJSON(t, p.config)["mcpServers"]
	if !reflect.DeepEqual(m["mcpServers"], want) {
		t.Fatalf("Claude-3p's mcpServers: %v, want %v", m["mcpServers"], want)
	}
	if b, _ := os.ReadFile(p.config); !strings.Contains(string(b), `"notes":{"command":"notes-mcp"}`) {
		t.Fatalf("Claude's servers changed:\n%s", b)
	}
	// on again and Sync: nothing doubled, nothing more written
	once := desktopTree(t, p)
	if err := f.Set("magpie"); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if again := desktopTree(t, p); !reflect.DeepEqual(again, once) {
		t.Fatalf("on again changed:\n%v\nwas:\n%v", again, once)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if after := desktopTree(t, p); !reflect.DeepEqual(after, before) {
		t.Fatalf("off left:\n%v\nwas:\n%v", after, before)
	}
}

// A server Claude-3p has already, by the same name, is the user's (or the
// library's) and stays; one the user changed in 3p mode since it was
// copied is theirs and outlives off; Claude-3p's other servers stay.
func TestClaudeDesktopMCPKeepsThe3pServers(t *testing.T) {
	home, p := desktopSandbox(t)
	desktopWrite(t, p.config, `{"mcpServers":{"a":{"command":"a"},"b":{"command":"b-signed-in"},"c":{"command":"c"}}}`)
	desktopWrite(t, p.config3p, `{"mcpServers":{"b":{"command":"b-3p"},"x":{"command":"x"}}}`)
	f := claudeDesktop(home).Field("provider")
	if err := f.Set("magpie"); err != nil {
		t.Fatal(err)
	}
	got := desktopJSON(t, p.config3p)["mcpServers"]
	want := map[string]any{"a": map[string]any{"command": "a"}, "b": map[string]any{"command": "b-3p"},
		"c": map[string]any{"command": "c"}, "x": map[string]any{"command": "x"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("on: %v", got)
	}
	// the user changes c in 3p mode
	if err := edit.SetJSON(p.config3p, edit.KV{Path: "mcpServers.c.command", Value: "c-mine"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	m := desktopJSON(t, p.config3p)
	got = m["mcpServers"]
	want = map[string]any{"b": map[string]any{"command": "b-3p"}, "c": map[string]any{"command": "c-mine"}, "x": map[string]any{"command": "x"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("off: %v", got)
	}
	if _, has := m["deploymentMode"]; has {
		t.Fatalf("off kept the mode: %v", m)
	}
}

// A Desktop on magpie from before the servers were carried gets them from
// Sync at magpie's next start, once: a server the user then takes out in
// 3p mode isn't brought back by the next Sync.
func TestClaudeDesktopSyncCarriesTheMCPServersOnce(t *testing.T) {
	home, p := desktopSandbox(t)
	a := claudeDesktop(home)
	if err := a.Field("provider").Set("magpie"); err != nil {
		t.Fatal(err)
	}
	forget(desktopMCPSeen + p.key)
	if err := edit.SetJSON(p.config, edit.KV{Path: "mcpServers", Value: map[string]any{"fs": map[string]any{"command": "fs"}}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if got := desktopJSON(t, p.config3p)["mcpServers"]; !reflect.DeepEqual(got, map[string]any{"fs": map[string]any{"command": "fs"}}) {
		t.Fatalf("Sync: %v", got)
	}
	if err := edit.DelJSON(p.config3p, "mcpServers"); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if _, has := desktopJSON(t, p.config3p)["mcpServers"]; has {
		t.Fatal("Sync brought back a server the user took out")
	}
}

// aa86222 on Discord: Desktop's configuration exported on one computer
// (magpie's profile, with the connectors the user added) and imported on
// another is a new configLibrary entry under an id Desktop makes, applied
// beside magpie's own and CC Switch's. It points at this magpie's
// gateway, so Desktop runs on magpie: the row reads connected, and
// Reconnect keeps it applied and its connectors as they are.
func TestClaudeDesktopImportedProfileIsOnMagpie(t *testing.T) {
	const imported = "3f6c2a8e-9b1d-4e57-a0c4-7d2e5f81b9a6"
	const ccswitch = "c1d2e3f4-a5b6-4c7d-8e9f-0a1b2c3d4e5f"
	theirs := func(key string) string {
		return `{
  "inferenceProvider": "gateway",
  "inferenceGatewayBaseUrl": "` + gateway.URL() + `",
  "inferenceGatewayApiKey": "` + key + `",
  "inferenceGatewayAuthScheme": "bearer",
  "disableDeploymentModeChooser": true,
  "coworkEgressAllowedHosts": ["*"],
  "claudeAiImport": {"enabled": true, "bannerBehavior": "detect"},
  "managedMcpServers": [
    {"name": "linear", "url": "https://mcp.linear.app/mcp", "transport": "http"},
    {"name": "notion", "url": "https://mcp.notion.com/mcp", "transport": "http"}
  ]
}`
	}
	setup := func(t *testing.T, key string) (*Agent, desktopPaths, string) {
		home, p := desktopSandbox(t)
		os.MkdirAll(p.dir, 0o755)
		a := claudeDesktop(home)
		if err := a.Field("provider").Set("magpie"); err != nil {
			t.Fatal(err)
		}
		// what Desktop wrote: CC Switch's profile and the imported one,
		// the imported one applied
		desktopWrite(t, filepath.Join(p.library, ccswitch+".json"),
			`{"inferenceProvider":"gateway","inferenceGatewayBaseUrl":"http://127.0.0.1:15721/claude-desktop","inferenceGatewayApiKey":"cc-switch"}`)
		f := filepath.Join(p.library, imported+".json")
		desktopWrite(t, f, theirs(key))
		desktopWrite(t, p.meta, `{"appliedId":"`+imported+`","entries":[{"id":"`+ccswitch+`","name":"CC Switch"},`+
			`{"id":"`+desktopProfileID+`","name":"magpie"},{"id":"`+imported+`","name":"magpie"}]}`)
		return a, p, f
	}
	applied := func(t *testing.T, p desktopPaths) string {
		id, _ := desktopJSON(t, p.meta)["appliedId"].(string)
		return id
	}

	t.Run("this magpie's key", func(t *testing.T) {
		a, p, f := setup(t, agentKey("claude-desktop"))
		if c := a.Check(); c != "" {
			t.Errorf("check: %s", c)
		}
		if err := a.Sync(); err != nil {
			t.Fatal(err)
		}
		// Reconnect: nothing of theirs changes, theirs stays applied
		if err := a.Field("provider").Set("magpie"); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(f); string(b) != theirs(agentKey("claude-desktop")) {
			t.Errorf("the imported profile was changed:\n%s", b)
		}
		if id := applied(t, p); id != imported {
			t.Errorf("applied after reconnect: %q", id)
		}
		if c := a.Check(); c != "" {
			t.Errorf("check after reconnect: %s", c)
		}
		// off: theirs stays applied and as it was
		if err := a.Field("provider").Set(""); err != nil {
			t.Fatal(err)
		}
		if id := applied(t, p); id != imported {
			t.Errorf("applied after off: %q", id)
		}
		if b, _ := os.ReadFile(f); string(b) != theirs(agentKey("claude-desktop")) {
			t.Errorf("the imported profile after off:\n%s", b)
		}
	})

	t.Run("another magpie's key", func(t *testing.T) {
		a, p, f := setup(t, "mk-from-the-other-computer")
		if c := a.Check(); c == "" {
			t.Error("check: a key the gateway refuses is fine")
		}
		// magpie's start gives it a key the gateway takes, and nothing else
		if err := a.Sync(); err != nil {
			t.Fatal(err)
		}
		var want map[string]any
		json.Unmarshal([]byte(theirs(agentKey("claude-desktop"))), &want)
		if got := desktopJSON(t, f); !reflect.DeepEqual(got, want) {
			t.Errorf("imported profile after sync:\n%v\nwant\n%v", got, want)
		}
		if c := a.Check(); c != "" {
			t.Errorf("check after sync: %s", c)
		}
		if id := applied(t, p); id != imported {
			t.Errorf("applied after sync: %q", id)
		}
	})

	t.Run("reconnect fixes the key", func(t *testing.T) {
		a, p, f := setup(t, "mk-from-the-other-computer")
		if err := a.Field("provider").Set("magpie"); err != nil {
			t.Fatal(err)
		}
		var want map[string]any
		json.Unmarshal([]byte(theirs(agentKey("claude-desktop"))), &want)
		if got := desktopJSON(t, f); !reflect.DeepEqual(got, want) {
			t.Errorf("imported profile after reconnect:\n%v\nwant\n%v", got, want)
		}
		if id := applied(t, p); id != imported || a.Check() != "" {
			t.Errorf("applied %q, check %q", id, a.Check())
		}
	})

	t.Run("CC Switch applied", func(t *testing.T) {
		a, p, _ := setup(t, agentKey("claude-desktop"))
		edit.SetJSON(p.meta, edit.KV{Path: "appliedId", Value: ccswitch})
		if c := a.Check(); c == "" {
			t.Error("check: CC Switch's profile reads as magpie's")
		}
		if err := a.Sync(); err != nil {
			t.Fatal(err)
		}
		if got := desktopJSON(t, filepath.Join(p.library, ccswitch+".json")); got["inferenceGatewayApiKey"] != "cc-switch" {
			t.Errorf("CC Switch's profile was written: %v", got)
		}
		// Reconnect applies magpie's own, as before
		if err := a.Field("provider").Set("magpie"); err != nil {
			t.Fatal(err)
		}
		if id := applied(t, p); id != desktopProfileID {
			t.Errorf("applied after reconnect: %q", id)
		}
	})
}

// Desktop's 3p mode runs in Claude-3p, a userData of its own, so the
// chats, projects and Code sessions of the Anthropic sign-in (in Claude/)
// aren't shown on magpie (xgddm88 on X: 之前的工程和会话记录全没了). Turning
// Desktop on and off leaves every byte of that history as it was, and the
// notice says before the restart that it stays and comes back.
func TestClaudeDesktopKeepsTheSignedInHistory(t *testing.T) {
	home, p := desktopSandbox(t)
	history := map[string]string{
		filepath.Join(p.dir, "config.json"):                                                 `{"locale":"zh-CN","oauth:tokenCache":"x"}`,
		filepath.Join(p.dir, "Local Storage", "leveldb", "000003.log"):                      "chats",
		filepath.Join(p.dir, "IndexedDB", "https_claude.ai_0.indexeddb.leveldb", "CURRENT"): "MANIFEST-000001\n",
		filepath.Join(p.dir, "claude-code-sessions", "a", "b", "local_1.json"):              `{"cwd":"/Users/me/proj"}`,
		filepath.Join(p.dir, "local-agent-mode-sessions", "s", "session.json"):              `{"title":"proj"}`,
	}
	for f, s := range history {
		desktopWrite(t, f, s)
	}
	a := claudeDesktop(home)
	f := a.Field("provider")
	if err := f.Set("magpie"); err != nil {
		t.Fatal(err)
	}
	for f, s := range history {
		if b, err := os.ReadFile(f); err != nil || string(b) != s {
			t.Errorf("on changed %s: %q %v", f, b, err)
		}
	}
	if n := a.Notice(); !strings.Contains(n, noticeClaudedesktop3.String()) {
		t.Errorf("on, the notice doesn't say the signed-in history stays: %q", n)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	for f, s := range history {
		if b, err := os.ReadFile(f); err != nil || string(b) != s {
			t.Errorf("off changed %s: %q %v", f, b, err)
		}
	}
	if n := a.Notice(); !strings.Contains(n, noticeClaudedesktop4.String()) {
		t.Errorf("off, the notice doesn't say the history is back: %q", n)
	}
}
