package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// ZCode reads its providers at start: while the app runs on a list older
// than magpie's last write, its row offers a Restart, and the Restart quits
// and opens it again (yetone/magpie#5662).
func TestZCodeRestartOnWindows(t *testing.T) {
	home := restartSandbox(t)
	restartOnWindows = true
	appCloseWait, appBackWait = 50*time.Millisecond, time.Second
	const exe = `C:\Users\me\AppData\Local\Programs\ZCode\ZCode.exe`
	start := time.Now().Add(-time.Hour)
	alive := true
	winProcs = func(name string) []winProc {
		if !alive || !strings.EqualFold(name, "ZCode.exe") {
			return nil
		}
		return []winProc{{pid: 11, parent: 4, image: exe, start: start}}
	}
	var did []string
	winClose = func(pids []int) error {
		did = append(did, "close")
		return nil
	}
	winEnd = func(pid int) error {
		did = append(did, "end")
		alive = false
		return nil
	}
	winLaunch = func(p winProc) error {
		did = append(did, "open")
		alive, start = true, time.Now()
		return nil
	}

	a := zcode(home)
	if err := a.Field("provider").Set("magpie"); err != nil {
		t.Fatal(err)
	}
	got := a.AppBehind()
	if got == nil || !got.Since.Equal(start) {
		t.Fatalf("behind: %+v, want since %v", got, start)
	}
	if err := a.RestartApp(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"close", "end", "open"}; !slices.Equal(did, want) {
		t.Fatalf("did %q, want %q", did, want)
	}
	if a.AppBehind() != nil {
		t.Fatal("still offered after the restart")
	}
}

// zcodePart is magpie's part of ZCode's two files only: another provider's
// rule, which ZCode writes itself, is no change of magpie's.
func TestZCodePartIsMagpiesOnly(t *testing.T) {
	dir := t.TempDir()
	cfg, rules := filepath.Join(dir, "config.json"), filepath.Join(dir, "provider_config.json")
	if err := os.WriteFile(cfg, []byte(`{"provider":{"magpie":{"options":{"apiKey":"secret"},"models":{"a":{"limit":{"output":1}}}},"other":{"models":{"b":{}}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rules, []byte(`{"config":{"providerConfigRules":{"providerRules":[{"providerId":"magpie","config":{"access":{"apiKey":"secret"},"x":1}},{"providerId":"other","config":{"x":2}}]},"modelConfigRules":{"providerModelRules":[{"providerId":"magpie","modelId":"a"},{"providerId":"other","modelId":"b"}],"manualProviderModelRules":[{"providerId":"magpie","modelId":"a"}]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got := zcodePart(cfg, rules)
	if strings.Contains(got, `"other"`) || strings.Contains(got, "secret") {
		t.Fatalf("another provider's rule or magpie's key is in magpie's part:\n%s", got)
	}
	for _, want := range []string{`"models":{"a":{"limit":{"output":1}}}`, `"providerId":"magpie"`, `"modelId":"a"`, `"x":1`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
}
