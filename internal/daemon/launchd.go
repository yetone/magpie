package daemon

import (
	"html"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// launchd runs the service as a launch agent of the user's: started at
// login, and again when it stops with a failure.
type launchd struct{}

// Label is the launch agent's; the tray's at login is com.yetone.magpie.
const Label = "com.yetone.magpie.serve"

func home() string {
	h, _ := os.UserHomeDir()
	return h
}

func (launchd) file() string {
	return filepath.Join(home(), "Library", "LaunchAgents", Label+".plist")
}

func logFile() string { return filepath.Join(home(), "Library", "Logs", "magpie", "serve.log") }

func (launchd) logs() string { return "tail -f " + strconv.Quote(logFile()) }

func domain() string  { return "gui/" + strconv.Itoa(os.Getuid()) }
func service() string { return domain() + "/" + Label }

// plist is the launch agent running exe with args, with env set in it.
// ProcessType is Standard, not Background: the gateway is on the request
// path of interactive agents, and Background throttles it on macOS.
func plist(exe string, args, env []string) string {
	s := func(v string) string { return "<string>" + html.EscapeString(v) + "</string>" }
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<!-- Written by magpie daemon install; magpie daemon uninstall removes it. -->
<plist version="1.0">
<dict>
	<key>Label</key>` + s(Label) + `
	<key>ProgramArguments</key>
	<array>` + s(exe) + strings.Join(mapped(args, s), "") + `</array>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key>
	<dict><key>SuccessfulExit</key><false/></dict>
	<key>ThrottleInterval</key><integer>5</integer>
	` + processType + `
	<key>StandardOutPath</key>` + s(logFile()) + `
	<key>StandardErrorPath</key>` + s(logFile()) + `
`)
	if len(env) > 0 {
		b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
		for _, kv := range env {
			k, v, _ := strings.Cut(kv, "=")
			b.WriteString("\t\t<key>" + html.EscapeString(k) + "</key>" + s(v) + "\n")
		}
		b.WriteString("\t</dict>\n")
	}
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

func (l launchd) install(exe string, args, env []string) error {
	if err := os.MkdirAll(filepath.Dir(l.file()), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(logFile()), 0o755); err != nil {
		return err
	}
	if err := private(l.file(), []byte(plist(exe, args, env))); err != nil {
		return err
	}
	// one loaded before runs the old plist until it is taken out
	l.unload()
	_, err := launchctl("bootstrap", domain(), l.file())
	return err
}

func (l launchd) uninstall() error {
	if _, err := os.Stat(l.file()); err != nil {
		return nil
	}
	l.unload()
	if err := os.Remove(l.file()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// loaded reports whether launchd has the agent, running or not.
func (launchd) loaded() bool {
	_, err := launchctl("print", service())
	return err == nil
}

// unload takes the agent out of launchd, stopping it; one not loaded is
// no failure.
func (l launchd) unload() {
	if l.loaded() {
		launchctl("bootout", service())
	}
}

func (l launchd) start() error {
	if l.mended() {
		l.unload()
	}
	if !l.loaded() {
		_, err := launchctl("bootstrap", domain(), l.file())
		return err
	}
	_, err := launchctl("kickstart", service())
	return err
}

// stop takes it out of launchd: stopped alone, KeepAlive would have it
// back. The next login, or start, loads it again.
func (l launchd) stop() error {
	if !l.loaded() {
		return nil
	}
	_, err := launchctl("bootout", service())
	return err
}

// restart takes the agent out and loads it again: launchd reads the file
// when it loads it, not on a kickstart, so a file mended or changed since
// (Mend, as the service started) is what runs.
func (l launchd) restart() error {
	l.unload()
	return l.start()
}

// oldProcessType is what an agent written before #2961 runs the gateway
// as, and processType what plist writes now.
const (
	oldProcessType = "<key>ProcessType</key><string>Background</string>"
	processType    = "<key>ProcessType</key><string>Standard</string>"
)

// mended rewrites an agent written before #2961, which ran the gateway as
// Background, to run it as Standard, and says whether it did. It changes
// that line alone: the rest is the user's install. launchd takes it up
// when it next loads the agent: at the next login, or a start or restart.
func (l launchd) mended() bool {
	b, err := os.ReadFile(l.file())
	if err != nil || !strings.Contains(string(b), oldProcessType) {
		return false
	}
	if err := private(l.file(), []byte(strings.Replace(string(b), oldProcessType, processType, 1))); err != nil {
		log.Printf("daemon: can't rewrite %s to run the gateway as Standard: %v", l.file(), err)
		return false
	}
	return true
}

func (l launchd) status() (Status, error) {
	// a plist in LaunchAgents is loaded at every login
	st := Status{Enabled: true}
	out, err := launchctl("print", service())
	if err != nil {
		st.State = "not loaded"
		return st, nil
	}
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), " = ")
		if !ok {
			continue
		}
		switch k {
		case "state":
			if st.State == "" {
				st.State = v
			}
		case "pid":
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				st.PID = n
			}
		case "last exit code":
			if v != "0" && !strings.HasPrefix(v, "(never") {
				st.LastExit = v
			}
		}
	}
	st.Running = st.State == "running"
	return st, nil
}

func mapped(xs []string, f func(string) string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = f(x)
	}
	return out
}

func launchctl(args ...string) (string, error) { return run("launchctl", args...) }
