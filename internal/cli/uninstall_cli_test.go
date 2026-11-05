package cli

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/daemon"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/uninstall"
)

func fakeUninstall(t *testing.T, served gateway.Served, service daemon.Status) *int {
	t.Helper()
	ran := 0
	wasServed, wasStatus, wasRun, wasKeep := servedBy, serviceStatus, runUninstall, runKeepData
	t.Cleanup(func() { servedBy, serviceStatus, runUninstall, runKeepData = wasServed, wasStatus, wasRun, wasKeep })
	servedBy = func() gateway.Served { return served }
	serviceStatus = func() (daemon.Status, error) { return service, nil }
	runUninstall = func(context.Context) *uninstall.Report { ran++; return &uninstall.Report{} }
	runKeepData = func(context.Context) *uninstall.Report { ran += 100; return &uninstall.Report{} }
	return &ran
}

// install.sh --uninstall keeps magpie's settings, and took the gateway
// away with Codex still pointed at it (whimsical_wolf_15477 on Discord):
// --keep-data disconnects every agent and deletes none of magpie's files.
func TestUninstallKeepData(t *testing.T) {
	ran := fakeUninstall(t, gateway.Served{}, daemon.Status{})
	if err := uninstallCmd([]string{"--yes", "--keep-data"}); err != nil || *ran != 100 {
		t.Fatalf("err %v, ran %d", err, *ran)
	}
	// without --yes and no terminal: nothing, and the refusal doesn't
	// say the settings go
	ran = fakeUninstall(t, gateway.Served{}, daemon.Status{})
	err := uninstallCmd([]string{"--keep-data"})
	if err == nil || strings.Contains(err.Error(), "deletes") || *ran != 0 {
		t.Fatalf("err %v, ran %d", err, *ran)
	}
}

// install.sh --uninstall deletes magpie after it runs, a downloaded copy
// in a temporary folder too: under the script, magpie doesn't tell the
// user to delete a path that is about to be gone.
func TestUninstallScriptLeavesExeToIt(t *testing.T) {
	fakeUninstall(t, gateway.Served{}, daemon.Status{})
	runKeepData = func(context.Context) *uninstall.Report {
		return &uninstall.Report{Exe: "/tmp/tmp.x/magpie-cli-linux-amd64"}
	}
	out, err := stdoutOf(t, func() error { return uninstallCmd([]string{"--yes", "--keep-data"}) })
	if err != nil || !strings.Contains(out, "/tmp/tmp.x/magpie-cli-linux-amd64") {
		t.Fatalf("by hand: err %v, out %q", err, out)
	}
	t.Setenv("MAGPIE_UNINSTALL_SCRIPT", "1")
	out, err = stdoutOf(t, func() error { return uninstallCmd([]string{"--yes", "--keep-data"}) })
	if err != nil || strings.Contains(out, "magpie-cli-linux-amd64") || strings.Contains(out, "magpie is uninstalled") {
		t.Fatalf("under the script: err %v, out %q", err, out)
	}
}

// A magpie still serving that isn't the background service, which
// uninstalling removes first, would keep writing its files after its
// folders are gone: it is quit first, and nothing is changed.
func TestUninstallRefusesWhileMagpieRuns(t *testing.T) {
	ran := fakeUninstall(t, gateway.Served{Running: true, Pid: 4242, Serves: "app"}, daemon.Status{})
	err := uninstallCmd([]string{"--yes"})
	if err == nil || !strings.Contains(err.Error(), "magpie's app is running (pid 4242)") || *ran != 0 {
		t.Fatalf("err %v, ran %d", err, *ran)
	}
	// the background service itself is stopped and removed by uninstalling
	ran = fakeUninstall(t, gateway.Served{Running: true, Pid: 4242, Serves: "serve"}, daemon.Status{Installed: true, Running: true, PID: 4242})
	if err := uninstallCmd([]string{"--yes"}); err != nil || *ran != 1 {
		t.Fatalf("err %v, ran %d", err, *ran)
	}
	// a magpie serve beside an installed service is another one
	ran = fakeUninstall(t, gateway.Served{Running: true, Pid: 4242, Serves: "serve"}, daemon.Status{Installed: true, Running: true, PID: 99})
	if err := uninstallCmd([]string{"--yes"}); err == nil || *ran != 0 {
		t.Fatalf("err %v, ran %d", err, *ran)
	}
}

// With no terminal to ask on, only --yes uninstalls; a word it doesn't
// know changes nothing.
func TestUninstallAsksFirst(t *testing.T) {
	ran := fakeUninstall(t, gateway.Served{}, daemon.Status{})
	if err := uninstallCmd(nil); err == nil || !strings.Contains(err.Error(), "--yes") || *ran != 0 {
		t.Fatalf("err %v, ran %d", err, *ran)
	}
	if err := uninstallCmd([]string{"--help-me"}); err == nil || *ran != 0 {
		t.Fatalf("err %v, ran %d", err, *ran)
	}
	if err := uninstallCmd([]string{"--yes"}); err != nil || *ran != 1 {
		t.Fatalf("err %v, ran %d", err, *ran)
	}
}

// --help prints its usage and uninstalls nothing; install.sh reads
// --keep-data in it to know this magpie can disconnect and keep its files.
func TestUninstallHelp(t *testing.T) {
	ran := fakeUninstall(t, gateway.Served{}, daemon.Status{})
	if !helpAsked([]string{"uninstall", "--help"}) || !strings.Contains(helpFor([]string{"uninstall"}), "magpie uninstall [--yes]") || !strings.Contains(helpFor([]string{"uninstall"}), "--keep-data") || *ran != 0 {
		t.Fatal("no help for uninstall")
	}
}

// --data-after with no process to wait for (0, a negative, this one)
// refuses at once: kill(0) asks the caller's own group, alive while it
// runs, and the files went only when the wait ran out.
func TestUninstallDataAfterNeedsAProcess(t *testing.T) {
	for _, a := range []string{"0", "-1", "x", strconv.Itoa(os.Getpid())} {
		if err := uninstallData([]string{a}); err == nil || !strings.Contains(err.Error(), "nothing was changed") {
			t.Errorf("%s: %v", a, err)
		}
	}
}
