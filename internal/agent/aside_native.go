package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/provider"
)

var asideRead = func() (map[string]json.RawMessage, error) {
	if testing.Testing() {
		return nil, fmt.Errorf("Aside runtime is isolated in tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), asideTimeout)
	defer cancel()
	out, err := proc.CommandContext(ctx, "aside", "repl", "--account", asideAccountID(), "--host", "local",
		"console.log('MAGPIE_ASIDE_STATE '+JSON.stringify({defaultModel:aside.settings.get('defaultModel'),modelCategories:aside.settings.get('modelCategories'),imageGenerationModel:aside.settings.get('imageGenerationModel')}))").Output()
	if err != nil {
		return nil, fmt.Errorf("Aside settings could not be read: %w", err)
	}
	_, tail, ok := bytes.Cut(out, []byte("MAGPIE_ASIDE_STATE "))
	if !ok {
		return nil, fmt.Errorf("Aside returned no settings snapshot")
	}
	line, _, _ := bytes.Cut(tail, []byte("\n"))
	var values map[string]json.RawMessage
	err = json.Unmarshal(line, &values)
	return values, err
}

var asideMu sync.Mutex

type asideOwned struct {
	Before  json.RawMessage `json:"before"`
	Last    json.RawMessage `json:"last,omitempty"`
	Pending json.RawMessage `json:"pending,omitempty"`
}

type asideRecord struct {
	Fields map[string]asideOwned `json:"fields"`
}

type asideConnection struct {
	at                   place
	path, models, record string
	snapshot             map[string]json.RawMessage
	readErr              error
	read                 bool
}

func newAsideConnection(at place) *asideConnection {
	dir := asideDir(at)
	return &asideConnection{at: at, path: filepath.Join(dir, "settings.json"), models: filepath.Join(dir, "models.json"), record: filepath.Join(filepath.Dir(stashPath()), "aside-state.json")}
}

func (c *asideConnection) settings() map[string]json.RawMessage {
	if !c.read {
		c.read = true
		b, err := edit.Read(c.path)
		c.readErr = err
		c.snapshot = map[string]json.RawMessage{}
		if err == nil && len(b) > 0 {
			c.readErr = json.Unmarshal(b, &c.snapshot)
		}
	}
	return c.snapshot
}

func (c *asideConnection) liveSettings() map[string]json.RawMessage {
	snapshot, err := asideRead()
	return c.reconcileSettings(snapshot, err)
}

func (c *asideConnection) reconcileSettings(snapshot map[string]json.RawMessage, readErr error) map[string]json.RawMessage {
	c.snapshot, c.readErr = snapshot, readErr
	c.read = c.readErr == nil
	if c.snapshot == nil {
		c.snapshot = map[string]json.RawMessage{}
	}
	if c.readErr == nil {
		r, err := c.load()
		if err != nil {
			c.readErr = err
			return c.snapshot
		}
		changed := false
		for key, field := range r.Fields {
			if len(field.Pending) == 0 || !asideSameSetting(asideRaw(c.snapshot, asideFieldPath(key)), field.Pending) {
				continue
			}
			if strings.HasPrefix(asideSelection(field.Pending), "magpie/") {
				field.Last = field.Pending
				field.Pending = nil
				r.Fields[key] = field
			} else {
				delete(r.Fields, key)
			}
			changed = true
		}
		if changed {
			c.readErr = c.save(r)
		}
	}
	return c.snapshot
}

func jsString(s string) string { b, _ := json.Marshal(s); return string(b) }

func asideOwnOptions(path, cur string) []Option {
	b, _ := edit.Read(path)
	var s struct {
		Providers map[string]struct{ Models []struct{ ID, Name string } }
	}
	_ = json.Unmarshal(b, &s)
	var out []Option
	names := make([]string, 0, len(s.Providers))
	for p := range s.Providers {
		names = append(names, p)
	}
	sort.Strings(names)
	for _, p := range names {
		v := s.Providers[p]
		if p == "magpie" {
			continue
		}
		for _, m := range v.Models {
			out = append(out, Option{Value: p + "/" + m.ID, Label: m.Name, Group: p})
		}
	}
	if cur != "" && !strings.HasPrefix(cur, "magpie/") {
		found := false
		for _, o := range out {
			if o.Value == cur {
				found = true
			}
		}
		if !found {
			out = append(out, Option{Value: cur})
		}
	}
	return out
}

func asideSameSetting(a, b json.RawMessage) bool {
	if len(a) == 0 {
		a = json.RawMessage("null")
	}
	if len(b) == 0 {
		b = json.RawMessage("null")
	}
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	aa, _ := json.Marshal(x)
	bb, _ := json.Marshal(y)
	return bytes.Equal(aa, bb)
}

func asideSelection(raw json.RawMessage) string {
	var m struct {
		Provider string
		ModelID  string `json:"modelId"`
	}
	_ = json.Unmarshal(raw, &m)
	if m.Provider == "" || m.ModelID == "" {
		return ""
	}
	return m.Provider + "/" + m.ModelID
}

func asideFieldPath(key string) string {
	switch key {
	case "model", "effort":
		return "defaultModel"
	case "image":
		return asideImageKey
	}
	return "modelCategories." + key
}

func asideRaw(s map[string]json.RawMessage, key string) json.RawMessage {
	if strings.HasPrefix(key, "modelCategories.") {
		var roles map[string]json.RawMessage
		_ = json.Unmarshal(s["modelCategories"], &roles)
		return roles[strings.TrimPrefix(key, "modelCategories.")]
	}
	return s[key]
}

func (c *asideConnection) value(key string) string {
	raw := asideRaw(c.settings(), asideFieldPath(key))
	if key == "effort" {
		var m struct {
			ThinkingLevel string `json:"thinkingLevel"`
		}
		_ = json.Unmarshal(raw, &m)
		return m.ThinkingLevel
	}
	return asideSelection(raw)
}

func (c *asideConnection) load() (asideRecord, error) {
	r := asideRecord{Fields: map[string]asideOwned{}}
	b, err := edit.Read(c.record)
	if err != nil {
		return r, err
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &r); err != nil {
			return r, err
		}
	}
	if r.Fields == nil {
		r.Fields = map[string]asideOwned{}
	}
	if len(b) > 0 {
		return r, nil
	}
	// Legacy stash entries exist only for selections explicitly replaced by
	// magpie. Reading them does not adopt fields with no restore point.
	legacy := stashLoad()
	for _, key := range append([]string{"model", "image"}, asideRoles...) {
		if _, ok := r.Fields[key]; ok {
			continue
		}
		name := "aside.was"
		if key != "model" {
			name += "." + key
		}
		value, ok := legacy[c.at.key(name)]
		p, m, valid := strings.Cut(value, "/")
		if !ok || !valid || p == "" || m == "" || p == "magpie" {
			continue
		}
		raw, _ := edit.GetJSON(c.path, asideFieldPath(key))
		var selection map[string]any
		_ = json.Unmarshal([]byte(raw), &selection)
		if selection == nil {
			selection = map[string]any{}
		}
		selection["provider"], selection["modelId"] = p, m
		before, _ := json.Marshal(selection)
		r.Fields[key] = asideOwned{Before: before}
	}
	return r, nil
}

func (c *asideConnection) save(r asideRecord) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.record), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(c.record); os.IsNotExist(err) {
		if err := os.WriteFile(c.record, []byte("{}"), 0o600); err != nil {
			return err
		}
	}
	return edit.WriteAtomic(c.record, append(b, '\n'))
}

