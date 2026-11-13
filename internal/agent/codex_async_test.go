package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// setAsyncCompact sets Codex's background compaction as the Settings row
// does, put back when the test ends.
func setAsyncCompact(t *testing.T, on bool, percent int) {
	t.Helper()
	was := settings.Load()
	t.Cleanup(func() {
		s := settings.Load()
		s.CodexAsyncCompact, s.CodexAsyncCompactPercent = was.CodexAsyncCompact, was.CodexAsyncCompactPercent
		if err := settings.Save(s); err != nil {
			t.Errorf("restoring settings: %v", err)
		}
	})
	s := settings.Load()
	s.CodexAsyncCompact, s.CodexAsyncCompactPercent = on, percent
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
}

// codexReadersAre has the Codexes reading every config be a CLI of each
// version given, put back when the test ends.
func codexReadersAre(t *testing.T, versions ...string) {
	t.Helper()
	was := codexReaders
	t.Cleanup(func() { codexReaders = was })
	codexReaders = func(place) []CodexReader {
		var out []CodexReader
		for _, v := range versions {
			out = append(out, CodexReader{Kind: "cli", Version: v})
		}
		return out
	}
}

// asyncOf is the config's async_compaction value and the capability on
// magpie's provider table.
func asyncOf(t *testing.T, config string) (feature any, capability any) {
	t.Helper()
	var doc map[string]any
	if err := toml.Unmarshal([]byte(config), &doc); err != nil {
		t.Fatalf("%v:\n%s", err, config)
	}
	features, _ := doc["features"].(map[string]any)
	providers, _ := doc["model_providers"].(map[string]any)
	p, _ := providers[magpieID].(map[string]any)
	caps, _ := p["capabilities"].(map[string]any)
	return features["async_compaction"], caps["remote_compaction"]
}

// The switch on, Codex routed through magpie as its provider (no ChatGPT
// sign-in) gets [features.async_compaction] with the threshold in that
// table, where Codex 0.163 reads it, and remote_compaction = "v2" on
// magpie's provider table: without it Codex never starts a background
// compaction on a provider other than OpenAI's (tried with Codex 0.163:
// none in a whole turn past the threshold; with it, one per step, its
// summary taken in). A model switch writing the table again keeps it, and
// the switch off takes all of it out again.
func TestCodexAsyncCompactionWritten(t *testing.T) {
	quietCodex(t, false, provider.CodexDaemonCheck{})
	codexReadersAre(t, "0.164.0", "0.163.0-alpha.10.2")
	home, read := codexHome(t, "", "model = \"gpt-5.5\"\n")
	setAsyncCompact(t, true, 70)
	cx := codex(home)
	route(t, cx, read)
	feature, capability := asyncOf(t, read())
	table, _ := feature.(map[string]any)
	if table["enabled"] != true || table["model_async_compact_threshold_percent"] != int64(70) {
		t.Fatalf("async_compaction = %v:\n%s", feature, read())
	}
	if capability != "v2" {
		t.Fatalf("no remote_compaction = \"v2\" on magpie's provider:\n%s", read())
	}
	// a model switch writes magpie's provider table again
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if _, capability := asyncOf(t, read()); capability != "v2" {
		t.Fatalf("the capability went as the table was written again:\n%s", read())
	}
	if strings.Count(read(), "capabilities") != 1 || strings.Count(read(), "[features.async_compaction]") != 1 {
		t.Fatalf("written twice:\n%s", read())
	}
	// another threshold
	setAsyncCompact(t, true, 80)
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if feature, _ := asyncOf(t, read()); feature.(map[string]any)["model_async_compact_threshold_percent"] != int64(80) {
		t.Fatalf("threshold not moved to 80:\n%s", read())
	}
	setAsyncCompact(t, false, 80)
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if feature, capability := asyncOf(t, read()); feature != nil || capability != nil {
		t.Fatalf("left behind with the switch off: %v %v\n%s", feature, capability, read())
	}
	if _, ok := stashLoad()["codex.async"]; ok {
		t.Fatal("the stash still claims what magpie wrote")
	}
}

