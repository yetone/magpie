package agent

import (
	"context"
	"path/filepath"
	"testing"
)

// Open the Codex app (Yink88564391 on X): on a Mac it opens the bundle
// whose Info.plist says com.openai.codex, never the ChatGPT chat app's
// of the same name; on Windows it starts the Store package's app; with no
// app installed it opens nothing and says so.
func TestOpenCodexApp(t *testing.T) {
	restartSandbox(t)
	apps := filepath.Join(t.TempDir(), "Applications")
	appFolders = func() []string { return []string{apps} }
	var opened []string
	appOpen = func(_ context.Context, b string) error { opened = append(opened, b); return nil }
	var launched []winProc
	winLaunch = func(p winProc) error { launched = append(launched, p); return nil }

	restartOnMac = true
	if CodexAppInstalled() || OpenCodexApp(context.Background()) == nil || len(opened) != 0 {
		t.Fatalf("no app installed, yet installed=%v opened=%q", CodexAppInstalled(), opened)
	}
	installApp(t, apps, "ChatGPT.app", codexAppPlist("com.openai.chat", "1.2026.280"))
	if CodexAppInstalled() || OpenCodexApp(context.Background()) == nil || len(opened) != 0 {
		t.Fatalf("the chat app taken for Codex's: opened %q", opened)
	}
	installApp(t, apps, "Codex.app", codexAppPlist("com.openai.codex", "26.930.31730"))
	if !CodexAppInstalled() {
		t.Fatal("Codex.app not seen")
	}
	if err := OpenCodexApp(context.Background()); err != nil || len(opened) != 1 || opened[0] != filepath.Join(apps, "Codex.app") {
		t.Fatalf("opened %q (%v)", opened, err)
	}

	// Linux has no launcher magpie starts
	restartOnMac = false
	if CodexAppInstalled() || OpenCodexApp(context.Background()) == nil {
		t.Fatal("offered to open the app where magpie cannot")
	}
	if len(launched) != 0 {
		t.Fatalf("launched %v off Windows", launched)
	}
}
