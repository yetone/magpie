package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
)

// #969 (pan17): the DeepSeek Harness desktop app, wired through magpie, failed
// every turn with `no credential for provider route "magpie"; its profile
// resolves MAGPIE_GATEWAY_KEY, which is not set — store MAGPIE_GATEWAY_KEY
// through the credentials service (the web Models page writes it) or export
// it` (MISSING_CREDENTIAL), while magpie said it was connected. The route was
// in its profile; the key was in ~/.dsh/.env, which only dsh's product CLI
// loads. The key now goes into dsh's own store, ~/.dsh/.credentials.yaml,
// which every dsh reads (dsh-credentials-local), the desktop app included.
func TestDshDesktopGetsTheKeyFromItsStore(t *testing.T) {
	home, dir, web := dshRouteHome(t)
	desktop := filepath.Join(dir, "profiles", "desktop", "cordis.patch.yml")
	os.MkdirAll(filepath.Dir(desktop), 0o755)
	os.WriteFile(web, []byte("# Your patch layer for this dsh profile.\n"), 0o644)
	os.WriteFile(desktop, []byte("# Your patch layer for this dsh profile.\n"), 0o644)
	a := dsh(home)
	if err := a.Field("model").Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(desktop); !strings.Contains(string(b), "apiKeyEnv: "+dshKeyRef) {
		t.Fatalf("the desktop app's profile has no route:\n%s", b)
	}
	creds := filepath.Join(dir, ".credentials.yaml")
	b, err := os.ReadFile(creds)
	if err != nil {
		t.Fatal(err)
	}
	if s := string(b); !strings.HasPrefix(s, "version: 1\n") || !strings.Contains(s, "refs:\n  "+dshKeyRef+": magpie\n") {
		t.Fatalf("the key is not in dsh's store:\n%s", s)
	}
	// dsh refuses to start on a store any other user can read
	if st, _ := os.Stat(creds); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("store mode %v", st.Mode().Perm())
	}
	if d := a.Check(); d != "" {
		t.Fatalf("check: %s", d)
	}

	// wired by an earlier magpie, the key in .env alone: the check says so,
	// and the sync that runs as magpie starts writes it
	os.Remove(creds)
	if d := a.Check(); !strings.Contains(d, ".credentials.yaml") || !strings.Contains(d, "desktop") {
		t.Fatalf("a store without the key is not named: %q", d)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if d := a.Check(); d != "" {
		t.Fatalf("after the sync: %s", d)
	}

	// back to dsh's own models: magpie's key goes, the rest stays
	os.WriteFile(creds, []byte("version: 1\n# mine\nrefs:\n  DEEPSEEK_API_KEY: sk-mine\n  "+dshKeyRef+": magpie\n"), 0o600)
	if err := a.Field("model").Set("deepseek-v4-pro"); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(creds)
	if s := string(b); strings.Contains(s, dshKeyRef) || !strings.Contains(s, "DEEPSEEK_API_KEY: sk-mine") || !strings.Contains(s, "# mine") {
		t.Fatalf("back to dsh's own:\n%s", s)
	}
}

// #1332: a caller key this gateway issued, held in dsh's own store, takes dsh
// through magpie all the same — the store wins over .env (#969) and the
// gateway authenticates the key — so the check is quiet. A key the gateway
// doesn't take still is drift.
func TestDshStoreHoldingAGatewayCallerKey(t *testing.T) {
	home, dir, web := dshRouteHome(t)
	os.WriteFile(web, []byte("# Your patch layer for this dsh profile.\n"), 0o644)
	a := dsh(home)
	if err := a.Field("model").Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	secret, err := access.Update("add-key", access.Change{Name: "dsh"})
	if err != nil {
		t.Fatal(err)
	}
	creds := filepath.Join(dir, ".credentials.yaml")
	os.WriteFile(creds, []byte("version: 1\nrefs:\n  "+dshKeyRef+": "+secret+"\n"), 0o600)
	if d := a.Check(); d != "" {
		t.Fatalf("a key the gateway issued is drift: %s", d)
	}
	os.WriteFile(creds, []byte("version: 1\nrefs:\n  "+dshKeyRef+": sk-not-this-gateways\n"), 0o600)
	if d := a.Check(); !strings.Contains(d, "holds another") {
		t.Fatalf("a key the gateway doesn't take is not reported: %q", d)
	}
}

// A store the user keeps is written into, not over; one holding a key of the
// user's under magpie's name, or in dsh's pre-release flat layout, is left to
// the user and to dsh.
func TestDshStoreOfTheUsersOwn(t *testing.T) {
	home, dir, web := dshRouteHome(t)
	os.WriteFile(web, []byte("# Your patch layer for this dsh profile.\n"), 0o644)
	creds := filepath.Join(dir, ".credentials.yaml")
	a := dsh(home)

	own := "version: 1\nrefs:\n  DEEPSEEK_API_KEY: sk-mine\nrecords:\n  llm-pi-ai/amazon-bedrock:\n    kind: api-key\n    env:\n      AWS_PROFILE: prod\n"
	os.WriteFile(creds, []byte(own), 0o600)
	if err := a.Field("model").Set("magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(creds)
	for _, want := range []string{"DEEPSEEK_API_KEY: sk-mine", "AWS_PROFILE: prod", dshKeyRef + ": magpie"} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("missing %q:\n%s", want, b)
		}
	}

	theirs := "version: 1\nrefs:\n  " + dshKeyRef + ": sk-mine\n"
	os.WriteFile(creds, []byte(theirs), 0o600)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(creds); string(b) != theirs {
		t.Fatalf("the user's key was written over:\n%s", b)
	}
	if d := a.Check(); !strings.Contains(d, "holds another") {
		t.Fatalf("the user's key is not reported: %q", d)
	}

	flat := "DEEPSEEK_API_KEY: sk-mine\n"
	os.WriteFile(creds, []byte(flat), 0o600)
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(creds); string(b) != flat {
		t.Fatalf("a flat store was written:\n%s", b)
	}
}