// A value the user set is theirs: on, it is never written over; off, it
// stays. The feature the user turned on by hand gets the capability it
// needs on magpie's provider, the switch on or not (#2493).
func TestCodexAsyncCompactionKeepsTheUsers(t *testing.T) {
	for _, tc := range []struct {
		name, config string
		on           bool
		want         string // the config holds this after
		capability   any    // on magpie's provider once routed
	}{
		{"enabled false, threshold", "[features.async_compaction]\nenabled = false\nmodel_async_compact_threshold_percent = 50\n", true,
			"[features.async_compaction]\nenabled = false\nmodel_async_compact_threshold_percent = 50\n", "v2"},
		{"bare true", "[features]\nasync_compaction = true\n", false, "[features]\nasync_compaction = true\n", "v2"},
		{"bare false", "[features]\nasync_compaction = false\n", true, "[features]\nasync_compaction = false\n", "v2"},
		{"inline", "[features]\nasync_compaction = { enabled = true }\n", false, "async_compaction = { enabled = true }\n", "v2"},
		{"bare true, switch on", "[features]\nasync_compaction = true\n", true, "[features]\nasync_compaction = true\n", "v2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			quietCodex(t, false, provider.CodexDaemonCheck{})
			codexReadersAre(t, "0.164.0")
			home, read := codexHome(t, "", "model = \"gpt-5.5\"\n\n"+tc.config)
			setAsyncCompact(t, tc.on, 70)
			cx := codex(home)
			route(t, cx, read)
			if !strings.Contains(read(), tc.want) || strings.Contains(read(), "= 70") {
				t.Fatalf("the user's value was written over:\n%s", read())
			}
			if _, capability := asyncOf(t, read()); capability != tc.capability {
				t.Fatalf("capability %v, want %v:\n%s", capability, tc.capability, read())
			}
			setAsyncCompact(t, false, 0)
			if err := cx.Sync(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(read(), tc.want) {
				t.Fatalf("the user's value went with the switch off:\n%s", read())
			}
		})
	}
}

// A capability the user gave magpie's provider stays as they wrote it.
func TestCodexAsyncCompactionKeepsUsersCapability(t *testing.T) {
	quietCodex(t, false, provider.CodexDaemonCheck{})
	home, read := codexHome(t, "", "model = \"gpt-5.5\"\n")
	cx := codex(home)
	route(t, cx, read)
	b := strings.Replace(read(), "wire_api = \"responses\"\n", "wire_api = \"responses\"\ncapabilities = { remote_compaction = \"unsupported\" }\n", 1)
	writeFile(t, cx.Path, b)
	setAsyncCompact(t, true, 0)
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if _, capability := asyncOf(t, read()); capability != "unsupported" {
		t.Fatalf("the user's capability was written over:\n%s", read())
	}
	setAsyncCompact(t, false, 0)
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if _, capability := asyncOf(t, read()); capability != "unsupported" {
		t.Fatalf("the user's capability went:\n%s", read())
	}
}

// A Codex older than 0.163.0-alpha.8 takes every [features] entry it
// doesn't know for a bool, and a [features.async_compaction] table stops
// it loading its config ("invalid type: map, expected a boolean", the
// Codex app on Windows of 多喝热水 on Discord, which wouldn't open). Where
// one reads the config, or one of no known version does, or none is
// found, the switch writes the bare async_compaction = true every Codex
// reads, and no threshold.
func TestCodexAsyncCompactionOldCodexGetsTheBareKey(t *testing.T) {
	for _, tc := range []struct {
		name     string
		versions []string
	}{
		{"0.162.1", []string{"0.162.1"}},
		{"alpha.5 beside a new one", []string{"0.164.0", "0.163.0-alpha.5"}},
		{"a version not known", []string{"0.164.0", ""}},
		{"none found", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			quietCodex(t, false, provider.CodexDaemonCheck{})
			codexReadersAre(t, tc.versions...)
			home, read := codexHome(t, "", "model = \"gpt-5.5\"\n")
			setAsyncCompact(t, true, 70)
			cx := codex(home)
			route(t, cx, read)
			feature, capability := asyncOf(t, read())
			if feature != true || strings.Contains(read(), "[features.async_compaction]") || strings.Contains(read(), "= 70") {
				t.Fatalf("async_compaction = %v, want the bare true:\n%s", feature, read())
			}
			if capability != "v2" {
				t.Fatalf("no remote_compaction = \"v2\":\n%s", read())
			}
			setAsyncCompact(t, false, 70)
			if err := cx.Sync(); err != nil {
				t.Fatal(err)
			}
			if feature, capability := asyncOf(t, read()); feature != nil || capability != nil {
				t.Fatalf("left behind with the switch off: %v %v\n%s", feature, capability, read())
			}
			if _, ok := stashLoad()["codex.async"]; ok {
				t.Fatal("the stash still claims what magpie wrote")
			}
		})
	}
}

