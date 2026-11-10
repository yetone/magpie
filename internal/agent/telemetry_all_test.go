package agent

import (
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/testenv"
)

// onPath puts the agents' commands on PATH, which is how they are found
// (Gemini CLI by its binary alone).
func onPath(t *testing.T, names ...string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a #! stand-in isn't a command on Windows")
	}
	bin := t.TempDir()
	for _, n := range names {
		testenv.Program(t, filepath.Join(bin, n), "#!/bin/sh\n")
	}
	t.Setenv("PATH", bin)
}

// OipsAnthony on X: Settings › Privacy turns off every agent's telemetry,
// over each agent's own row, an agent found later too; off again, each
// agent is back to its own row's choice, and a value of the user's own is
// never written over.
func TestTelemetryAllOff(t *testing.T) {
	home, gpath := telemetryHome(t, ".gemini/settings.json", `{"ui": {"theme": "x"}}`)
	onPath(t, "gemini", "qwen", "codex")
	qpath := filepath.Join(home, ".qwen", "settings.json")
	writeAt(t, qpath, `{}`)
	// Codex: the user keeps analytics on themselves
	const codexTheirs = "[analytics]\nenabled = true\n"
	cpath := filepath.Join(home, ".codex", "config.toml")
	writeAt(t, cpath, codexTheirs)
	// Qwen Code off by its own row before
	if err := qwen(home).SetTelemetry(true); err != nil {
		t.Fatal(err)
	}

	err := SetTelemetryAll(true)
	if err == nil || !strings.Contains(err.Error(), "Codex") {
		t.Fatalf("Codex's own on isn't named: %v", err)
	}
	if !settings.Load().AgentTelemetryOff {
		t.Fatal("the switch wasn't saved")
	}
	if readFile(cpath) != codexTheirs {
		t.Fatalf("the user's Codex value was written over:\n%s", readFile(cpath))
	}
	if v, ok := jsonAt(t, gpath, "privacy.usageStatisticsEnabled"); !ok || v != false {
		t.Fatalf("Gemini CLI not turned off:\n%s", readFile(gpath))
	}
	// an agent's own row is held while it is on
	if err := gemini(home).SetTelemetry(false); !errors.Is(err, ErrTelemetryAllOff) {
		t.Fatalf("Gemini CLI's row turned on under the switch: %v", err)
	}
	if err := qwen(home).SetTelemetry(false); !errors.Is(err, ErrTelemetryAllOff) {
		t.Fatalf("Qwen Code's row turned on under the switch: %v", err)
	}

	// Claude Code found later
	cl := filepath.Join(home, ".claude", "settings.json")
	writeAt(t, cl, `{"model": "opus"}`)
	if err := KeepTelemetryOff(claude(home)); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"env.DISABLE_TELEMETRY", "env.DISABLE_ERROR_REPORTING"} {
		if v, ok := jsonAt(t, cl, k); !ok || v != "1" {
			t.Fatalf("Claude Code found later kept %s:\n%s", k, readFile(cl))
		}
	}

	if err := SetTelemetryAll(false); err != nil {
		t.Fatal(err)
	}
	if settings.Load().AgentTelemetryOff {
		t.Fatal("the switch is still on")
	}
	if _, ok := jsonAt(t, gpath, "privacy.usageStatisticsEnabled"); ok {
		t.Fatalf("Gemini CLI not back on:\n%s", readFile(gpath))
	}
	if v, _ := jsonAt(t, gpath, "ui.theme"); v != "x" {
		t.Fatalf("the user's Gemini CLI settings moved:\n%s", readFile(gpath))
	}
	if _, ok := jsonAt(t, cl, "env.DISABLE_TELEMETRY"); ok {
		t.Fatalf("Claude Code not back on:\n%s", readFile(cl))
	}
	// Qwen Code's own row said off: it stays off
	if st := telemetryOf(t, qwen(home)); !st.Off {
		t.Fatalf("Qwen Code's own off was undone: %+v", st)
	}
	if readFile(cpath) != codexTheirs {
		t.Fatalf("Codex's file changed:\n%s", readFile(cpath))
	}
	// and the rows are the user's again
	if err := qwen(home).SetTelemetry(false); err != nil {
		t.Fatal(err)
	}
}

// An agent the user turned off in its own config stays as they wrote it
// when the switch goes off again.
func TestTelemetryAllLeavesTheUsersOff(t *testing.T) {
	const theirs = `{"privacy": {"usageStatisticsEnabled": false}}`
	_, gpath := telemetryHome(t, ".gemini/settings.json", theirs)
	onPath(t, "gemini")
	if err := SetTelemetryAll(true); err != nil {
		t.Fatal(err)
	}
	if err := SetTelemetryAll(false); err != nil {
		t.Fatal(err)
	}
	if readFile(gpath) != theirs {
		t.Fatalf("the user's own off was changed:\n%s", readFile(gpath))
	}
}

// An unreadable settings.json is unknown: nothing is turned off by it.
func TestTelemetryAllUnreadableSettings(t *testing.T) {
	home, gpath := telemetryHome(t, ".gemini/settings.json", `{}`)
	writeAt(t, settings.Path(), `{"agentTelemetryOff": true`)
	if TelemetryAllOff() {
		t.Fatal("an unreadable settings.json read as on")
	}
	if err := KeepTelemetryOff(gemini(home)); err == nil {
		t.Fatal("no error for an unreadable settings.json")
	}
	if readFile(gpath) != `{}` {
		t.Fatalf("written under an unreadable settings.json:\n%s", readFile(gpath))
	}
}
