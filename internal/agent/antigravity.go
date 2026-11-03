package agent

// Antigravity, Google's desktop app (2.15 and later), sends its requests to
// a gateway of the user's instead of Google when ~/.antigravity/settings.json
// turns one on: Google's Antigravity Enterprise Gateway, documented at
// docs.cloud.google.com/gemini/enterprise/docs/ai-developer-tools/connect-enterprise-gateway
// (ryougi_hachi on X):
//
//	{"gateway":{"enabled":true,"url":…,"apiKey":…,"wireProtocol":"openai"},
//	 "models":{"defaultModelId":…,"list":[{"modelId":…}]}}
//
// On the openai wire protocol the app asks <url>/v1/models for its model
// picker (models.list, when there is one, narrows it) and sends a chat to
// <url>/v1/chat/completions, streamed, with apiKey as a bearer token and a
// User-Agent of antigravity/hub/<version> (auth_method=gateway). magpie's
// gateway answers both, so what magpie writes is that block at its own
// address, with a key that names the app, and the model the app starts on.
// The user's own gateway, default model and model list are stashed and put
// back when magpie steps out. On a gateway the app asks for no Google
// sign-in and uses none: Google's docs say the two can't be used at once.
//
// The same file is read by agy (Antigravity's CLI) too, below its own
// ~/.gemini/antigravity-cli/settings.json: an agy connected on its own row
// (modelProvider gemini) keeps that route, one that isn't follows the app's
// gateway. admin_settings.json, which an organisation installs, and the
// AGY_LLM_GATEWAY_* variables win over this file, field by field.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/provider"
)

const (
	antigravityModel = "models.defaultModelId"
	antigravityList  = "models.list"
)

// antigravityAdmin is where an organisation's admin_settings.json is kept
// on this system (Google's docs), whose gateway outranks the user's; under
// a test's home in a test binary (proc.SystemDirs).
func antigravityAdmin() string {
	p := "/etc/antigravity/admin_settings.json"
	switch runtime.GOOS {
	case "darwin":
		p = "/Library/Application Support/Antigravity/admin_settings.json"
	case "windows":
		d := os.Getenv("ProgramData")
		if d == "" {
			return ""
		}
		p = filepath.Join(d, "Antigravity", "admin_settings.json")
	}
	return proc.SystemDirs(p)[0]
}

// antigravityAdminPath is antigravityAdmin, swapped by a test.
var antigravityAdminPath = antigravityAdmin

