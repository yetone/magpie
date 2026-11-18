package ccswitchlink

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

const link = "ccswitch://v1/import?resource=provider&app=claude&name=Relay&apiKey=sk-x"

// fakeTools puts update-desktop-database and an xdg-mime that keeps the
// default it is given and answers `query default` with it.
func fakeTools(t *testing.T, root, def string) string {
	t.Helper()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "default")
	if def != "" {
		write(t, state, def+"\n")
	}
	t.Setenv("CCSWITCH_TEST_DEFAULT", state)
	t.Setenv("PATH", bin)
	testenv.Program(t, filepath.Join(bin, "update-desktop-database"), "#!/bin/sh\nexit 0\n")
	testenv.Program(t, filepath.Join(bin, "xdg-mime"), "#!/bin/sh\n"+
		`[ "$1" = query ] && { [ -f "$CCSWITCH_TEST_DEFAULT" ] && read -r v < "$CCSWITCH_TEST_DEFAULT" && echo "$v"; exit 0; }`+"\n"+
		`[ "$1" = default ] && [ "$3" = x-scheme-handler/ccswitch ] && echo "$2" > "$CCSWITCH_TEST_DEFAULT"`+"\n"+
		"exit 0\n")
	return state
}

func write(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// pkgEntry is what CC Switch v4.0.35's .deb installs, byte for byte;
// handlerEntry is the kind the app writes on its first start.
const (
	pkgEntry     = "[Desktop Entry]\nCategories=\nComment=All-in-One Assistant for Claude Code, Codex & Gemini CLI\nExec=cc-switch\nStartupWMClass=cc-switch\nIcon=cc-switch\nName=CC Switch\nTerminal=false\nType=Application\nMimeType=x-scheme-handler/ccswitch"
	handlerEntry = "[Desktop Entry]\nType=Application\nName=CC Switch\nExec=\"/opt/CC Switch/cc-switch\" %u\nMimeType=x-scheme-handler/ccswitch\nNoDisplay=true\n"
)

type setup struct {
	data, def string
	started   *[][]string
}

func linuxSetup(t *testing.T, def string, handler, pkg, magpie bool) setup {
	t.Helper()
	root := t.TempDir()
	s := setup{data: filepath.Join(root, "data")}
	sys := filepath.Join(root, "sys")
	t.Setenv("XDG_DATA_HOME", s.data)
	t.Setenv("XDG_DATA_DIRS", sys)
	s.def = fakeTools(t, root, def)
	self := filepath.Join(root, "magpie")
	write(t, self, "")
	e, st := executable, start
	t.Cleanup(func() { executable, start = e, st })
	executable = func() (string, error) { return self, nil }
	var started [][]string
	s.started = &started
	start = func(argv []string) error { started = append(started, argv); return nil }
	if handler {
		write(t, filepath.Join(s.data, "applications", "cc-switch-handler.desktop"), handlerEntry)
	}
	if pkg {
		write(t, filepath.Join(sys, "applications", "CC Switch.desktop"), pkgEntry)
	}
	if magpie {
		// as magpie v0.1.3981 to v0.1.4454 wrote it
		write(t, filepath.Join(s.data, "applications", magpieEntry), "[Desktop Entry]\nType=Application\nName=magpie (ccswitch:// links)\nExec="+self+" %u\nNoDisplay=true\nMimeType=x-scheme-handler/ccswitch;\nTerminal=false\n")
	}
	return s
}

func (s setup) defaultIs() string {
	b, _ := os.ReadFile(s.def)
	return strings.TrimSpace(string(b))
}

func (s setup) magpieEntry() bool {
	_, err := os.Stat(filepath.Join(s.data, "applications", magpieEntry))
	return err == nil
}

// The default an older magpie took goes to CC Switch's handler, which gets
// the link, and magpie's entry goes.
func TestForwardHandsTheDefaultBack(t *testing.T) {
	s := linuxSetup(t, magpieEntry, true, true, true)
	if err := Forward(link); err != nil {
		t.Fatal(err)
	}
	if len(*s.started) != 1 || !slices.Equal((*s.started)[0], []string{"/opt/CC Switch/cc-switch", link}) {
		t.Fatalf("started %q", *s.started)
	}
	if got := s.defaultIs(); got != "cc-switch-handler.desktop" {
		t.Fatalf("default %q", got)
	}
	if s.magpieEntry() {
		t.Fatal("magpie's ccswitch entry stayed")
	}
}

// Only the package's entry, whose Exec takes no URL: the link is its last
// argument.
func TestForwardToThePackagesEntry(t *testing.T) {
	s := linuxSetup(t, magpieEntry, false, true, true)
	if err := Forward(link); err != nil {
		t.Fatal(err)
	}
	if len(*s.started) != 1 || !slices.Equal((*s.started)[0], []string{"cc-switch", link}) {
		t.Fatalf("started %q", *s.started)
	}
	if got := s.defaultIs(); got != "CC Switch.desktop" {
		t.Fatalf("default %q", got)
	}
}

// A default that isn't magpie's is the user's or CC Switch's: left alone,
// and so is magpie's entry.
func TestForwardLeavesAnotherDefault(t *testing.T) {
	s := linuxSetup(t, "other.desktop", true, false, true)
	if err := Forward(link); err != nil {
		t.Fatal(err)
	}
	if len(*s.started) != 1 {
		t.Fatalf("started %q", *s.started)
	}
	if got := s.defaultIs(); got != "other.desktop" {
		t.Fatalf("default %q", got)
	}
	if !s.magpieEntry() {
		t.Fatal("magpie's entry went")
	}
}

// No CC Switch: nothing is started and the claim stays.
func TestForwardWithoutCCSwitch(t *testing.T) {
	s := linuxSetup(t, magpieEntry, false, false, true)
	if err := Forward(link); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("err %v", err)
	}
	if len(*s.started) != 0 || s.defaultIs() != magpieEntry || !s.magpieEntry() {
		t.Fatalf("started %q, default %q, entry %v", *s.started, s.defaultIs(), s.magpieEntry())
	}
}

func TestExpand(t *testing.T) {
	for exec, want := range map[string][]string{
		`cc-switch`:                          {"cc-switch", link},
		`/usr/bin/cc-switch %u`:              {"/usr/bin/cc-switch", link},
		`"/opt/CC Switch/cc-switch" %U`:      {"/opt/CC Switch/cc-switch", link},
		`env A=1 cc-switch --x %i %c %k %u`:  {"env", "A=1", "cc-switch", "--x", link},
		`sh -c "echo \"a\$b\\\\\" 100%%" %u`: {"sh", "-c", `echo "a$b\\" 100%`, link},
		`"/home/u/Apps/CC-Switch.AppImage"`:  {"/home/u/Apps/CC-Switch.AppImage", link},
	} {
		if got := expand(exec, link); !slices.Equal(got, want) {
			t.Errorf("expand(%q) = %q, want %q", exec, got, want)
		}
	}
}