// A config an earlier magpie broke for an old Codex — the table it wrote
// with every Codex taken to read it — is mended on the next sync: magpie's
// table goes, whole, and the bare key takes its place. Once every Codex
// is new enough again, the table comes back.
func TestCodexAsyncCompactionTableTakenBackForOldCodex(t *testing.T) {
	quietCodex(t, false, provider.CodexDaemonCheck{})
	codexReadersAre(t, "0.164.0")
	home, read := codexHome(t, "", "model = \"gpt-5.5\"\n")
	setAsyncCompact(t, true, 70)
	cx := codex(home)
	route(t, cx, read)
	if !strings.Contains(read(), "[features.async_compaction]") {
		t.Fatalf("no table with every Codex new:\n%s", read())
	}
	codexReadersAre(t, "0.164.0", "0.162.1")
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if feature, _ := asyncOf(t, read()); feature != true || strings.Contains(read(), "[features.async_compaction]") || strings.Contains(read(), "model_async_compact_threshold_percent") {
		t.Fatalf("magpie's table not mended to the bare key: %v\n%s", feature, read())
	}
	codexReadersAre(t, "0.164.0")
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	feature, _ := asyncOf(t, read())
	if table, _ := feature.(map[string]any); table["enabled"] != true || table["model_async_compact_threshold_percent"] != int64(70) || strings.Contains(read(), "async_compaction = true") {
		t.Fatalf("the table didn't come back: %v\n%s", feature, read())
	}
}

// A table the user wrote is theirs, an old Codex reading it or not.
func TestCodexAsyncCompactionUsersTableStaysForOldCodex(t *testing.T) {
	quietCodex(t, false, provider.CodexDaemonCheck{})
	codexReadersAre(t, "0.162.1")
	users := "[features.async_compaction]\nenabled = true\nmodel_async_compact_threshold_percent = 50\n"
	home, read := codexHome(t, "", "model = \"gpt-5.5\"\n\n"+users)
	setAsyncCompact(t, true, 70)
	cx := codex(home)
	route(t, cx, read)
	if !strings.Contains(read(), users) || strings.Contains(read(), "async_compaction = true") {
		t.Fatalf("the user's table was changed:\n%s", read())
	}
}

// The versions that take the table: 0.163.0-alpha.8 on.
func TestCodexAsyncBlocked(t *testing.T) {
	for v, blocked := range map[string]bool{
		"0.160.0": true, "0.162.1": true, "0.163.0-alpha.5": true, "": true,
		"0.163.0-alpha.8": false, "0.163.0-alpha.10.2": false, "0.163.0": false, "0.164.0": false,
	} {
		if got := len(codexAsyncBlocked([]CodexReader{{Kind: "cli", Version: v}})) > 0; got != blocked {
			t.Errorf("%q blocked = %v, want %v", v, got, blocked)
		}
	}
	if len(codexAsyncBlocked(nil)) == 0 {
		t.Error("no Codex found counts as one that takes the table")
	}
}

// The Codex app's own codex is read from its bundle's codex-package.json
// (ChatGPT.app 26.1013 ships 0.163.0-alpha.10.2); a Codex app without one
// is of no known version, and the ChatGPT chat app is no Codex.
func TestBundledCodexVersion(t *testing.T) {
	dir := t.TempDir()
	app := filepath.Join(dir, "ChatGPT.app")
	writeFile(t, filepath.Join(app, "Contents", "Resources", "codex-cli", "codex-package.json"), `{
  "layoutVersion": 1,
  "version": "0.163.0-alpha.10.2",
  "target": "aarch64-apple-darwin"
}`)
	if v, ok := bundledCodexVersion(app, "com.openai.codex"); !ok || v != "0.163.0-alpha.10.2" {
		t.Fatalf("got %q %v", v, ok)
	}
	plist := func(id string) string {
		return "<plist><dict><key>CFBundleIdentifier</key><string>" + id + "</string></dict></plist>"
	}
	codexApp := filepath.Join(dir, "Codex.app")
	writeFile(t, filepath.Join(codexApp, "Contents", "Info.plist"), plist("com.openai.codex"))
	if v, ok := bundledCodexVersion(codexApp, "com.openai.codex"); !ok || v != "" {
		t.Fatalf("a Codex app with no codex-package.json: %q %v", v, ok)
	}
	chat := filepath.Join(dir, "Chat", "ChatGPT.app")
	writeFile(t, filepath.Join(chat, "Contents", "Info.plist"), plist("com.openai.chat"))
	if _, ok := bundledCodexVersion(chat, "com.openai.codex"); ok {
		t.Fatal("the chat app taken for a Codex")
	}
}