func (c *asideConnection) provider() (string, string) {
	b, err := edit.Read(c.models)
	if err != nil {
		return "foreign", "Aside's provider configuration could not be read"
	}
	if len(bytes.TrimSpace(b)) != 0 && !json.Valid(b) {
		return "foreign", "Aside's provider configuration could not be read"
	}
	raw, ok := edit.GetJSON(c.models, "providers.magpie")
	if !ok {
		return "disconnected", ""
	}
	var p struct {
		Key    string          `json:"apiKey"`
		URL    string          `json:"baseUrl"`
		API    string          `json:"api"`
		Models json.RawMessage `json:"models"`
	}
	if json.Unmarshal([]byte(raw), &p) != nil {
		return "invalid", "Aside's provider configuration could not be read"
	}
	if p.Key == "" || p.URL == "" {
		return "foreign", "Aside's magpie provider has no ownership credential; it will not be overwritten"
	}
	if p.Key != gateway.TokenFor("aside") {
		return "foreign", "Aside's magpie provider belongs to another configuration; it will not be overwritten"
	}
	if !sameGateway(p.URL, c.at.v1()) {
		return "invalid", "Aside's magpie provider points to another gateway; reconnect explicitly to use this one"
	}
	if p.API == "" || len(p.Models) == 0 {
		return "invalid", "Aside's magpie provider has an incomplete model catalog; reconnect the provider"
	}
	return "connected", ""
}

