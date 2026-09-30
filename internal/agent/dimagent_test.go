package agent

import (
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// The schema from DimAgent v2. Extra columns exercise updates that leave
// client-owned settings intact; SQLite, including WAL, is real in these tests.
const dimagentSchema = `CREATE TABLE providers (
 providerId TEXT PRIMARY KEY, source TEXT NOT NULL, displayName TEXT NOT NULL,
 driverKind TEXT NOT NULL, defaultBaseUrl TEXT, baseUrl TEXT, auth TEXT NOT NULL,
 credential TEXT, headers TEXT, proxy TEXT, timeoutMs INTEGER, retryConfig TEXT,
 models TEXT NOT NULL, modelConfigOverrides TEXT, capabilityDefaults TEXT,
 activeModelId TEXT, modelsSource TEXT NOT NULL, catalogVersion TEXT,
 modelsUpdatedAt TEXT, metadata TEXT, enabled INTEGER NOT NULL,
 createdAt TEXT NOT NULL, updatedAt TEXT NOT NULL, version INTEGER NOT NULL,
 accountUsageSettings TEXT
);
CREATE TABLE provider_selections (
 selectionId TEXT PRIMARY KEY, scope TEXT NOT NULL, cwd TEXT,
 providerId TEXT NOT NULL, modelId TEXT, createdAt TEXT NOT NULL,
 updatedAt TEXT NOT NULL, version INTEGER NOT NULL
);
CREATE TABLE sessions (id TEXT PRIMARY KEY, data TEXT);`

func dimagentSandbox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("DIMCODE_HOME", "")
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "k", Models: []string{"m1", "m2"}}); err != nil {
		t.Fatal(err)
	}
	return home
}

