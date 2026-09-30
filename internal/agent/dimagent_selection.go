package agent

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
)

type dimagentReader interface {
	QueryRow(string, ...any) *sql.Row
}

type dimagentSelection struct {
	Provider string         `json:"provider"`
	Model    sql.NullString `json:"model"`
}

func dimagentGlobal(db dimagentReader) (*dimagentSelection, error) {
	var s dimagentSelection
	err := db.QueryRow("SELECT providerId, modelId FROM provider_selections WHERE scope = 'global'").Scan(&s.Provider, &s.Model)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &s, err
}

func dimagentCurrent(path string) string {
	db, err := dimagentDB(path, false)
	if err != nil {
		return ""
	}
	defer db.Close()
	s, err := dimagentGlobal(db)
	if err != nil || s == nil {
		return ""
	}
	if !s.Model.Valid {
		_ = db.QueryRow("SELECT activeModelId FROM providers WHERE providerId = ?", s.Provider).Scan(&s.Model)
	}
	if !s.Model.Valid || s.Model.String == "" {
		return ""
	}
	p := s.Provider
	if p == dimagentProviderID {
		p = magpieID
	}
	return p + "/" + s.Model.String
}

type dimagentModel struct {
	ID           string `json:"modelId"`
	Name         string `json:"displayName"`
	Capabilities struct {
		Reasoning bool `json:"reasoning"`
		Context   int  `json:"contextWindow"`
	} `json:"capabilities"`
	Metadata struct {
		Reasoning struct {
			Efforts []string        `json:"effortOptions"`
			Off     json.RawMessage `json:"off"`
		} `json:"reasoning"`
	} `json:"metadata"`
}

type dimagentProvider struct {
	Name     string
	Enabled  bool
	Models   []dimagentModel
	Metadata map[string]json.RawMessage
}

func dimagentReadProvider(db dimagentReader, id string) (dimagentProvider, error) {
	var p dimagentProvider
	var models, metadata string
	err := db.QueryRow(`SELECT displayName, enabled, models, COALESCE(metadata, '{}')
		FROM providers WHERE providerId = ?`, id).Scan(&p.Name, &p.Enabled, &models, &metadata)
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal([]byte(models), &p.Models); err != nil {
		return p, err
	}
	err = json.Unmarshal([]byte(metadata), &p.Metadata)
	if p.Metadata == nil {
		p.Metadata = map[string]json.RawMessage{}
	}
	return p, err
}

func (p dimagentProvider) allows(id string) bool {
	var enabled, disabled []string
	if b, ok := p.Metadata["enabledModelIds"]; ok && json.Unmarshal(b, &enabled) == nil && enabled != nil && !slices.Contains(enabled, id) {
		return false
	}
	_ = json.Unmarshal(p.Metadata["disabledModelIds"], &disabled)
	return !slices.Contains(disabled, id)
}

func dimagentOwnModels(path, current string) []Option {
	db, err := dimagentDB(path, false)
	if err != nil {
		return nil
	}
	defer db.Close()
	rows, err := db.Query("SELECT providerId FROM providers WHERE enabled = 1 AND providerId != ? ORDER BY displayName, providerId", dimagentProviderID)
	if err != nil {
		return nil
	}
	// Close the rows before reading models on this single-connection handle.
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	var out []Option
	for _, id := range ids {
		p, err := dimagentReadProvider(db, id)
		if err != nil {
			continue
		}
		for _, m := range p.Models {
			if !p.allows(m.ID) {
				continue
			}
			out = append(out, Option{Value: id + "/" + m.ID, Label: m.Name, Icon: modelIcon("", m.ID),
				Group: p.Name, Note: p.Name + " · DimAgent", Context: m.Capabilities.Context})
		}
	}
	if current != "" && !strings.HasPrefix(current, magpieID+"/") && !slices.ContainsFunc(out, func(o Option) bool { return o.Value == current }) {
		out = append(out, Option{Value: current, Note: "current value"})
	}
	return out
}

func dimagentSplitModel(value string) (string, string, error) {
	p, m, ok := strings.Cut(value, "/")
	if !ok || p == "" || m == "" {
		return "", "", fmt.Errorf("DimAgent model must be provider/model")
	}
	if p == magpieID {
		p = dimagentProviderID
	}
	return p, m, nil
}

