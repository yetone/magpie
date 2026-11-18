package agent

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/update"
)

// fakeDim is Dim alpha's daemon as magpie talks to it: Provider records
// with versions every write is checked against, a key that is stored and
// never read back, provider.refresh listing the models the gateway would,
// and one global model pick with a version of its own, as
// crates/dim-app-server/src/provider.rs has them.
type fakeDim struct {
	mu        sync.Mutex
	providers map[string]*fakeDimProvider
	sel       *dimSelection
	listed    []dimModel // what refresh lists for magpie
	calls     []string
	starts    int
}

type fakeDimProvider struct {
	version int
	enabled bool
	base    string
	key     string // "" none
	models  []dimModel
}

func newFakeDim() *fakeDim {
	return &fakeDim{providers: map[string]*fakeDimProvider{
		"magpie": {version: 1, base: dimDefault},
		// one of the user's own, on the user's own key and model
		"deepseek": {version: 7, enabled: true, base: "https://api.deepseek.com/v1", key: "sk-users", models: []dimModel{{ID: "deepseek-chat"}}},
	}, sel: &dimSelection{Provider: "deepseek", Model: "deepseek-chat", Version: 3}}
}

// use makes f the daemon of every connection magpie opens; away is none.
func (f *fakeDim) use(t *testing.T) {
	old := dimOpen
	t.Cleanup(func() { dimOpen = old })
	dimOpen = func(dir string, start bool) (io.ReadWriteCloser, error) {
		a, b := net.Pipe()
		go f.serve(b)
		return a, nil
	}
}

func away(t *testing.T) {
	old := dimOpen
	t.Cleanup(func() { dimOpen = old })
	dimOpen = func(string, bool) (io.ReadWriteCloser, error) { return nil, errDimAway }
}

func (f *fakeDim) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return
		}
		var m struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		json.Unmarshal(line, &m)
		res, code := f.answer(m.Method, m.Params)
		out := map[string]any{"jsonrpc": "2.0", "id": m.ID}
		if code != "" {
			out["error"] = map[string]any{"code": -32000, "message": code, "data": map[string]any{"appError": code}}
		} else {
			out["result"] = res
		}
		b, _ := json.Marshal(out)
		if _, err := c.Write(append(b, '\n')); err != nil {
			return
		}
	}
}

func (f *fakeDim) record(id string, p *fakeDimProvider) map[string]any {
	return map[string]any{"providerId": id, "version": p.version, "enabled": p.enabled,
		"connection": map[string]any{"baseUrl": p.base, "credentialConfigured": p.key != ""}}
}

func (f *fakeDim) answer(method string, raw json.RawMessage) (any, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, method)
	var p struct {
		ProviderID      string         `json:"providerId"`
		ModelID         string         `json:"modelId"`
		ExpectedVersion *int           `json:"expectedVersion"`
		Patch           map[string]any `json:"patch"`
		Edit            map[string]int `json:"edit"`
	}
	json.Unmarshal(raw, &p)
	pr := f.providers[p.ProviderID]
	switch method {
	case "hello":
		return map[string]any{"serverVersion": "0.1.0-alpha.24", "protocolVersion": 4}, ""
	case "provider.management":
		if pr == nil {
			return nil, "provider_not_found"
		}
		return map[string]any{"providers": []any{f.record(p.ProviderID, pr)}}, ""
	case "provider.update":
		if pr == nil {
			return nil, "provider_not_found"
		}
		if p.ExpectedVersion == nil || *p.ExpectedVersion != pr.version {
			return nil, "provider_conflict"
		}
		if v, ok := p.Patch["baseUrl"].(string); ok {
			pr.base = v
		}
		if v, ok := p.Patch["enabled"].(bool); ok {
			pr.enabled = v
		}
		if c, ok := p.Patch["credential"].(map[string]any); ok {
			switch c["action"] {
			case "set":
				pr.key, _ = c["value"].(string)
			case "clear":
				if pr.enabled {
					return nil, "provider_invalid_config" // clear goes with enabled:false
				}
				pr.key = ""
			}
		}
		pr.version++
		return f.record(p.ProviderID, pr), ""
	case "provider.refresh":
		if pr == nil || pr.key == "" || !pr.enabled {
			return nil, "provider_invalid_config"
		}
		pr.models = slices.Clone(f.listed)
		pr.version++
		return map[string]any{"models": pr.models, "version": pr.version}, ""
	case "provider.models.edit":
		if p.ExpectedVersion == nil || *p.ExpectedVersion != pr.version {
			return nil, "provider_conflict"
		}
		for i, m := range pr.models {
			if m.ID == p.ModelID {
				pr.models[i].MissingLimits = slices.DeleteFunc(slices.Clone(m.MissingLimits), func(k string) bool { return p.Edit[k] > 0 })
			}
		}
		pr.version++
		return map[string]any{"models": pr.models, "version": pr.version}, ""
	case "provider.selection":
		return map[string]any{"selection": f.sel}, ""
	case "provider.select":
		have := (*int)(nil)
		if f.sel != nil {
			have = &f.sel.Version
		}
		if (have == nil) != (p.ExpectedVersion == nil) || have != nil && *have != *p.ExpectedVersion {
			return nil, "provider_conflict"
		}
		if pr == nil {
			return nil, "provider_not_found"
		}
		i := slices.IndexFunc(pr.models, func(m dimModel) bool { return m.ID == p.ModelID })
		if i < 0 {
			return nil, "provider_not_found"
		}
		if len(pr.models[i].MissingLimits) > 0 {
			return nil, "provider_invalid_config"
		}
		v := 1
		if f.sel != nil {
			v = f.sel.Version + 1
		}
		f.sel = &dimSelection{Provider: p.ProviderID, Model: p.ModelID, Version: v}
		return f.sel, ""
	}
	return nil, "method_not_found"
}