func dimagentFixture(t *testing.T, path string) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// A raw SQLite DSN treats ? as the start of its options, even in a
	// filename on Unix. Escape the fixture's path just like a file URL.
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(dimagentSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO providers (providerId, source, displayName, driverKind, auth,
		credential, models, modelsSource, enabled, createdAt, updatedAt, version)
		VALUES ('mine', 'custom', 'Mine', 'anthropic', '{}', '{"apiKey":"keep"}', '[]', 'remote', 1, 'before', 'before', 7);
		INSERT INTO provider_selections VALUES ('project:mine', 'project', '/work', 'mine', 'own', 'before', 'before', 1);
		INSERT INTO sessions VALUES ('session', 'untouched');`); err != nil {
		t.Fatal(err)
	}
	return db
}

func dimagentJSON(t *testing.T, db *sql.DB, column string, out any) {
	t.Helper()
	var raw string
	if err := db.QueryRow("SELECT "+column+" FROM providers WHERE providerId = ?", dimagentProviderID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), out); err != nil {
		t.Fatal(err)
	}
}

// The provider lifecycle remains independently testable beneath the model field.
func dimagentInstallField(a *Agent) *Field {
	return &Field{Get: func() string {
		if dimagentWired(a.Path) {
			return "magpie"
		}
		return ""
	},
		Set: func(v string) error { return dimagentWrite(a.Path, v != "", false) }}
}

func TestDimAgent(t *testing.T) {
	home := dimagentSandbox(t)
	a := dimagent(home)
	db := dimagentFixture(t, a.Path)
	f := dimagentInstallField(a)
	if !a.Detected() || f.Get() != "" {
		t.Fatalf("detected %v, value %q", a.Detected(), f.Get())
	}
	for _, alias := range []string{"dimagent", "dim", "dimcode"} {
		got, err := Find(alias)
		if err != nil || got.ID != "dimagent" || got.Path != a.Path {
			t.Fatalf("Find(%q): %v, %v", alias, got, err)
		}
	}
	// Merely refreshing models never wires an agent in.
	if err := a.Sync(); err != nil || f.Get() != "" {
		t.Fatalf("sync before install: %v, %q", err, f.Get())
	}
	if err := f.Set("magpie"); err != nil {
		t.Fatal(err)
	}
	var source, driver, base, created string
	var enabled, version int
	if err := db.QueryRow(`SELECT source, driverKind, baseUrl, enabled, createdAt, version
		FROM providers WHERE providerId = ?`, dimagentProviderID).Scan(&source, &driver, &base, &enabled, &created, &version); err != nil {
		t.Fatal(err)
	}
	if f.Get() != "magpie" || source != "custom" || driver != "openai-compatible" || base != gatewayV1() || enabled != 1 || version != 1 {
		t.Fatalf("provider: %s %s %s %d %d", source, driver, base, enabled, version)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT version FROM providers WHERE providerId = ?", dimagentProviderID).Scan(&version); err != nil || version != 1 {
		t.Fatalf("unchanged catalog rewrote provider: version %d, %v", version, err)
	}
	var credential, auth, headers map[string]string
	dimagentJSON(t, db, "credential", &credential)
	dimagentJSON(t, db, "auth", &auth)
	dimagentJSON(t, db, "headers", &headers)
	if credential["type"] != "apiKey" || credential["apiKey"] != gateway.TokenFor("dimagent") ||
		auth["headerName"] != "Authorization" || auth["prefix"] != "Bearer " || headers["User-Agent"] != "DimAgent" {
		t.Fatalf("gateway authentication: %v %v %v", credential, auth, headers)
	}
	if a.Check() != "" {
		t.Fatalf("correct wiring: %s", a.Check())
	}
	if _, err := db.Exec("UPDATE providers SET baseUrl = 'https://changed.example/v1' WHERE providerId = ?", dimagentProviderID); err != nil {
		t.Fatal(err)
	}
	if d := a.Drift(); d == nil || d.Kind != "unwired" {
		t.Fatalf("changed gateway not detected: %+v", d)
	}
	if err := f.Set("magpie"); err != nil || a.Check() != "" {
		t.Fatalf("reapply: %v, %s", err, a.Check())
	}
	var models []struct {
		ID string `json:"modelId"`
	}
	dimagentJSON(t, db, "models", &models)
	if len(models) != 2 || models[0].ID != "relay/m1" || models[1].ID != "relay/m2" {
		t.Fatalf("models: %+v", models)
	}
	// Reinstall updates one row, never duplicates it.
	if err := f.Set("magpie"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM providers").Scan(&n); err != nil || n != 2 {
		t.Fatalf("providers: %d, %v", n, err)
	}
	if _, err := db.Exec(`INSERT INTO provider_selections VALUES ('project:magpie', 'project', '/another', ?, 'relay/m1', 'before', 'before', 1)`, dimagentProviderID); err != nil {
		t.Fatal(err)
	}
	if err := f.Set(""); err != nil || f.Get() != "" {
		t.Fatalf("reset: %v, %q", err, f.Get())
	}
	var own, session string
	if err := db.QueryRow("SELECT credential FROM providers WHERE providerId = 'mine'").Scan(&own); err != nil || own != `{"apiKey":"keep"}` {
		t.Fatalf("own credential: %q, %v", own, err)
	}
	if err := db.QueryRow("SELECT data FROM sessions WHERE id = 'session'").Scan(&session); err != nil || session != "untouched" {
		t.Fatalf("session: %q, %v", session, err)
	}
	if err := db.QueryRow("SELECT count(*) FROM provider_selections").Scan(&n); err != nil || n != 1 {
		t.Fatalf("selections: %d, %v", n, err)
	}
	if err := a.Sync(); err != nil || f.Get() != "" {
		t.Fatalf("removed provider restored: %v", err)
	}
}

func TestDimAgentPathsAndMissingDatabase(t *testing.T) {
	home := dimagentSandbox(t)
	escaped := "custom dir #"
	if runtime.GOOS != "windows" {
		escaped += " ?"
	}
	for _, c := range []struct{ env, want string }{
		{"", filepath.Join(home, ".dimcode", "v2")},
		{filepath.Join(home, escaped, "数据"), filepath.Join(home, escaped, "数据")},
		{"~/dim", filepath.Join(home, "dim")},
		{`~\dim-backslash`, filepath.Join(home, "dim-backslash")},
	} {
		t.Run(c.env, func(t *testing.T) {
			t.Setenv("DIMCODE_HOME", c.env)
			a := dimagent(home)
			if a.Dir != c.want || a.Path != filepath.Join(c.want, "dimcode.sqlite") {
				t.Fatalf("dir %q, path %q", a.Dir, a.Path)
			}
			if err := a.Sync(); err != nil {
				t.Fatal(err)
			}
			if err := dimagentInstallField(a).Set(""); err != nil {
				t.Fatal(err)
			}
			if err := dimagentInstallField(a).Set("magpie"); err == nil || !strings.Contains(err.Error(), "open DimAgent once") {
				t.Fatalf("missing database: %v", err)
			}
			if _, err := os.Stat(a.Path); !os.IsNotExist(err) {
				t.Fatalf("missing database created: %v", err)
			}
			dimagentFixture(t, a.Path)
			if err := dimagentInstallField(a).Set("magpie"); err != nil || !dimagentWired(a.Path) {
				t.Fatalf("escaped SQLite path: %v", err)
			}
		})
	}
}

func TestDimAgentSyncKeepsClientSettings(t *testing.T) {
	a := dimagent(dimagentSandbox(t))
	db := dimagentFixture(t, a.Path)
	if err := dimagentInstallField(a).Set("magpie"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE providers SET enabled = 0, activeModelId = 'relay/m1',
		proxy = 'keep', modelConfigOverrides = '{"relay/m1":{"contextWindow":123}}',
		metadata = '{"magpieManaged":true,"user":"keep","disabledModelIds":["relay/m2"]}' WHERE providerId = ?`, dimagentProviderID); err != nil {
		t.Fatal(err)
	}
	s := settings.Load()
	s.HiddenModels = map[string][]string{"dimagent": {"relay/m2"}}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	var enabled int
	var active, proxy, overrides string
	if err := db.QueryRow("SELECT enabled, activeModelId, proxy, modelConfigOverrides FROM providers WHERE providerId = ?", dimagentProviderID).Scan(&enabled, &active, &proxy, &overrides); err != nil {
		t.Fatal(err)
	}
	if enabled != 0 || active != "relay/m1" || proxy != "keep" || overrides != `{"relay/m1":{"contextWindow":123}}` {
		t.Fatalf("client settings: %d %s %s %s", enabled, active, proxy, overrides)
	}
	var meta map[string]any
	dimagentJSON(t, db, "metadata", &meta)
	if meta["user"] != "keep" {
		t.Fatalf("metadata lost: %v", meta)
	}
	var models []map[string]any
	dimagentJSON(t, db, "models", &models)
	if len(models) != 1 || models[0]["modelId"] != "relay/m1" {
		t.Fatalf("hidden models: %v", models)
	}
}

