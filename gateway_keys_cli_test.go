package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/settings"
)

func TestGatewayKeyCLI(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := settings.Save(settings.Settings{LAN: true}); err != nil {
		t.Fatal(err)
	}
	call := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if err := gatewayKeysTo(&out, append([]string{"gateway-key"}, args...)); err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out.String())
	}
	if got := call("list"); !strings.Contains(got, "GATEWAY KEY") {
		t.Fatal(got)
	}
	secret := call("add", "Remote laptop")
	keys, _ := access.List()
	if len(keys) != 1 {
		t.Fatal(keys)
	}
	id := keys[0].ID
	if who, ok := access.Authenticate(secret); !ok || who.KeyID != id || who.KeyName != "Remote laptop" {
		t.Fatal(who, ok)
	}
	if got := call(); strings.Contains(got, secret) || !strings.Contains(got, id) || !strings.Contains(got, "Remote laptop") {
		t.Fatal("list must mask credentials", got)
	}
	next := call("rotate", id)
	if next == secret {
		t.Fatal("rotation reused the secret")
	}
	if _, ok := access.Authenticate(secret); ok {
		t.Fatal("old credential works after rotation")
	}
	if who, ok := access.Authenticate(next); !ok || who.KeyID != id {
		t.Fatal("rotation changed identity", who, ok)
	}
	call("remove", id)
	if _, ok := access.Authenticate(next); ok {
		t.Fatal("removed credential still works")
	}
	for _, args := range [][]string{{"add"}, {"add", ""}, {"list", "extra"}, {"rotate", "missing"}, {"remove", "missing"}, {"unknown", "x"}} {
		if err := gatewayKeysTo(&bytes.Buffer{}, append([]string{"gateway-key"}, args...)); err == nil {
			t.Error("accepted", args)
		}
	}
}

func TestGatewayKeyListWithReadOnlyMigration(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := settings.Settings{LAN: true, LANKey: "sk-magpie-fixture-cli"}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(settings.Path(), 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(settings.Path(), 0o600) })
	if err := settings.Save(s); err == nil {
		t.Skip("settings.json remains writable")
	}
	var out bytes.Buffer
	if err := gatewayKeysTo(&out, []string{"gateway-key", "list"}); err != nil || !strings.Contains(out.String(), "Magpie") {
		t.Fatal("read-only migration blocked CLI list", out.String(), err)
	}
}
