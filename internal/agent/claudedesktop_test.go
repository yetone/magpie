package agent

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/yetone/magpie/internal/desktopdir"
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
	desktopWrite(t, p.config, `{"deploymentMode":"1p"}`)
	if c := a.Check(); c == "" {
		t.Error("check: 1p is fine")
	}
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
// the package's folders, which the packaged app reads, not the usual ones
// it doesn't see.
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
	p := desktopPathsOf(filepath.Join(pkg, "Local", "Claude"), filepath.Join(pkg, "Local", "Claude-3p"))
	if !desktopWired(p) || a.Field("provider").Get() != magpieID {
		t.Fatalf("not wired in the package's folders: %v", desktopTree(t, p))
	}
	if m := desktopJSON(t, p.config3p); m["deploymentMode"] != "3p" {
		t.Errorf("Claude-3p: %v", m)
	}
	for _, d := range []string{filepath.Join(roaming, "Claude"), filepath.Join(local, "Claude"), filepath.Join(local, "Claude-3p")} {
		if _, err := os.Stat(d); err == nil {
			t.Errorf("%s written, which the packaged Desktop doesn't read", d)
		}
	}
}