func (c *asideConnection) block() any {
	p := magpieProviderJSONAt("pi", "aside", c.at.gw()).(map[string]any)
	p["apiKey"] = gateway.TokenFor("aside")
	return p
}

func (c *asideConnection) connect() error {
	asideMu.Lock()
	defer asideMu.Unlock()
	return c.connectLocked()
}

func (c *asideConnection) connectLocked() error {
	status, detail := c.provider()
	if status == "foreign" {
		return fmt.Errorf("%s", detail)
	}
	if status == "connected" {
		return syncJSON(c.models, "providers.magpie", c.kept())
	}
	return edit.SetJSON(c.models, edit.KV{Path: "providers.magpie", Value: c.kept()()})
}

func (c *asideConnection) sync() error {
	asideMu.Lock()
	defer asideMu.Unlock()
	status, _ := c.provider()
	if status != "connected" {
		return nil
	}
	return syncJSON(c.models, "providers.magpie", c.kept())
}

// kept is the block with what else Aside's providers.magpie holds kept
// (theirsKept).
func (c *asideConnection) kept() func() any {
	return theirsKept(c.models, "providers.magpie", c.block, "models")
}

func (c *asideConnection) state() NativeState {
	c.settings()
	status, detail := c.provider()
	s := NativeState{Provider: status, Detail: detail, Runtime: "applied", Fields: map[string]NativeField{}}
	if c.readErr != nil {
		s.Runtime = "unavailable"
	}
	r, err := c.load()
	if err != nil {
		s.Detail = err.Error()
	}
	for _, key := range append([]string{"model", "image"}, asideRoles...) {
		value := c.value(key)
		f := NativeField{Value: value, Status: "unmanaged"}
		if strings.HasPrefix(value, "magpie/") {
			f.Status = "managed"
			if owned, ok := r.Fields[key]; !ok || len(owned.Before) == 0 {
				f.Status = "missingRestorePoint"
				f.Detail = "No verified restore point; select a native model before disconnecting"
			} else if len(owned.Last) == 0 {
				f.Status = "restorePointOnly"
			}
			if key == "image" {
				f.Status = "unverified"
				f.Detail = "Image generation is configured, but Aside's image provider support has not been verified"
				if !asideImageOffered(value) {
					f.Status = "unavailable"
					f.Detail = "This image model is not in the gateway's image generation catalog"
				}
			}
		} else if _, ok := r.Fields[key]; ok {
			f.Status = "userChanged"
		}
		if strings.HasPrefix(value, "magpie/") && status == "disconnected" {
			f.Status = "unavailable"
			f.Detail = "The magpie provider is missing; reconnect the provider without changing this selection"
		}
		if owned, ok := r.Fields[key]; ok && len(owned.Pending) > 0 {
			f.Status = "pendingRestart"
			f.Detail = "Saved for Aside's next start; the running model has not been changed"
		}
		if c.readErr != nil && f.Status != "pendingRestart" {
			f.Detail = "Aside's runtime could not be read; showing saved settings"
		}
		s.Fields[key] = f
	}
	return s
}

