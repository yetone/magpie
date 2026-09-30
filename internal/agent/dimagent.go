package agent

// DimAgent v2 keeps its providers in ~/.dimcode/v2/dimcode.sqlite.
// DIMCODE_HOME overrides that v2 directory itself, not its parent.
// Default models are in provider_selections (scope=global), and default
// efforts are in providers.metadata.reasoningEffortByModel. The desktop's
// unsent new-thread choices live in memory; existing sessions override these
// defaults. All database changes use SQLite transactions, including WAL.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	_ "modernc.org/sqlite"
)

const dimagentProviderID = "custom-magpie"

func dimagent(home string) *Agent {
	dir := os.Getenv("DIMCODE_HOME")
	if dir == "" {
		dir = filepath.Join(home, ".dimcode", "v2")
	} else if dir == "~" {
		dir = home
	} else if strings.HasPrefix(dir, "~/") || strings.HasPrefix(dir, `~\`) {
		dir = filepath.Join(home, dir[2:])
	}
	// Older releases treat DIMCODE_HOME as the parent of v2. Prefer the
	// documented directory, but find an already-created older database too.
	if os.Getenv("DIMCODE_HOME") != "" {
		if _, err := os.Stat(filepath.Join(dir, "dimcode.sqlite")); errors.Is(err, os.ErrNotExist) {
			if _, err := os.Stat(filepath.Join(dir, "v2", "dimcode.sqlite")); err == nil {
				dir = filepath.Join(dir, "v2")
			}
		}
	}
	path := filepath.Join(dir, "dimcode.sqlite")
	return &Agent{
		ID: "dimagent", Name: "DimAgent", Icon: "dimagent", Aliases: []string{"dim", "dimcode"},
		Bin: "dim", Dir: dir, Path: path, UA: []string{"dimagent", "dimcode"},
		Notice: func() string {
			return "Restart DimAgent to load changes to its providers, models and global defaults. Existing sessions and drafts keep their own choices."
		},
		Sync:  func() error { return dimagentWrite(path, true, true) },
		Check: func() string { return dimagentCheck(path) },
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: func() string { return dimagentCurrent(path) },
			Set: func(v string) error { return dimagentSetModel(path, v) },
			Options: func(cur map[string]string) []Option {
				return append(dimagentOwnModels(path, cur["model"]), viaMagpie("dimagent", magpieID+"/")...)
			},
		}, {
			Key: "effort", Label: "effort",
			Get: func() string { return dimagentCurrentEffort(path) },
			Set: func(v string) error { return dimagentSetEffort(path, v) },
			Options: func(cur map[string]string) []Option {
				return static(dimagentEfforts(path, cur["model"])...)
			},
		}},
	}
}

// dimagentDB never creates a database or migrates the client's schema.
// File URLs escape spaces, # and ? and work with drive letters on Windows.
func dimagentDB(path string, write bool) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	p := filepath.ToSlash(abs)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	mode := "ro"
	if write {
		mode = "rw"
	}
	u := url.URL{Scheme: "file", Path: p, RawQuery: "mode=" + mode + "&_pragma=busy_timeout(1000)"}
	db, err := sql.Open("sqlite", u.String())
	if err == nil {
		db.SetMaxOpenConns(1)
	}
	return db, err
}

type dimagentMetadata struct {
	MagpieManaged bool `json:"magpieManaged"`
}

func dimagentOwned(source, metadata string) bool {
	var m dimagentMetadata
	return source == "custom" && json.Unmarshal([]byte(metadata), &m) == nil && m.MagpieManaged
}

func dimagentWired(path string) bool {
	db, err := dimagentDB(path, false)
	if err != nil {
		return false
	}
	defer db.Close()
	var source, metadata string
	err = db.QueryRow("SELECT source, COALESCE(metadata, '{}') FROM providers WHERE providerId = ?", dimagentProviderID).Scan(&source, &metadata)
	return err == nil && dimagentOwned(source, metadata)
}

func dimagentCheck(path string) string {
	db, err := dimagentDB(path, false)
	if err != nil {
		return ""
	}
	defer db.Close()
	var source, metadata, driver, base, auth, credential string
	err = db.QueryRow(`SELECT source, COALESCE(metadata, '{}'), driverKind, COALESCE(baseUrl, ''),
		auth, COALESCE(credential, '{}') FROM providers WHERE providerId = ?`, dimagentProviderID).
		Scan(&source, &metadata, &driver, &base, &auth, &credential)
	if err != nil || !dimagentOwned(source, metadata) {
		return ""
	}
	var a struct {
		Type, HeaderName, Prefix string
	}
	var c struct {
		Type, APIKey string
	}
	if driver != "openai-compatible" || !sameHost(base, gatewayV1()) || strings.TrimRight(base, "/") != gatewayV1() {
		return "DimAgent's magpie provider no longer points at the gateway — reapply it."
	}
	if json.Unmarshal([]byte(auth), &a) != nil || json.Unmarshal([]byte(credential), &c) != nil ||
		a.Type != "api_key" || a.HeaderName != "Authorization" || a.Prefix != "Bearer " ||
		c.Type != "apiKey" || c.APIKey != gateway.TokenFor("dimagent") {
		return "DimAgent's magpie provider's authentication was changed — reapply it."
	}
	return ""
}

// dimagentWrite installs/removes the provider. syncOnly never installs one
// on its own, or re-enables one disabled in DimAgent. Updates leave its
// timeout, proxy, model overrides and other client-owned fields untouched.
func dimagentWrite(path string, on, syncOnly bool) error {
	return dimagentUpdate(path, on, syncOnly, nil)
}

// change runs inside the provider transaction, so a failed selection cannot
// leave a half-installed provider or destroy the previous default.
func dimagentUpdate(path string, on, syncOnly bool, change func(*sql.Tx) error) error {
	db, err := dimagentDB(path, true)
	if errors.Is(err, os.ErrNotExist) {
		if syncOnly || !on {
			return nil
		}
		return fmt.Errorf("open DimAgent once to create its database at %s", path)
	}
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var source, metadata string
	var name, driver, base, authBefore, credentialBefore, headersBefore, modelsBefore, modelsSource string
	var active sql.NullString
	err = tx.QueryRow(`SELECT source, COALESCE(metadata, '{}'), activeModelId, displayName, driverKind,
		COALESCE(baseUrl, ''), auth, COALESCE(credential, '{}'), COALESCE(headers, '{}'), models, modelsSource
		FROM providers WHERE providerId = ?`, dimagentProviderID).Scan(&source, &metadata, &active,
		&name, &driver, &base, &authBefore, &credentialBefore, &headersBefore, &modelsBefore, &modelsSource)
	found := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read DimAgent providers: %w", err)
	}
	if found && !dimagentOwned(source, metadata) {
		return fmt.Errorf("DimAgent already has a provider named %s that magpie does not manage", dimagentProviderID)
	}
	if !found && (syncOnly || !on && change == nil) {
		return nil
	}
	if !on {
		if found {
			if err := dimagentRestoreSelection(tx, metadata); err != nil {
				return err
			}
		}
		// Remove only selections pointing at our provider; every other
		// provider's selections, credentials and sessions stay as they are.
		if _, err := tx.Exec("DELETE FROM provider_selections WHERE providerId = ?", dimagentProviderID); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM providers WHERE providerId = ?", dimagentProviderID); err != nil {
			return err
		}
		if change != nil {
			if err := change(tx); err != nil {
				return err
			}
		}
		return tx.Commit()
	}

	models := []map[string]any{}
	ids := []string{}
	for _, m := range magpieModels("dimagent") {
		efforts := slices.DeleteFunc(slices.Clone(dimagentLevels), func(v string) bool { return !slices.Contains(m.Efforts, v) })
		// DimAgent requires a complete capabilities object, even for an
		// otherwise unknown model. These are its custom-model defaults.
		window, output := m.Context, m.Output
		if window <= 0 {
			window = 128000
		}
		if output <= 0 {
			output = min(16384, window)
		}
		inputs := []string{"text"}
		if m.Images {
			inputs = append(inputs, "image")
		}
		capabilities := map[string]any{
			"modalities": inputs, "toolCalling": true, "streaming": true,
			"structuredOutput": false, "vision": m.Images, "audio": false,
			"reasoning": len(efforts) > 0, "contextWindow": window, "maxOutputTokens": output,
		}
		reasoning := map[string]any{"supported": len(efforts) > 0}
		if len(efforts) > 0 {
			reasoning["mode"] = "effort"
			reasoning["effortOptions"] = efforts
		}
		models = append(models, map[string]any{"modelId": m.ID, "displayName": m.Name, "status": "active",
			"capabilities": capabilities, "metadata": map[string]any{"reasoning": reasoning}})
		ids = append(ids, m.ID)
	}
	meta := map[string]any{}
	if found {
		if err := json.Unmarshal([]byte(metadata), &meta); err != nil {
			return err
		}
	}
	meta["magpieManaged"], meta["customModels"], meta["adapter"], meta["name"] = true, true, "openai-compatible", "magpie"
	// A model disabled in DimAgent stays disabled after a catalog refresh.
	var disabled []string
	if b, err := json.Marshal(meta["disabledModelIds"]); err == nil {
		_ = json.Unmarshal(b, &disabled)
	}
	disabled = slices.DeleteFunc(disabled, func(id string) bool { return !slices.Contains(ids, id) })
	if disabled == nil {
		disabled = []string{}
	}
	enabled := slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return slices.Contains(disabled, id) })
	meta["enabledModelIds"], meta["disabledModelIds"] = enabled, disabled
	activeBefore := active
	if active.Valid && !slices.Contains(enabled, active.String) {
		active = sql.NullString{}
	}
	encode := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	auth := encode(map[string]any{"type": "api_key", "headerName": "Authorization", "prefix": "Bearer "})
	credential := encode(map[string]any{"type": "apiKey", "apiKey": gateway.TokenFor("dimagent")})
	headers := encode(map[string]string{"User-Agent": "DimAgent"})
	if syncOnly && name == "magpie" && driver == "openai-compatible" && base == gatewayV1() && modelsSource == "user" &&
		sameJSON(authBefore, json.RawMessage(auth)) && sameJSON(credentialBefore, json.RawMessage(credential)) &&
		sameJSON(headersBefore, json.RawMessage(headers)) && sameJSON(modelsBefore, models) && sameJSON(metadata, meta) && active == activeBefore {
		if err := dimagentPruneSelections(tx, enabled); err != nil {
			return err
		}
		return tx.Commit()
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if found {
		_, err = tx.Exec(`UPDATE providers SET displayName = ?, driverKind = ?, baseUrl = ?, auth = ?,
			credential = ?, headers = ?, models = ?, metadata = ?, activeModelId = ?,
			modelsSource = ?, modelsUpdatedAt = ?, updatedAt = ?, version = version + 1 WHERE providerId = ?`,
			"magpie", "openai-compatible", gatewayV1(), auth, credential, headers, encode(models), encode(meta), active,
			"user", now, now, dimagentProviderID)
	} else {
		_, err = tx.Exec(`INSERT INTO providers (providerId, source, displayName, driverKind, baseUrl, auth,
			credential, headers, models, metadata, activeModelId, modelsSource, modelsUpdatedAt,
			enabled, createdAt, updatedAt, version) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			dimagentProviderID, "custom", "magpie", "openai-compatible", gatewayV1(), auth, credential, headers,
			encode(models), encode(meta), active, "user", now, 1, now, now, 1)
	}
	if err != nil {
		return fmt.Errorf("write DimAgent provider: %w", err)
	}
	if err := dimagentPruneSelections(tx, enabled); err != nil {
		return err
	}
	if change != nil {
		if err := change(tx); err != nil {
			return err
		}
	}
	return tx.Commit()
}
