package main

import (
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// magpie update takes --proxy and --mirror, as --flag value or
// --flag=value, before or after check; magpie update mirror keeps one for
// every update, the app's too, and off clears it.
func TestUpdateFlags(t *testing.T) {
	rest, proxy, mirror, err := updateFlags([]string{"update", "--proxy", "socks5://127.0.0.1:1080", "check", "--mirror=https://mirror.example/"})
	if err != nil || !slices.Equal(rest, []string{"update", "check"}) || proxy != "socks5://127.0.0.1:1080" || mirror != "https://mirror.example/" {
		t.Fatalf("%v %q %q %v", rest, proxy, mirror, err)
	}
	for _, bad := range [][]string{
		{"update", "--proxy"},
		{"update", "--proxy", "ftp://x:1"},
		{"update", "--mirror", "mirror.example"},
	} {
		if _, _, _, err := updateFlags(bad); err == nil {
			t.Errorf("%v taken", bad)
		}
	}
	if err := updateCmd([]string{"update", "chek"}); err == nil {
		t.Error("magpie update chek ran an update")
	}

	groupsHome(t)
	if err := updateCmd([]string{"update", "mirror", "https://mirror.example/"}); err != nil {
		t.Fatal(err)
	}
	if got := settings.Load().UpdateMirror; got != "https://mirror.example/" {
		t.Fatalf("saved %q", got)
	}
	if err := updateCmd([]string{"update", "mirror", "nope"}); err == nil || settings.Load().UpdateMirror != "https://mirror.example/" {
		t.Fatalf("a bad mirror: %v, kept %q", err, settings.Load().UpdateMirror)
	}
	if err := updateCmd([]string{"update", "mirror", "off"}); err != nil || settings.Load().UpdateMirror != "" {
		t.Fatalf("off: %v, kept %q", err, settings.Load().UpdateMirror)
	}
}
