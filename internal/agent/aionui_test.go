package agent

import (
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The link's bytes, as AionUi is handed them.
func TestAionUiLink(t *testing.T) {
	got := AionUiLink("http://127.0.0.1:3425")
	want := "aionui://add-provider?api_key=magpie-aionui&base_url=http%3A%2F%2F127.0.0.1%3A3425%2Fv1&name=magpie&platform=custom"
	if got != want {
		t.Fatalf("link\n got %s\nwant %s", got, want)
	}
}

// AionUi 2.2.2's own parser (testdata/aionui/deeplink-2.2.2.js, copied
// from its release) takes the link to Settings → Model with the add form
// filled in: OpenAI-compatible ("custom"), the gateway's /v1, the key that
// names AionUi.
func TestAionUiLinkParsedAs2_2_2(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node to run AionUi's parser")
	}
	out, err := exec.Command(node, filepath.Join("testdata", "aionui", "deeplink-2.2.2.js"), AionUiLink("http://127.0.0.1:3425")).Output()
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		Parsed *struct {
			Action string
			Params map[string]string
		}
		Route   string
		Pending map[string]string
		Form    map[string]string
	}
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if r.Parsed == nil || r.Parsed.Action != "add-provider" || r.Route != "/settings/model" {
		t.Fatalf("AionUi read the link as %s", out)
	}
	want := map[string]string{"api_key": "magpie-aionui", "base_url": "http://127.0.0.1:3425/v1", "name": "magpie", "platform": "custom"}
	for k, v := range want {
		if r.Pending[k] != v {
			t.Errorf("pending %s = %q, want %q (%s)", k, r.Pending[k], v, out)
		}
	}
	if r.Form["platform"] != "custom" || r.Form["base_url"] != want["base_url"] || r.Form["api_key"] != want["api_key"] {
		t.Errorf("the add form opens with %v", r.Form)
	}
}

// aionuiSchema is AionUi 2.2.2's providers table, from the database it
// made on its first start.
const aionuiSchema = `CREATE TABLE providers (id TEXT PRIMARY KEY NOT NULL, platform TEXT NOT NULL, name TEXT NOT NULL, base_url TEXT NOT NULL, api_key_encrypted TEXT NOT NULL, models TEXT NOT NULL DEFAULT '[]', enabled INTEGER NOT NULL DEFAULT 1, capabilities TEXT NOT NULL DEFAULT '[]', context_limit INTEGER, model_protocols TEXT, model_enabled TEXT, model_health TEXT, bedrock_config TEXT, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, is_full_url INTEGER NOT NULL DEFAULT 0, model_settings TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(model_settings)), user_id TEXT NOT NULL DEFAULT 'system_default_user');
CREATE INDEX idx_providers_platform ON providers(platform);
CREATE INDEX idx_providers_user_platform ON providers(user_id, platform);`

func aionuiFixture(t *testing.T, path string) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0;" + aionuiSchema); err != nil {
		t.Fatal(err)
	}
	return db
}

func aionuiRow(t *testing.T, db *sql.DB, id, base string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO providers (id, platform, name, base_url, api_key_encrypted, created_at, updated_at) VALUES (?, 'custom', ?, ?, 'enc', 1, 1)`, id, id, base); err != nil {
		t.Fatal(err)
	}
}

func TestAionUiHas(t *testing.T) {
	gw := "http://127.0.0.1:3425"
	path := filepath.Join(t.TempDir(), "aionui", "aionui-backend.db")
	if has, known := aionuiHas(path, gw); has || !known {
		t.Fatal("no database, yet has magpie or unknown")
	}
	db := aionuiFixture(t, path)
	if has, known := aionuiHas(path, gw); has || !known {
		t.Fatal("no providers, yet has magpie or unknown")
	}
	aionuiRow(t, db, "openai", "https://api.openai.com/v1")
	aionuiRow(t, db, "other-magpie", "http://127.0.0.1:3426/v1")
	if has, known := aionuiHas(path, gw); has || !known {
		t.Fatal("another gateway's provider counted as magpie")
	}
	// AionUi still running: the row is in its log only (no checkpoint),
	// read from a copy, AionUi's folder left as it was
	before := dirNames(t, filepath.Dir(path))
	aionuiRow(t, db, "m1", gw+"/v1")
	if has, known := aionuiHas(path, gw); !has || !known {
		t.Fatal("magpie's provider, in the log of a running AionUi, not found")
	}
	if after := dirNames(t, filepath.Dir(path)); after != before {
		t.Fatalf("AionUi's folder changed: %s -> %s", before, after)
	}
	// AionUi closed: its log checkpointed and gone
	if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	os.Remove(path + "-wal")
	os.Remove(path + "-shm")
	before = dirNames(t, filepath.Dir(path))
	if has, known := aionuiHas(path, gw); !has || !known {
		t.Fatal("magpie's provider in a closed AionUi's database not found")
	}
	if after := dirNames(t, filepath.Dir(path)); after != before {
		t.Fatalf("AionUi's folder changed: %s -> %s", before, after)
	}
}

func TestAionUiUnreadableIsUnknown(t *testing.T) {
	dir := t.TempDir()
	junk := filepath.Join(dir, "aionui-backend.db")
	if err := os.WriteFile(junk, []byte("not a database, AionUi mid-write"), 0o600); err != nil {
		t.Fatal(err)
	}
	if has, known := aionuiHas(junk, "http://127.0.0.1:3425"); has || known {
		t.Fatalf("an unreadable database read as has=%v known=%v, want unknown", has, known)
	}
	// a database of another shape (no providers table) is unknown too
	other := filepath.Join(dir, "other.db")
	db, err := sql.Open("sqlite", other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE x (a)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if has, known := aionuiHas(other, "http://127.0.0.1:3425"); has || known {
		t.Fatalf("no providers table read as has=%v known=%v, want unknown", has, known)
	}
}

// The Agents page's poll reads AionUi's database again only once it or
// its log changed; a failed read is tried again.
func TestAionUiStatusRereadsOnChange(t *testing.T) {
	gw := "http://127.0.0.1:3425"
	path := filepath.Join(t.TempDir(), "aionui-backend.db")
	if err := os.WriteFile(path, []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := &dbStatus{has: aionuiHas}
	if _, known := st.read(path, gw); known {
		t.Fatal("junk read as known")
	}
	os.Remove(path)
	db := aionuiFixture(t, path)
	if has, known := st.read(path, gw); has || !known {
		t.Fatalf("after the junk went: has=%v known=%v, want known no", has, known)
	}
	aionuiRow(t, db, "m1", gw+"/v1")
	// the log grew; on a coarse clock its time may not have moved, its size has
	if has, known := st.read(path, gw); !has || !known {
		t.Fatal("magpie added since the last read, not seen")
	}
}

func TestAionUiDetect(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	a := aionui()
	if a.detect() {
		t.Fatal("detected with no AionUi folder")
	}
	cfg, _ := os.UserConfigDir()
	if a.Dir != filepath.Join(cfg, "AionUi") {
		t.Fatalf("AionUi's folder %s", a.Dir)
	}
	if err := os.MkdirAll(filepath.Join(cfg, "AionUi", "aionui"), 0o700); err != nil {
		t.Fatal(err)
	}
	if !a.detect() {
		t.Fatal("AionUi's userData not detected")
	}
	if has, known := a.Added(); has || !known {
		t.Fatalf("no database yet: has=%v known=%v, want known no", has, known)
	}
}
