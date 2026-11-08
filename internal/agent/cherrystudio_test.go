package agent

import (
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The link's bytes, as Cherry Studio is handed them.
func TestCherryStudioLink(t *testing.T) {
	got := CherryStudioLink("http://127.0.0.1:3425")
	want := "cherrystudio://providers/api-keys?v=1&data=eyJpZCI6Im1hZ3BpZSIsImJhc2VVcmwiOiJodHRwOi8vMTI3LjAuMC4xOjM0MjUvdjEiLCJhcGlLZXkiOiJtYWdwaWUtY2hlcnJ5c3R1ZGlvIiwibmFtZSI6Im1hZ3BpZSIsInR5cGUiOiJvcGVuYWkifQ%3D%3D"
	if got != want {
		t.Fatalf("link\n got %s\nwant %s", got, want)
	}
}

// Cherry Studio 2.1.10's own handling (testdata/cherrystudio, copied from
// its source) creates one OpenAI chat-completions provider named magpie,
// at the gateway's /v1, with the key that names Cherry Studio. The other
// gateways put a ~ and a ? where base64 spells them + and /, which
// Cherry Studio swaps for _ and - before it reads the query: the data
// must be standard base64 for them to come back.
func TestCherryStudioLinkParsedAs2_1_10(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node to run Cherry Studio's parser")
	}
	var plus, slash bool
	for _, gw := range []string{"http://127.0.0.1:3425", "http://127.0.0.1:3425/~?", "http://127.0.0.1:3425/x~?", "http://127.0.0.1:3425/xy~?"} {
		link := CherryStudioLink(gw)
		data := link[strings.Index(link, "data=")+5:]
		plus = plus || strings.Contains(data, "%2B")
		slash = slash || strings.Contains(data, "%2F")
		out, err := exec.Command(node, filepath.Join("testdata", "cherrystudio", "providers-import-2.1.10.js"), link).Output()
		if err != nil {
			t.Fatal(err)
		}
		var r struct {
			Opened *struct {
				Version string
				Route   string
			}
			Created *struct {
				ProviderID          string
				Name                string
				DefaultChatEndpoint string
				EndpointConfigs     map[string]struct{ BaseURL string }
			}
			APIKey string
		}
		if err := json.Unmarshal(out, &r); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		if r.Opened == nil || r.Opened.Version != "1" || !strings.HasPrefix(r.Opened.Route, "/settings/provider?addProviderData=") {
			t.Fatalf("%s: Cherry Studio opened %s", gw, out)
		}
		c := r.Created
		if c == nil {
			t.Fatalf("%s: no provider created: %s", gw, out)
		}
		if c.ProviderID != "magpie" || c.Name != "magpie" || c.DefaultChatEndpoint != "openai-chat-completions" ||
			len(c.EndpointConfigs) != 1 || c.EndpointConfigs["openai-chat-completions"].BaseURL != gw+"/v1" {
			t.Errorf("%s: created %s", gw, out)
		}
		if r.APIKey != agentKeyAt("cherrystudio", gw) {
			t.Errorf("%s: key %q, want %q", gw, r.APIKey, agentKeyAt("cherrystudio", gw))
		}
	}
	if !plus || !slash {
		t.Fatalf("no link spelled + and / in base64 (plus %v, slash %v): the test no longer tries them", plus, slash)
	}
}

// cherrySchema is Cherry Studio 2.1.10's user_provider table: its first
// migration (0000_orange_jasper_sitwell.sql) less the api_features column
// 0012_sink_endpoint_dialect.sql drops.
const cherrySchema = "CREATE TABLE `user_provider` (`provider_id` text PRIMARY KEY NOT NULL, `preset_provider_id` text, `name` text NOT NULL, `logo_key` text, `endpoint_configs` text, `default_chat_endpoint` text, `api_keys` text DEFAULT '[]', `auth_config` text, `provider_settings` text, `is_enabled` integer DEFAULT false NOT NULL, `order_key` text NOT NULL, `created_at` integer NOT NULL, `updated_at` integer NOT NULL);" +
	"CREATE INDEX `user_provider_preset_idx` ON `user_provider` (`preset_provider_id`);"

func cherryFixture(t *testing.T, path string) *sql.DB {
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
	if _, err := db.Exec("PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0;" + cherrySchema); err != nil {
		t.Fatal(err)
	}
	return db
}

// cherryRow adds a provider as useProviderDeepLinkImport creates one:
// endpoint_configs JSON.stringify'd, keyed by its endpoint.
func cherryRow(t *testing.T, db *sql.DB, id, base string) {
	t.Helper()
	cfg, _ := json.Marshal(map[string]map[string]string{"openai-chat-completions": {"baseUrl": base}})
	if _, err := db.Exec("INSERT INTO user_provider (provider_id, name, endpoint_configs, default_chat_endpoint, order_key, created_at, updated_at) VALUES (?, ?, ?, 'openai-chat-completions', 'a0', 1, 1)", id, id, string(cfg)); err != nil {
		t.Fatal(err)
	}
}