func (f *fakeDim) magpie() fakeDimProvider {
	f.mu.Lock()
	defer f.mu.Unlock()
	return *f.providers["magpie"]
}

func dimSandbox(t *testing.T) (home string) {
	home = t.TempDir()
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
	os.WriteFile(filepath.Join(home, ".zshrc"), []byte("export A=1\n"), 0o644)
	return home
}

// Connecting Dim fills in Dim's own built-in Magpie provider through its
// daemon: magpie's key for Dim, on, the model listed (its missing limits
// given) and picked as Dim's model, so a plain dima runs on magpie. The
// user's own provider and its key are untouched, no dima function is
// added, and Disconnect puts Dim's pick and the provider back.
func TestDimFillsItsMagpieProvider(t *testing.T) {
	home := dimSandbox(t)
	f := newFakeDim()
	f.listed = []dimModel{{ID: "deepseek/pro", MissingLimits: []string{"contextWindow", "maxOutputTokens"}}, {ID: "deepseek/flash"}}
	f.use(t)
	a, err := Find("dim")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	m := f.magpie()
	if m.key != gateway.TokenFor("dim") || !m.enabled || m.base != dimDefault {
		t.Fatalf("Dim's Magpie provider: %+v (want key %q)", m, gateway.TokenFor("dim"))
	}
	if f.sel == nil || f.sel.Provider != "magpie" || f.sel.Model != "deepseek/pro" {
		t.Fatalf("Dim's model %+v", f.sel)
	}
	if !slices.Contains(f.calls, "provider.models.edit") {
		t.Fatalf("no limits given: %v", f.calls)
	}
	if d := f.providers["deepseek"]; d.version != 7 || d.key != "sk-users" || !d.enabled || d.base != "https://api.deepseek.com/v1" {
		t.Fatalf("the user's own provider changed: %+v", d)
	}
	if update.ShellFunctionIn("dima") != nil {
		t.Fatalf("dima function added in %v", update.ShellFunctionIn("dima"))
	}
	if s := a.Start(); s.Bin != "dima" || s.Model != "magpie/deepseek/pro" || len(s.Env) != 0 {
		t.Fatalf("start %+v", s)
	}
	if a.Launch() != "" || a.Typed() != "" || a.Notice() != "" {
		t.Fatalf("launch %q typed %q notice %q", a.Launch(), a.Typed(), a.Notice())
	}
	if got := a.Field("model").Get(); got != "magpie/deepseek/pro" {
		t.Fatalf("model %q", got)
	}
	// another model: picked, the provider's key not set again
	updates := strings.Count(strings.Join(f.calls, " "), "provider.update")
	if err := a.Apply("model", "magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if f.sel.Model != "deepseek/flash" || strings.Count(strings.Join(f.calls, " "), "provider.update") != updates {
		t.Fatalf("second pick: %+v %v", f.sel, f.calls)
	}

	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	if f.sel.Provider != "deepseek" || f.sel.Model != "deepseek-chat" {
		t.Fatalf("Dim's model not put back: %+v", f.sel)
	}
	if m := f.magpie(); m.key != "" || m.enabled || m.base != dimDefault {
		t.Fatalf("Dim's Magpie provider not put back: %+v", m)
	}
	if _, err := os.Stat(dimFile()); !os.IsNotExist(err) {
		t.Fatalf("dim.json kept: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".zshrc")); string(b) != "export A=1\n" {
		t.Fatalf(".zshrc:\n%s", b)
	}
}

// A gateway on another port than Dim's 3425 is given to the provider, and
// a later port is given to it again while it is as magpie left it.
func TestDimMagpieProviderPort(t *testing.T) {
	dimSandbox(t)
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:4567")
	f := newFakeDim()
	f.sel = nil // a Dim with no model picked yet
	f.listed = []dimModel{{ID: "deepseek/pro"}}
	f.use(t)
	a, _ := Find("dim")
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if m := f.magpie(); m.base != "http://127.0.0.1:4567/v1" || m.key == "" {
		t.Fatalf("provider %+v", m)
	}
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:4568")
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if m := f.magpie(); m.base != "http://127.0.0.1:4568/v1" {
		t.Fatalf("new port not given: %+v", m)
	}
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	if m := f.magpie(); m.base != dimDefault || m.key != "" || m.enabled {
		t.Fatalf("not put back: %+v", m)
	}
}

// A key, or an address of the user's own, on Dim's Magpie provider is
// theirs: magpie picks the model on it and leaves it as it was, on
// Disconnect too.
func TestDimMagpieProviderUsersOwn(t *testing.T) {
	for _, tc := range []struct {
		name, base, key string
	}{
		{"key", dimDefault, "users-own-key"},
		{"address", "http://192.168.1.9:3425/v1", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dimSandbox(t)
			f := newFakeDim()
			mp := f.providers["magpie"]
			mp.base, mp.key, mp.enabled = tc.base, tc.key, true
			f.listed = []dimModel{{ID: "deepseek/pro"}}
			f.use(t)
			a, _ := Find("dim")
			before := f.magpie()
			if tc.key == "" {
				// Dim lists nothing without a key: the pick fails and dima
				// is started on magpie's variables instead
				if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
					t.Fatal(err)
				}
				if m := f.magpie(); m.base != before.base || m.key != "" || m.version != before.version {
					t.Fatalf("the user's address changed: %+v", m)
				}
				if a.Start().Env == nil {
					t.Fatalf("no variables with no pick: %+v", a.Start())
				}
			} else {
				if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
					t.Fatal(err)
				}
				m := f.magpie()
				if m.key != tc.key || m.base != tc.base || slices.Contains(f.calls, "provider.update") {
					t.Fatalf("the user's own value changed: %+v %v", m, f.calls)
				}
				if f.sel.Provider != "magpie" || f.sel.Model != "deepseek/pro" {
					t.Fatalf("not picked: %+v", f.sel)
				}
			}
			if err := a.Apply("model", ""); err != nil {
				t.Fatal(err)
			}
			if m := f.magpie(); m.key != before.key || m.base != before.base || !m.enabled || slices.Contains(f.calls, "provider.update") {
				t.Fatalf("Disconnect changed the user's own: %+v %v", m, f.calls)
			}
			if f.sel.Provider != "deepseek" {
				t.Fatalf("Dim's model not put back: %+v", f.sel)
			}
		})
	}
}