func dimagentPutGlobal(tx *sql.Tx, s dimagentSelection) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.Exec(`UPDATE provider_selections SET providerId = ?, modelId = ?, updatedAt = ?,
		version = version + 1 WHERE scope = 'global'`, s.Provider, s.Model, now)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n > 0 {
		return err
	}
	_, err = tx.Exec(`INSERT INTO provider_selections
		(selectionId, scope, cwd, providerId, modelId, createdAt, updatedAt, version)
		VALUES ('global', 'global', NULL, ?, ?, ?, ?, 1)`, s.Provider, s.Model, now, now)
	return err
}

// A removed/hidden catalog model must not leave an invalid default behind.
// A NULL model lets DimAgent pick its provider's default; sessions are separate.
func dimagentPruneSelections(tx *sql.Tx, enabled []string) error {
	ids, _ := json.Marshal(enabled)
	_, err := tx.Exec(`UPDATE provider_selections SET modelId = NULL, updatedAt = ?, version = version + 1
		WHERE providerId = ? AND modelId IS NOT NULL AND modelId NOT IN (SELECT value FROM json_each(?))`,
		time.Now().UTC().Format(time.RFC3339Nano), dimagentProviderID, string(ids))
	return err
}

func dimagentSaveSelection(tx *sql.Tx) error {
	p, err := dimagentReadProvider(tx, dimagentProviderID)
	if err != nil {
		return err
	}
	prev, err := dimagentGlobal(tx)
	if err != nil {
		return err
	}
	if prev != nil && prev.Provider == dimagentProviderID {
		if _, saved := p.Metadata["magpiePreviousSelection"]; saved {
			return nil
		}
		prev = nil
	}
	p.Metadata["magpiePreviousSelection"], _ = json.Marshal(prev)
	meta, _ := json.Marshal(p.Metadata)
	_, err = tx.Exec("UPDATE providers SET metadata = ? WHERE providerId = ?", string(meta), dimagentProviderID)
	return err
}

func dimagentRestoreSelection(tx *sql.Tx, metadata string) error {
	s, err := dimagentGlobal(tx)
	if err != nil || s == nil || s.Provider != dimagentProviderID {
		return err
	}
	var meta struct {
		Previous *dimagentSelection `json:"magpiePreviousSelection"`
	}
	if err := json.Unmarshal([]byte(metadata), &meta); err != nil {
		return err
	}
	if meta.Previous != nil {
		p, err := dimagentReadProvider(tx, meta.Previous.Provider)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && p.Enabled && (!meta.Previous.Model.Valid || p.allows(meta.Previous.Model.String) && slices.ContainsFunc(p.Models, func(m dimagentModel) bool { return m.ID == meta.Previous.Model.String })) {
			return dimagentPutGlobal(tx, *meta.Previous)
		}
	}
	_, err = tx.Exec("DELETE FROM provider_selections WHERE scope = 'global' AND providerId = ?", dimagentProviderID)
	return err
}

