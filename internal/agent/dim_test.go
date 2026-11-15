package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/update"
)

// Dim alpha is started on magpie through the variables Dim documents for
// an OpenAI-compatible endpoint; the model is in magpie's own file, and
// nothing of Dim's (its home, settings, database or account) is written.
func TestDim(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("DIMCODE_HOME", "")
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("ZDOTDIR", "")
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	// Dim's own home, with a settings file of the user's
	dimHome := filepath.Join(home, ".dimcode-alpha")
	os.MkdirAll(dimHome, 0o755)
	own := filepath.Join(dimHome, "settings.json")
	os.WriteFile(own, []byte(`{"theme":"dark"}`), 0o644)
	zshrc := filepath.Join(home, ".zshrc")
	os.WriteFile(zshrc, []byte("export A=1\n"), 0o644)

	a, err := Find("dim")
	if err != nil || a.Bin != "dima" || a.Dir != dimHome {
		t.Fatalf("find: %+v %v", a, err)
	}
	if b := Starter("dima"); b == nil || b.ID != "dim" {
		t.Fatalf("starter dima: %v", b)
	}
	if a.Launch() != "" || a.Start().Model != "" || a.Notice() != "" {
		t.Fatalf("on magpie before a pick: %q %+v %q", a.Launch(), a.Start(), a.Notice())
	}
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if got := a.Field("model").Get(); got != "magpie/deepseek/pro" {
		t.Fatalf("model %q", got)
	}
	s := a.Start()
	if s.Bin != "dima" || s.Model != "magpie/deepseek/pro" || s.Flag != "" || len(s.Args) != 0 {
		t.Fatalf("start %+v", s)
	}
	env := map[string]string{}
	for _, kv := range s.Env {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	for k, want := range map[string]string{
		"DIM_BASE_URL":   gateway.URL() + "/v1",
		"DIM_API_FORMAT": "openai-compatible",
		"DIM_API_KEY":    gateway.TokenFor("dim"),
		"DIM_MODEL":      "deepseek/pro",
	} {
		if env[k] != want {
			t.Fatalf("%s = %q, want %q (%v)", k, env[k], want, s.Env)
		}
	}
	// Dim refuses a model it doesn't know without both limits
	for _, k := range []string{"DIM_MAX_CONTEXT_TOKENS", "DIM_MAX_OUTPUT_TOKENS"} {
		if env[k] == "" || env[k] == "0" {
			t.Fatalf("%s missing: %v", k, s.Env)
		}
	}
	if !strings.Contains(a.Launch(), "DIM_MODEL='deepseek/pro'") || !strings.HasSuffix(a.Launch(), "dima") {
		t.Fatalf("launch %q", a.Launch())
	}
	// the function dima is in the user's shells (zsh's on macOS, where it
	// is always there), and the notice says to type it
	in := update.ShellFunctionIn("dima")
	if runtime.GOOS == "darwin" && !slices.Contains(in, zshrc) {
		t.Fatalf("function in %v", in)
	}
	if slices.Contains(in, zshrc) {
		b, _ := os.ReadFile(zshrc)
		if !strings.Contains(string(b), "dima() {") || !strings.Contains(string(b), " dim run ") || !strings.HasPrefix(string(b), "export A=1\n") {
			t.Fatalf(".zshrc:\n%s", b)
		}
	}
	if len(in) > 0 && (a.Typed() != "dima" || !strings.Contains(a.Notice(), "Type dima")) {
		t.Fatalf("typed %q notice %q", a.Typed(), a.Notice())
	}
	if !strings.Contains(a.Notice(), "DIM_API_FORMAT") {
		t.Fatalf("notice %q", a.Notice())
	}
	// the options are magpie's models
	var vals []string
	for _, o := range a.Field("model").Options(a.Values()) {
		vals = append(vals, o.Value)
	}
	if !slices.Contains(vals, "magpie/deepseek/pro") || !slices.Contains(vals, "magpie/deepseek/flash") {
		t.Fatalf("options %v", vals)
	}

	// back to Dim's own: magpie's file and the function are gone, the
	// profile is as it was
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dimFile()); !os.IsNotExist(err) {
		t.Fatalf("dim.json kept: %v", err)
	}
	if update.ShellFunctionIn("dima") != nil {
		t.Fatalf("function kept in %v", update.ShellFunctionIn("dima"))
	}
	if b, _ := os.ReadFile(zshrc); string(b) != "export A=1\n" {
		t.Fatalf(".zshrc after:\n%s", b)
	}
	if a.Launch() != "" || a.Start().Model != "" || a.Field("model").Get() != "" {
		t.Fatalf("still on magpie: %q %+v", a.Launch(), a.Start())
	}
	// nothing of Dim's was written or made
	if b, _ := os.ReadFile(own); string(b) != `{"theme":"dark"}` {
		t.Fatalf("Dim's settings written: %s", b)
	}
	ents, _ := os.ReadDir(dimHome)
	if len(ents) != 1 {
		t.Fatalf("Dim's home holds %v", ents)
	}
}

// DIMCODE_HOME moves Dim's folder; it is used as given, as Dim does.
func TestDimHome(t *testing.T) {
	home := t.TempDir()
	at := here(home)
	t.Setenv("DIMCODE_HOME", "")
	if d := dimDir(at); d != filepath.Join(home, ".dimcode-alpha") {
		t.Fatalf("dir %q", d)
	}
	other := filepath.Join(home, "elsewhere")
	t.Setenv("DIMCODE_HOME", other)
	if d := dimDir(at); d != other {
		t.Fatalf("dir %q", d)
	}
}

// A model the catalog knows no limits of still gets both, the output no
// larger than the context.
func TestDimLimits(t *testing.T) {
	ctx, out := dimLimits("magpie/nobody/none")
	if ctx <= 0 || out <= 0 || out > ctx {
		t.Fatalf("limits %d %d", ctx, out)
	}
}