func TestDimAgentResetRollsBack(t *testing.T) {
	a := dimagent(dimagentSandbox(t))
	db := dimagentFixture(t, a.Path)
	if err := dimagentInstallField(a).Set("magpie"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO provider_selections VALUES ('magpie', 'global', NULL, ?, 'relay/m1', 'before', 'before', 1)`, dimagentProviderID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER keep_provider BEFORE DELETE ON providers BEGIN SELECT RAISE(ABORT, 'cannot delete'); END`); err != nil {
		t.Fatal(err)
	}
	if err := dimagentInstallField(a).Set(""); err == nil {
		t.Fatal("reset succeeded despite failed delete")
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM provider_selections WHERE providerId = ?", dimagentProviderID).Scan(&n); err != nil || n != 1 || !dimagentWired(a.Path) {
		t.Fatalf("partial reset: %d, %v", n, err)
	}
}

func TestDimAgentDoesNotOverwriteUnmanagedProvider(t *testing.T) {
	a := dimagent(dimagentSandbox(t))
	db := dimagentFixture(t, a.Path)
	if _, err := db.Exec("UPDATE providers SET providerId = ? WHERE providerId = 'mine'", dimagentProviderID); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"magpie", ""} {
		if err := dimagentInstallField(a).Set(v); err == nil {
			t.Fatalf("unmanaged provider overwritten on %q", v)
		}
	}
	if dimagentInstallField(a).Get() != "" {
		t.Fatal("unmanaged provider mistaken for ours")
	}
}

