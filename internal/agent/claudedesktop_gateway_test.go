package agent

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

// Desktop's SSH remote sessions are handed the profile's address and key
// (#4546): loopback is refused there, and the key must be one the gateway
// takes from another machine.
func TestDesktopPointedAtLANHTTPS(t *testing.T) {
	home, p := desktopSandbox(t)
	t.Setenv("MAGPIE_PUBLIC_URL", "https://magpie.example.test")
	if err := access.ConfigureLAN(true, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { access.ConfigureLAN(false, false) })
	lan := "https://magpie.example.test"
	if len(desktopLANURLs()) == 0 {
		t.Fatalf("no https address offered: %v (LANURLs %v)", desktopLANURLs(), gateway.LANURLs())
	}
	a := claudeDesktop(home)
	f := a.Field("gateway")
	if f == nil {
		t.Fatal("no gateway field")
	}
	if len(f.Options(nil)) != 0 {
		t.Fatal("offered while Desktop is off magpie")
	}
	if err := a.Field("provider").Set("magpie"); err != nil {
		t.Fatal(err)
	}
	prof := p.prof
	if v, _ := edit.GetJSON(prof, "inferenceGatewayBaseUrl"); v != gateway.URL() {
		t.Fatalf("default address %q, want loopback", v)
	}
	if strings.Contains(readFile(prof), "sshHostAllowlist") {
		t.Fatal("hosts allowed while Desktop reaches loopback")
	}
	if len(f.Options(nil)) < 2 {
		t.Fatalf("options %v", f.Options(nil))
	}
	if err := f.Set("http://evil.test:1"); err == nil {
		t.Fatal("an address that isn't ours was taken")
	}
	if err := f.Set(lan); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetJSON(prof, "inferenceGatewayBaseUrl"); v != lan {
		t.Fatalf("address %q, want %q:\n%s", v, lan, readFile(prof))
	}
	open, _ := edit.GetJSON(prof, "inferenceGatewayApiKey")
	if o, _ := desktopOpen("inferenceGatewayApiKey", open); o != access.LANSecret() {
		t.Fatalf("key %q is not the sharing key", o)
	}
	if !strings.Contains(readFile(prof), `"sshHostAllowlist"`) || !strings.Contains(readFile(prof), `"*"`) {
		t.Fatalf("no SSH host allowed:\n%s", readFile(prof))
	}
	if d := a.Drift(); d != nil {
		t.Fatalf("drift on its own address: %+v", d)
	}
	// back to this computer
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetJSON(prof, "inferenceGatewayBaseUrl"); v != gateway.URL() {
		t.Fatalf("address %q after taking it back", v)
	}
	// sharing off: the pick can't hold, and Rekey puts loopback back
	if err := f.Set(lan); err != nil {
		t.Fatal(err)
	}
	on := OnGateway()
	if err := access.ConfigureLAN(false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := Rekey(on); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetJSON(prof, "inferenceGatewayBaseUrl"); v != gateway.URL() {
		t.Fatalf("address %q with sharing off", v)
	}
}
