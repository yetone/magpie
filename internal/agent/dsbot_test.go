package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// dsbotAppPlist is DS Bot's Info.plist as its 0.3.0 macOS build ships it
// (github.com/Fei2411/DS-Bot's release), cut to the keys around the ones
// read, in their order and indentation.
const dsbotAppPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleDisplayName</key>
	<string>DS Bot</string>
	<key>CFBundleExecutable</key>
	<string>DS Bot</string>
	<key>CFBundleIconFile</key>
	<string>icon.icns</string>
	<key>CFBundleIdentifier</key>
	<string>io.github.fei2411.dsbot</string>
	<key>CFBundleInfoDictionaryVersion</key>
	<string>6.0</string>
	<key>CFBundleName</key>
	<string>DS Bot</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleShortVersionString</key>
	<string>0.3.0</string>
	<key>CFBundleVersion</key>
	<string>0.3.0</string>
</dict>
</plist>
`

// dsbotSandbox is dshRouteHome with DS Bot's own places in it: its
// Electron userData (DS_BOT_SHELL_DATA) and an app folder, both empty.
func dsbotSandbox(t *testing.T) (home, dsh, userData, apps string) {
	t.Helper()
	home, dsh, _ = dshRouteHome(t)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("DS_BOT_HOME", "")
	userData = filepath.Join(home, "shell")
	t.Setenv("DS_BOT_SHELL_DATA", userData)
	t.Setenv("PATH", t.TempDir())
	apps = t.TempDir()
	was := appFolders
	appFolders = func() []string { return []string{apps} }
	t.Cleanup(func() { appFolders = was })
	return home, dsh, userData, apps
}

func dsbotRead(p string) string { b, _ := os.ReadFile(p); return string(b) }

// rnhung (Discord): DS Bot (dsbot.app) asked for. The app runs a DSH of
// its own on its data folder, ~/.ds-bot by default, booting the profile
// ds-bot, and never reads ~/.dsh: magpie's dsh row didn't reach it. It is
// its own row now, found by its bundle, and a model picked there goes into
// its profile, DSH's key store and .env in that folder, with ~/.dsh left
// alone. Not opened yet, the profile is made as its first start makes it,
// which the app keeps (seen with 0.3.0); Disconnect before then takes it
// out again.
func TestDsBotWired(t *testing.T) {
	judgeFor(t, "darwin")
	home, dshDir, _, apps := dsbotSandbox(t)
	if err := os.RemoveAll(dshDir); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(home, ".ds-bot")
	file := filepath.Join(data, "profiles", "ds-bot", "cordis.patch.yml")

	if a := dsbot(home); a.Detected() {
		t.Fatal("no app and no data folder, yet detected")
	}
	installApp(t, apps, "DS Bot.app", strings.Replace(dsbotAppPlist, "io.github.fei2411.dsbot", "com.example.other", 1))
	if dsbot(home).Detected() {
		t.Fatal("another bundle under DS Bot's name taken for it")
	}
	if err := os.RemoveAll(filepath.Join(apps, "DS Bot.app")); err != nil {
		t.Fatal(err)
	}
	installApp(t, apps, "DS Bot.app", dsbotAppPlist)
	a := dsbot(home)
	if !a.Detected() || a.CLIMissing() {
		t.Fatalf("the app: detected %v, CLI missing %v", a.Detected(), a.CLIMissing())
	}
	if c, ok := a.CLI(); !ok || c.App != "0.3.0" {
		t.Fatalf("the app's version: %+v %v", c, ok)
	}
	if a.Path != file {
		t.Fatalf("names %s, not DS Bot's profile", a.Path)
	}

	if err := a.Pick("model", "magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	s := dsbotRead(file)
	if !strings.HasPrefix(s, strings.TrimSuffix(dshProfileTemplate, "[]\n")) || !strings.Contains(s, "apiKeyEnv: "+dshKeyRef) || !strings.Contains(s, "provider: "+dshRoute) || !strings.Contains(s, "- id: deepseek/pro") {
		t.Fatalf("DS Bot's profile:\n%s", s)
	}
	if c := dsbotRead(filepath.Join(data, ".credentials.yaml")); !strings.Contains(c, dshKeyRef+": ") {
		t.Fatalf("no key in DS Bot's DSH store:\n%s", c)
	}
	if e := dsbotRead(filepath.Join(data, ".env")); !strings.Contains(e, dshKeyRef+"=") {
		t.Fatalf("no key in DS Bot's .env:\n%s", e)
	}
	if _, err := os.Stat(dshDir); !os.IsNotExist(err) {
		t.Fatalf("~/.dsh written for DS Bot: %v", err)
	}
	a = dsbot(home)
	if got := a.Field("model").Get(); got != "magpie/deepseek/flash" {
		t.Fatalf("model reads %q", got)
	}
	if !a.Wired() || a.Check() != "" || a.Drift() != nil {
		t.Fatalf("not wired: wired %v, check %q, drift %+v", a.Wired(), a.Check(), a.Drift())
	}
	if n := a.Notice(); !strings.Contains(n, noticeDsbotUnopened.String()) {
		t.Fatalf("notice before the app opened: %q", n)
	}

	// Disconnect before the app ever opened: what magpie made goes
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(data, "profiles")); !os.IsNotExist(err) {
		t.Fatalf("the profile magpie made outlived Disconnect: %v\n%s", err, dsbotRead(file))
	}

	// connected again, and the app's first start beside it
	a = dsbot(home)
	if err := a.Pick("model", "magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(file), "package.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if n := dsbot(home).Notice(); !strings.Contains(n, noticeDsbotNewBots.String()) || strings.Contains(n, noticeDsbotUnopened.String()) {
		t.Fatalf("notice after the app opened: %q", n)
	}
	// dsh's own row still has no ~/.dsh
	if dsh(home).Detected() {
		t.Fatal("DS Bot's folder taken for dsh's")
	}
}

// DS Bot's data folder is where its first run's choice put it
// (locations.json in its userData), its legacy userData (DeepSeek Bot)
// while DS Bot's has none, or DS_BOT_HOME. A locations.json that can't be
// read is unknown: nothing is written, least of all to ~/.ds-bot, which
// the app doesn't run on then.
func TestDsBotDataFolder(t *testing.T) {
	home, _, userData, _ := dsbotSandbox(t)
	if dir, ok := dsbotHome(); !ok || dir != filepath.Join(home, ".ds-bot") {
		t.Fatalf("no locations.json: %q %v", dir, ok)
	}
	picked := filepath.Join(home, "Documents", "bot-data")
	if err := os.MkdirAll(userData, 0o755); err != nil {
		t.Fatal(err)
	}
	loc := filepath.Join(userData, "locations.json")
	if err := os.WriteFile(loc, []byte(`{"dataDir":"`+filepath.ToSlash(picked)+`","workspace":"x","freeAtStart":false}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if dir, ok := dsbotHome(); !ok || filepath.Clean(dir) != picked {
		t.Fatalf("locations.json's dataDir: %q %v", dir, ok)
	}
	a := dsbot(home)
	if err := a.Pick("model", "magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if s := dsbotRead(dsbotFile(picked)); !strings.Contains(s, "provider: "+dshRoute) {
		t.Fatalf("the picked folder's profile:\n%s", s)
	}

	t.Setenv("DS_BOT_HOME", filepath.Join(home, "throwaway"))
	if dir, ok := dsbotHome(); !ok || dir != filepath.Join(home, "throwaway") {
		t.Fatalf("DS_BOT_HOME: %q %v", dir, ok)
	}
	t.Setenv("DS_BOT_HOME", "")

	for _, bad := range []string{"{", `{"dataDir":""}`, `{"workspace":"x"}`} {
		if err := os.WriteFile(loc, []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		if dir, ok := dsbotHome(); ok {
			t.Fatalf("%s taken for %q", bad, dir)
		}
		a := dsbot(home)
		if a.Detected() || len(a.Fields) != 0 {
			t.Fatalf("%s: detected %v, fields %d", bad, a.Detected(), len(a.Fields))
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".ds-bot")); !os.IsNotExist(err) {
		t.Fatalf("~/.ds-bot made: %v", err)
	}

	// the userData of DS Bot's name before, while DS Bot's has none
	t.Setenv("DS_BOT_SHELL_DATA", "")
	cfg, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(cfg, "DeepSeek Bot")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(home, "old-bot")
	if err := os.WriteFile(filepath.Join(legacy, "locations.json"), []byte(`{"dataDir":"`+filepath.ToSlash(old)+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if dir, ok := dsbotHome(); !ok || filepath.Clean(dir) != old {
		t.Fatalf("legacy userData: %q %v", dir, ok)
	}
	now := filepath.Join(cfg, "DS Bot")
	if err := os.MkdirAll(now, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(now, "locations.json"), []byte(`{"dataDir":"`+filepath.ToSlash(picked)+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if dir, ok := dsbotHome(); !ok || filepath.Clean(dir) != picked {
		t.Fatalf("DS Bot's own userData beside the legacy one: %q %v", dir, ok)
	}
}

// A Bot made in DS Bot starts in the app's own profile; one the app makes
// while magpie serves (empty, as DSH makes them) is filled by the serving
// round as ~/.dsh's are, with DS Bot's models.
func TestDsBotProfileMadeWhileServing(t *testing.T) {
	home, _, _, _ := dsbotSandbox(t)
	data := filepath.Join(home, ".ds-bot")
	if err := dsbot(home).Pick("model", "magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(data, "profiles", "import-check", "cordis.patch.yml")
	if err := os.MkdirAll(filepath.Dir(other), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte(dshProfileTemplate), 0o644); err != nil {
		t.Fatal(err)
	}
	if trouble := dshWiredOnce(); trouble != "" {
		t.Fatal(trouble)
	}
	if s := dsbotRead(other); !strings.Contains(s, "apiKeyEnv: "+dshKeyRef) || !strings.Contains(s, "- id: deepseek/pro") {
		t.Fatalf("the round left DS Bot's new profile bare:\n%s", s)
	}
	if d := dsbot(home).Check(); d != "" {
		t.Fatalf("after the round: %s", d)
	}
}

// The models shown for DS Bot on the Agents page are its own: one hidden
// for it is out of its route and still in dsh's, and the other way round.
func TestDsBotOwnModels(t *testing.T) {
	home, dshDir, _, _ := dsbotSandbox(t)
	data := filepath.Join(home, ".ds-bot")
	if err := provider.SetHiddenModels(dsbotID, []string{"deepseek/pro"}); err != nil {
		t.Fatal(err)
	}
	if err := dsbot(home).Pick("model", "magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if err := dsh(home).Pick("model", "magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	bot := dsbotRead(dsbotFile(data))
	web := dsbotRead(filepath.Join(dshDir, "profiles", "web", "cordis.patch.yml"))
	if strings.Contains(bot, "- id: deepseek/pro") || !strings.Contains(bot, "- id: deepseek/flash") {
		t.Fatalf("DS Bot's route with pro hidden for it:\n%s", bot)
	}
	if !strings.Contains(web, "- id: deepseek/pro") {
		t.Fatalf("dsh's route lost a model hidden for DS Bot only:\n%s", web)
	}
	if d := dsbot(home).Check(); d != "" {
		t.Fatalf("DS Bot's check: %s", d)
	}
	if d := dsh(home).Check(); d != "" {
		t.Fatalf("dsh's check: %s", d)
	}
	// the serving round keeps each to its own
	if trouble := dshWiredOnce(); trouble != "" {
		t.Fatal(trouble)
	}
	if strings.Contains(dsbotRead(dsbotFile(data)), "- id: deepseek/pro") {
		t.Fatal("the round put pro back in DS Bot's route")
	}
}
