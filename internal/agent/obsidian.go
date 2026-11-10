package agent

// Obsidian (obsidian.md) has no AI of its own: its community plugins bring
// it. magpie wires the one that takes an OpenAI-compatible endpoint the
// user names, Copilot for Obsidian (logancyang/obsidian-copilot, plugin id
// "copilot"), in every vault that has it (shirotokiren on X).
//
// Obsidian lists its vaults in obsidian.json in its config folder:
// ~/Library/Application Support/obsidian on a Mac, %APPDATA%\obsidian on
// Windows, $XDG_CONFIG_HOME/obsidian on Linux, or the Flatpak's and the
// Snap's own config folders. The file is {"vaults": {"<id>": {"path":
// "<vault folder>", "ts": …, "open": true}}}. A plugin keeps its settings
// in <vault>/.obsidian/plugins/<id>/data.json, which Obsidian reads when
// the plugin loads and writes whole from what the plugin holds whenever it
// saves.
//
// Copilot 4 (settingsVersion 4 and on; 4.0.19's src/settings/model.ts and
// src/modelManagement/types/persisted.ts) keeps its endpoints there as
//
//	"providers": {"<id>": {"providerId", "providerType": "openai-compatible",
//	  "displayName", "baseUrl", "enableCors", "apiKeyKeychainId",
//	  "requiresApiKey", "origin": {"kind": "byok"}, "addedAt"}},
//	"configuredModels": [{"configuredModelId", "providerId",
//	  "info": {"id", "displayName", "limits", "modalities", "reasoning", …},
//	  "configuredAt"}],
//	"backends": {"chat": {"enabledModels": ["<configuredModelId>", …]}},
//	"defaultModelKey": "<configuredModelId>"
//
// A key lives in Obsidian's keychain (apiKeyKeychainId), which magpie
// doesn't write, so magpie's provider has none: requiresApiKey false is
// Copilot's keyless custom endpoint (optionalCustomProviderAuthMigration),
// whose requests carry no Authorization, and the gateway takes a call
// with no key from this computer. Copilot calls it from Obsidian's window
// with its fetch, so the reply streams; the gateway answers that window's
// CORS (appOrigin). The address names Obsidian, /agent/obsidian, since the
// window's User-Agent is a browser's.
//
// magpie's provider is "magpie", its models' ids are made from theirs, so
// a sync finds them again: a model the user turned off in Copilot stays
// off, a new one comes on, one gone goes with its place in every backend.
// The first time, Copilot's default model becomes magpie's first, and
// what it was is kept in the stash to put back when magpie is turned off.
// Everything else in the file stays as written.
//
// Copilot 3 kept models in activeModels, which Copilot 4 moves to
// providers when it first loads; magpie writes only Copilot 4's, so a
// vault with Copilot 3 is left alone and named in the error.
//
// Smart Connections (brianpetro) is not wired: its free plugin embeds
// with a local model only (obsidian-smart-env's embedding_models.js), and
// its API providers are in the closed Pro plugin, keys in the keychain.

