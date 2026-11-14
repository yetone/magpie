package cli

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// magpie provider keys prints every key, one a line and nothing else, and
// keys … add takes them from stdin or as words (@emilochen on X: one
// provider's keys to a magpie in the cloud). A flag is never a key, and
// nothing is written for one.
func TestKeysCmd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("DSH_HOME", "")
	for _, p := range []provider.Provider{
		{ID: "relay", Name: "Relay", Key: "k1", KeyName: "Team", Chat: "http://127.0.0.1:1/v1", Keys: []provider.KeyAccount{{Key: "k2", Off: true}, {Name: "Spare", Key: "k3"}}},
		{ID: "cloud", Name: "Cloud", Key: "k9", Chat: "http://127.0.0.1:1/v1"},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := keysCmd([]string{"relay"}, nil, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "k1\nk2\nk3\n" {
		t.Fatalf("printed %q", out.String())
	}
	if err := keysCmd([]string{"cloud", "add"}, strings.NewReader(out.String()), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := keysCmd([]string{"cloud", "add", "k4", "k1"}, nil, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := provider.AllKeys("cloud"); !slices.Equal(got, []string{"k9", "k1", "k2", "k3", "k4"}) {
		t.Fatalf("cloud holds %q", got)
	}
	for _, bad := range [][]string{{}, {"--help"}, {"cloud", "--help"}, {"cloud", "add", "--help"}, {"cloud", "add", "k5", "-x"}, {"nope"}} {
		if err := keysCmd(bad, strings.NewReader(""), &bytes.Buffer{}); err == nil {
			t.Fatalf("%v taken", bad)
		}
	}
	if got, _ := provider.AllKeys("cloud"); len(got) != 5 {
		t.Fatalf("a refused call wrote %q", got)
	}
}
