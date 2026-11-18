package ccswitchlink

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func stubDarwin(t *testing.T, exe string, openErr error) *[]string {
	t.Helper()
	var calls []string
	r, e := run, executable
	t.Cleanup(func() { run, executable = r, e })
	run = func(name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if name == "open" {
			return openErr
		}
		return nil
	}
	executable = func() (string, error) { return exe, nil }
	return &calls
}

const link = "ccswitch://v1/import?resource=provider&app=claude&name=Relay&endpoint=https%3A%2F%2Fapi.example.com&apiKey=sk-x"

// The link goes to CC Switch by its bundle id, and magpie's bundle is
// registered again, which drops the claim an older Info.plist made.
func TestForwardOpensCCSwitchAndRegistersMagpieAgain(t *testing.T) {
	calls := stubDarwin(t, "/Applications/magpie.app/Contents/MacOS/magpie", nil)
	if err := Forward(link); err != nil {
		t.Fatal(err)
	}
	want := []string{"open -b com.ccswitch.desktop " + link, lsregister + " -f /Applications/magpie.app"}
	if !slices.Equal(*calls, want) {
		t.Fatalf("calls %q, want %q", *calls, want)
	}
}

// No CC Switch: Launch Services is told nothing more.
func TestForwardWithoutCCSwitch(t *testing.T) {
	calls := stubDarwin(t, "/Applications/magpie.app/Contents/MacOS/magpie", errors.New("Unable to find application named 'com.ccswitch.desktop'"))
	if err := Forward(link); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("err %v, want ErrNotInstalled", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("calls %q: Launch Services was told more", *calls)
	}
}

// A magpie outside a bundle (go run, the CLI on PATH) registers nothing.
func TestForwardOutsideABundle(t *testing.T) {
	calls := stubDarwin(t, "/usr/local/bin/magpie", nil)
	if err := Forward(link); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("calls %q", *calls)
	}
}
