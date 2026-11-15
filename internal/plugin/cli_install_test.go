package plugin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A plugin that runs its vendor's CLI says how to install it (auth.cli):
// it is listed with the CLI's name and manual, magpie asks whether it is
// there and has the plugin install it, and gives up an install it no
// longer waits for (niuniu_8964 on X: agy in magpie's container).
func TestPluginInstallsItsCLI(t *testing.T) {
	sandbox(t)
	home := os.Getenv("HOME")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, name := range []string{"cliinstall", "importer"} {
		abs, _ := filepath.Abs("testdata/" + name)
		if _, err := Add(ctx, abs); err != nil {
			t.Fatal(err)
		}
	}
	ps, err := Providers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var cli, other *Provider
	for i := range ps {
		switch ps[i].ID {
		case "cliinstall":
			cli = &ps[i]
		case "importer":
			other = &ps[i]
		}
	}
	if cli == nil || other == nil {
		t.Fatalf("providers = %+v", ps)
	}
	if cli.CLI == nil || cli.CLI.Name != "Fake CLI" || cli.CLI.Manual != "curl -fsSL https://fake.invalid/install.sh | bash" {
		t.Fatalf("cliinstall's CLI = %+v", cli.CLI)
	}
	if other.CLI != nil {
		t.Errorf("a plugin with no auth.cli names one: %+v", other.CLI)
	}
	if _, err := CLIInstalled(ctx, "importer"); err == nil {
		t.Error("a plugin with no auth.cli answered whether its CLI is installed")
	}

	if in, err := CLIInstalled(ctx, "cliinstall"); err != nil || in {
		t.Fatalf("before the install: %v, %v", in, err)
	}
	os.WriteFile(filepath.Join(home, "cli-mode"), []byte("fail"), 0o600)
	if err := InstallCLI(ctx, "cliinstall"); err == nil || !strings.Contains(err.Error(), "the download was refused") {
		t.Fatalf("a failing install = %v", err)
	}

	// given up, the plugin's install hears of it and the call returns
	os.WriteFile(filepath.Join(home, "cli-mode"), []byte("hang"), 0o600)
	hctx, hcancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- InstallCLI(hctx, "cliinstall") }()
	time.Sleep(200 * time.Millisecond)
	hcancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled install returned no error")
		}
	case <-ctx.Done():
		t.Fatal("a cancelled install never returned")
	}
	for range 200 {
		if _, err := os.Stat(filepath.Join(home, "cli-aborted")); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(home, "cli-aborted")); err != nil {
		t.Error("the plugin's install never heard it was given up")
	}

	os.Remove(filepath.Join(home, "cli-mode"))
	if err := InstallCLI(ctx, "cliinstall"); err != nil {
		t.Fatal(err)
	}
	if in, err := CLIInstalled(ctx, "cliinstall"); err != nil || !in {
		t.Fatalf("after the install: %v, %v", in, err)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatal("ran out of time")
	}
}
