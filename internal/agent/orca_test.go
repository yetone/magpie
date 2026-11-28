package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// Orca's Codex sessions reach magpie with the clientInfo Orca hands
// Codex's app-server as their User-Agent's product (GeniusQA, #5474: the
// logs said agent=orca_desktop): they are Orca's, by its name and icon.
func TestOrcaNamesItsRequests(t *testing.T) {
	for ua, want := range map[string]string{
		"orca_desktop/0.130.0 (Mac OS 26.2.0; arm64) iTerm.app/3.6.6 (orca_desktop; 1.4.281)": "orca",
		"orca_desktop/0.130.0 (Windows 10.0.26100; x86_64) unknown (orca_desktop; 1.4.281)":   "orca",
		"codex_cli_rs/0.130.0 (Mac OS 26.2.0; arm64) iTerm.app/3.6.6":                         "codex",
	} {
		if got := usage.AgentOf(ua); got != want {
			t.Errorf("%q: %q, want %q", ua, got, want)
		}
	}
}

// orcaSandbox is a sandbox HOME with Orca's data folder, Codex and
// Claude Code installed and neither connected to magpie.
func orcaSandbox(t *testing.T) (a, cx, cc *Agent) {
	t.Helper()
	home := syncHome(t)
	t.Setenv("PATH", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	cx = codex(home)
	if err := os.MkdirAll(cx.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(cx.Path, []byte("model = \"gpt-5.5\"\n"), 0o600)
	cc = claude(home)
	if err := os.MkdirAll(cc.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(cc.Path, []byte("{\"theme\": \"dark\"}\n"), 0o600)
	if !cx.Detected() || cx.Wired() || !cc.Detected() || cc.Wired() {
		t.Fatalf("Codex detected %v wired %v, Claude Code detected %v wired %v", cx.Detected(), cx.Wired(), cc.Detected(), cc.Wired())
	}
	home, cfg := homes()
	a = orca(home, cfg)
	return a, cx, cc
}

// Connecting Orca puts the Codex and Claude Code it runs on magpie, in the
// one step; turning it off takes them off again and leaves their files as
// their own Disconnect does, neither recorded as the user's disconnect.
func TestOrcaConnectsWhatItRuns(t *testing.T) {
	a, cx, cc := orcaSandbox(t)
	if a.Wired() {
		t.Fatal("wired before connecting")
	}
	if _, err := a.ConnectHow(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if !cx.Wired() || !cc.Wired() {
		t.Fatalf("Codex wired %v, Claude Code wired %v", cx.Wired(), cc.Wired())
	}
	if !a.Wired() || a.Values()["provider"] != magpieID {
		t.Errorf("Orca's values %v", a.Values())
	}
	if n := a.Notice(); !strings.Contains(n, "Orca runs Codex, Claude Code with their own settings") {
		t.Errorf("notice %q", n)
	}
	if d := a.Drift(); d != nil {
		t.Errorf("drift %+v", d)
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if cx.Wired() || cc.Wired() {
		t.Errorf("after Orca's Disconnect: Codex wired %v, Claude Code wired %v", cx.Wired(), cc.Wired())
	}
	if cx.Disconnected() || cc.Disconnected() {
		t.Errorf("recorded as the user's own disconnect: Codex %v, Claude Code %v", cx.Disconnected(), cc.Disconnected())
	}
	if b, _ := os.ReadFile(cc.Path); !strings.Contains(string(b), `"theme": "dark"`) || strings.Contains(string(b), "ANTHROPIC") {
		t.Errorf("Claude Code's settings.json\n%s", b)
	}
	// magpie's provider table stays for the threads that name it, as
	// Codex's own Disconnect leaves it
	if b, _ := os.ReadFile(cx.Path); !strings.Contains(string(b), `model = "gpt-5.5"`) || strings.Contains(string(b), "model_provider =") || strings.Contains(string(b), "openai_base_url") {
		t.Errorf("Codex's config.toml\n%s", b)
	}
	if _, ok := stashLoad()[orcaJoinedKey]; ok {
		t.Error("joined mark left")
	}
}

// An engine the user connected themselves is theirs: Orca's connect leaves
// it as it is and its Disconnect leaves it on. With every engine the
// user's own, turning Orca off is what takes Orca off magpie, so it does.
func TestOrcaLeavesTheUsersOwn(t *testing.T) {
	t.Run("one theirs", func(t *testing.T) {
		a, cx, cc := orcaSandbox(t)
		if _, err := cx.ConnectHow(); err != nil {
			t.Fatal(err)
		}
		if a.Wired() {
			t.Fatal("Orca on magpie with Claude Code off it")
		}
		if _, err := a.ConnectHow(); err != nil {
			t.Fatal(err)
		}
		if n := a.Notice(); !strings.Contains(n, "Orca runs Claude Code with") {
			t.Errorf("notice %q", n)
		}
		if err := a.Disconnect(); err != nil {
			t.Fatal(err)
		}
		if !cx.Wired() {
			t.Error("the user's Codex taken off")
		}
		if cc.Wired() {
			t.Error("Claude Code Orca connected left on")
		}
	})
	t.Run("all theirs", func(t *testing.T) {
		a, cx, cc := orcaSandbox(t)
		for _, e := range []*Agent{cx, cc} {
			if _, err := e.ConnectHow(); err != nil {
				t.Fatal(err)
			}
		}
		if !a.Wired() {
			t.Fatal("Orca not on magpie with both it runs on it")
		}
		if err := a.Disconnect(); err != nil {
			t.Fatal(err)
		}
		if a.Wired() || cx.Wired() || cc.Wired() {
			t.Errorf("Orca wired %v, Codex %v, Claude Code %v", a.Wired(), cx.Wired(), cc.Wired())
		}
	})
	t.Run("off before", func(t *testing.T) {
		// the user's disconnect from before stays theirs once Orca is off
		a, cx, _ := orcaSandbox(t)
		if _, err := cx.ConnectHow(); err != nil {
			t.Fatal(err)
		}
		if err := cx.Disconnect(); err != nil {
			t.Fatal(err)
		}
		if _, err := a.ConnectHow(); err != nil {
			t.Fatal(err)
		}
		if err := a.Disconnect(); err != nil {
			t.Fatal(err)
		}
		if cx.Wired() || !cx.Disconnected() {
			t.Errorf("Codex wired %v, the user's disconnect %v", cx.Wired(), cx.Disconnected())
		}
	})
	t.Run("taken back after", func(t *testing.T) {
		// the user turns off Claude Code Orca connected, then on again:
		// it is theirs, and Orca shows off in between, not changed outside
		a, _, cc := orcaSandbox(t)
		if _, err := a.ConnectHow(); err != nil {
			t.Fatal(err)
		}
		if err := cc.Disconnect(); err != nil {
			t.Fatal(err)
		}
		if a.Wired() {
			t.Error("Orca on magpie with Claude Code off it")
		}
		if d := a.Drift(); d != nil {
			t.Errorf("drift %+v after the user's own disconnect", d)
		}
		if _, err := cc.ConnectHow(); err != nil {
			t.Fatal(err)
		}
		if err := a.Disconnect(); err != nil {
			t.Fatal(err)
		}
		if !cc.Wired() {
			t.Error("Claude Code the user connected again taken off")
		}
	})
}

// With neither Codex nor Claude Code here, there is nothing Orca can run
// on magpie: connecting says so and writes nothing.
func TestOrcaWithNothingToRun(t *testing.T) {
	a, cx, cc := orcaSandbox(t)
	os.RemoveAll(cx.Dir)
	os.RemoveAll(cc.Dir)
	os.Remove(filepath.Join(filepath.Dir(cc.Dir), ".claude.json"))
	if cx.Detected() || cc.Detected() {
		t.Skip("Codex or Claude Code found here anyway")
	}
	if _, err := a.ConnectHow(); err == nil || !strings.Contains(err.Error(), "install either one") {
		t.Fatalf("connect: %v", err)
	}
	if a.Wired() {
		t.Error("wired with nothing to run")
	}
}

// Orca is found by its Electron userData folder or its app on each
// system: Orca.app (com.stablyai.orca), Orca.exe in
// %LOCALAPPDATA%\Programs\orca, /opt/Orca/orca-ide. Never by `orca` alone,
// GNOME's screen reader.
func TestOrcaFound(t *testing.T) {
	home := syncHome(t)
	cfg := filepath.Join(home, ".config")
	apps := filepath.Join(home, "Applications")
	wasOS, wasF, wasS, wasL, wasR, wasReg, wasStore := orcaOS, appFolders, spotlightApps, linuxAppOS, removedOS, windowsRegisteredApp, windowsStorePackages
	t.Cleanup(func() {
		orcaOS, appFolders, spotlightApps, linuxAppOS, removedOS, windowsRegisteredApp, windowsStorePackages = wasOS, wasF, wasS, wasL, wasR, wasReg, wasStore
	})
	spotlightApps = func(string) []string { return nil }
	windowsRegisteredApp = func(string) (string, string) { return "", "" }
	windowsStorePackages = func() []string { return nil }
	stand := func(goos string) {
		orcaOS, linuxAppOS, removedOS = goos, goos, goos
		appFolders = func() []string { return nil }
		switch goos {
		case "darwin":
			appFolders = func() []string { return []string{apps} }
		case "windows":
			appFolders = func() []string { return []string{filepath.Join(home, "AppData", "Local", "Programs")} }
		}
	}
	for goos, data := range map[string]string{
		"darwin":  filepath.Join(home, "Library", "Application Support", "Orca"),
		"windows": filepath.Join(home, "AppData", "Roaming", "Orca"),
		"linux":   filepath.Join(cfg, "Orca"),
	} {
		t.Run(goos, func(t *testing.T) {
			stand(goos)
			if orca(home, cfg).Detected() {
				t.Fatal("found with nothing there")
			}
			// GNOME's screen reader: `orca` and its folders
			bin := filepath.Join(home, "bin")
			writeTestFile(t, filepath.Join(bin, "orca"), "#!/bin/sh\n", 0o755)
			t.Setenv("PATH", bin)
			os.MkdirAll(filepath.Join(home, ".orca"), 0o755)
			os.MkdirAll(filepath.Join(home, ".local", "share", "orca"), 0o755)
			if orca(home, cfg).Detected() {
				t.Fatal("GNOME's screen reader taken for Orca")
			}
			os.MkdirAll(data, 0o700)
			t.Cleanup(func() { os.RemoveAll(data) })
			if got := orca(home, cfg).Detected(); got != (goos != "darwin") {
				t.Errorf("by its data folder alone: %v", got)
			}
			var app string
			switch goos {
			case "darwin":
				plantApp(t, apps, "Orca.app", "com.stablyai.orca")
				app = filepath.Join(apps, "Orca.app")
			case "windows":
				app = filepath.Join(home, "AppData", "Local", "Programs", "orca")
				writeTestFile(t, filepath.Join(app, "Orca.exe"), "MZ", 0o755)
			case "linux":
				app = proc.SystemDirs("/opt/Orca")[0]
				writeTestFile(t, filepath.Join(app, "orca-ide"), "\x7fELF", 0o755)
				writeTestFile(t, filepath.Join(app, "resources", "app.asar"), "asar", 0o644)
			}
			t.Cleanup(func() { os.RemoveAll(app) })
			if !orca(home, cfg).Detected() {
				t.Error("installed and run, not found")
			}
			os.RemoveAll(data)
			if !orca(home, cfg).HasApp() || !orca(home, cfg).Detected() {
				t.Error("its app alone, not found")
			}
		})
	}
}

// Orca is on the Agents page.
func TestOrcaListed(t *testing.T) {
	h := t.TempDir()
	if !slices.ContainsFunc(onMachine(h, filepath.Join(h, ".config")), func(a *Agent) bool { return a.ID == "orca" && a.Name == "Orca" }) {
		t.Error("Orca isn't an agent")
	}
}
