package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/settings"
)

// gateway-key admin (ljayic on X): the one way to give a key the
// management API besides the app.
func TestGatewayKeyCLIAdmin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := settings.Save(settings.Settings{LAN: true}); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := gatewayKeysTo(&out, append([]string{"gateway-key"}, args...))
		return strings.TrimSpace(out.String()), err
	}
	secret, err := run("add", "Automation")
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := access.List()
	id := keys[len(keys)-1].ID
	if got, err := run("admin", id); err != nil || !strings.Contains(strings.ToLower(got), "off") && !strings.Contains(strings.ToLower(got), "no") {
		t.Fatalf("admin (show): %q %v", got, err)
	}
	if _, err := run("admin", id, "local"); err != nil {
		t.Fatal(err)
	}
	if who, _ := access.Authenticate(secret); who.Admin != access.AdminLocal {
		t.Fatalf("after local: %q", who.Admin)
	}
	if _, err := run("admin", id, "any"); err != nil {
		t.Fatal(err)
	}
	if who, _ := access.Authenticate(secret); who.Admin != access.AdminAny {
		t.Fatalf("after any: %q", who.Admin)
	}
	if _, err := run("admin", id, "--help"); err == nil {
		t.Error("a flag was taken as a level")
	}
	if _, err := run("admin", id, "banana"); err == nil {
		t.Error("an unknown level was taken")
	}
	if who, _ := access.Authenticate(secret); who.Admin != access.AdminAny {
		t.Errorf("a refused level changed the key: %q", who.Admin)
	}
	if _, err := run("admin", id, "off"); err != nil {
		t.Fatal(err)
	}
	if who, _ := access.Authenticate(secret); who.Admin != "" {
		t.Errorf("after off: %q", who.Admin)
	}
}