func TestCherryStudioHas(t *testing.T) {
	gw := "http://127.0.0.1:3425"
	path := filepath.Join(t.TempDir(), "CherryStudio", "Data", "cherrystudio.sqlite")
	if has, known := cherryHas(path, gw); has || !known {
		t.Fatal("no database, yet has magpie or unknown")
	}
	db := cherryFixture(t, path)
	if has, known := cherryHas(path, gw); has || !known {
		t.Fatal("no providers, yet has magpie or unknown")
	}
	cherryRow(t, db, "openai", "https://api.openai.com/v1")
	cherryRow(t, db, "other", "http://127.0.0.1:34250/v1")
	cherryRow(t, db, "other2", "http://127.0.0.1:3426")
	if has, known := cherryHas(path, gw); has || !known {
		t.Fatal("another gateway's provider counted as magpie")
	}
	// Cherry Studio still running: the row is in its log only, read from
	// a copy, its folder left as it was
	before := dirNames(t, filepath.Dir(path))
	cherryRow(t, db, "magpie", gw+"/v1")
	if has, known := cherryHas(path, gw); !has || !known {
		t.Fatal("magpie's provider, in the log of a running Cherry Studio, not found")
	}
	if after := dirNames(t, filepath.Dir(path)); after != before {
		t.Fatalf("Cherry Studio's folder changed: %s -> %s", before, after)
	}
	// closed: its log checkpointed and gone
	if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	os.Remove(path + "-wal")
	os.Remove(path + "-shm")
	before = dirNames(t, filepath.Dir(path))
	if has, known := cherryHas(path, gw); !has || !known {
		t.Fatal("magpie's provider in a closed Cherry Studio's database not found")
	}
	if after := dirNames(t, filepath.Dir(path)); after != before {
		t.Fatalf("Cherry Studio's folder changed: %s -> %s", before, after)
	}
	// the gateway with no /v1, as a user may type it
	p2 := filepath.Join(t.TempDir(), "cherrystudio.sqlite")
	cherryRow(t, cherryFixture(t, p2), "m", gw)
	if has, known := cherryHas(p2, gw); !has || !known {
		t.Fatal("the gateway without /v1 not found")
	}
}

func TestCherryStudioUnreadableIsUnknown(t *testing.T) {
	dir := t.TempDir()
	junk := filepath.Join(dir, "cherrystudio.sqlite")
	if err := os.WriteFile(junk, []byte("not a database, mid-write"), 0o600); err != nil {
		t.Fatal(err)
	}
	if has, known := cherryHas(junk, "http://127.0.0.1:3425"); has || known {
		t.Fatalf("an unreadable database read as has=%v known=%v, want unknown", has, known)
	}
	other := filepath.Join(dir, "other.sqlite")
	db, err := sql.Open("sqlite", other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE x (a)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if has, known := cherryHas(other, "http://127.0.0.1:3425"); has || known {
		t.Fatalf("no user_provider table read as has=%v known=%v, want unknown", has, known)
	}
}

func cherrySandbox(t *testing.T) (home, userData string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	cfg, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	return home, filepath.Join(cfg, "CherryStudio")
}

func TestCherryStudioDetect(t *testing.T) {
	home, ud := cherrySandbox(t)
	a := cherryStudio(home)
	if a.detect() {
		t.Fatal("detected with no Cherry Studio folder")
	}
	if a.Dir != ud {
		t.Fatalf("Cherry Studio's folder %s, want %s", a.Dir, ud)
	}
	if has, known := a.Added(); has || !known {
		t.Fatalf("never run: has=%v known=%v, want known no", has, known)
	}
	// 1.x: providers in Chromium's Local Storage, which magpie doesn't read
	if err := os.MkdirAll(filepath.Join(ud, "Local Storage", "leveldb"), 0o700); err != nil {
		t.Fatal(err)
	}
	if !a.detect() {
		t.Fatal("Cherry Studio 1.x not detected")
	}
	if has, known := a.Added(); has || known {
		t.Fatalf("1.x: has=%v known=%v, want unknown", has, known)
	}
	db := cherryFixture(t, cherryDB(ud))
	if has, known := a.Added(); has || !known {
		t.Fatalf("2.x, no magpie: has=%v known=%v, want known no", has, known)
	}
	cherryRow(t, db, "magpie", "http://127.0.0.1:3425/v1")
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:3425")
	if has, known := cherryStudio(home).Added(); !has || !known {
		t.Fatalf("magpie added: has=%v known=%v", has, known)
	}
}

// A userData the user moved, in ~/.cherrystudio/boot-config.json, is
// detected and read; a boot-config that can't be read leaves it unknown.
func TestCherryStudioMovedUserData(t *testing.T) {
	home, _ := cherrySandbox(t)
	t.Setenv("MAGPIE_ADDR", "127.0.0.1:3425")
	moved := filepath.Join(home, "Elsewhere", "Cherry")
	boot := filepath.Join(home, ".cherrystudio", "boot-config.json")
	if err := os.MkdirAll(filepath.Dir(boot), 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"app.user_data_path": map[string]string{"/Applications/Cherry Studio.app/Contents/MacOS/Cherry Studio": moved}, "other": 1})
	if err := os.WriteFile(boot, b, 0o600); err != nil {
		t.Fatal(err)
	}
	a := cherryStudio(home)
	if a.detect() {
		t.Fatal("detected before the moved folder exists")
	}
	db := cherryFixture(t, cherryDB(moved))
	if !a.detect() {
		t.Fatal("moved userData not detected")
	}
	if has, known := a.Added(); has || !known {
		t.Fatalf("moved, no magpie: has=%v known=%v", has, known)
	}
	cherryRow(t, db, "magpie", "http://127.0.0.1:3425/v1")
	if has, known := cherryStudio(home).Added(); !has || !known {
		t.Fatalf("moved, magpie added: has=%v known=%v", has, known)
	}
	if err := os.WriteFile(boot, []byte("{half"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, known := cherryStudio(home).Added(); known {
		t.Fatal("a boot-config that can't be read, yet known")
	}
}