func asideImageOptions() []Option {
	var out []Option
	for _, p := range provider.All() {
		if !p.On() {
			continue
		}
		for _, m := range gateway.Drawers(p) {
			ref := p.ID + "/" + m.ID
			out = append(out, Option{Value: "magpie/" + ref, Ref: ref, Label: m.Name, Group: p.Name, Icon: p.Icon, Note: "image generation · via magpie"})
		}
	}
	return out
}

func asideImageOffered(value string) bool {
	for _, o := range asideImageOptions() {
		if o.Value == value {
			return true
		}
	}
	return false
}

func (c *asideConnection) validateSelection(key, value string) (string, error) {
	a := asideIn(c.at)
	if a.Field(key) == nil {
		return "", fmt.Errorf("unknown Aside field %q", key)
	}
	if key == "effort" || value == "" {
		return value, nil
	}
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "magpie/") {
		if _, err := a.Spell(key, value); err != nil {
			return "", err
		}
	}
	p, m, ok := strings.Cut(value, "/")
	if !ok || p == "" || m == "" {
		return "", fmt.Errorf("expected provider/model")
	}
	if key == "image" && p == "magpie" && !asideImageOffered(value) {
		return "", fmt.Errorf("%s is not an image generation model", value)
	}
	return value, nil
}

func (c *asideConnection) apply(key, value string) error {
	asideMu.Lock()
	defer asideMu.Unlock()
	value, err := c.validateSelection(key, value)
	if err != nil {
		return err
	}
	if _, err := c.load(); err != nil {
		return err
	}
	c.read = false
	snapshot, err := asideRead()
	if err != nil {
		action := OfflineAction("")
		if key != "effort" && value != "" {
			action = OfflineStage
		}
		return &RuntimeUnavailableError{Agent: "aside", Operation: "apply", Offline: action, Cause: err}
	}
	s := c.reconcileSettings(snapshot, nil)
	if c.readErr != nil {
		return c.readErr
	}
	r, err := c.load()
	if err != nil {
		return err
	}
	before := asideRaw(s, asideFieldPath(key))
	if len(before) == 0 {
		before = json.RawMessage("null")
	}
	if _, ok := r.Fields[key]; ok && !strings.HasPrefix(asideSelection(before), "magpie/") {
		delete(r.Fields, key)
	}
	if strings.HasPrefix(value, "magpie/") {
		if err := c.connectLocked(); err != nil {
			return err
		}
		if _, ok := r.Fields[key]; !ok && !strings.HasPrefix(asideSelection(before), "magpie/") {
			r.Fields[key] = asideOwned{Before: append(json.RawMessage(nil), before...)}
			if err := c.save(r); err != nil {
				return err
			}
		}
	}
	var after any
	if key == "effort" {
		var m map[string]any
		_ = json.Unmarshal(before, &m)
		if m == nil {
			m = map[string]any{}
		}
		if value == "" {
			delete(m, "thinkingLevel")
		} else {
			m["thinkingLevel"] = value
		}
		after = m
	} else if value == "" {
		if owned, ok := r.Fields[key]; ok {
			_ = json.Unmarshal(owned.Before, &after)
		} else if key == "model" {
			return fmt.Errorf("select a native default model; no restore point is available")
		}
	} else {
		p, m, ok := strings.Cut(value, "/")
		if !ok || p == "" || m == "" {
			return fmt.Errorf("expected provider/model")
		}
		sel := map[string]any{}
		if key != "image" {
			_ = json.Unmarshal(before, &sel)
			if sel == nil {
				sel = map[string]any{}
			}
			if _, ok := sel["thinkingLevel"]; !ok {
				sel["thinkingLevel"] = "medium"
			}
		}
		sel["provider"], sel["modelId"] = p, m
		after = sel
	}
	raw, _ := json.Marshal(after)
	if err := c.setSetting(asideFieldPath(key), raw); err != nil {
		c.read = false
		return err
	}
	c.liveSettings()
	if c.readErr != nil {
		return fmt.Errorf("Aside changed but readback failed: %w", c.readErr)
	}
	got := asideRaw(c.snapshot, asideFieldPath(key))
	for pendingKey, field := range r.Fields {
		if len(field.Pending) > 0 && asideSameSetting(asideRaw(c.snapshot, asideFieldPath(pendingKey)), field.Pending) {
			field.Pending = nil
			r.Fields[pendingKey] = field
		}
	}
	if key == "effort" {
		if c.value(key) != value {
			return fmt.Errorf("Aside did not apply the thinking level")
		}
	} else if asideSelection(got) != asideSelection(raw) {
		return fmt.Errorf("Aside did not apply %s; its current selection is %s", key, c.value(key))
	}
	if key == "effort" {
		if owned, ok := r.Fields["model"]; ok && strings.HasPrefix(asideSelection(got), "magpie/") {
			owned.Last = append(json.RawMessage(nil), got...)
			r.Fields["model"] = owned
		}
	}
	if owned, ok := r.Fields[key]; ok {
		if strings.HasPrefix(value, "magpie/") {
			owned.Last = append(json.RawMessage(nil), got...)
			owned.Pending = nil
			r.Fields[key] = owned
		} else {
			delete(r.Fields, key)
		}
	}
	return c.save(r)
}

