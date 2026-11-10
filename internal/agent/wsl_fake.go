package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// FakeWSL, for other packages' tests, stands in for wsl.exe on any
// system: each distro named in probes is installed and running, answers
// the probe with what probes has for it (wslProbeScript()'s output: "home:/
// home/me\ndir:.codex\n…") and is opened at roots' folder for it. What
// magpie remembers of WSL is forgotten, before and after; the returned
// func puts everything back.
func FakeWSL(probes, roots map[string]string) func() {
	on, run, root, exec := wslOn, wslRun, wslRoot, wslExec
	reset := func() {
		wsl.Lock()
		wsl.seen, wsl.at, wsl.runAt, wsl.names, wsl.running, wsl.probed, wsl.failed, wsl.dirty = nil, time.Time{}, time.Time{}, nil, nil, nil, nil, false
		wsl.Unlock()
	}
	reset()
	var names []string
	for n := range probes {
		names = append(names, n)
	}
	list := []byte(strings.Join(names, "\n"))
	stopped := map[string]bool{}
	fakeStop = func(name string, stop bool) {
		wsl.Lock()
		stopped[name] = stop
		wsl.runAt = time.Time{}
		wsl.Unlock()
	}
	fakeCalls.Lock()
	fakeCalls.in = map[string]int{}
	fakeCalls.Unlock()
	wslOn = true
	wslRun = func(_ time.Duration, args ...string) ([]byte, error) {
		switch strings.Join(args, " ") {
		case "-l -q":
			return list, nil
		case "-l --running -q":
			var run []string
			for _, n := range names {
				if !stopped[n] {
					run = append(run, n)
				}
			}
			return []byte(strings.Join(run, "\n")), nil
		}
		if len(args) > 2 && args[0] == "-d" {
			fakeCalls.Lock()
			fakeCalls.in[args[1]]++
			fakeCalls.Unlock()
			if p, ok := probes[args[1]]; ok {
				return []byte(p), nil
			}
		}
		return nil, errors.New("unexpected wsl.exe " + strings.Join(args, " "))
	}
	wslRoot = func(name string) string { return roots[name] }
	wslExec = func(name string, argv ...string) ([]byte, error) {
		fakeCalls.Lock()
		fakeCalls.in[name]++
		fakeCalls.Unlock()
		return fakeLinks(roots[name], argv)
	}
	links.Lock()
	links.m = nil
	links.Unlock()
	return func() {
		wslOn, wslRun, wslRoot, wslExec = on, run, root, exec
		links.Lock()
		links.m = nil
		links.Unlock()
		fakeStop = nil
		reset()
	}
}

var fakeStop func(string, bool)

// fakeCalls counts, for each of FakeWSL's distros, the commands run in it
// (wsl.exe -d name …): each one would start a stopped distro.
var fakeCalls struct {
	sync.Mutex
	in map[string]int
}

// StopFakeWSL, for other packages' tests, has FakeWSL's distro name stop,
// as the user stopping it (wsl --shutdown) does: wsl.exe lists it as not
// running from now on.
func StopFakeWSL(name string) {
	if fakeStop != nil {
		fakeStop(name, true)
	}
}

// StartFakeWSL has a distro StopFakeWSL stopped run again, as the user
// opening a WSL shell does.
func StartFakeWSL(name string) {
	if fakeStop != nil {
		fakeStop(name, false)
	}
}

// FakeWSLRuns is how many commands magpie ran in FakeWSL's distro name
// (wsl.exe -d name …), each of which starts a stopped distro.
func FakeWSLRuns(name string) int {
	fakeCalls.Lock()
	defer fakeCalls.Unlock()
	return fakeCalls.in[name]
}

// FakeWSLDrives, for other packages' tests, has Windows' drives ("C:")
// opened at roots' folder for each, where a link in a FakeWSL distro to
// /mnt/c leads (distro.open); the returned func puts it back.
func FakeWSLDrives(roots map[string]string) func() {
	was := wslDrive
	wslDrive = func(drive string) string {
		if r, ok := roots[drive]; ok {
			return r + string(filepath.Separator)
		}
		return was(drive)
	}
	return func() { wslDrive = was }
}

// fakeLinks does what wsl_links.go's scripts do in a distro opened at
// root, on this system's own links: a link the distro would lead to
// /home/me/x leads to root's home/me/x here, which this system follows.
func fakeLinks(root string, argv []string) ([]byte, error) {
	if len(argv) < 5 || argv[0] != "sh" || argv[1] != "-c" {
		return nil, errors.New("unexpected command in WSL: " + strings.Join(argv, " "))
	}
	at := func(linux string) string { return filepath.Join(root, filepath.FromSlash(linux)) }
	args := argv[4:]
	switch argv[2] {
	case wslLinkScript:
		to, link := at(args[0]), at(args[1])
		if fi, err := os.Lstat(link); err == nil && fi.Mode()&os.ModeSymlink == 0 {
			return []byte(args[1] + " is not a link"), errors.New("exit status 3")
		}
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			return nil, err
		}
		os.Remove(link)
		return nil, os.Symlink(to, link)
	case wslUnlinkScript:
		p := at(args[0])
		fi, err := os.Lstat(p)
		if err != nil {
			return nil, nil
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			return []byte(args[0] + " is not a link"), errors.New("exit status 3")
		}
		return nil, os.Remove(p)
	case wslLinksScript:
		es, _ := os.ReadDir(at(args[0]))
		var b strings.Builder
		for _, e := range es {
			if to, err := os.Readlink(filepath.Join(at(args[0]), e.Name())); err == nil {
				// spelt as the distro spells it
				if rel, ok := strings.CutPrefix(to, root); ok {
					to = filepath.ToSlash(rel)
				}
				fmt.Fprintf(&b, "%s\t%s\n", e.Name(), to)
			}
		}
		return []byte(b.String()), nil
	}
	return nil, errors.New("unexpected script in WSL: " + argv[2])
}

// realWSLRun is wsl.exe itself, for FakeWSLRealLinks.
var realWSLRun = wslRun

// FakeWSLRealLinks, for a test on Windows with a real distro behind
// FakeWSL's root (\\wsl.localhost\<distro>), has the links in it made,
// read and removed by the real wsl.exe, in that distro alone.
func FakeWSLRealLinks() (restore func()) {
	exec := wslExec
	wslExec = func(distro string, argv ...string) ([]byte, error) {
		return realWSLRun(30*time.Second, append([]string{"-d", distro, "--exec"}, argv...)...)
	}
	return func() { wslExec = exec }
}
