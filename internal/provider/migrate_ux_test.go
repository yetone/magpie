package provider

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/zed"
)

// fakePlugin readies the test plugin's host for a move of fakeco, with
// the plugin not yet installed: the move's install adds it.
func fakePlugin(t *testing.T, inUse *[]string) (context.Context, string, *int) {
	t.Helper()
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("no bun on PATH")
	}
	claudeHome(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	fakeMover(t, inUse)
	movers["fakeco"].pkg = abs
	installs := 0
	installPlugin = func(ctx context.Context, pkg, _ string) error {
		installs++
		if pluginListed(pkg) {
			return nil
		}
		_, err := plugin.Add(ctx, pkg)
		return err
	}
	return ctx, abs, &installs
}

func setFakeLogins(t *testing.T, ls ...savedLogin) {
	t.Helper()
	loginsMu.Lock()
	defer loginsMu.Unlock()
	if err := writeLogins(ls); err != nil {
		t.Fatal(err)
	}
}

// Every account lapsed: there's nothing to try the plugin with, so the
// move says so before installing anything.
func TestMoveNeedsAWorkingAccount(t *testing.T) {
	claudeHome(t)
	inUse := []string{"fake-1"}
	fakeMover(t, &inUse)
	installs := 0
	installPlugin = func(context.Context, string, string) error { installs++; return nil }
	l := fakeLogin("a@fake", "r-a", true, true)
	l.Lapsed = "expired"
	setFakeLogins(t, l)
	err := Move(context.Background(), "fakeco")
	if w := WhyOf(err); w == nil || w.Code != "lapsed" || installs != 0 {
		t.Fatalf("Move with every account lapsed = %v (why %+v), %d installs", err, w, installs)
	}
	if m, _ := MigrationOf("fakeco"); m.State != MoveFailed || m.Why == nil || m.Why.Code != "lapsed" {
		t.Fatalf("the failed move's record: %+v", m)
	}
}

// A move that installed the plugin and then failed takes it away again,
// leaving no plugin provider saying it's signed out; one that got through
// keeps it, and going back takes it away when it serves nothing else. A
// plugin the user had installed stays either way.
func TestMoveTidiesItsPlugin(t *testing.T) {
	inUse := []string{"fake-1", "gone-model"}
	ctx, abs, _ := fakePlugin(t, &inUse)
	setFakeLogins(t, fakeLogin("a@fake", "r-a", true, true))
	err := Move(ctx, "fakeco")
	if w := WhyOf(err); w == nil || w.Code != "unserved" || !strings.Contains(err.Error(), "gone-model") {
		t.Fatalf("Move short of a model = %v (why %+v)", err, w)
	}
	if pluginListed(abs) {
		t.Fatal("a failed move left the plugin it installed")
	}
	if s := fakeSaved(t); fakeToken(s["a@fake"]) != "r-a" {
		t.Fatalf("the built-in's account after a failed move: %+v", s)
	}

	inUse = []string{"fake-1"}
	if err := Move(ctx, "fakeco"); err != nil {
		t.Fatal(err)
	}
	if m, _ := MigrationOf("fakeco"); !m.Installed || !pluginListed(abs) {
		t.Fatalf("after moving: installed %v, listed %v", m.Installed, pluginListed(abs))
	}
	if err := MoveBack(ctx, "fakeco"); err != nil {
		t.Fatal(err)
	}
	if pluginListed(abs) {
		t.Fatal("moving back left the plugin the move installed")
	}
	if s := fakeSaved(t); len(s) != 1 || !strings.HasPrefix(fakeToken(s["a@fake"]), "r-a") {
		t.Fatalf("the built-in's account moved back: %+v", s)
	}

	// the user's own plugin: a failed move leaves it, as does going back
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	inUse = []string{"gone-model"}
	if err := Move(ctx, "fakeco"); err == nil || !pluginListed(abs) {
		t.Fatalf("a failed move onto the user's plugin: %v, listed %v", err, pluginListed(abs))
	}
	inUse = []string{"fake-1"}
	os.Remove(migrationsPath())
	if err := Move(ctx, "fakeco"); err != nil {
		t.Fatal(err)
	}
	if err := MoveBack(ctx, "fakeco"); err != nil || !pluginListed(abs) {
		t.Fatalf("moving back off the user's plugin: %v, listed %v", err, pluginListed(abs))
	}
}