// What magpie writes is loaded by real Codex binaries, old and new: each
// path in MAGPIE_TEST_CODEX_BINS (separated by the system's list
// separator) runs `codex features list` against the config a sync wrote
// for it, in a CODEX_HOME of the test's own. Skipped without it: a test
// fetches no binary.
func TestCodexAsyncConfigLoadsInRealCodex(t *testing.T) {
	bins := filepath.SplitList(os.Getenv("MAGPIE_TEST_CODEX_BINS"))
	if len(bins) == 0 {
		t.Skip("MAGPIE_TEST_CODEX_BINS names no codex binaries")
	}
	for _, bin := range bins {
		out, err := exec.Command(bin, "--version").Output()
		if err != nil {
			t.Fatalf("%s --version: %v", bin, err)
		}
		version := parseVersion(string(out))
		t.Run(version, func(t *testing.T) {
			quietCodex(t, false, provider.CodexDaemonCheck{})
			codexReadersAre(t, version)
			home, read := codexHome(t, "", "model = \"gpt-5.5\"\n")
			setAsyncCompact(t, true, 70)
			cx := codex(home)
			route(t, cx, read)
			codexHomeDir := filepath.Dir(cx.Path)
			cmd := exec.Command(bin, "features", "list")
			cmd.Env = append(os.Environ(), "CODEX_HOME="+codexHomeDir, "HOME="+home)
			cmd.Stdin = nil
			got, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("codex %s can't load the config: %v\n%s\n%s", version, err, got, read())
			}
			on := false
			for _, line := range strings.Split(string(got), "\n") {
				if f := strings.Fields(line); len(f) > 0 && f[0] == "async_compaction" {
					on = f[len(f)-1] == "true"
				}
			}
			if !Newer(codexAsyncTableFrom, version) && !on {
				t.Fatalf("codex %s loaded it with async_compaction off:\n%s\n%s", version, got, read())
			}
		})
	}
}

// The editor extension carries its own codex, with its version in the
// codex-package.json beside it (#X: HermanHongbb on X, Fedora, the
// extension in VS Code and Cursor, both newer than the gate). The reader was
// made with no version, so it was always "unknown" and held the table (and
// the threshold) out. A version that is newer passes; one that can't be
// read stays unknown, not old.
func TestExtensionCodexVersionRead(t *testing.T) {
	home := t.TempDir()
	put := func(rel, body string) {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pkg := func(v string) string {
		return `{"layoutVersion": 1, "version": "` + v + `", "entrypoint": "bin/codex"}`
	}
	put(".vscode/extensions/openai.chatgpt-26.51111.21308-linux-x64/bin/linux-x86_64/codex-package.json", pkg("0.164.0-alpha.11.1"))
	put(".vscode-server/extensions/openai.chatgpt-26.5908.1-linux-x64/bin/linux-x86_64/codex-package.json", pkg("0.167.0-alpha.4.1"))
	put(".cursor/extensions/openai.chatgpt-26.1-win32-x64/bin/windows-x86_64/codex-package.json", pkg("0.169.0"))
	put(".windsurf/extensions/openai.chatgpt-26.2-linux-x64/bin/linux-x86_64/codex", "binary only")
	put(".vscode-oss/extensions/openai.chatgpt-25.1-linux-x64/bin/linux-x86_64/codex-package.json", pkg("0.100.0"))
	put(".vscode-oss/extensions/.obsolete", `{"openai.chatgpt-25.1-linux-x64": true}`)
	put(".vscode/extensions/other.ext-1/bin/x/codex-package.json", pkg("0.1.0"))

	got := map[string]int{}
	for _, r := range extensionCodexReaders(home) {
		if r.Kind != "ide" {
			t.Errorf("kind %q", r.Kind)
		}
		got[r.Version]++
	}
	want := map[string]int{"0.164.0-alpha.11.1": 1, "0.167.0-alpha.4.1": 1, "0.169.0": 1, "": 1}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for v, n := range want {
		if got[v] != n {
			t.Fatalf("got %v, want %v", got, want)
		}
	}

	// what the gate makes of them: only the unreadable one holds the table
	blocked := codexAsyncBlocked(extensionCodexReaders(home))
	if len(blocked) != 1 || blocked[0].Version != "" {
		t.Errorf("blocked %v, want only the one of unknown version", blocked)
	}
}