func antigravity(home string) *Agent {
	dir := filepath.Join(home, ".antigravity")
	path := filepath.Join(dir, "settings.json")
	model := jsonGet(path, antigravityModel)
	stashGateway, stashModel, stashList := "antigravity:"+path+":gateway", "antigravity:"+path+":defaultModelId", "antigravity:"+path+":list"
	// wired: the file's gateway is on and carries a key magpie gave it
	wired := func() bool {
		on, _ := edit.GetJSON(path, "gateway.enabled")
		key, _ := edit.GetJSON(path, "gateway.apiKey")
		return on == "true" && gatewayKey(key)
	}
	gatewayBlock := func() map[string]any {
		return map[string]any{"enabled": true, "url": gateway.URL(), "apiKey": agentKey("antigravity"), "wireProtocol": "openai"}
	}
	// putRaw writes back what was stashed as it was, or takes key out
	putRaw := func(key, raw string) error {
		if raw == "" {
			return edit.DelJSON(path, key)
		}
		return edit.SetJSON(path, edit.KV{Path: key, Value: json.RawMessage(raw)})
	}
	return &Agent{
		ID: "antigravity", Name: "Antigravity", Icon: "antigravity-color", Aliases: []string{"antigravity-app", "agy-app"},
		Spelled: prefixed,
		// ~/.antigravity is also where Antigravity IDE (a VS Code fork) and
		// agy keep theirs, so it says nothing of the app: the app is its
		// bundle (2.15 or later, the first to take a gateway) or the file
		// only its own backend writes
		Dir: dir, Path: path, dirShared: true,
		sign: func() bool {
			if v := (&Agent{ID: "antigravity"}).AppVersion(); v != "" && !Newer("2.15.0", v) {
				return true
			}
			return isFile(filepath.Join(home, ".gemini", "antigravity", "agyhub_summaries_proto.pb"))
		},
		// its picker lists the gateway's /v1/models as asked with its key,
		// by id
		ListsModels: true, NoNames: true,
		Notice: func() string {
			if wired() {
				return noticeAntigravity.String()
			}
			return ""
		},
		Sync: func() error {
			if !wired() {
				return nil
			}
			return syncJSON(path, "gateway", theirsKept(path, "gateway", func() any { return gatewayBlock() }))
		},
		Check: func() string {
			if !wired() {
				return ""
			}
			if p := antigravityAdminPath(); p != "" {
				if u, ok := edit.GetJSON(p, "gateway.url"); ok && u != "" {
					return noticeAntigravityAdmin.say("path", p)
				}
			}
			if u := os.Getenv("AGY_LLM_GATEWAY_URL"); u != "" && u != gateway.URL() {
				return noticeAntigravityEnv.say("url", u)
			}
			return wiringOff("Antigravity", path, func(k string) (string, bool) { return edit.GetJSON(path, "gateway."+k) },
				"url", gateway.URL(), "apiKey", agentKey("antigravity"))
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: func() string {
				if v := model(); wired() && v != "" {
					return magpieID + "/" + v
				}
				return model()
			},
			Set: func(v string) error {
				on := wired()
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					if !on {
						g, _ := edit.GetJSONRaw(path, "gateway")
						m, _ := edit.GetJSONRaw(path, antigravityModel)
						l, _ := edit.GetJSONRaw(path, antigravityList)
						stash(map[string]string{stashGateway: g, stashModel: m, stashList: l})
					}
					// a list of the user's would hide magpie's models from
					// the picker; it is stashed with their gateway
					if err := edit.DelJSON(path, antigravityList); err != nil {
						return err
					}
					// set again on magpie, what the user added to its block stays
					block := any(gatewayBlock())
					if on {
						block = theirsKept(path, "gateway", func() any { return gatewayBlock() })()
					}
					return edit.SetJSON(path, edit.KV{Path: "gateway", Value: block}, edit.KV{Path: antigravityModel, Value: ref})
				}
				if on {
					// back to what the user had
					if err := putRaw("gateway", unstash(stashGateway)); err != nil {
						return err
					}
					if err := putRaw(antigravityList, unstash(stashList)); err != nil {
						return err
					}
					if v == "" {
						if err := putRaw(antigravityModel, unstash(stashModel)); err != nil {
							return err
						}
						return antigravityTidy(path)
					}
					forget(stashModel)
				}
				if v == "" {
					if err := edit.DelJSON(path, antigravityModel); err != nil {
						return err
					}
				} else if err := edit.SetJSON(path, edit.KV{Path: antigravityModel, Value: v}); err != nil {
					return err
				}
				return antigravityTidy(path)
			},
			Options: func(cur map[string]string) []Option {
				// the user's own model is Google's, on their sign-in, or their
				// own gateway's
				var own []Option
				if c := cur["model"]; c != "" && !prefixed(c) {
					own = []Option{{Value: c, Icon: modelIcon("", c), Direct: "Google"}}
				}
				return withVia(group("Antigravity", own), "antigravity", magpieID+"/")
			},
		}, {
			// how hard its models reason: the app's picker lists the
			// gateway's models by id, with no control of its own for a
			// level, so Antigravity's own families went at the level
			// the app asked or their default (MDX-Tom, #4838). The
			// gateway asks its requests for the level picked here
			// (provider.AgentEffort), and with none they go as it asks.
			Key: "effort", Label: "effort",
			Get: func() string { return provider.AgentEffort("antigravity") },
			Set: func(v string) error { return provider.SetAgentEffort("antigravity", v) },
			Options: func(map[string]string) []Option {
				if !wired() {
					return nil
				}
				return pickedEfforts("antigravity")
			},
		}},
	}
}

// antigravityTidy takes out a models object magpie left empty.
func antigravityTidy(path string) error {
	if v, ok := edit.GetJSON(path, "models"); ok && strings.TrimSpace(v) == "{}" {
		return edit.DelJSON(path, "models")
	}
	return nil
}

// what antigravity says after a change (notice.go)
var (
	noticeAntigravity      = newNotice("Antigravity reads its gateway at start-up — restart Antigravity to use magpie's models. On magpie it doesn't use your Google sign-in; turning it off here gives that back. An agy not connected on its own row follows it too.")
	noticeAntigravityAdmin = newNotice("Antigravity's {path} sets a gateway of its own, which wins over ~/.antigravity/settings.json, so it doesn't reach magpie")
	noticeAntigravityEnv   = newNotice("AGY_LLM_GATEWAY_URL is set to {url}, which wins over ~/.antigravity/settings.json, so Antigravity started with it doesn't reach magpie")
)
