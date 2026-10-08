package agent

import (
	"context"
	"debug/buildinfo"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

func TestReasonixRegistered(t *testing.T) {
	t.Setenv("REASONIX_HOME", t.TempDir())
	a, err := Find("reasonix")
	if err != nil {
		t.Fatalf("Reasonix Studio is missing from the agent registry: %v", err)
	}
	if a.ID != "reasonix" || a.Name != "Reasonix Studio" || a.Field("model") == nil {
		t.Fatalf("incomplete Reasonix Studio agent: %+v", a)
	}
}

func TestReasonixCredentialFileTakesPrecedence(t *testing.T) {
	a, _ := reasonixFixture(t, reasonixNativeConfig)
	t.Setenv(reasonixKey, "unused-shell-credential")
	if err := a.Field("model").Set("magpie/a/pro"); err != nil {
		t.Fatal(err)
	}
	if got := a.Check(); got != "" {
		t.Fatalf("Studio 2.x resolves provider credentials from its global .env: %s", got)
	}
}

func TestReasonixDuplicatePrivateCredentialIsAConflict(t *testing.T) {
	a, env := reasonixFixture(t, reasonixNativeConfig)
	if err := a.Field("model").Set("magpie/a/pro"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(a.Path)
	b, _ := os.ReadFile(env)
	b = append(b, []byte("\nexport "+reasonixKey+" = later-user-credential\n")...)
	if err := os.WriteFile(env, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if a.Check() == "" {
		t.Fatal("multiple private credentials were reported as owned")
	}
	if err := a.Field("model").Set(""); err == nil {
		t.Fatal("restore removed a later credential sharing the private key")
	}
	after, _ := os.ReadFile(a.Path)
	afterEnv, _ := os.ReadFile(env)
	if string(before) != string(after) || string(b) != string(afterEnv) {
		t.Fatal("ambiguous restore changed configuration")
	}
}

func TestReasonixCapabilities(t *testing.T) {
	yes, no := true, false
	p := reasonixCatalog([]catalog.Model{
		{ID: "a/vision", Context: 128000, Output: 384000, ImageInput: &yes, Efforts: []string{"off", "none", "minimal", "high", "xhigh", "unknown"}},
		{ID: "a/text", Context: 32768, Output: 8192, ImageInput: &no},
	}, "a/vision", "xhigh")
	vision, text := p.Overrides["a/vision"], p.Overrides["a/text"]
	if vision.Context != 128000 || vision.Output != 128000 || vision.Vision == nil || !*vision.Vision {
		t.Fatalf("vision model limits: %+v", vision)
	}
	if !reflect.DeepEqual(vision.Efforts, []string{"none", "minimal", "high", "xhigh"}) || p.Effort != "xhigh" {
		t.Fatalf("explicit reasoning capabilities: %+v", vision)
	}
	if text.Protocol != "none" || len(text.Efforts) != 0 || text.Vision == nil || *text.Vision {
		t.Fatalf("text model gained unadvertised capabilities: %+v", text)
	}
}

func TestReasonixRestoresAbsentAndEmptyDefault(t *testing.T) {
	for _, root := range []string{"", "default_model = \"\"\n"} {
		t.Run(root, func(t *testing.T) {
			a, _ := reasonixFixture(t, root+"[desktop]\nprovider_access = []\n")
			if err := a.Field("model").Set("magpie/a/pro"); err != nil {
				t.Fatal(err)
			}
			if err := a.Field("model").Set(""); err != nil {
				t.Fatal(err)
			}
			cfg, err := readReasonix(a.Path)
			if err != nil || (cfg.DefaultModel == nil) != (root == "") || cfg.DefaultModel != nil && *cfg.DefaultModel != "" {
				t.Fatalf("default presence changed: %+v %v", cfg, err)
			}
			if cfg.Desktop.Access == nil || len(cfg.Desktop.Access) != 0 {
				t.Fatalf("intentional empty access list changed: %v", cfg.Desktop.Access)
			}
		})
	}
}

func TestReasonixRejectsCorruptRestoreRecord(t *testing.T) {
	for _, record := range []string{"", "null", "{}", "{\"version\":999}", "{\"version\":1}", "{\"version\":1,\"default_model\":null}"} {
		t.Run(record, func(t *testing.T) {
			a, env := reasonixFixture(t, reasonixNativeConfig)
			if err := a.Field("model").Set("magpie/a/pro"); err != nil {
				t.Fatal(err)
			}
			files, _ := filepath.Glob(filepath.Join(filepath.Dir(provider.Path()), "reasonix-*.json"))
			if len(files) != 1 {
				t.Fatalf("restore records: %v", files)
			}
			os.WriteFile(files[0], []byte(record), 0o600)
			before, _ := os.ReadFile(a.Path)
			beforeEnv, _ := os.ReadFile(env)
			if err := a.Field("model").Set(""); err == nil {
				t.Fatal("corrupt journal discarded the original selection")
			}
			after, _ := os.ReadFile(a.Path)
			afterEnv, _ := os.ReadFile(env)
			if string(before) != string(after) || string(beforeEnv) != string(afterEnv) {
				t.Fatal("failed restore changed configuration")
			}
		})
	}
}

func TestReasonixRestoreKeepsLaterUserEdits(t *testing.T) {
	a, env := reasonixFixture(t, reasonixNativeConfig)
	if err := a.Field("model").Set("magpie/a/pro"); err != nil {
		t.Fatal(err)
	}
	edit.SetTOMLTop(a.Path, edit.KV{Path: "default_model", Value: "native/other"})
	edit.SetTOMLKey(a.Path, "ui", "theme", "light")
	edit.SetTOMLKey(a.Path, "desktop", "provider_access", edit.Raw("[\"native\", \"magpie\", \"later\"]"))
	edit.SetEnvFile(env, edit.KV{Path: "LATER_API_KEY", Value: "later-secret"})
	if err := a.Field("model").Set(""); err != nil {
		t.Fatal(err)
	}
	cfg := reasonixDecode(t, a.Path)
	if cfg["default_model"] != "native/other" || cfg["ui"].(map[string]any)["theme"] != "light" {
		t.Fatal("restore overwrote later preferences")
	}
	if got := cfg["desktop"].(map[string]any)["provider_access"]; !reflect.DeepEqual(got, []any{"native", "later"}) {
		t.Fatal(got)
	}
	if value, _ := edit.GetEnvFile(env, "LATER_API_KEY"); value != "later-secret" {
		t.Fatal("removed another credential")
	}
}

func TestReasonixCLIRequiresSupportedNativeVersion(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "cmd", "reasonix"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module reasonix\n\ngo 1.26\n"), 0o600)
	bin := filepath.Join(dir, "reasonix.exe")
	for _, tc := range []struct {
		version string
		want    bool
	}{{"1.18.0", false}, {"1.39.4", true}, {"2.24.0", true}} {
		source := "package main\nimport \"fmt\"\nfunc main() { fmt.Println(\"reasonix v" + tc.version + "\") }\n"
		os.WriteFile(filepath.Join(dir, "cmd", "reasonix", "main.go"), []byte(source), 0o600)
		cmd := proc.Command("go", "build", "-o", bin, "./cmd/reasonix")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOWORK=off")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build fixture: %v %s", err, b)
		}
		warmReasonixCLIFixture(t, bin)
		if got := reasonixCLI(bin); got != tc.want {
			info, infoErr := buildinfo.ReadFile(bin)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			out, probeErr := proc.CommandContext(ctx, bin, "--version").CombinedOutput()
			t.Fatalf("CLI %s detected = %v; metadata=%v (%v), probe=%q (%v)", tc.version, got, info, infoErr, out, probeErr)
		}
	}
	script := filepath.Join(dir, "reasonix.js")
	os.WriteFile(script, []byte("throw new Error('do not execute npm wrapper')"), 0o600)
	if reasonixCLI(script) {
		t.Fatal("npm wrapper counted as Studio 2.x")
	}
}

func TestReasonixCLIRecoversAfterProbeTimeout(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "cmd", "reasonix"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REASONIX_TEST_PROBE_MARKER", filepath.Join(dir, "probed"))
	for path, source := range map[string]string{
		"go.mod": "module reasonix\n\ngo 1.26\n",
		"cmd/reasonix/main.go": `package main
import ("fmt"; "os"; "time")
func main() {
  if len(os.Args) > 1 && os.Args[1] == "--warmup" { return }
  marker := os.Getenv("REASONIX_TEST_PROBE_MARKER")
  if _, err := os.Stat(marker); os.IsNotExist(err) {
    os.WriteFile(marker, nil, 0600)
    time.Sleep(3 * time.Second)
  }
  fmt.Println("reasonix v2.24.0")
}
`,
	} {
		if err := os.WriteFile(filepath.Join(dir, path), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(dir, "reasonix.exe")
	cmd := proc.Command("go", "build", "-o", bin, "./cmd/reasonix")
	cmd.Dir, cmd.Env = dir, append(os.Environ(), "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build native probe: %v %s", err, out)
	}
	warmReasonixCLIFixture(t, bin)
	if reasonixCLI(bin) {
		t.Fatal("the initial slow probe should time out")
	}
	if _, err := os.Stat(os.Getenv("REASONIX_TEST_PROBE_MARKER")); err != nil {
		t.Fatalf("the timed-out probe never reached the intentional delay: %v", err)
	}
	if !reasonixCLI(bin) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, err := proc.CommandContext(ctx, bin, "--version").CombinedOutput()
		t.Fatalf("a transient timeout permanently hid the installed 2.x CLI; direct probe=%q (%v)", out, err)
	}
}

// Separate a temporary executable's first launch from the version and timeout
// assertions. Windows, and macOS's first-run check of a newly written
// program, may delay that first launch beyond the probe budget: on a loaded
// Mac the check alone outlasts any fixed wait, so the launch is waited for
// as long as the test runs, and ended with it.
func warmReasonixCLIFixture(t *testing.T, bin string) {
	t.Helper()
	if out, err := proc.CommandContext(t.Context(), bin, "--warmup").CombinedOutput(); err != nil {
		t.Fatalf("start native CLI fixture: %v %s", err, out)
	}
}

func reasonixFixture(t testing.TB, config string) (*Agent, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", filepath.Join(home, "appdata"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("REASONIX_HOME", filepath.Join(home, "reasonix"))
	if err := provider.Save(provider.Provider{ID: "a", Name: "A", Chat: "http://127.0.0.1:1/v1", Key: "upstream-secret", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelEfforts("a/pro", []string{"none", "low", "high", "xhigh", "max"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{Name: "code", Members: []string{"a/pro"}, Levels: []string{"low", "high", "xhigh"}}); err != nil {
		t.Fatal(err)
	}
	a, err := Find("reasonix")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(a.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if config != "" {
		if err := os.WriteFile(a.Path, []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	env := filepath.Join(a.Dir, ".env")
	if err := os.WriteFile(env, []byte("# original key\nNATIVE_API_KEY=native-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return a, env
}

func reasonixDecode(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := toml.Unmarshal(b, &out); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	return out
}

const reasonixNativeConfig = `# configuration I wrote
default_model = "native/old" # keep this comment
[desktop]
provider_access = ["native"]
[ui]
theme = 'dark'
[[providers]]
name = "native"
kind = "openai"
base_url = "https://native.example/v1"
api_key_env = "NATIVE_API_KEY"
models = ["old", "other"]
[providers.headers]
custom = "keep"
[plugins]
enabled = ["docs"]
`

func TestReasonixGatewayAndRestore(t *testing.T) {
	a, env := reasonixFixture(t, reasonixNativeConfig)
	original := reasonixDecode(t, a.Path)
	f := a.Field("model")
	if f.Get() != "native/old" {
		t.Fatalf("native selection: %q", f.Get())
	}
	var opts []string
	for _, o := range f.Options(a.Values()) {
		opts = append(opts, o.Value)
	}
	for _, want := range []string{"native/other", "magpie/a/pro", "magpie/group/code"} {
		if !slices.Contains(opts, want) {
			t.Fatalf("missing option %s: %v", want, opts)
		}
	}
	if err := f.Set("magpie/group/code"); err != nil {
		t.Fatal(err)
	}
	cfg := reasonixDecode(t, a.Path)
	if cfg["default_model"] != "magpie/group/code" {
		t.Fatalf("default: %v", cfg)
	}
	providers := cfg["providers"].([]any)
	if len(providers) != 2 || !reflect.DeepEqual(providers[0], original["providers"].([]any)[0]) {
		t.Fatalf("native provider changed: %v", providers)
	}
	p := providers[1].(map[string]any)
	if p["name"] != "magpie" || p["kind"] != "openai" || p["base_url"] != gatewayV1() || p["default"] != "group/code" {
		t.Fatalf("wiring: %v", p)
	}
	key, _ := p["api_key_env"].(string)
	if value, ok := edit.GetEnvFile(env, key); !ok || value != gateway.TokenFor("reasonix") {
		t.Fatalf("gateway credential: %s %q", key, value)
	}
	if len(cfg["desktop"].(map[string]any)["provider_access"].([]any)) != 2 {
		t.Fatal("Studio can't select the new provider")
	}
	if usage.AgentOf("reasonix/2.24.0") != "reasonix" {
		t.Fatal("Reasonix user agent not recognized")
	}
	if a.Check() != "" {
		t.Fatal(a.Check())
	}
	if err := f.Set("magpie/a/flash"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if got := reasonixDecode(t, a.Path); !reflect.DeepEqual(got, original) {
		t.Fatalf("restore: %v", got)
	}
	raw, _ := os.ReadFile(a.Path)
	if !strings.Contains(string(raw), "# keep this comment") {
		t.Fatal("lost the default model comment")
	}
	if got, _ := os.ReadFile(env); string(got) != "# original key\nNATIVE_API_KEY=native-secret\n" {
		t.Fatalf("restore credentials: %s", got)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
}

func TestReasonixEffortAndSync(t *testing.T) {
	a, env := reasonixFixture(t, reasonixNativeConfig)
	if err := a.Field("model").Set("magpie/a/pro"); err != nil {
		t.Fatal(err)
	}
	effort := a.Field("effort")
	if effort == nil {
		t.Fatal("missing effort field")
	}
	if err := effort.Set("xhigh"); err != nil {
		t.Fatal(err)
	}
	if effort.Get() != "xhigh" {
		t.Fatal(effort.Get())
	}
	cfgBefore, _ := os.ReadFile(a.Path)
	envBefore, _ := os.ReadFile(env)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	cfgAfter, _ := os.ReadFile(a.Path)
	envAfter, _ := os.ReadFile(env)
	if string(cfgAfter) != string(cfgBefore) || string(envAfter) != string(envBefore) {
		t.Fatal("sync changed selection or credentials")
	}
	if err := effort.Set("impossible"); err == nil {
		t.Fatal("accepted unsupported effort")
	}
	if err := provider.Save(provider.Provider{ID: "b", Name: "B", Chat: "http://127.0.0.1:1/v1", Key: "k", Models: []string{"new"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	cfg := reasonixDecode(t, a.Path)
	p := cfg["providers"].([]any)[1].(map[string]any)
	if !slices.Contains(p["models"].([]any), any("b/new")) || cfg["default_model"] != "magpie/a/pro" {
		t.Fatalf("new catalog: %v", p)
	}
	overrides := p["model_overrides"].(map[string]any)
	if !slices.Contains(overrides["a/pro"].(map[string]any)["supported_efforts"].([]any), any("xhigh")) {
		t.Fatal("Studio lost extended effort levels")
	}
	if err := a.Field("model").Set("magpie/a/flash"); err != nil {
		t.Fatal(err)
	}
	if effort.Get() != "" {
		t.Fatalf("effort leaked to another model: %q", effort.Get())
	}
}

func TestReasonixConflictsAndReadErrorsLeaveFiles(t *testing.T) {
	for _, extra := range []string{
		"\n[[providers]]\nname = \"magpie\"\nmodels = [\"mine\"]\n",
		"\n[[providers]]\nname = \"broken\"\nmodels = [\n",
	} {
		t.Run(extra, func(t *testing.T) {
			a, env := reasonixFixture(t, reasonixNativeConfig+extra)
			before, _ := os.ReadFile(a.Path)
			beforeEnv, _ := os.ReadFile(env)
			if err := a.Field("model").Set("magpie/a/pro"); err == nil {
				t.Fatal("accepted conflicting or malformed config")
			}
			after, _ := os.ReadFile(a.Path)
			afterEnv, _ := os.ReadFile(env)
			if string(before) != string(after) || string(beforeEnv) != string(afterEnv) {
				t.Fatal("failed operation changed config")
			}
		})
	}
	a, env := reasonixFixture(t, reasonixNativeConfig)
	os.Remove(env)
	os.Mkdir(env, 0o700)
	before, _ := os.ReadFile(a.Path)
	if err := a.Field("model").Set("magpie/a/pro"); err == nil {
		t.Fatal("accepted unreadable credential file")
	}
	after, _ := os.ReadFile(a.Path)
	if string(before) != string(after) {
		t.Fatal("failure changed config")
	}
}

func TestReasonixSyncDoesNotCreateConfiguration(t *testing.T) {
	a, _ := reasonixFixture(t, "")
	if a.Sync == nil {
		t.Fatal("missing catalog synchronization")
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.Path); !os.IsNotExist(err) {
		t.Fatal("sync created an unused agent config")
	}
	if err := a.Field("model").Set(""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.Path); !os.IsNotExist(err) {
		t.Fatal("reset created a config")
	}
	if err := a.Field("model").Set("magpie/a/pro"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set(""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.Path); !os.IsNotExist(err) {
		t.Fatal("reset kept a config made by magpie")
	}
}

func TestReasonixBuiltinDefaultCanBeReselected(t *testing.T) {
	const original = "default_model = \"deepseek-flash\"\n"
	t.Run("current", func(t *testing.T) {
		a, _ := reasonixFixture(t, original)
		if err := a.Field("model").Set("deepseek-flash"); err != nil {
			t.Fatalf("the picker offers the current native model but refuses it: %v", err)
		}
	})
	t.Run("return", func(t *testing.T) {
		a, env := reasonixFixture(t, original)
		f := a.Field("model")
		if err := f.Set("magpie/a/pro"); err != nil {
			t.Fatal(err)
		}
		if !slices.ContainsFunc(f.Options(a.Values()), func(o Option) bool { return o.Value == "deepseek-flash" }) {
			t.Fatal("the original built-in default disappears from the picker")
		}
		if err := f.Set("deepseek-flash"); err != nil {
			t.Fatal(err)
		}
		cfg, err := readReasonix(a.Path)
		if err != nil || cfg.DefaultModel == nil || *cfg.DefaultModel != "deepseek-flash" || cfg.magpie().Name != "" {
			t.Fatalf("native default was not restored: %+v %v", cfg, err)
		}
		if _, exists := edit.GetEnvFile(env, reasonixKey); exists {
			t.Fatal("the managed credential survived native restoration")
		}
	})
}

func TestReasonixRestoreRemovesAccessAddedLater(t *testing.T) {
	a, _ := reasonixFixture(t, strings.Replace(reasonixNativeConfig, "provider_access = [\"native\"]\n", "", 1))
	f := a.Field("model")
	if err := f.Set("magpie/a/pro"); err != nil {
		t.Fatal(err)
	}
	if err := edit.SetTOMLKey(a.Path, "desktop", "provider_access", edit.Raw("[\"native\", \"later\"]")); err != nil {
		t.Fatal(err)
	}
	if err := f.Set("magpie/a/flash"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	cfg, err := readReasonix(a.Path)
	if err != nil || !reflect.DeepEqual(cfg.Desktop.Access, []string{"native", "later"}) {
		t.Fatalf("restore kept access that magpie added after initial setup: %v %v", cfg.Desktop.Access, err)
	}
}

func TestReasonixConcurrentEditsAndSync(t *testing.T) {
	a, _ := reasonixFixture(t, reasonixNativeConfig)
	if err := a.Field("model").Set("magpie/a/pro"); err != nil {
		t.Fatal(err)
	}
	b, err := Find("reasonix") // API calls and catalog sync construct separate agents.
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	errors := make(chan error, 128)
	for i := range 16 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			target := []*Agent{a, b}[i%2]
			for j := range 4 {
				if err := target.Field("model").Set([]string{"magpie/a/pro", "magpie/a/flash"}[(i+j)%2]); err != nil {
					errors <- err
				}
				if err := target.Sync(); err != nil {
					errors <- err
				}
			}
		}()
	}
	workers.Wait()
	close(errors)
	for err := range errors {
		t.Errorf("concurrent configuration operation failed: %v", err)
		break
	}
	cfg, err := readReasonix(a.Path)
	if err != nil || cfg.DefaultModel == nil || *cfg.DefaultModel != "magpie/"+cfg.magpie().Default {
		t.Fatalf("concurrent edits split the selected model and provider: %+v %v", cfg, err)
	}
	if drift := a.Check(); drift != "" {
		t.Fatal(drift)
	}
}
