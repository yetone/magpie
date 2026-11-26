package agent

import (
	"context"
	"strings"
	"testing"
	"time"
)

// ZCode reads provider_config.json again every second while it runs (3.14
// to 4.0.0), so magpie's change to its list while the app runs offers no
// Restart and gives no "restart ZCode" notice (bitssly on X: the row said
// 已接入 · 重启 ZCode 后生效 while the change had already reached it).
func TestZCodeRunningGetsNoRestart(t *testing.T) {
	home := restartSandbox(t)
	restartOnWindows = true
	const exe = `C:\Users\me\AppData\Local\Programs\ZCode\ZCode.exe`
	winProcs = func(name string) []winProc {
		if !strings.EqualFold(name, "ZCode.exe") {
			return nil
		}
		return []winProc{{pid: 11, parent: 4, image: exe, start: time.Now().Add(-time.Hour)}}
	}
	winClose = func([]int) error { t.Error("ZCode was asked to close"); return nil }
	winEnd = func(int) error { t.Error("ZCode was ended"); return nil }
	winLaunch = func(winProc) error { t.Error("ZCode was opened"); return nil }

	a := zcode(home)
	if err := a.Field("provider").Set("magpie"); err != nil {
		t.Fatal(err)
	}
	if !a.Wired() {
		t.Fatal("not connected")
	}
	if got := a.AppBehind(); got != nil {
		t.Fatalf("a Restart offered for a ZCode that started before magpie's change: %+v", got)
	}
	if a.Notice != nil {
		if n := a.Notice(); n != "" {
			t.Fatalf("notice %q", n)
		}
	}
	if err := a.RestartApp(context.Background()); err == nil {
		t.Fatal("RestartApp restarted ZCode")
	}
}