import (
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

// obsidianApp is Obsidian's macOS bundle.
var obsidianApp = desktopApp{id: "md.obsidian", names: []string{"Obsidian.app"}}

// obsidianProc is Obsidian's process: the .app's Obsidian, the .exe's, the
// AppImage's and the packages' obsidian.
var obsidianProc = []string{`(^|/)(Obsidian|obsidian)( |$)`}

// obsCopilotPlugin is Copilot for Obsidian's plugin id (its manifest.json).
const obsCopilotPlugin = "copilot"

// obsCopilotMagpie is magpie's provider id in Copilot's providers.
const obsCopilotMagpie = magpieID

// obsCopilotModelSpace makes magpie's configured model ids in Copilot, one
// per model id, the same on every sync (uuid v5 of "magpie/<id>").
var obsCopilotModelSpace = uuid.NewSHA1(uuid.NameSpaceURL, []byte("https://usemagpie.ai/obsidian/copilot"))

var noticeObsidian = newNotice("Obsidian holds Copilot's settings while it runs — reload Copilot in Settings › Community plugins (or restart Obsidian) to see magpie's models, before changing Copilot's settings there.")

func obsidian(home, cfg string) *Agent {
	dirs := obsidianDirs(home, cfg)
	dir := dirs[0]
	for _, d := range dirs {
		if isFile(filepath.Join(d, "obsidian.json")) {
			dir = d
			break
		}
	}
	vaults := obsidianVaults(dirs)
	var paths []string
	for _, v := range vaults {
		paths = append(paths, obsCopilotData(v))
	}
	wired := func() bool {
		return slices.ContainsFunc(vaults, func(v string) bool { return obsCopilotWired(obsCopilotData(v)) })
	}
	write := func(on bool) error {
		var errs []error
		n := 0
		for _, v := range vaults {
			if !obsCopilotHere(v) {
				continue
			}
			p := obsCopilotData(v)
			var err error
			if on {
				err = obsCopilotWire(p, gateway.URL())
			} else {
				err = obsCopilotUnwire(p)
			}
			if err != nil {
				errs = append(errs, err)
				continue
			}
			n++
		}
		if on && n == 0 && len(errs) == 0 {
			return errors.New("no Obsidian vault here has Copilot for Obsidian: install Copilot from Obsidian's Settings › Community plugins first")
		}
		return errors.Join(errs...)
	}
	return atomic(&Agent{
		ID: "obsidian", Name: "Obsidian", Icon: "obsidian", Aliases: []string{"obsidian-copilot"},
		Dir: dir, Path: filepath.Join(dir, "obsidian.json"),
		detect: func() bool {
			return slices.ContainsFunc(dirs, isDir) || macBundle(obsidianApp)
		},
		Notice: func() string {
			if wired() && Running(obsidianProc...) {
				return noticeObsidian.String()
			}
			return ""
		},
		// a sync brings magpie's models up to date in each vault it is in,
		// and wires no vault the user hasn't asked for
		Sync: func() error {
			var errs []error
			for _, v := range vaults {
				if p := obsCopilotData(v); obsCopilotWired(p) {
					errs = append(errs, obsCopilotWire(p, gateway.URL()))
				}
			}
			return errors.Join(errs...)
		},
		ListsModels: true,
		Fields: []Field{{
			Key: "provider", Label: "provider",
			Get: func() string {
				if wired() {
					return magpieID
				}
				return ""
			},
			Set: func(v string) error { return write(v != "") },
			Options: func(map[string]string) []Option {
				return []Option{{Value: magpieID, Label: "magpie", Icon: "magpie", Note: "every magpie model in Copilot for Obsidian, in each vault that has it"}}
			},
		}},
	}, paths...)
}

// obsidianDirs are the folders Obsidian may keep obsidian.json in here,
// the one it uses on this system first.
func obsidianDirs(home, cfg string) []string {
	return obsidianDirsOn(runtime.GOOS, home, cfg, appdir.Getenv("APPDATA"))
}

// obsidianDirsOn is obsidianDirs on the system goos, with the
// XDG_CONFIG_HOME folder cfg and %APPDATA% appData.
func obsidianDirsOn(goos, home, cfg, appData string) []string {
	switch goos {
	case "darwin":
		return []string{filepath.Join(home, "Library", "Application Support", "obsidian")}
	case "windows":
		d := appData
		if d == "" {
			d = filepath.Join(home, "AppData", "Roaming")
		}
		return []string{filepath.Join(d, "obsidian")}
	}
	return []string{
		filepath.Join(cfg, "obsidian"),
		filepath.Join(home, ".var", "app", "md.obsidian.Obsidian", "config", "obsidian"),
		filepath.Join(home, "snap", "obsidian", "current", ".config", "obsidian"),
	}
}

// obsidianVaults are the vault folders obsidian.json in dirs lists that
// are still there, each once, in the order of their ids.
func obsidianVaults(dirs []string) []string {
	var out []string
	for _, d := range dirs {
		b, err := edit.Read(filepath.Join(d, "obsidian.json"))
		if err != nil || len(b) == 0 {
			continue
		}
		var doc struct {
			Vaults map[string]struct {
				Path string `json:"path"`
			} `json:"vaults"`
		}
		if json.Unmarshal(b, &doc) != nil {
			continue
		}
		ids := make([]string, 0, len(doc.Vaults))
		for id := range doc.Vaults {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			p := doc.Vaults[id].Path
			if p == "" || !isDir(p) || slices.Contains(out, filepath.Clean(p)) {
				continue
			}
			out = append(out, filepath.Clean(p))
		}
	}
	return out
}

// obsCopilotDir is Copilot's plugin folder in vault.
func obsCopilotDir(vault string) string {
	return filepath.Join(vault, ".obsidian", "plugins", obsCopilotPlugin)
}

// obsCopilotData is Copilot's settings file in vault.
func obsCopilotData(vault string) string { return filepath.Join(obsCopilotDir(vault), "data.json") }

// obsCopilotHere says vault has Copilot installed.
func obsCopilotHere(vault string) bool {
	return isFile(filepath.Join(obsCopilotDir(vault), "manifest.json"))
}

// obsCopilotWired says the settings file at path has magpie's provider.
func obsCopilotWired(path string) bool {
	b, err := edit.Read(path)
	return err == nil && obsCopilotOurs(gjson.GetBytes(b, "providers."+obsCopilotMagpie))
}

// obsCopilotOurs says a provider named magpie in Copilot's settings is the
// one magpie wrote: its baseUrl is magpie's Obsidian route, on whatever
// port. One the user added by hand under that name is theirs, and magpie
// neither counts it as connected nor takes it out.
func obsCopilotOurs(p gjson.Result) bool {
	return p.IsObject() && strings.HasSuffix(strings.TrimRight(p.Get("baseUrl").String(), "/"), "/agent/obsidian/v1")
}

// obsCopilotRead is the settings file at path as a map, with the version of
// the Copilot installed beside it; a file that isn't there or holds
// nothing is an empty one. A file Copilot wouldn't read, or one Copilot 3
// writes, is an error, and magpie leaves it alone.
func obsCopilotRead(path string) (map[string]json.RawMessage, []byte, error) {
	dir := filepath.Dir(path)
	if v := obsCopilotMajor(filepath.Join(dir, "manifest.json")); v > 0 && v < 4 {
		return nil, nil, fmt.Errorf("%s is Copilot %d: magpie sets Copilot for Obsidian 4 and later — update it in Obsidian's Settings › Community plugins", dir, v)
	}
	b, err := edit.Read(path)
	if err != nil {
		return nil, nil, err
	}
	doc := map[string]json.RawMessage{}
	if strings.TrimSpace(string(b)) == "" {
		return doc, b, nil
	}
	if err := json.Unmarshal(b, &doc); err != nil || doc == nil {
		return nil, nil, fmt.Errorf("%s isn't JSON Copilot reads, so magpie leaves it alone", path)
	}
	return doc, b, nil
}

// obsCopilotMajor is the major version manifest.json at path says, 0 when it
// says none.
func obsCopilotMajor(path string) int {
	b, err := edit.Read(path)
	if err != nil {
		return 0
	}
	v := gjson.GetBytes(b, "version").String()
	major, _, _ := strings.Cut(v, ".")
	n, _ := strconv.Atoi(major)
	return n
}

// obsCopilotProvider is magpie's entry of providers, for a gateway at gw.
type obsCopilotProvider struct {
	ProviderID       string          `json:"providerId"`
	ProviderType     string          `json:"providerType"`
	DisplayName      string          `json:"displayName"`
	BaseURL          string          `json:"baseUrl"`
	EnableCors       bool            `json:"enableCors"`
	APIKeyKeychainID *string         `json:"apiKeyKeychainId"`
	RequiresAPIKey   bool            `json:"requiresApiKey"`
	Origin           json.RawMessage `json:"origin"`
	AddedAt          int64           `json:"addedAt"`
}

// obsCopilotModelInfo is a configured model's info (ModelInfo, catalog.ts).
type obsCopilotModelInfo struct {
	ID               string     `json:"id"`
	DisplayName      string     `json:"displayName"`
	Modalities       *obsMods   `json:"modalities,omitempty"`
	Limits           *obsLimits `json:"limits,omitempty"`
	Reasoning        bool       `json:"reasoning,omitempty"`
	ReasoningEfforts []string   `json:"reasoningEfforts,omitempty"`
}

type obsMods struct {
	Input  []string `json:"input,omitempty"`
	Output []string `json:"output,omitempty"`
}

type obsLimits struct {
	Context int `json:"context,omitempty"`
	Output  int `json:"output,omitempty"`
}

// obsCopilotModel is an entry of configuredModels.
type obsCopilotModel struct {
	ConfiguredModelID string              `json:"configuredModelId"`
	ProviderID        string              `json:"providerId"`
	Info              obsCopilotModelInfo `json:"info"`
	ConfiguredAt      int64               `json:"configuredAt"`
}

// obsCopilotModelID is magpie's configured model id in Copilot for the model
// id.
func obsCopilotModelID(id string) string {
	return uuid.NewHash(sha1.New(), obsCopilotModelSpace, []byte("magpie/"+id), 5).String()
}

// obsCopilotModels are magpie's models as Copilot's configured models.
func obsCopilotModels(now int64) []obsCopilotModel {
	var out []obsCopilotModel
	seen := map[string]bool{}
	for _, m := range sharedModels("obsidian") {
		if m.ID == "" || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		info := obsCopilotModelInfo{ID: m.ID, DisplayName: m.Name}
		if info.DisplayName == "" {
			info.DisplayName = m.ID
		}
		in := []string{"text"}
		if m.Images {
			in = append(in, "image")
		}
		info.Modalities = &obsMods{Input: in, Output: []string{"text"}}
		if m.Context > 0 || m.Output > 0 {
			info.Limits = &obsLimits{Context: m.Context, Output: m.Output}
		}
		if len(m.Efforts) > 0 {
			info.Reasoning = true
			info.ReasoningEfforts = slices.Clone(m.Efforts)
		}
		out = append(out, obsCopilotModel{ConfiguredModelID: obsCopilotModelID(m.ID), ProviderID: obsCopilotMagpie, Info: info, ConfiguredAt: now})
	}
	return out
}

// stashKey is where the default model Copilot had in the vault of the
// settings file at path is kept while magpie's is.
func obsCopilotStashKey(path string) string { return "obsidian.copilot.default:" + path }

// obsCopilotWire puts magpie's provider and models into the settings file at
// path for a gateway at gw, and writes it only when that changes it.
func obsCopilotWire(path, gw string) error {
	doc, raw, err := obsCopilotRead(path)
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()

	providers := map[string]json.RawMessage{}
	if raw, ok := doc["providers"]; ok && json.Unmarshal(raw, &providers) != nil {
		return fmt.Errorf("%s: providers is not an object", path)
	}
	first := providers[obsCopilotMagpie] == nil
	if !first && !obsCopilotOurs(gjson.ParseBytes(providers[obsCopilotMagpie])) {
		return fmt.Errorf("%s: Copilot has a provider of its own named magpie, at another address — rename it in Copilot's settings, and magpie adds its own", path)
	}
	want := obsCopilotProvider{
		ProviderID: obsCopilotMagpie, ProviderType: "openai-compatible", DisplayName: "magpie",
		BaseURL: gateway.AgentURL(gw, "obsidian") + "/v1", RequiresAPIKey: false,
		Origin: json.RawMessage(`{"kind":"byok"}`), AddedAt: now,
	}
	if !first {
		old := gjson.ParseBytes(providers[obsCopilotMagpie])
		if n := old.Get("displayName").String(); n != "" {
			want.DisplayName = n
		}
		if a := old.Get("addedAt"); a.Type == gjson.Number {
			want.AddedAt = a.Int()
		}
		if k := old.Get("apiKeyKeychainId"); k.Type == gjson.String {
			s := k.String()
			want.APIKeyKeychainID = &s
		}
	}

	// the user's models as written, magpie's made again
	var list []json.RawMessage
	if raw, ok := doc["configuredModels"]; ok && json.Unmarshal(raw, &list) != nil {
		return fmt.Errorf("%s: configuredModels is not a list", path)
	}
	had := map[string]gjson.Result{}
	var kept []json.RawMessage
	for _, e := range list {
		if gjson.GetBytes(e, "providerId").String() == obsCopilotMagpie {
			had[gjson.GetBytes(e, "configuredModelId").String()] = gjson.ParseBytes(e)
			continue
		}
		kept = append(kept, e)
	}
	ms := obsCopilotModels(now)
	ours := map[string]bool{}
	for i, m := range ms {
		ours[m.ConfiguredModelID] = true
		if h, ok := had[m.ConfiguredModelID]; ok {
			if a := h.Get("configuredAt"); a.Type == gjson.Number {
				ms[i].ConfiguredAt = a.Int()
			}
		}
		b, _ := json.Marshal(ms[i])
		kept = append(kept, b)
	}
	if len(ms) == 0 {
		return errors.New("magpie has no models to give Copilot for Obsidian: add a provider in magpie first")
	}

	// each backend's enabled models: the user's as they were, magpie's that
	// were on still on, magpie's gone out, and in chat magpie's new ones on
	sets := []edit.KV{
		{Path: "providers." + obsCopilotMagpie, Value: want},
		{Path: "configuredModels", Value: kept},
	}
	backends := map[string]json.RawMessage{}
	if b, ok := doc["backends"]; ok && json.Unmarshal(b, &backends) != nil {
		return fmt.Errorf("%s: backends is not an object", path)
	}
	for name, b := range backends {
		ids, ok, err := obsCopilotEnabled(b)
		if err != nil {
			return fmt.Errorf("%s: backends.%s: %w", path, name, err)
		}
		if !ok && name != "chat" {
			continue
		}
		var next []string
		for _, id := range ids {
			if _, mine := had[id]; mine && !ours[id] {
				continue
			}
			next = append(next, id)
		}
		if name == "chat" {
			for _, m := range ms {
				if _, before := had[m.ConfiguredModelID]; !before && !slices.Contains(next, m.ConfiguredModelID) {
					next = append(next, m.ConfiguredModelID)
				}
			}
		}
		if next == nil {
			next = []string{}
		}
		sets = append(sets, edit.KV{Path: "backends." + obsJSONKey(name) + ".enabledModels", Value: next})
	}
	if _, ok := backends["chat"]; !ok {
		var ids []string
		for _, m := range ms {
			ids = append(ids, m.ConfiguredModelID)
		}
		sets = append(sets, edit.KV{Path: "backends.chat.enabledModels", Value: ids})
	}

	def := gjson.GetBytes(doc["defaultModelKey"], "@this").String()
	switch {
	case first:
		stash(map[string]string{obsCopilotStashKey(path): def})
		sets = append(sets, edit.KV{Path: "defaultModelKey", Value: ms[0].ConfiguredModelID})
	case def != "" && !ours[def]:
		if _, mine := had[def]; mine {
			// the default was a magpie model that is gone
			sets = append(sets, edit.KV{Path: "defaultModelKey", Value: ms[0].ConfiguredModelID})
		}
	}
	return obsCopilotPatch(path, raw, sets, nil)
}

// obsCopilotEnabled is a backend's enabledModels, and whether it has them.
func obsCopilotEnabled(raw json.RawMessage) ([]string, bool, error) {
	e := gjson.GetBytes(raw, "enabledModels")
	if !e.Exists() {
		return nil, false, nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(e.Raw), &ids); err != nil {
		return nil, false, errors.New("enabledModels is not a list of ids")
	}
	return ids, true, nil
}

// obsCopilotPatch sets set and deletes del in the settings file at path, read
// before as raw, and writes it, through a temp file renamed over it, only
// when that changes something: each key the user wrote stays as written.
func obsCopilotPatch(path string, raw []byte, set []edit.KV, del []string) error {
	var change []edit.KV
	for _, kv := range set {
		cur := gjson.GetBytes(raw, kv.Path)
		if cur.Exists() && sameJSON(cur.Raw, kv.Value) {
			continue
		}
		change = append(change, kv)
	}
	var gone []string
	for _, k := range del {
		if gjson.GetBytes(raw, k).Exists() {
			gone = append(gone, k)
		}
	}
	if len(change) == 0 && len(gone) == 0 {
		return nil
	}
	out, err := edit.PatchJSON(raw, change, gone)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return edit.WriteAtomic(path, out)
}

// obsCopilotUnwire takes magpie's provider, its models and their places out
// of the settings file at path, and puts back the default model Copilot
// had before when magpie's is still it.
func obsCopilotUnwire(path string) error {
	if !obsCopilotWired(path) {
		return nil
	}
	doc, raw, err := obsCopilotRead(path)
	if err != nil {
		return err
	}
	var list, kept []json.RawMessage
	json.Unmarshal(doc["configuredModels"], &list)
	mine := map[string]bool{}
	for _, e := range list {
		if gjson.GetBytes(e, "providerId").String() == obsCopilotMagpie {
			mine[gjson.GetBytes(e, "configuredModelId").String()] = true
			continue
		}
		kept = append(kept, e)
	}
	if kept == nil {
		kept = []json.RawMessage{}
	}
	sets := []edit.KV{{Path: "configuredModels", Value: kept}}
	backends := map[string]json.RawMessage{}
	json.Unmarshal(doc["backends"], &backends)
	for name, b := range backends {
		ids, ok, err := obsCopilotEnabled(b)
		if err != nil || !ok {
			continue
		}
		next := slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return mine[id] })
		if next == nil {
			next = []string{}
		}
		sets = append(sets, edit.KV{Path: "backends." + obsJSONKey(name) + ".enabledModels", Value: next})
	}
	before := unstash(obsCopilotStashKey(path))
	if def := gjson.GetBytes(doc["defaultModelKey"], "@this").String(); mine[def] {
		sets = append(sets, edit.KV{Path: "defaultModelKey", Value: before})
	}
	return obsCopilotPatch(path, raw, sets, []string{"providers." + obsCopilotMagpie})
}

// obsJSONKey is name as one key of a JSON key path.
func obsJSONKey(name string) string { return strings.ReplaceAll(name, ".", `\.`) }