// A Dim connected before magpie filled its provider in (its dima function
// in the user's shells) is moved onto the provider by a Sync while its
// daemon runs, and the function goes; with no daemon running, the
// function stays and nothing is started.
func TestDimSyncMovesToProvider(t *testing.T) {
	home := dimSandbox(t)
	away(t)
	a, _ := Find("dim")
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	zshrc := filepath.Join(home, ".zshrc")
	if b, _ := os.ReadFile(zshrc); !strings.Contains(string(b), "dima() {") {
		t.Skipf("no dima function on this system: %s", b)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(zshrc); !strings.Contains(string(b), "dima() {") {
		t.Fatalf("function gone with no daemon: %s", b)
	}
	f := newFakeDim()
	f.listed = []dimModel{{ID: "deepseek/pro"}}
	f.use(t)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if f.sel.Provider != "magpie" || f.magpie().key == "" {
		t.Fatalf("not moved: %+v %+v", f.sel, f.magpie())
	}
	if b, _ := os.ReadFile(zshrc); string(b) != "export A=1\n" {
		t.Fatalf("function kept:\n%s", b)
	}
}

// Dim gone (no daemon, no dima) doesn't keep Disconnect from finishing.
func TestDimDisconnectWithDimGone(t *testing.T) {
	dimSandbox(t)
	t.Setenv("PATH", t.TempDir())
	f := newFakeDim()
	f.listed = []dimModel{{ID: "deepseek/pro"}}
	f.use(t)
	a, _ := Find("dim")
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	old := dimOpen
	dimOpen = func(string, bool) (io.ReadWriteCloser, error) { return nil, errors.Join(errDimAway, os.ErrNotExist) }
	defer func() { dimOpen = old }()
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dimFile()); !os.IsNotExist(err) {
		t.Fatalf("dim.json kept: %v", err)
	}
}

// Dim's Magpie provider names its models as the gateway tells them, with
// the provider after each (T_t__biu on X), but takes the names only when
// it lists the models again: a Sync lists them again once magpie's models
// or their names changed (a Dim connected before had none listed with the
// providers), and not while they stay the same.
func TestDimListsAgainWhenNamesChange(t *testing.T) {
	dimSandbox(t)
	f := newFakeDim()
	f.listed = []dimModel{{ID: "deepseek/pro"}}
	f.use(t)
	a, _ := Find("dim")
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	refreshes := func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		return strings.Count(strings.Join(f.calls, " "), "provider.refresh")
	}
	n := refreshes()
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if refreshes() != n {
		t.Fatalf("listed again with nothing changed: %v", f.calls)
	}
	// one connected by an older magpie, which kept no listing
	st := readDimState(dimFile())
	st.Dim.Listed = ""
	writeDimState(dimFile(), st)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if refreshes() != n+1 {
		t.Fatalf("not listed again for an older connect: %v", f.calls)
	}
	// the provider renamed: the names Dim shows change
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek Official", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if refreshes() != n+2 {
		t.Fatalf("not listed again after a rename: %v", f.calls)
	}
	if f.sel.Provider != "magpie" || f.sel.Model != "deepseek/pro" {
		t.Fatalf("pick changed: %+v", f.sel)
	}
}
