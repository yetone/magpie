package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/codexcat"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/lastgood"
)

// Codex's profile-only wiring (hpnjstar on X: `codex -p magpie` on magpie,
// plain `codex` on its own provider). Codex reads --profile <name> from
// <name>.config.toml beside config.toml, a layer with the keys config.toml
// has, and refuses -p while config.toml holds the legacy [profiles.<name>]
// or profile = "<name>" (tried with codex-cli 0.165.0). So magpie writes
// magpie.config.toml alone: model_provider, model, effort, the catalog and
// its own [model_providers.magpie]. config.toml, its top-level
// model_provider, model and openai_base_url are never touched. Disconnect
// deletes that file and magpie's catalog. It is chosen by the Codex agent's
// "applies to" field, which changes only while Codex is not connected, so
// the two wirings never meet in one config.

// codexScopeKey is where the choice is kept: "profile" or nothing.
const codexScopeKey = "codex.scope"

// codexProfileFile is the file `codex -p magpie` reads.
const codexProfileFile = magpieID + ".config.toml"

// codexProviderTable is magpie's [model_providers.magpie], with token as
// the bearer key.
func codexProviderTable(v1, token string) []edit.KV {
	return []edit.KV{
		{Path: "name", Value: "magpie"},
		{Path: "base_url", Value: v1},
		{Path: "wire_api", Value: "responses"},
		{Path: "experimental_bearer_token", Value: token},
		// Codex offers its built-in image tool (image_gen.imagegen) and
		// the other OpenAI-provider tools only to a provider it reads as
		// the OpenAI actor: one that does not require OpenAI auth but
		// carries x-openai-actor-authorization. Without this header the
		// model is never given the tool, so a Codex on magpie can't draw
		// (the request that names model/draws never happens at all).
		// uses_openai_actor_authorization() is exactly this check.
		// An Inline table: a config that holds the headers as their own
		// [model_providers.magpie.http_headers] table, or as dotted keys,
		// gets the header set there, the user's others kept; written
		// inline beside that table it defined http_headers twice and
		// every model switch failed (wztlink1013 on Discord).
		{Path: "http_headers", Value: edit.Inline{{Path: "x-openai-actor-authorization", Value: "magpie"}}},
	}
}

// codexKeptToken is table's bearer token in path when it is one the gateway
// takes, else tok: see keptBearerToken's note in codexIn.
func codexKeptToken(path, table, tok string) string {
	if t, err := edit.GetTOMLTable(path, table); err == nil {
		if v := t["experimental_bearer_token"]; v != tok && keyHolds(v, tok) && !access.Default(v) {
			return v
		}
	}
	return tok
}

// codexProfileOnly: this Codex is wired by its profile alone.
func codexProfileOnly(at place) bool { return stashLoad()[at.key(codexScopeKey)] == "profile" }

// codexScopeField is the "applies to" choice. It changes only while Codex
// is not connected: switching under a live wiring would leave the other one
// behind in config.toml.
func codexScopeField(at place, connected func() bool) Field {
	return Field{
		Key: "scope", Label: "applies to", Quiet: true,
		Get: func() string { return stashLoad()[at.key(codexScopeKey)] },
		Set: func(v string) error {
			if v != "" && v != "profile" {
				return fmt.Errorf("applies to is all of Codex or the profile alone, not %q", v)
			}
			if v == stashLoad()[at.key(codexScopeKey)] {
				return nil
			}
			if connected() {
				return fmt.Errorf("disconnect Codex from magpie first, then change what it applies to")
			}
			stash(map[string]string{at.key(codexScopeKey): v})
			return nil
		},
		Options: func(map[string]string) []Option {
			return []Option{
				{Value: "", Label: "All of Codex"},
				{Value: "profile", Label: "codex -p magpie only", Note: "magpie writes magpie.config.toml and nothing else; plain codex keeps its own provider and models"},
			}
		},
	}
}