func (c *asideConnection) setSetting(key string, raw json.RawMessage) error {
	expr := ""
	if role, ok := strings.CutPrefix(key, "modelCategories."); ok {
		expr = "const c=aside.settings.get('modelCategories')||{};"
		if string(raw) == "null" {
			expr += "delete c[" + jsString(role) + "];"
		} else {
			expr += "c[" + jsString(role) + "]=" + string(raw) + ";"
		}
		expr += "aside.settings.set('modelCategories',c);"
	} else {
		expr = "aside.settings.set(" + jsString(key) + "," + string(raw) + ");"
	}
	return asideSet(asideAccountID(), expr+"console.log('"+asideOK+"')")
}

func (c *asideConnection) plan() (*DisconnectPlan, error) {
	asideMu.Lock()
	defer asideMu.Unlock()
	return c.planLocked()
}

func (c *asideConnection) planLocked() (*DisconnectPlan, error) {
	filememo.Forget()
	status, detail := c.provider()
	if status == "foreign" {
		return nil, fmt.Errorf("%s", detail)
	}
	b, err := edit.Read(c.path)
	if err != nil {
		return nil, err
	}
	var saved map[string]json.RawMessage
	if len(b) > 0 {
		if err := json.Unmarshal(b, &saved); err != nil {
			return nil, err
		}
	}
	r, err := c.load()
	if err != nil {
		return nil, err
	}
	plan := &DisconnectPlan{}
	var set []edit.KV
	var del []string
	for _, key := range append([]string{"model", "image"}, asideRoles...) {
		path := asideFieldPath(key)
		now := asideRaw(saved, path)
		if !strings.HasPrefix(asideSelection(now), "magpie/") {
			continue
		}
		owned, ok := r.Fields[key]
		if !ok || len(owned.Before) == 0 {
			return nil, fmt.Errorf("%s has no verified restore point; choose a native model before disconnecting", key)
		}
		plan.Settings = append(plan.Settings, PlannedSetting{Key: path, Before: now, After: owned.Before})
		if string(owned.Before) == "null" && key != "image" {
			del = append(del, path)
		} else {
			set = append(set, edit.KV{Path: path, Value: owned.Before})
		}
	}
	after, err := edit.PatchJSON(b, set, del)
	if err != nil {
		return nil, err
	}
	plan.Files = append(plan.Files, PlannedFile{Path: c.path, Before: b, After: after})
	models, err := edit.Read(c.models)
	if err != nil {
		return nil, err
	}
	without, err := edit.PatchJSON(models, nil, []string{"providers.magpie"})
	if err != nil {
		return nil, err
	}
	record, err := edit.Read(c.record)
	if err != nil {
		return nil, err
	}
	empty, _ := json.MarshalIndent(asideRecord{Fields: map[string]asideOwned{}}, "", "  ")
	plan.Files = append(plan.Files, PlannedFile{Path: c.record, Before: record, After: append(empty, '\n')})
	// Legacy restore points come from the stash only while no record exists.
	if len(record) == 0 {
		legacy, err := edit.Read(stashPath())
		if err != nil {
			return nil, err
		}
		plan.Files = append(plan.Files, PlannedFile{Path: stashPath(), Before: legacy, After: legacy})
	}
	plan.Files = append(plan.Files, PlannedFile{Path: c.models, Before: models, After: without})
	return plan, nil
}

