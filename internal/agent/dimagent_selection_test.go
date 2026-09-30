package agent

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// Exercise persisted selections using magpie while Magpie switches the global
// default to a native model. Keep their provider and session records intact;
// this database test does not exercise the desktop's in-memory draft choices.
func TestDimAgentAlternatingClientSelections(t *testing.T) {
	home := dimagentSandbox(t)
	a := dimagent(home)
	db := dimagentFixture(t, a.Path)
	if err := catalog.SaveLive("relay", "https://relay.example/v1", []catalog.Model{
		{ID: "m1", Efforts: []string{"low", "medium", "high"}},
		{ID: "m2", Efforts: []string{"low", "high"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE providers SET models = '[{"modelId":"glm-5.3",
		"capabilities":{"reasoning":true},"metadata":{"reasoning":{"effortOptions":["low","high","max"]}}}]'
		WHERE providerId = 'mine';
		CREATE TABLE session_states (sessionId TEXT PRIMARY KEY, selectedProviderId TEXT, selectedModelId TEXT, settings TEXT, version INTEGER);`); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("model", "magpie/relay/m1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("effort", "medium"); err != nil {
		t.Fatal(err)
	}
	// DimAgent chooses another model/effort for a workspace and a session.
	cwd := filepath.Join(home, "project")
	if _, err := db.Exec(`INSERT INTO provider_selections VALUES
		('workspace:project', 'workspace', ?, ?, 'relay/m2', 'before', 'before', 4)`, cwd, dimagentProviderID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO session_states VALUES
		('desktop-session', ?, 'relay/m2', '{"metadata":{"dimcode":{"reasoningEffortByModel":{"relay/m2":"high"}}}}', 3)`, dimagentProviderID); err != nil {
		t.Fatal(err)
	}
	p, err := dimagentReadProvider(db, dimagentProviderID)
	if err != nil {
		t.Fatal(err)
	}
	p.Metadata["reasoningEffortByModel"] = json.RawMessage(`{"relay/m1":"medium","relay/m2":"high"}`)
	meta, _ := json.Marshal(p.Metadata)
	if _, err := db.Exec("UPDATE providers SET activeModelId = 'relay/m2', metadata = ?, version = version + 1 WHERE providerId = ?", string(meta), dimagentProviderID); err != nil {
		t.Fatal(err)
	}
	// Magpie switches to GLM and its highest effort, as in the report.
	if err := a.Apply("model", "mine/glm-5.3"); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("effort", "max"); err != nil {
		t.Fatal(err)
	}
	retained, err := dimagentReadProvider(db, dimagentProviderID)
	if err != nil || !retained.Enabled || !retained.allows("relay/m2") ||
		!slices.ContainsFunc(retained.Models, func(m dimagentModel) bool { return m.ID == "relay/m2" }) ||
		string(retained.Metadata["reasoningEffortByModel"]) != string(p.Metadata["reasoningEffortByModel"]) {
		t.Fatalf("desktop draft no longer resolves: %+v %v", retained, err)
	}
	var provider, model, settings string
	var version int
	if err := db.QueryRow("SELECT providerId, modelId, version FROM provider_selections WHERE selectionId = 'workspace:project'").Scan(&provider, &model, &version); err != nil || provider != dimagentProviderID || model != "relay/m2" || version != 4 {
		t.Fatalf("workspace selection changed: %s %s %d %v", provider, model, version, err)
	}
	if err := db.QueryRow("SELECT selectedProviderId, selectedModelId, settings, version FROM session_states WHERE sessionId = 'desktop-session'").Scan(&provider, &model, &settings, &version); err != nil || provider != dimagentProviderID || model != "relay/m2" || version != 3 || settings != `{"metadata":{"dimcode":{"reasoningEffortByModel":{"relay/m2":"high"}}}}` {
		t.Fatalf("session changed: %s %s %d %v", provider, model, version, err)
	}
	if a.Field("model").Get() != "mine/glm-5.3" || a.Field("effort").Get() != "max" {
		t.Fatal(a.Values())
	}
	if err := a.Sync(); err != nil || !dimagentWired(a.Path) {
		t.Fatalf("sync lost retained provider: %v", err)
	}
	// Another round trip keeps DimAgent's model effort, and reset returns
	// to the latest native default, rather than the original empty default.
	if err := a.Apply("model", "magpie/relay/m2"); err != nil || a.Field("effort").Get() != "high" {
		t.Fatalf("round trip lost effort: %v %v", a.Values(), err)
	}
	if err := a.Field("model").Set(""); err != nil || a.Field("model").Get() != "mine/glm-5.3" || a.Field("effort").Get() != "max" {
		t.Fatalf("round trip default not restored: %v %v", a.Values(), err)
	}
}

func TestDimAgentModelAndEffort(t *testing.T) {
	a := dimagent(dimagentSandbox(t))
	db := dimagentFixture(t, a.Path)
	if err := catalog.SaveLive("relay", "https://relay.example/v1", []catalog.Model{
		{ID: "m1", Efforts: []string{"low", "medium", "high"}}, {ID: "m2", Efforts: []string{"high", "xhigh"}},
	}); err != nil {
		t.Fatal(err)
	}
	if len(a.Fields) != 2 || a.Field("model") == nil || a.Field("effort") == nil {
		t.Fatalf("model/effort controls missing: %+v", a.Fields)
	}
	if err := a.Field("effort").Set("medium"); err == nil {
		t.Fatal("effort accepted with no selected model")
	}
	if err := a.Apply("model", "magpie/relay/m1"); err != nil {
		t.Fatal(err)
	}
	if a.Field("model").Get() != "magpie/relay/m1" {
		t.Fatal(a.Values())
	}
	var scope, selected, active string
	if err := db.QueryRow("SELECT scope, modelId FROM provider_selections WHERE selectionId = 'global'").Scan(&scope, &selected); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT activeModelId FROM providers WHERE providerId = ?", dimagentProviderID).Scan(&active); err != nil || scope != "global" || selected != "relay/m1" || active != selected {
		t.Fatalf("selection not persisted: %s %s %s %v", scope, selected, active, err)
	}
	if err := a.Apply("effort", "medium"); err != nil || a.Field("effort").Get() != "medium" {
		t.Fatalf("effort: %v %v", a.Values(), err)
	}
	var meta map[string]any
	dimagentJSON(t, db, "metadata", &meta)
	if meta["reasoningEffortByModel"].(map[string]any)["relay/m1"] != "medium" {
		t.Fatal(meta)
	}
	if err := a.Field("effort").Set("xhigh"); err == nil || a.Field("effort").Get() != "medium" {
		t.Fatal("unsupported effort changed the configuration")
	}
	options := a.Field("model").Options(a.Values())
	if !slices.ContainsFunc(options, func(o Option) bool { return o.Value == "magpie/relay/m1" && o.Ref == "relay/m1" }) {
		t.Fatal("catalog picker missing model reference")
	}
	if got := a.Field("effort").Options(a.Values()); len(got) != 3 || got[1].Value != "medium" {
		t.Fatalf("wrong per-model efforts: %+v", got)
	}
	if err := a.Field("model").Set("magpie/relay/m2"); err != nil {
		t.Fatal(err)
	}
	if a.Field("effort").Get() != "" || len(a.Field("effort").Options(a.Values())) != 2 {
		t.Fatal("effort leaked across models")
	}
	if err := a.Field("effort").Set("xhigh"); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "k", Models: []string{"m1", "m2", "m3"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil || a.Field("model").Get() != "magpie/relay/m2" || a.Field("effort").Get() != "xhigh" {
		t.Fatalf("catalog sync clobbered selection: %v %v", a.Values(), err)
	}
	if err := a.Field("model").Set("magpie/relay/m1"); err != nil || a.Field("effort").Get() != "medium" {
		t.Fatalf("model effort not remembered: %v %v", a.Values(), err)
	}
	if err := a.Field("effort").Set(""); err != nil || a.Field("effort").Get() != "" {
		t.Fatalf("effort default: %v %v", a.Values(), err)
	}
	dimagentJSON(t, db, "metadata", &meta)
	if _, exists := meta["reasoningEffortByModel"].(map[string]any)["relay/m1"]; exists || meta["reasoningEffortByModel"].(map[string]any)["relay/m2"] != "xhigh" {
		t.Fatal("reset modified another model's effort")
	}
	if err := a.Field("model").Set(""); err != nil || a.Field("model").Get() != "" {
		t.Fatalf("model default: %v %v", a.Values(), err)
	}
}

func TestDimAgentNativeModelAndRestore(t *testing.T) {
	a := dimagent(dimagentSandbox(t))
	db := dimagentFixture(t, a.Path)
	models := `[{
		"modelId":"seed-2.1-pro","displayName":"Seed 2.1 Pro",
		"capabilities":{"reasoning":true},
		"metadata":{"reasoning":{"effortOptions":["minimal","low","medium","high"]}}
	},{"modelId":"plain","capabilities":{"reasoning":false}},
	{"modelId":"disabled","capabilities":{"reasoning":true}}]`
	if _, err := db.Exec(`UPDATE providers SET models = ?, activeModelId = 'seed-2.1-pro', metadata =
		'{"user":"keep","disabledModelIds":["disabled"],"reasoningEffortByModel":{"seed-2.1-pro":"medium"}}'
		WHERE providerId = 'mine';
		INSERT INTO provider_selections VALUES ('original-global', 'global', NULL, 'mine', 'seed-2.1-pro', 'before', 'before', 9);
		CREATE UNIQUE INDEX global_selection ON provider_selections(scope) WHERE scope = 'global';`, models); err != nil {
		t.Fatal(err)
	}
	if a.Field("model").Get() != "mine/seed-2.1-pro" || a.Field("effort").Get() != "medium" {
		t.Fatal(a.Values())
	}
	opts := a.Field("model").Options(a.Values())
	if !slices.ContainsFunc(opts, func(o Option) bool { return o.Value == "mine/seed-2.1-pro" && o.Label == "Seed 2.1 Pro" }) ||
		slices.ContainsFunc(opts, func(o Option) bool { return o.Value == "mine/disabled" }) {
		t.Fatalf("native picker: %+v", opts)
	}
	if len(a.Field("effort").Options(a.Values())) != 4 {
		t.Fatal("Seed effort options missing")
	}
	if err := a.Field("model").Set("magpie/relay/m1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set("magpie/relay/m2"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set(""); err != nil || a.Field("model").Get() != "mine/seed-2.1-pro" || a.Field("effort").Get() != "medium" {
		t.Fatalf("previous default not restored: %v %v", a.Values(), err)
	}
	var id string
	if err := db.QueryRow("SELECT selectionId FROM provider_selections WHERE scope = 'global'").Scan(&id); err != nil || id != "original-global" {
		t.Fatalf("selection identity changed: %s %v", id, err)
	}
	if err := a.Field("effort").Set("high"); err != nil || a.Field("effort").Get() != "high" {
		t.Fatal(err, a.Values())
	}
	var metadata string
	if err := db.QueryRow("SELECT metadata FROM providers WHERE providerId = 'mine'").Scan(&metadata); err != nil {
		t.Fatal(err)
	}
	var meta map[string]json.RawMessage
	_ = json.Unmarshal([]byte(metadata), &meta)
	if string(meta["user"]) != `"keep"` {
		t.Fatal("native metadata overwritten")
	}
	if err := a.Field("model").Set("mine/disabled"); err == nil || a.Field("model").Get() != "mine/seed-2.1-pro" {
		t.Fatal("disabled model selected")
	}
	if err := a.Field("model").Set("mine/plain"); err != nil {
		t.Fatal(err)
	}
	if len(a.Field("effort").Options(a.Values())) != 0 || a.Field("effort").Set("medium") == nil {
		t.Fatal("non-reasoning model accepted effort")
	}
}

func TestDimAgentSelectionTransaction(t *testing.T) {
	a := dimagent(dimagentSandbox(t))
	db := dimagentFixture(t, a.Path)
	if _, err := db.Exec(`CREATE TRIGGER reject_selection BEFORE INSERT ON provider_selections
		BEGIN SELECT RAISE(ABORT, 'selection failed'); END`); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set("magpie/relay/m1"); err == nil {
		t.Fatal("selection failure ignored")
	}
	if dimagentWired(a.Path) || a.Field("model").Get() != "" {
		t.Fatal("failed selection left a provider behind")
	}
	if _, err := db.Exec("DROP TRIGGER reject_selection"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set("magpie/relay/m1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set("mine/missing"); err == nil || !dimagentWired(a.Path) || a.Field("model").Get() != "magpie/relay/m1" {
		t.Fatal("failed native selection removed magpie")
	}
	if _, err := db.Exec(`CREATE TRIGGER reject_reset BEFORE DELETE ON providers
		BEGIN SELECT RAISE(ABORT, 'reset failed'); END`); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set(""); err == nil || a.Field("model").Get() != "magpie/relay/m1" {
		t.Fatal("reset failure lost the selection")
	}
}

func TestDimAgentSyncRemovesInvalidSelections(t *testing.T) {
	a := dimagent(dimagentSandbox(t))
	db := dimagentFixture(t, a.Path)
	if err := a.Field("model").Set("magpie/relay/m1"); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "k", Models: []string{"m2"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	var model *string
	if err := db.QueryRow("SELECT modelId FROM provider_selections WHERE scope = 'global'").Scan(&model); err != nil || model != nil {
		t.Fatalf("invalid default retained: %v %v", model, err)
	}
	var own string
	if err := db.QueryRow("SELECT modelId FROM provider_selections WHERE selectionId = 'project:mine'").Scan(&own); err != nil || own != "own" {
		t.Fatal("sync changed another provider's selection")
	}
	// The same repair is needed when the catalog itself has not changed.
	if _, err := db.Exec("UPDATE provider_selections SET modelId = 'gone' WHERE scope = 'global'"); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT modelId FROM provider_selections WHERE scope = 'global'").Scan(&model); err != nil || model != nil {
		t.Fatal("unchanged catalog skipped selection repair")
	}
}
