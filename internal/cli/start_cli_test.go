package cli

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
)

// `magpie agy run`, what the function agy in the user's shells runs
// (bestony on Discord): the agy on PATH, given magpie's gateway, key and
// --model while settings.json's model is magpie's, every argument agy's;
// as it is off magpie.
func TestAgyRun(t *testing.T) {
	home := helpHome(t)
	settings := filepath.Join(home, ".gemini", "antigravity-cli", "magpie-settings.json")
	os.MkdirAll(filepath.Dir(settings), 0o755)
	t.Setenv("GEMINI_API_KEY", "users-own")
	t.Setenv("GOOGLE_GEMINI_BASE_URL", "")
	var bin, name string
	var args, env []string
	oldExec, oldLook := startExec, startLook
	t.Cleanup(func() { startExec, startLook = oldExec, oldLook })
	startExec = func(b, n string, a, e []string) error {
		bin, name, args, env = b, n, a, e
		return nil
	}
	real := filepath.Join(home, "bin", "agy")
	startLook = func(b string) (string, error) {
		if b != "agy" {
			return "", errors.New("looked for " + b)
		}
		return real, nil
	}
	envOf := func(k string) []string {
		var out []string
		for _, kv := range env {
			if strings.HasPrefix(kv, k+"=") {
				out = append(out, strings.TrimPrefix(kv, k+"="))
			}
		}
		return out
	}

	os.WriteFile(settings, []byte(`{"model": "magpie/deepseek/pro", "modelProvider": "gemini"}`), 0o644)
	for _, c := range []struct{ in, want []string }{
		{nil, []string{"--settings", settings, "--model", "magpie/deepseek/pro"}},
		{[]string{"--help"}, []string{"--settings", settings, "--model", "magpie/deepseek/pro", "--help"}},
		{[]string{"-p", "hi", "--model", "mine"}, []string{"--settings", settings, "-p", "hi", "--model", "mine"}},
		{[]string{"--model=mine"}, []string{"--settings", settings, "--model=mine"}},
		{[]string{"-model", "mine"}, []string{"--settings", settings, "-model", "mine"}},
	} {
		if _, err := runOut(t, append([]string{"agy", "run"}, c.in...)...); err != nil {
			t.Fatal(err)
		}
		if bin != real || name != "agy" || !slices.Equal(args, c.want) {
			t.Fatalf("agy run %v: %s %v, want %v", c.in, bin, args, c.want)
		}
		if k, u := envOf("GEMINI_API_KEY"), envOf("GOOGLE_GEMINI_BASE_URL"); !slices.Equal(k, []string{gateway.TokenFor("agy")}) || !slices.Equal(u, []string{gateway.URL()}) {
			t.Fatalf("env: key %v, url %v", k, u)
		}
	}
	out, err := runOut(t, "agy", "env")
	if want := "GEMINI_API_KEY=" + gateway.TokenFor("agy") + "\nGOOGLE_GEMINI_BASE_URL=" + gateway.URL() + "\nsettings=" + settings + "\nmodel=magpie/deepseek/pro\n"; err != nil || out != want {
		t.Fatalf("agy env: %q %v", out, err)
	}

	// remote control needs agy's Google sign-in: plain, with the user's own
	// environment, however magpie has agy wired (baysonfox on X)
	for _, in := range [][]string{{"remote-control", "start"}, {"--remote-control"}, {"-p", "hi", "--remote-control"}} {
		if _, err := runOut(t, append([]string{"agy", "run"}, in...)...); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(args, in) || !slices.Equal(envOf("GEMINI_API_KEY"), []string{"users-own"}) {
			t.Fatalf("agy run %v: %v, key %v", in, args, envOf("GEMINI_API_KEY"))
		}
	}

	// off magpie: as it is
	os.WriteFile(settings, []byte(`{"model": "mine"}`), 0o644)
	if _, err := runOut(t, "agy", "run", "-p", "hi"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(args, []string{"-p", "hi"}) || !slices.Equal(envOf("GEMINI_API_KEY"), []string{"users-own"}) {
		t.Fatalf("off magpie: %v %v", args, envOf("GEMINI_API_KEY"))
	}
	if out, err := runOut(t, "agy", "env"); err != nil || out != "" {
		t.Fatalf("agy env off magpie: %q %v", out, err)
	}

	// what --help and a stray value say
	if out, _ := runOut(t, "agy", "--help"); !strings.Contains(out, "magpie agy run [args…]") {
		t.Fatalf("agy --help:\n%s", out)
	}
	if _, err := runOut(t, "agy", "env", "x"); err == nil {
		t.Fatal("agy env x: no error")
	}
	startLook = func(string) (string, error) { return "", errors.New("not found") }
	if _, err := runOut(t, "agy", "run"); err == nil || !strings.Contains(err.Error(), "isn't installed") {
		t.Fatalf("no agy: %v", err)
	}
}