func (c *asideConnection) execute(plan *DisconnectPlan) error {
	asideMu.Lock()
	defer asideMu.Unlock()
	if err := plan.CheckFiles(); err != nil {
		return err
	}
	if len(plan.Settings) > 0 {
		snapshot, err := asideRead()
		if err != nil {
			return &RuntimeUnavailableError{Agent: "aside", Operation: "disconnect", Offline: OfflineDisconnect, Cause: err}
		}
		s := c.reconcileSettings(snapshot, nil)
		if c.readErr != nil {
			return c.readErr
		}
		for _, change := range plan.Settings {
			if !asideSameSetting(asideRaw(s, change.Key), change.Before) {
				return fmt.Errorf("Aside changed %s since the preview; try again", change.Key)
			}
		}
		for _, change := range plan.Settings {
			if err := c.setSetting(change.Key, change.After); err != nil {
				return fmt.Errorf("disconnect partially completed at %s; provider retained: %w", change.Key, err)
			}
			c.liveSettings()
			if c.readErr != nil || !asideSameSetting(asideRaw(c.snapshot, change.Key), change.After) {
				return fmt.Errorf("Aside did not confirm restoration of %s; provider retained", change.Key)
			}
		}
	}
	for _, file := range plan.Files {
		if file.Path == c.models {
			current, err := edit.Read(file.Path)
			if err != nil {
				return err
			}
			if !bytes.Equal(current, file.Before) {
				return fmt.Errorf("Aside's providers changed during restoration; provider retained")
			}
			if err := edit.WriteAtomic(file.Path, file.After); err != nil {
				return err
			}
		}
	}
	if err := c.save(asideRecord{Fields: map[string]asideOwned{}}); err != nil {
		return err
	}
	return nil
}

func (c *asideConnection) executeOffline(plan *DisconnectPlan) error {
	asideMu.Lock()
	defer asideMu.Unlock()
	if plan == nil {
		return fmt.Errorf("missing Aside disconnect plan")
	}
	// Rebuilding under the lock also verifies ownership and the plan structure.
	current, err := c.planLocked()
	if err != nil {
		return err
	}
	if plan.Revision("aside") != current.Revision("aside") {
		return fmt.Errorf("Aside disconnect plan changed; preview it again")
	}
	if err := plan.CheckFiles(); err != nil {
		return err
	}
	return edit.Atomically(func() error {
		if err := edit.WriteAtomic(c.path, plan.Files[0].After); err != nil {
			return err
		}
		if err := c.save(asideRecord{Fields: map[string]asideOwned{}}); err != nil {
			return err
		}
		if err := edit.WriteAtomic(c.models, plan.Files[len(plan.Files)-1].After); err != nil {
			return err
		}
		c.read = false
		return nil
	}, c.path, c.models, c.record)
}