// The move proves each account through the vendor: one whose usage read
// the vendor refuses goes along marked and untried, as a lapsed one does,
// and the move fails when that leaves none tried; one the plugin couldn't
// reach its vendor for (its models hook keeping a list of its own) stops
// the move, as nothing proved it works.
func TestMoveProvesEachAccount(t *testing.T) {
	inUse := []string{"fake-1"}
	ctx, abs, _ := fakePlugin(t, &inUse)
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	setFakeLogins(t, fakeLogin("z@fake", "r-gone", true, true))
	if err := Move(ctx, "fakeco"); WhyOf(err) == nil || WhyOf(err).Code != "lapsed" || Moved("fakeco") {
		t.Fatalf("Move with its one account refused = %v", err)
	}

	setFakeLogins(t, fakeLogin("a@fake", "r-a", true, true), fakeLogin("z@fake", "r-unreachable", false, true))
	err := Move(ctx, "fakeco")
	w := WhyOf(err)
	if w == nil || w.Code != "account" || w.Args["user"] != "z@fake" || !strings.Contains(err.Error(), "couldn't reach FakeCo") {
		t.Fatalf("Move with the vendor unreachable = %v (why %+v)", err, w)
	}
	if Moved("fakeco") || plugin.SignedIn("fakeco") || len(fakeSaved(t)) != 2 {
		t.Fatal("a move with an account unproven moved")
	}
	loginsMu.Lock()
	for _, l := range readLogins() {
		if l.Agent == "plugin:fakeco" {
			t.Errorf("a failed move left the plugin's row of %s", l.User)
		}
	}
	loginsMu.Unlock()

	setFakeLogins(t, fakeLogin("a@fake", "r-a", true, true), fakeLogin("z@fake", "r-gone", false, true))
	if err := Move(ctx, "fakeco"); err != nil {
		t.Fatalf("an account refused stopped the move: %v", err)
	}
	for _, l := range pluginLogins(mustPlugin(t)) {
		if (l.User == "z@fake") != (l.Lapsed != "") {
			t.Fatalf("moved %s: lapsed %q", l.User, l.Lapsed)
		}
	}
}

// A Zed account's list goes to the plugin with it, and comes back with it:
// the plugin serves the account's own models until it reads them, not the
// families it declares for a sign-in with none.
func TestZedMoveCarriesItsModels(t *testing.T) {
	claudeHome(t)
	list := `{"models":[{"provider":"anthropic","id":"claude-sonnet-4-5","display_name":"Claude Sonnet 4.5"}]}`
	setFakeLogins(t, savedLogin{Agent: "zed", User: "z@example.com", On: true, First: true,
		Auth: []byte(`{"userId":"u1","accessToken":"a","models":` + list + `}`)})
	out, err := movers["zed"].out()
	if err != nil || len(out) != 1 {
		t.Fatal(out, err)
	}
	var r struct {
		Models json.RawMessage `json:"models"`
	}
	if json.Unmarshal([]byte(str(out[0].Auth["refresh"])), &r) != nil || !strings.Contains(string(r.Models), "claude-sonnet-4-5") {
		t.Fatalf("the refresh carries no list: %v", out[0].Auth["refresh"])
	}
	ls, _, err := movers["zed"].back(nil, "z@example.com", out[0].Auth)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := zedSaved(ls[0])
	if ms, err := zed.ParseModels(c.Models); err != nil || len(ms) != 1 || ms[0].ID != "claude-sonnet-4-5" {
		t.Fatalf("back without its list: %s %v", c.Models, err)
	}
}

// A moved provider shows the built-in's API host, which the move keeps,
// not plugin://<id>'s: "" for one that had none, as Zed's.
func TestMovedShowsBuiltinHost(t *testing.T) {
	claudeHome(t)
	pp := plugin.Provider{ID: "factory", Models: []plugin.Model{{ID: "m"}}}
	if h := pluginProvider(pp, pluginLogin{}).Host(); h != "factory" {
		t.Fatalf("a plugin's own provider: %q", h)
	}
	// the host the move kept, else (moved before it kept one) the built-in's
	for host, want := range map[string]string{"llm.factory.test": "llm.factory.test", "": HostOf(factoryAPI)} {
		_ = setMigration("factory", func(m *Migration) { *m = Migration{State: MovePlugin, Host: host} })
		p := pluginProvider(pp, pluginLogin{})
		if h := p.Host(); h != want || want == "" {
			t.Fatalf("moved: %q, want %q", h, want)
		}
		if w := p.Where(); w != want {
			t.Fatalf("moved calls go to %q, want %q", w, want)
		}
	}
	// one that showed no host shows none
	_ = setMigration("devin", func(m *Migration) { *m = Migration{State: MovePlugin} })
	if h := pluginProvider(plugin.Provider{ID: "devin"}, pluginLogin{}).Host(); h != "" {
		t.Fatalf("moved devin: %q", h)
	}
}