func dimagentSetModel(path, value string) error {
	if value == "" {
		return dimagentWrite(path, false, false)
	}
	id, model, err := dimagentSplitModel(value)
	if err != nil {
		return err
	}
	on := id == dimagentProviderID
	if on {
		found := false
		for _, m := range magpieModels("dimagent") {
			found = found || m.ID == model
		}
		if !found {
			return fmt.Errorf("DimAgent model is not in magpie's catalog: %s", model)
		}
	}
	change := func(tx *sql.Tx) error {
		p, err := dimagentReadProvider(tx, id)
		if err != nil {
			return fmt.Errorf("read DimAgent model provider: %w", err)
		}
		if !slices.ContainsFunc(p.Models, func(m dimagentModel) bool { return m.ID == model }) || !on && (!p.Enabled || !p.allows(model)) {
			return fmt.Errorf("DimAgent model is not enabled: %s", value)
		}
		if on {
			if err := dimagentSaveSelection(tx); err != nil {
				return err
			}
			var disabled, enabled []string
			_ = json.Unmarshal(p.Metadata["disabledModelIds"], &disabled)
			_ = json.Unmarshal(p.Metadata["enabledModelIds"], &enabled)
			disabled = slices.DeleteFunc(disabled, func(v string) bool { return v == model })
			if !slices.Contains(enabled, model) {
				enabled = append(enabled, model)
			}
			// Re-read the metadata that now contains the previous selection.
			p, err = dimagentReadProvider(tx, id)
			if err != nil {
				return err
			}
			p.Metadata["disabledModelIds"], _ = json.Marshal(disabled)
			p.Metadata["enabledModelIds"], _ = json.Marshal(enabled)
		}
		meta, _ := json.Marshal(p.Metadata)
		_, err = tx.Exec(`UPDATE providers SET activeModelId = ?, enabled = 1, metadata = ?, updatedAt = ?,
			version = version + 1 WHERE providerId = ?`, model, string(meta), time.Now().UTC().Format(time.RFC3339Nano), id)
		if err != nil {
			return err
		}
		return dimagentPutGlobal(tx, dimagentSelection{Provider: id, Model: sql.NullString{String: model, Valid: true}})
	}
	if on {
		return dimagentUpdate(path, true, false, change)
	}
	// Choosing a native model changes the global default, not the lifetime
	// of magpie's provider. Open drafts, workspace selections and sessions
	// may still use it; deleting it makes the desktop show "unconfigured".
	db, err := dimagentDB(path, true)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := change(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// DimAgent accepts these levels; each model advertises its own subset.
var dimagentLevels = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

func dimagentModelEfforts(p dimagentProvider, model string) []string {
	for _, m := range p.Models {
		if m.ID != model || !m.Capabilities.Reasoning {
			continue
		}
		var out []string
		for _, level := range dimagentLevels {
			if slices.Contains(m.Metadata.Reasoning.Efforts, level) || level == "none" && len(m.Metadata.Reasoning.Off) > 0 && len(m.Metadata.Reasoning.Efforts) > 0 {
				out = append(out, level)
			}
		}
		return out
	}
	return nil
}

func dimagentEfforts(path, value string) []string {
	id, model, err := dimagentSplitModel(value)
	if err != nil {
		return nil
	}
	if id == dimagentProviderID {
		for _, m := range magpieModels("dimagent") {
			if m.ID == model {
				return slices.DeleteFunc(slices.Clone(dimagentLevels), func(v string) bool { return !slices.Contains(m.Efforts, v) })
			}
		}
	}
	db, err := dimagentDB(path, false)
	if err != nil {
		return nil
	}
	defer db.Close()
	p, err := dimagentReadProvider(db, id)
	if err != nil {
		return nil
	}
	return dimagentModelEfforts(p, model)
}

func dimagentCurrentEffort(path string) string {
	db, err := dimagentDB(path, false)
	if err != nil {
		return ""
	}
	defer db.Close()
	s, err := dimagentGlobal(db)
	if err != nil || s == nil {
		return ""
	}
	p, err := dimagentReadProvider(db, s.Provider)
	if err != nil {
		return ""
	}
	var efforts map[string]string
	_ = json.Unmarshal(p.Metadata["reasoningEffortByModel"], &efforts)
	return efforts[s.Model.String]
}

func dimagentSetEffort(path, value string) error {
	db, err := dimagentDB(path, true)
	if err != nil {
		if value == "" && errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	s, err := dimagentGlobal(tx)
	if err != nil {
		return err
	}
	if s == nil || !s.Model.Valid {
		if value == "" {
			return nil
		}
		return fmt.Errorf("pick DimAgent's model before setting its reasoning effort")
	}
	p, err := dimagentReadProvider(tx, s.Provider)
	if err != nil {
		return err
	}
	if value != "" && (!p.Enabled || !p.allows(s.Model.String) || !slices.Contains(dimagentModelEfforts(p, s.Model.String), value)) {
		return fmt.Errorf("DimAgent model %s does not support reasoning effort %q", s.Model.String, value)
	}
	efforts := map[string]string{}
	if raw, ok := p.Metadata["reasoningEffortByModel"]; ok {
		if err := json.Unmarshal(raw, &efforts); err != nil {
			return err
		}
	}
	if efforts == nil {
		efforts = map[string]string{}
	}
	if value == "" {
		delete(efforts, s.Model.String)
	} else {
		efforts[s.Model.String] = value
	}
	if len(efforts) == 0 {
		delete(p.Metadata, "reasoningEffortByModel")
	} else {
		p.Metadata["reasoningEffortByModel"], _ = json.Marshal(efforts)
	}
	meta, _ := json.Marshal(p.Metadata)
	_, err = tx.Exec("UPDATE providers SET metadata = ?, updatedAt = ?, version = version + 1 WHERE providerId = ?",
		string(meta), time.Now().UTC().Format(time.RFC3339Nano), s.Provider)
	if err != nil {
		return err
	}
	return tx.Commit()
}
