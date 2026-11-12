package agent

// Zoo Code (Zoo-Code-Org/Zoo-Code, the VS Code extension
// ZooCodeOrganization.zoo-code; a fork of Roo Code) keeps its provider
// profiles in VS Code's secret storage, encrypted and out of reach. What it
// does read from a file is the one its zoo-code.autoImportSettingsPath
// setting names: on every start of the extension (autoImportSettings,
// src/utils/autoImportSettings.ts, Zoo Code 3.90.0) it imports that file as
// its own Settings → Import does (importSettingsFromPath,
// src/core/config/importExport.ts):
//
//	{"providerProfiles":{"currentApiConfigName":"magpie",
//	  "apiConfigs":{"magpie":{"id":"magpie","apiProvider":"openai",
//	    "openAiBaseUrl":"http://127.0.0.1:3425/v1","openAiApiKey":"magpie-zoo-code",
//	    "openAiModelId":"<model>","openAiCustomModelInfo":{…}}}}}
//
// Imported profiles are merged over the user's own, which stay, and the
// imported currentApiConfigName becomes the profile Zoo Code uses. magpie
// writes that file in Zoo Code's own folder of VS Code's
// (zooCodeImportPath) and points the setting at it in VS Code's settings.json, the default profile's and
// each other profile's (vscodeProfiles), a value of the user's own kept
// aside and put back by Disconnect. Its OpenAI Compatible provider asks
// the gateway's Chat Completions under Bearer openAiApiKey, its requests
// saying "ZooCode/<version>" as their User-Agent.
//
// What this route can't do: Zoo Code reads the file only when it starts
// (the Notice asks for a window reload), and imports it again on each
// start, so while connected each start puts Zoo Code back on magpie's
// profile. A profile once imported lives in Zoo Code's secret storage,
// which magpie can't write, so Disconnect stops the import and leaves the
// profile "magpie" there for the user to switch from or delete. Which
// profile the user picks inside Zoo Code magpie can't see either.
//
// Zoo Code for JetBrains runs the same extension in its own extension
// host, which is handed an empty configuration on every start
// (RPCManager.startInitialize), so the setting can't reach it there, and
// keeps its secrets in ~/.zoo-code/secrets.json, a store of its own with
// no lock and no documented format. magpie doesn't write either.

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
)

// zooImport is the setting naming the file Zoo Code imports at start-up,
// a key with a dot in it, escaped for edit's key paths.
const zooImport = `zoo-code\.autoImportSettingsPath`

// zooExtension is Zoo Code's extension id, as the folder it is installed
// in begins: zoocodeorganization.zoo-code-<version>.
const zooExtension = "zoocodeorganization.zoo-code"

var noticeZoo = newNotice("Zoo Code reads magpie's profile when it starts — run Developer: Reload Window in VS Code to use this.")

func zooCode(home, cfg string) *Agent {
	// VS Code's own User and extensions folders: a portable build's are
	// both in its data folder
	return zooCodeAt(vscodeDirs(vscodeStable, home, cfg))
}

// zooCodeImportPath is the file magpie has Zoo Code import, in the VS Code
// User folder dir: in Zoo Code's own folder there, beside the settings
// files it keeps itself (globalStorage/<extension>/settings). It is the
// agent's config, not magpie's: one the user points at a magpie on another
// machine keeps that address through a sync (kept.away), which skips
// magpie's own folder.
func zooCodeImportPath(dir string) string {
	return filepath.Join(dir, "globalStorage", zooExtension, "settings", "magpie-import.json")
}

