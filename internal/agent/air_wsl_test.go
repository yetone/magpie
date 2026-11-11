package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
)

// An IDE starts Air's agent for a WSL project inside the distro, and
// refuses a Windows command there ("ACP executable belongs to a different
// environment": @yanghonghang on X, "Magpie does not run"). A running
// distro with OpenCode gets an entry of its own in acp.json: OpenCode as
// \\wsl.localhost reaches it, on a Linux config path inside the distro,
// whose provider reaches the gateway at the distro's address.
func TestAirIDEAddsMagpieAgentForWSLProjects(t *testing.T) {
	home, _ := airHome(t, "/opt/bin/opencode")
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "home", "me"), 0o755)
	fakeWSL(t, "Ubuntu-24.04\r\nStopped\r\n", "Ubuntu-24.04\r\n", map[string]string{
		"Ubuntu-24.04": "home:/home/me\nbin:opencode /home/me/.opencode/bin/opencode\nroute:default via 172.20.0.1 dev eth0\n",
	}, map[string]string{"Ubuntu-24.04": root})
	acp := filepath.Join(home, ".jetbrains", "acp.json")
	writeFile(t, acp, `{"agent_servers": {"Goose": {"command": "goose", "args": ["acp"]}}}`)

	a := airIDE(home, filepath.Join(home, ".config"))
	if err := a.Apply("model", "magpie/fake/m1"); err != nil {
		t.Fatal(err)
	}
	ds := wslDistros()
	if len(ds) != 1 {
		t.Fatalf("distros %+v", ds)
	}
	d := ds[0]
	entry := `agent_servers.Magpie (WSL Ubuntu-24\.04)`
	cfg := filepath.Join(root, "home", "me", ".jetbrains", "magpie-opencode.json")
	for k, v := range map[string]string{
		"agent_servers.Magpie.command": "/opt/bin/opencode",
		"agent_servers.Goose.command":  "goose",
		entry + ".command":             d.local("/home/me/.opencode/bin/opencode"),
		entry + ".args.0":              "acp",
		entry + ".env.OPENCODE_CONFIG": "/home/me/.jetbrains/magpie-opencode.json",
	} {
		if got, _ := edit.GetJSON(acp, k); got != v {
			t.Fatalf("%s = %q, want %q\n%s", k, got, v, readFile(acp))
		}
	}
	if v, _ := edit.GetJSON(cfg, "provider.magpie.options.baseURL"); v != d.base()+"/v1" {
		t.Fatalf("baseURL = %q, want the distro's %s\n%s", v, d.base(), readFile(cfg))
	}
	if v, _ := edit.GetJSON(cfg, "model"); v != "magpie/fake/m1" {
		t.Fatalf("model = %q", v)
	}
	if strings.Contains(readFile(acp), "Stopped") {
		t.Fatalf("an entry for a stopped distro:\n%s", readFile(acp))
	}
	if s := a.Check(); s != "" {
		t.Fatalf("check: %s", s)
	}
	// the gateway's address in the distro changes: sync follows it
	os.WriteFile(cfg, []byte(strings.ReplaceAll(readFile(cfg), d.base(), "http://10.9.9.9:1")), 0o644)
	if s := a.Check(); s == "" {
		t.Fatal("check says nothing of a distro's config on another address")
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetJSON(cfg, "provider.magpie.options.baseURL"); v != d.base()+"/v1" {
		t.Fatalf("sync left baseURL %q", v)
	}
	// taking Magpie off takes its entries and the distro's config out, the
	// user's agent stays
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	if got := readFile(acp); strings.Contains(got, "Magpie") || !strings.Contains(got, "Goose") {
		t.Fatalf("after off:\n%s", got)
	}
	if _, err := os.Stat(cfg); err == nil {
		t.Fatal("the distro's config stayed")
	}
}

// The probe says where OpenCode is in the distro, and a command on
// Windows' drives isn't the distro's.
func TestWSLProbeKeepsPaths(t *testing.T) {
	d := parseProbe("U", "home:/home/me\nbin:opencode /home/me/.opencode/bin/opencode\nbin:pi /mnt/c/Users/me/AppData/Roaming/npm/pi\n")
	if d == nil || d.Paths["opencode"] != "/home/me/.opencode/bin/opencode" || d.Paths["pi"] != "" || d.Has["bin:pi"] {
		t.Fatalf("%+v", d)
	}
}
