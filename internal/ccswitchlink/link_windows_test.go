package ccswitchlink

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows/registry"
)

const link = "ccswitch://v1/import?resource=provider&app=claude&name=Relay&apiKey=sk-x"

type winSetup struct {
	self, cc string
	started  *[][2]string
}

func touch(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// winForward points the handler at a key of the test's own, so the user's
// ccswitch:// handler is never read or written.
func winForward(t *testing.T, handler func(self string) string, installed bool) winSetup {
	t.Helper()
	root := t.TempDir()
	s := winSetup{self: filepath.Join(root, "magpie.exe"), cc: filepath.Join(root, "local", "Programs", "CC Switch", "cc-switch.exe")}
	touch(t, s.self)
	if installed {
		touch(t, s.cc)
	}
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "local"))
	k, e, st := schemeKey, executable, start
	schemeKey = fmt.Sprintf(`Software\Classes\magpie-test-ccswitch-%d`, os.Getpid())
	t.Cleanup(func() {
		for _, sub := range []string{`\shell\open\command`, `\shell\open`, `\shell`, `\DefaultIcon`, ``} {
			_ = registry.DeleteKey(registry.CURRENT_USER, schemeKey+sub)
		}
		schemeKey, executable, start = k, e, st
	})
	executable = func() (string, error) { return s.self, nil }
	var started [][2]string
	s.started = &started
	start = func(exe, l string) error { started = append(started, [2]string{exe, l}); return nil }
	if v := handler(s.self); v != "" {
		c, _, err := registry.CreateKey(registry.CURRENT_USER, schemeKey+`\shell\open\command`, registry.SET_VALUE)
		if err != nil {
			t.Fatal(err)
		}
		_ = c.SetStringValue("", v)
		c.Close()
	}
	return s
}

func command() string {
	k, err := registry.OpenKey(registry.CURRENT_USER, schemeKey+`\shell\open\command`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, _ := k.GetStringValue("")
	return v
}

// The handler an older magpie wrote runs this magpie: CC Switch, where its
// MSI installs it, gets the link and the handler.
func TestForwardHandsTheHandlerBack(t *testing.T) {
	s := winForward(t, func(self string) string { return `"` + self + `" "%1"` }, true)
	if err := Forward(link); err != nil {
		t.Fatal(err)
	}
	if len(*s.started) != 1 || (*s.started)[0] != [2]string{s.cc, link} {
		t.Fatalf("started %q", *s.started)
	}
	if got, want := command(), `"`+s.cc+`" "%1"`; got != want {
		t.Fatalf("handler %q, want %q", got, want)
	}
}

// CC Switch's own handler, installed elsewhere: it gets the link, and the
// handler is left as it is.
func TestForwardToTheHandlersProgram(t *testing.T) {
	var other string
	s := winForward(t, func(self string) string {
		other = filepath.Join(filepath.Dir(self), "elsewhere", "cc-switch.exe")
		touch(t, other)
		return `"` + other + `" "%1"`
	}, true)
	if err := Forward(link); err != nil {
		t.Fatal(err)
	}
	if len(*s.started) != 1 || (*s.started)[0] != [2]string{other, link} {
		t.Fatalf("started %q", *s.started)
	}
	if got := command(); got != `"`+other+`" "%1"` {
		t.Fatalf("handler rewritten: %q", got)
	}
}

// No CC Switch: nothing started, magpie's handler stays.
func TestForwardWithoutCCSwitch(t *testing.T) {
	s := winForward(t, func(self string) string { return `"` + self + `" "%1"` }, false)
	if err := Forward(link); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("err %v", err)
	}
	if len(*s.started) != 0 || command() != `"`+s.self+`" "%1"` {
		t.Fatalf("started %q, handler %q", *s.started, command())
	}
}

func TestCommandExe(t *testing.T) {
	for v, want := range map[string]string{
		`"C:\Users\u\AppData\Local\Programs\CC Switch\cc-switch.exe" "%1"`: `C:\Users\u\AppData\Local\Programs\CC Switch\cc-switch.exe`,
		`C:\x\cc-switch.exe "%1"`: `C:\x\cc-switch.exe`,
		``:                        ``,
	} {
		if got := commandExe(v); got != want {
			t.Errorf("commandExe(%q) = %q", v, got)
		}
	}
}