func TestDimAgentModelCapabilitiesAndWAL(t *testing.T) {
	a := dimagent(dimagentSandbox(t))
	db := dimagentFixture(t, a.Path)
	if err := catalog.SaveLive("relay", "https://relay.example/v1", []catalog.Model{
		{ID: "m1", Name: "Vision", Images: true, ImageInput: imageInputBool(true), Context: 256000, Output: 32000, Efforts: []string{"low", "high"}},
		{ID: "m2"},
	}); err != nil {
		t.Fatal(err)
	}
	// A client's open read transaction keeps its snapshot while magpie
	// writes; replacing the database file would break this guarantee.
	reader, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Rollback()
	var before, during, after int
	if err := reader.QueryRow("SELECT count(*) FROM providers").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := dimagentInstallField(a).Set("magpie"); err != nil {
		t.Fatal(err)
	}
	if err := reader.QueryRow("SELECT count(*) FROM providers").Scan(&during); err != nil {
		t.Fatal(err)
	}
	if err := reader.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT count(*) FROM providers").Scan(&after); err != nil || before != 1 || during != 1 || after != 2 {
		t.Fatalf("WAL snapshot: %d %d %d, %v", before, during, after, err)
	}
	var models []struct {
		ID           string         `json:"modelId"`
		Capabilities map[string]any `json:"capabilities"`
		Metadata     struct {
			Reasoning struct {
				Efforts []string `json:"effortOptions"`
			} `json:"reasoning"`
		} `json:"metadata"`
	}
	dimagentJSON(t, db, "models", &models)
	for _, m := range models {
		for _, key := range []string{"toolCalling", "streaming", "structuredOutput", "vision", "audio", "reasoning"} {
			if _, ok := m.Capabilities[key].(bool); !ok {
				t.Fatalf("DimAgent requires %s to be a boolean: %v", key, m.Capabilities)
			}
		}
		if m.ID == "relay/m1" && (m.Capabilities["vision"] != true || m.Capabilities["contextWindow"] != float64(256000) ||
			m.Capabilities["maxOutputTokens"] != float64(32000) || len(m.Metadata.Reasoning.Efforts) != 2) {
			t.Fatalf("catalog capabilities missing: %+v", m)
		}
	}
}

func TestDimAgentLegacyHome(t *testing.T) {
	home := dimagentSandbox(t)
	base := filepath.Join(home, "legacy")
	path := filepath.Join(base, "v2", "dimcode.sqlite")
	dimagentFixture(t, path)
	t.Setenv("DIMCODE_HOME", base)
	if a := dimagent(home); a.Path != path {
		t.Fatalf("older DIMCODE_HOME database not found: %s", a.Path)
	}
	// A direct database takes precedence when both are present.
	direct := filepath.Join(base, "dimcode.sqlite")
	dimagentFixture(t, direct)
	if a := dimagent(home); a.Path != direct {
		t.Fatalf("direct DIMCODE_HOME database not preferred: %s", a.Path)
	}
}

func TestDimAgentDoesNotMigrateUnknownSchema(t *testing.T) {
	a := dimagent(dimagentSandbox(t))
	if err := os.MkdirAll(a.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", a.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE unknown (value TEXT)"); err != nil {
		t.Fatal(err)
	}
	if err := dimagentInstallField(a).Set("magpie"); err == nil {
		t.Fatal("unsupported schema accepted")
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type = 'table'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("database migrated: %d, %v", n, err)
	}
}
