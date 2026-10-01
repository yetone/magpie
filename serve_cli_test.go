package main

import (
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// The serve banner says the gateway takes any key only from this machine
// while that is true: MAGPIE_ADDR on every interface (a Docker image) and
// not shared from Settings is open to anyone who reaches it.
func TestKeyNote(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, c := range []struct{ addr, want string }{
		{"", "only listens on localhost"},
		{"127.0.0.1:3426", "only listens on localhost"},
		{"localhost:3426", "only listens on localhost"},
		{"[::1]:3426", "only listens on localhost"},
		{"0.0.0.0:3425", "anyone who reaches it"},
		{":3425", "anyone who reaches it"},
		{"192.168.1.5:3425", "anyone who reaches it"},
	} {
		t.Setenv("MAGPIE_ADDR", c.addr)
		if n := keyNote(); !strings.Contains(n, c.want) {
			t.Errorf("MAGPIE_ADDR=%q: %q", c.addr, n)
		}
	}
	for _, key := range []string{"", "sk-magpie-k"} {
		if err := settings.Save(settings.Settings{LAN: true, LANKey: key}); err != nil {
			t.Fatal(err)
		}
		for _, addr := range []string{"", "0.0.0.0:3425"} {
			t.Setenv("MAGPIE_ADDR", addr)
			if n := keyNote(); !strings.Contains(n, "gateway key") || strings.Contains(n, "only listens on localhost") {
				t.Error("shared:", n)
			}
		}
	}
}

// The CLIs print the address other machines are told to reach the gateway
// at: a valid MAGPIE_PUBLIC_URL, otherwise the one reached from this
// machine, so `magpie models` and the serve banner never print a bad one.
func TestAdvertisedURL(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:3425")
	for _, c := range []struct{ public, want string }{
		{"", "http://127.0.0.1:3425"},
		{"https://magpie.example.com", "https://magpie.example.com"},
		{"nas.lan:3425/", "http://nas.lan:3425"},
		{"ftp://magpie.example.com", "http://127.0.0.1:3425"},
		{"https://magpie.example.com/magpie?x=1", "http://127.0.0.1:3425"},
	} {
		t.Setenv("MAGPIE_PUBLIC_URL", c.public)
		if got := advertisedURL(); got != c.want {
			t.Errorf("MAGPIE_PUBLIC_URL=%q: %q, want %q", c.public, got, c.want)
		}
	}
}

// Shared, the serve banner (docker logs) says where other machines reach
// the gateway: MAGPIE_PUBLIC_URL when a container sets it.
func TestShareLines(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("MAGPIE_ADDR", "0.0.0.0:3425")
	t.Setenv("MAGPIE_PUBLIC_URL", "http://192.168.1.20:3425/")
	if l := shareLines(); len(l) != 0 {
		t.Error("unshared:", l)
	}
	if err := settings.Save(settings.Settings{LAN: true, LANKey: "sk-magpie-k"}); err != nil {
		t.Fatal(err)
	}
	if l := strings.Join(shareLines(), "\n"); !strings.Contains(l, "http://192.168.1.20:3425") || strings.Contains(l, "container") {
		t.Error("shared:", l)
	}
}
