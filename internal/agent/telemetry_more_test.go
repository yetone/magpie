package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// daoche_wuzui on X: turn telemetry off for every agent where it can be.
// Each of these has its own switch in its own config, from its source.
var moreTelemetry = []struct {
	name, file, before, off string
	of                      func(home string) *Agent
	keys                    []string
	// theirs keeps telemetry on in the user's own words
	theirs string
	// own already turns it off
	own string
}{
	{"crush", ".config/crush/crush.json", `{"options": {"context_paths": ["x"]}}`, `"disable_metrics": true`,
		func(h string) *Agent { return crush(h, filepath.Join(h, ".config")) }, []string{"options.disable_metrics"},
		`{"options": {"disable_metrics": false}}`, `{"options": {"disable_metrics": true}}`},
	{"kimi", ".kimi-code/config.toml", "default_model = \"a\"\n\n[providers.x]\nbase_url = \"u\"\n", "telemetry = false",
		kimi, []string{"telemetry"}, "telemetry = true\n", "telemetry = false\n"},
	{"goose", ".config/goose/config.yaml", "GOOSE_MODE: auto\n", "GOOSE_TELEMETRY_ENABLED: false",
		func(h string) *Agent { return goose(h, filepath.Join(h, ".config")) }, []string{"GOOSE_TELEMETRY_ENABLED"},
		"GOOSE_TELEMETRY_ENABLED: true\n", "GOOSE_TELEMETRY_ENABLED: false\n"},
	{"pi", ".pi/agent/settings.json", `{"theme": "dark"}`, `"enableInstallTelemetry": false`,
		pi, []string{"enableInstallTelemetry"}, `{"enableInstallTelemetry": true}`, `{"enableInstallTelemetry": false}`},
	{"cline", ".cline/data/settings/global-settings.json", `{"autoUpdateEnabled": true}`, `"telemetryOptOut": true`,
		cline, []string{"telemetryOptOut"}, `{"telemetryOptOut": false}`, `{"telemetryOptOut": true}`},
}

// has: the file says off, however it spaces it
func has(body, off string) bool {
	squash := strings.NewReplacer(" ", "", "\n", "")
	return strings.Contains(squash.Replace(body), squash.Replace(off))
}

func TestMoreAgentsTelemetrySwitch(t *testing.T) {
	for _, c := range moreTelemetry {
		t.Run(c.name, func(t *testing.T) {
			home, path := telemetryHome(t, c.file, c.before)
			a := c.of(home)
			if st := telemetryOf(t, a); st.Off || len(st.Own) != 0 || strings.Join(st.Keys, ",") != strings.Join(c.keys, ",") {
				t.Fatalf("before: %+v", st)
			}
			if err := a.SetTelemetry(true); err != nil {
				t.Fatal(err)
			}
			got := readFile(path)
			if !has(got, c.off) {
				t.Fatalf("off:\n%s", got)
			}
			if st := telemetryOf(t, c.of(home)); !st.Off || len(st.Own) != 0 {
				t.Fatalf("after off: %+v", st)
			}
			if err := a.SetTelemetry(true); err != nil || readFile(path) != got {
				t.Fatalf("off again: %v\n%s", err, readFile(path))
			}
			if err := c.of(home).SetTelemetry(false); err != nil {
				t.Fatal(err)
			}
			if after := readFile(path); has(after, c.off) || !strings.Contains(after, strings.Fields(c.before)[0]) {
				t.Fatalf("on:\n%s", after)
			}
		})
	}
}

// A value of the user's that keeps telemetry on is never written over; one
// that already turns it off stays theirs when magpie's goes.
func TestMoreAgentsTelemetryLeavesTheUsers(t *testing.T) {
	for _, c := range moreTelemetry {
		t.Run(c.name, func(t *testing.T) {
			home, path := telemetryHome(t, c.file, c.theirs)
			if err := c.of(home).SetTelemetry(true); err == nil {
				t.Fatal("the user's on was written over")
			}
			if readFile(path) != c.theirs {
				t.Fatalf("written:\n%s", readFile(path))
			}
			home, path = telemetryHome(t, c.file, c.own)
			a := c.of(home)
			if st := telemetryOf(t, a); !st.Off || len(st.Own) != 1 {
				t.Fatalf("their own off: %+v", st)
			}
			if err := a.SetTelemetry(true); err != nil {
				t.Fatal(err)
			}
			if err := c.of(home).SetTelemetry(false); err == nil {
				t.Fatal("their own off was taken as magpie's")
			}
			if readFile(path) != c.own {
				t.Fatalf("changed:\n%s", readFile(path))
			}
		})
	}
}

// A config that doesn't parse is unknown: nothing is written.
func TestMoreAgentsTelemetryUnreadable(t *testing.T) {
	bad := map[string]string{"crush": `{"options": `, "kimi": "[providers\ntelemetry = ", "goose": "a: [\n  b: {", "pi": `{"x": [`, "cline": `{`}
	for _, c := range moreTelemetry {
		t.Run(c.name, func(t *testing.T) {
			home, path := telemetryHome(t, c.file, bad[c.name])
			a := c.of(home)
			if _, err := a.Telemetry.Get(); err == nil {
				t.Fatal("an unreadable config read as a state")
			}
			if err := a.SetTelemetry(true); err == nil {
				t.Fatal("off written into an unreadable config")
			}
			if readFile(path) != bad[c.name] {
				t.Fatalf("written:\n%s", readFile(path))
			}
		})
	}
}

// Only Pi itself has the switch, not a fork of it.
func TestPiForksHaveNoTelemetry(t *testing.T) {
	home, _ := telemetryHome(t, ".pi/agent/settings.json", `{}`)
	if pi(home).Telemetry == nil {
		t.Fatal("pi has none")
	}
	for _, a := range All() {
		if a.ID != "pi" && strings.HasPrefix(a.ID, "pi") && a.Telemetry != nil {
			t.Fatalf("%s has Pi's key", a.ID)
		}
	}
}

// Settings › Privacy reaches them too.
func TestTelemetryAllReachesMoreAgents(t *testing.T) {
	home, _ := telemetryHome(t, ".config/crush/crush.json", `{}`)
	onPath(t, "crush", "kimi", "goose", "pi", "cline")
	for _, c := range moreTelemetry {
		if c.name != "crush" {
			writeAt(t, filepath.Join(home, c.file), c.before)
		}
	}
	if err := SetTelemetryAll(true); err != nil {
		t.Fatal(err)
	}
	for _, c := range moreTelemetry {
		if got := readFile(filepath.Join(home, c.file)); !has(got, c.off) {
			t.Errorf("%s not turned off:\n%s", c.name, got)
		}
	}
	if err := SetTelemetryAll(false); err != nil {
		t.Fatal(err)
	}
	if settings.Load().AgentTelemetryOff {
		t.Fatal("still on")
	}
	for _, c := range moreTelemetry {
		if got := readFile(filepath.Join(home, c.file)); has(got, c.off) {
			t.Errorf("%s not back on:\n%s", c.name, got)
		}
	}
}
