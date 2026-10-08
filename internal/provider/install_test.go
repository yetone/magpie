package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

// a sign-in that needs a CLI this machine lacks installs it, then goes on
func TestSignInInstallsMissingCLI(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("the fake devin is a shell script")
	}
	home := claudeHome(t)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	exe := filepath.Join(home, "bin", "devin")
	oldExe := DevinExecutable
	DevinExecutable = func() string {
		if isFile(exe) {
			return exe
		}
		return ""
	}
	t.Cleanup(func() { DevinExecutable = oldExe })
	release := make(chan struct{})
	var ran string
	oldRun := runInstaller
	runInstaller = func(ctx context.Context, c agentCLI) ([]byte, error) {
		ran = c.sh
		<-release
		os.MkdirAll(filepath.Dir(exe), 0o755)
		testenv.Program(t, exe, "#!/bin/sh\nprintf 'Logged in (via Devin).\\n  Email: dev@example.com\\n  Tier: Devin Pro\\n'\n")
		// as Devin's does: `devin setup` gives up on its sign-in without a terminal
		return []byte("Installed devin\nError: Login canceled\n"), errors.New("exit status 1")
	}
	t.Cleanup(func() { runInstaller = oldRun })
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"sessionToken":"devin-session-token$sk-test"}`))
	}))
	defer fake.Close()
	oldTok := devinExchangeURL
	devinExchangeURL = fake.URL
	t.Cleanup(func() { devinExchangeURL = oldTok })

	st, err := StartSignIn("devin")
	if err != nil {
		t.Fatal(err)
	}
	if st.State != "installing" || st.Installing != "Devin CLI" || st.URL != "" {
		t.Fatalf("started %+v", st)
	}
	close(release)
	st = waitPast(t, st.ID, "installing")
	if st.State != "waiting" || !strings.HasPrefix(st.URL, devinAuthorizeURL) || st.Installing != "" {
		t.Fatalf("after install %+v", st)
	}
	if !strings.Contains(ran, "cli.devin.ai/install.sh") {
		t.Fatalf("installer %q", ran)
	}
	finishInBrowser(t, st, "dv-code")
	if st = waitDone(t, st.ID); st.State != "done" || st.User != "dev@example.com" {
		t.Fatalf("state %+v", st)
	}
}

// an installer that fails says so, and how to install it by hand
func TestSignInInstallFails(t *testing.T) {
	claudeHome(t)
	oldExe := DevinExecutable
	DevinExecutable = func() string { return "" }
	t.Cleanup(func() { DevinExecutable = oldExe })
	oldRun := runInstaller
	runInstaller = func(ctx context.Context, c agentCLI) ([]byte, error) {
		return []byte("downloading…\n\x1b[0;31mError: Failed to download\x1b[0m\n"), errors.New("exit status 1")
	}
	t.Cleanup(func() { runInstaller = oldRun })

	st, err := StartSignIn("devin")
	if err != nil {
		t.Fatal(err)
	}
	st = waitPast(t, st.ID, "installing")
	if st.State != "failed" || !strings.Contains(st.Error, "Error: Failed to download") || !strings.Contains(st.Error, "install it yourself") {
		t.Fatalf("state %+v", st)
	}
}

// on Windows the vendor's one-liner reaches PowerShell on stdin, never on
// its command line: Microsoft Defender took "powershell … -Command irm
// https://static.devin.ai/cli/setup.ps1 | iex" for a Trojan
// (Trojan:Win32/Commando.A!ml) and stopped it before it ran, so Devin's
// sign-in failed with "fork/exec …\powershell.exe: Access is denied"
func TestWindowsInstallerNotOnCommandLine(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("PowerShell's command line is Windows'")
	}
	c := agentCLI{Name: "Probe CLI", ps: "Write-Output ([Environment]::CommandLine); exit 7"}
	out, err := runInstaller(context.Background(), c)
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 7 {
		t.Fatalf("the installer's exit: %v (output %q)", err, out)
	}
	line := lastLine(out)
	if !strings.Contains(strings.ToLower(line), "powershell") {
		t.Fatalf("the installer didn't print PowerShell's command line: %q", out)
	}
	if strings.Contains(line, "Write-Output") {
		t.Fatalf("PowerShell's command line carries the installer: %s", line)
	}
}

// canceled while installing: the sign-in never starts
func TestSignInCanceledWhileInstalling(t *testing.T) {
	claudeHome(t)
	oldExe := DevinExecutable
	DevinExecutable = func() string { return "" }
	t.Cleanup(func() { DevinExecutable = oldExe })
	stopped := make(chan struct{})
	oldRun := runInstaller
	runInstaller = func(ctx context.Context, c agentCLI) ([]byte, error) {
		<-ctx.Done()
		close(stopped)
		return nil, ctx.Err()
	}
	t.Cleanup(func() { runInstaller = oldRun })

	st, _ := StartSignIn("devin")
	CancelSignIn(st.ID)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the installer went on")
	}
	if st, _ = SignInStatus(st.ID); st.State != "canceled" {
		t.Fatalf("state %+v", st)
	}
}

// signed in, but the CLI can't see it: a failure, not "done" with no one
func TestDevinSignInUnseen(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("the fake devin is a shell script")
	}
	home := claudeHome(t)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	exe := filepath.Join(home, "devin")
	testenv.Program(t, exe, "#!/bin/sh\necho 'Not logged in.'\n")
	oldExe := DevinExecutable
	DevinExecutable = func() string { return exe }
	t.Cleanup(func() { DevinExecutable = oldExe })
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"sessionToken":"devin-session-token$sk-test"}`))
	}))
	defer fake.Close()
	oldTok := devinExchangeURL
	devinExchangeURL = fake.URL
	t.Cleanup(func() { devinExchangeURL = oldTok })

	st, err := StartSignIn("devin")
	if err != nil {
		t.Fatal(err)
	}
	finishInBrowser(t, st, "dv-code")
	if st = waitDone(t, st.ID); st.State != "failed" || !strings.Contains(st.Error, "devin auth status") {
		t.Fatalf("state %+v", st)
	}
}

func waitPast(t *testing.T, id, state string) SignInState {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if st, _ := SignInStatus(id); st.State != state {
			return st
		}
	}
	t.Fatalf("still %s", state)
	return SignInState{}
}

// magpie's Docker image has bash and busybox's wget but no curl, which every
// vendor's one-liner downloads with: there the native installer is used
func TestShellInstallerNeedsCurl(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the shell installer is for macOS and Linux")
	}
	bin := t.TempDir()
	for _, p := range []string{"bash", "wget"} {
		testenv.Program(t, filepath.Join(bin, p), "#!/bin/sh\n")
	}
	t.Setenv("PATH", bin)
	if shellInstallerRuns() {
		t.Fatal("the shell installer runs with bash and wget, without curl")
	}
	testenv.Program(t, filepath.Join(bin, "curl"), "#!/bin/sh\n")
	if !shellInstallerRuns() {
		t.Fatal("the shell installer doesn't run with bash and curl")
	}
}