// codexProfileIn is Codex wired by its profile alone.
func codexProfileIn(at place) *Agent {
	dir := at.codexHome()
	path := filepath.Join(dir, codexProfileFile)
	catalogPath := filepath.Join(dir, "magpie-models.json")
	table := "model_providers." + magpieID
	get := func(k string) string { v, _ := edit.GetTOMLTop(path, k); return v }
	// the catalog as Codex reads it from this file: beside it, so by name
	catalogRef := filepath.Base(catalogPath)
	list := func() []byte { return codexcat.CatalogAt(magpieModels("codex"), get("model_reasoning_effort")) }
	ownList := func() []byte {
		cur, _ := edit.Read(catalogPath)
		return codexcat.Keep(cur, list())
	}
	// the list goes with the profile, unless the user added to it
	dropList := func() {
		cur, err := edit.Read(catalogPath)
		if err != nil {
			return
		}
		b := list()
		if string(codexcat.Keep(cur, b)) == string(b) {
			if err := lastgood.KeepUnparsed(catalogPath, catalogParses); err != nil {
				return
			}
			os.Remove(catalogPath)
		}
	}
	wired := func() bool { return get("model_provider") == magpieID && isMagpie(get("model")) }
	// put writes the profile as it should be, touching nothing else
	put := func(model string) error {
		if err := edit.SetTOMLTable(path, table, codexProviderTable(at.v1(), codexKeptToken(path, table, at.gwKey()))...); err != nil {
			return err
		}
		if err := writeOwnList(catalogPath, ownList()); err != nil {
			return err
		}
		return edit.SetTOMLTop(path,
			edit.KV{Path: "model_provider", Value: magpieID},
			edit.KV{Path: "model_catalog_json", Value: catalogRef},
			edit.KV{Path: "model", Value: model},
		)
	}
	// drop removes magpie's profile and its list: these two files only
	drop := func() error {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		dropList()
		return nil
	}
	return atomic(&Agent{
		ID: "codex", Name: "Codex", Icon: "codex-color", Bin: "codex", Dir: dir, Path: path,
		UA:     []string{"codex", "acp-extension-codex"},
		Unwire: drop,
		Routed: wired,
		Sync: func() error {
			if !wired() {
				return nil
			}
			t, _ := edit.GetTOMLTable(path, table)
			if t["base_url"] != at.v1() || t["experimental_bearer_token"] != codexKeptToken(path, table, at.gwKey()) {
				return put(get("model"))
			}
			if b := ownList(); string(b) != string(fileBytes(catalogPath)) {
				return writeOwnList(catalogPath, b)
			}
			return nil
		},
		Check: func() string {
			if !wired() {
				return ""
			}
			if t, _ := edit.GetTOMLTable(path, table); t["base_url"] != at.v1() {
				return "Codex's " + codexProfileFile + " no longer points at magpie's gateway (" + at.v1() + ")"
			}
			return ""
		},
		LastUsed: func() time.Time { return lastJSONLTime(filepath.Join(dir, "history.jsonl"), "ts", "text", nil) },
		Reached:  func(since time.Time) (time.Time, string, bool) { return codexReached(dir, since) },
		Notice: func() string {
			if wired() {
				return noticeCodexProfile.String()
			}
			return ""
		},
		Fields: []Field{
			{
				Key: "model", Label: "model",
				Get: func() string { return get("model") },
				Set: func(v string) error {
					switch {
					case v == "":
						return drop()
					case !isMagpie(v):
						return fmt.Errorf("pick a model through magpie: Codex's own models are what plain codex runs")
					}
					return put(v)
				},
				Options: func(map[string]string) []Option { return viaMagpie("codex", "") },
			},
			{
				Key: "effort", Label: "effort",
				Get: func() string { return get("model_reasoning_effort") },
				Set: func(v string) error {
					var err error
					if v == "" {
						err = edit.DelTOMLTop(path, "model_reasoning_effort")
					} else {
						err = edit.SetTOMLTop(path, edit.KV{Path: "model_reasoning_effort", Value: v})
					}
					if err != nil || !wired() {
						return err
					}
					return writeOwnList(catalogPath, ownList())
				},
				Options: func(cur map[string]string) []Option { return codexEfforts(magpieModels("codex"), cur["model"]) },
			},
			codexScopeField(at, wired),
		},
	}, path, catalogPath)
}

var noticeCodexProfile = newNotice("Start Codex with codex -p magpie; plain codex keeps its own provider.")

func fileBytes(path string) []byte { b, _ := edit.Read(path); return b }
