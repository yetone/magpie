package library

import (
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

// #1025's note — "RTK 0.51.0 is out, waiting for winget" — is a grey tag
// beside the version. It is what the package manager is asked for, and asking
// one is slow: `winget show` refreshes its sources, measured at 7.7 seconds on
// the machine this was found on, while the read that draws the tab waits at
// most 3. So the tag never arrived and every opening of the tab paid those
// three seconds for an answer nobody saw.
//
// The release itself (GitHub) is what the tab needs first — it is what says
// "v0.51.0 is out" or "Up to date" — so that stays on the way out. The package
// manager's answer is asked in the background (CheckChannel) and kept, so the
// next read of the tab draws with it.
//
// What is judged here is that separation, by counting rather than timing, so
// it holds on every platform: a read that draws the tab asks the package
// manager for nothing.
func TestCheckLatestNeverAsksThePackageManager(t *testing.T) {
	h := sandbox(t)
	latestServer(t, "v0.51.0")

	// an rtk out of date, found by its own name (what an installer puts on
	// PATH), installed through winget
	bin := filepath.Join(h, "bin")
	if runtime.GOOS == "windows" {
		write(t, filepath.Join(bin, "rtk.exe"), "")
	} else {
		testenv.Program(t, filepath.Join(bin, "rtk"), "#!/bin/sh\ncase \"$1\" in\n--version) echo 'rtk 0.50.0' ;;\nesac\nexit 0\n")
	}
	t.Setenv("PATH", bin)
	up := upgraderOf
	upgraderOf = func(string) []string { return []string{"winget", "show", "--id", "rtk-ai.rtk", "--exact"} }
	t.Cleanup(func() { upgraderOf = up })

	// a package manager that counts how often it is asked, rather than one
	// that is slow: the judgement is which read asks at all
	asks := 0
	ask := rtkChannelLatest
	rtkChannelLatest = func(c []string) string { asks++; return ask(c) }
	t.Cleanup(func() { rtkChannelLatest = ask })

	forgetChannels()
	v := ReadRTK()
	if v.Path == "" {
		t.Skip("no rtk in the sandbox to ask about")
	}
	// what the read says of this rtk: the version is what rtk itself said,
	// and on Windows the stand-in can't say it (there is no shell to run), so
	// the judgement is made about the view as the card draws it
	v.Version = "0.50.0"
	v.CheckLatest()

	if asks != 0 {
		t.Errorf("the read that draws the tab asked the package manager %d times, want 0: it is asked in the background (CheckChannel), since `winget show` refreshes its sources and takes seconds", asks)
	}
	if v.Latest != "0.51.0" {
		t.Errorf("Latest = %q, want 0.51.0: the release is what the card needs first", v.Latest)
	}

	// and it is asked in the background, and its answer is kept for the next
	// read to draw with (as rtkChannelLatest keeps it: six hours)
	forgetChannels()
	rtkChannelLatest = func(c []string) string {
		asks++
		rtkChannels.Lock()
		rtkChannels.m[c[0]] = rtkChannel{v: "0.50.0", next: time.Now().Add(6 * time.Hour)}
		rtkChannels.Unlock()
		return "0.50.0"
	}
	before := asks
	CheckChannel(v)
	if asks != before+1 {
		t.Fatalf("CheckChannel asked the package manager %d times, want 1", asks-before)
	}
	again := ReadRTK()
	again.Version = "0.50.0"
	again.CheckLatest()
	if again.Waiting != "winget" || again.WaitingHas != "0.50.0" {
		t.Fatalf("the tag once the answer came: waiting=%q has=%q, want winget/0.50.0", again.Waiting, again.WaitingHas)
	}
	if asks != before+1 {
		t.Errorf("the next read asked again (%d in all): it should draw with the answer kept", asks)
	}
}
