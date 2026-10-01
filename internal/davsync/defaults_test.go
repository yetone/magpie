package davsync

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/backup"
	"github.com/yetone/magpie/internal/profile"
)

// Two computers exchange an explicit default, a chosen model, and then a
// reset through an encrypted remote. A saved default profile travels too.
func TestSyncAgentDefaults(t *testing.T) {
	f, srv := newFakeS3(t)
	cfg := f.config(srv)
	a, b := newComputer(t), newComputer(t)
	use := func(c computer) *agent.Agent {
		t.Helper()
		c.use(t)
		t.Setenv("USERPROFILE", string(c))
		path := filepath.Join(string(c), ".claude", "settings.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		x, err := agent.Find("claude")
		if err != nil {
			t.Fatal(err)
		}
		return x
	}
	now := func() {
		t.Helper()
		if err := Now(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	set := func(a *agent.Agent, model string) {
		t.Helper()
		if err := a.Apply("model", model); err != nil {
			t.Fatal(err)
		}
	}
	use(a)
	p, err := profile.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := profile.Save("defaults", p); err != nil {
		t.Fatal(err)
	}
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	now()
	remote, err := backup.Open(f.objects["team x+y/magpie/magpie.magpie-backup"], cfg.Passphrase)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := remote.Agents["claude.model"]; !ok || v != "" {
		t.Errorf("remote omitted the default model: %+v", remote.Agents)
	}
	cb := use(b)
	set(cb, "opus")
	if err := Configure(cfg); err != nil {
		t.Fatal(err)
	}
	now()
	if v := cb.Field("model").Get(); v != "" {
		t.Errorf("b kept model %q after downloading a default", v)
	}
	ps, err := profile.Load()
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := ps["defaults"].Fields["claude.model"]; !ok || v != "" {
		t.Errorf("synced profile omitted its default: %+v", ps)
	}
	ca := use(a)
	set(ca, "sonnet")
	now()
	cb = use(b)
	now()
	if v := cb.Field("model").Get(); v != "sonnet" {
		t.Fatalf("b did not receive the chosen model: %q", v)
	}
	if _, err := profile.Apply(ps["defaults"]); err != nil {
		t.Fatal(err)
	}
	now()
	ca = use(a)
	now()
	if v := ca.Field("model").Get(); v != "" {
		t.Errorf("a kept model %q after b restored its default profile", v)
	}
}