// zooCodeAt is Zoo Code in the VS Code whose User folder is dir and whose
// extensions are in exts.
func zooCodeAt(dir, exts string) *Agent {
	path := filepath.Join(dir, "settings.json")
	file := zooCodeImportPath(dir)
	key := func(p string) string { return "zoo-code:" + p + ":import" }
	getAt := func(p string) string { v, _ := edit.GetJSON(p, zooImport); return v }
	ours := func(p string) bool { return getAt(p) == file }
	// profile is magpie's profile in the file, "" when there's none
	profile := func() string {
		b, err := os.ReadFile(file)
		if err != nil || !gjson.ValidBytes(b) {
			return ""
		}
		return gjson.GetBytes(b, "providerProfiles.apiConfigs."+magpieID).Raw
	}
	joined := func() bool { return ours(path) && profile() != "" }
	model := func() string {
		if !joined() {
			return ""
		}
		if id := gjson.Get(profile(), "openAiModelId").String(); id != "" {
			return magpieID + "/" + id
		}
		return ""
	}
	// restore puts back the setting a settings.json had before magpie's
	restore := func(p string) error {
		if was := unstash(key(p)); was != "" {
			return edit.SetJSON(p, edit.KV{Path: zooImport, Value: was})
		}
		return edit.DelJSON(p, zooImport)
	}
	// each runs fn on the default's settings.json and each profile's, put
	// back as they were if it fails
	each := func(fn func(p string) error) error {
		s, _ := vscodeProfiles(dir)
		all := append([]string{path}, s...)
		return edit.Atomically(func() error {
			for _, p := range all {
				if err := fn(p); err != nil {
					return err
				}
			}
			return nil
		}, append(all, file)...)
	}
	write := func(ref string) error {
		return edit.SetJSON(file, edit.KV{Path: "providerProfiles", Value: zooCodeProfiles(ref)})
	}
	off := func() error {
		err := each(func(p string) error {
			if ours(p) {
				if err := restore(p); err != nil {
					return err
				}
			}
			forget(key(p))
			return nil
		})
		if err != nil {
			return err
		}
		if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return atomic(&Agent{
		ID: "zoo-code", Name: "Zoo Code", Icon: "zoo-code", Spelled: prefixed,
		// its requests say "ZooCode/<version>" (DEFAULT_HEADERS,
		// src/api/providers/constants.ts): usage names them by the word
		// before the slash
		Aliases: []string{"zoocode", "zoo"},
		Dir:     dir, Path: path,
		detect: func() bool { return !Taken(dir) && zooCodeInstalled(exts) },
		Notice: func() string {
			if joined() {
				return noticeZoo.String()
			}
			return ""
		},
		Unwire: off,
		// the file is read again at every start: it follows the catalog and
		// the key, and a profile made since is pointed at it too
		Sync: func() error {
			if !joined() {
				return nil
			}
			ref := gjson.Get(profile(), "openAiModelId").String()
			if cur, ok := edit.GetJSON(file, "providerProfiles"); !ok || !sameJSON(cur, zooCodeProfiles(ref)) {
				if err := write(ref); err != nil {
					return err
				}
			}
			return each(func(p string) error {
				if ours(p) {
					return nil
				}
				stash(map[string]string{key(p): getAt(p)})
				return edit.SetJSON(p, edit.KV{Path: zooImport, Value: file})
			})
		},
		Check: func() string {
			if !joined() {
				return ""
			}
			return wiringOff("Zoo Code", file, func(k string) (string, bool) {
				r := gjson.Get(profile(), k)
				return r.String(), r.Exists()
			}, "openAiBaseUrl", gatewayV1(), "openAiApiKey", agentKey("zoo-code"))
		},
		Fields: []Field{{
			Key: "model", Label: "model", Get: model,
			Set: func(v string) error {
				ref, ok := strings.CutPrefix(v, magpieID+"/")
				if !ok || !isMagpie(ref) {
					// Zoo Code's own profiles are in its secret storage:
					// magpie stops the import and leaves them to it
					return off()
				}
				if err := write(ref); err != nil {
					return err
				}
				return each(func(p string) error {
					if !ours(p) {
						stash(map[string]string{key(p): getAt(p)})
					}
					return edit.SetJSON(p, edit.KV{Path: zooImport, Value: file})
				})
			},
			Options: func(map[string]string) []Option {
				return viaMagpie("zoo-code", magpieID+"/")
			},
		}},
	}, path, file, stashPath())
}

// zooCodeInstalled reports whether Zoo Code is in an extensions folder.
func zooCodeInstalled(exts string) bool {
	xs, err := os.ReadDir(exts)
	if err != nil {
		return false
	}
	for _, x := range xs {
		if x.IsDir() && strings.HasPrefix(strings.ToLower(x.Name()), zooExtension+"-") {
			return true
		}
	}
	return false
}

// zooCodeProfiles is magpie as Zoo Code's provider profiles to import: one
// profile, "magpie", on the gateway's Chat Completions with model ref, the
// one Zoo Code uses after the import.
func zooCodeProfiles(ref string) map[string]any {
	p := map[string]any{
		"id": magpieID, "apiProvider": "openai",
		"openAiBaseUrl": gatewayV1(), "openAiApiKey": agentKey("zoo-code"),
		"openAiModelId": ref, "openAiStreamingEnabled": true,
	}
	for _, m := range sharedModels("zoo-code") {
		if m.ID == ref {
			p["openAiCustomModelInfo"] = zooCodeModelInfo(m)
			break
		}
	}
	return map[string]any{
		"currentApiConfigName": magpieID,
		"apiConfigs":           map[string]any{magpieID: p},
	}
}

// zooCodeModelInfo is a model of magpie's as Zoo Code's modelInfo: the
// window it trims a conversation to, what a reply may take, and whether it
// sends images. contextWindow and supportsPromptCache are the two its
// schema requires.
func zooCodeModelInfo(m catalog.Model) map[string]any {
	info := map[string]any{"supportsPromptCache": false, "supportsImages": m.Images}
	// Zoo Code's own default for a model it isn't told about
	// (openAiModelInfoSaneDefaults)
	info["contextWindow"] = unknownWindow(128_000)
	if m.Context > 0 {
		info["contextWindow"] = m.Context
	}
	if m.Output > 0 {
		info["maxTokens"] = maxTokens(m)
	}
	return info
}
