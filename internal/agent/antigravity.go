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
// That list's models are read by id alone, so the app (2.29.0) gives each
// one its built-in catalog doesn't know a window of 200K tokens, whatever
// the model's (YujunT46308 on X). Its models.listSource, a catalog it asks
// ahead of the list, with the gateway's key, takes one per model
// (maxTokens), so magpie points it at the gateway's
// /v1/models?format=antigravity. The user's own gateway, default model,
// model list and catalog are stashed and put back when magpie steps out.
//
// On a gateway the app asks for no Google sign-in and uses none (Google's
// docs: the two can't be used at once), and its backend asks Google for
// nothing, so what Google's servers turn on for a signed-in account, its
// experiments and the commands behind them (/boost, /teamwork, /plugin,
// /generative_ui: YujunT46308), is off while it's on magpie. Nothing in
// the file keeps both; noticeAntigravity says so where it is connected.
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
	antigravityModel  = "models.defaultModelId"
	antigravityList   = "models.list"
	antigravitySource = "models.listSource"
)

// antigravityCatalog is the catalog Antigravity's app is pointed at: the
// gateway's models, each with its window (gateway.antigravityModels).
func antigravityCatalog() string {
	return gateway.URL() + "/v1/models?format=" + gateway.AntigravityFormat
}

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
	stashSource := "antigravity:" + path + ":listSource"
	// wired: the file's gateway is on and carries a key magpie gave it
	wired := func() bool {
		on, _ := edit.GetJSON(path, "gateway.enabled")
		key, _ := edit.GetJSON(path, "gateway.apiKey")
		return on == "true" && gatewayKey(key)
	}
	gatewayBlock := func() map[string]any {
		return map[string]any{"enabled": true, "url": gateway.URL(), "apiKey": agentKey("antigravity"), "wireProtocol": "openai"}
	}
	// keepSource stashes a catalog of the user's that a file wired before
	// magpie wrote one holds, to come back with their gateway
	keepSource := func() {
		v, _ := edit.GetJSON(path, antigravitySource)
		if v != "" && !strings.Contains(v, "format="+gateway.AntigravityFormat) && unstashPeek(stashSource) == "" {
			raw, _ := edit.GetJSONRaw(path, antigravitySource)
			stash(map[string]string{stashSource: raw})
		}
	}
	// startOn writes the model the app starts on, cur, at the level its
	// picker offers for want (the effort picked, "" for the row's), when
	// it is magpie's and the file is on magpie
	startOn := func(cur, want string) error {
		if cur == "" || !wired() {
			return nil
		}
		base, at := antigravityBase(cur), ""
		if want == "" {
			want = provider.AgentEffort("antigravity")
			if _, at = provider.MemberEffort(cur); at != "" {
				want = at // the level it starts on stays, if offered
			}
		}
		if !isMagpie(base) {
			return nil
		}
		if to := gateway.AntigravityModel(base, want); to != cur {
			return edit.SetJSON(path, edit.KV{Path: antigravityModel, Value: to})
		}
		return nil
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
		// its picker lists the gateway's models as asked with its key, by
		// the names its catalog gives them (antigravityCatalog)
		ListsModels: true,
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
			if err := syncJSON(path, "gateway", theirsKept(path, "gateway", func() any { return gatewayBlock() })); err != nil {
				return err
			}
			// the catalog at the gateway's address, and on a file wired
			// before magpie wrote one, so its windows reach it too
			if v, _ := edit.GetJSON(path, antigravitySource); v != antigravityCatalog() {
				keepSource()
				if err := edit.SetJSON(path, edit.KV{Path: antigravitySource, Value: antigravityCatalog()}); err != nil {
					return err
				}
			}
			// the model it starts on as its catalog lists it now: a model
			// with levels is listed once a level, and an id the catalog
			// doesn't list starts the app on its first model instead
			return startOn(model(), "")
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
					// the level its picker starts on is the effort's
					return magpieID + "/" + antigravityBase(v)
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
						c, _ := edit.GetJSONRaw(path, antigravitySource)
						stash(map[string]string{stashGateway: g, stashModel: m, stashList: l, stashSource: c})
					}
					// a list of the user's would hide magpie's models from
					// the picker, and it is read ahead of the catalog that
					// gives each its window; it is stashed with their gateway
					if err := edit.DelJSON(path, antigravityList); err != nil {
						return err
					}
					// set again on magpie, what the user added to its block stays
					block := any(gatewayBlock())
					if on {
						keepSource()
						block = theirsKept(path, "gateway", func() any { return gatewayBlock() })()
					}
					return edit.SetJSON(path, edit.KV{Path: "gateway", Value: block}, edit.KV{Path: antigravityModel, Value: gateway.AntigravityModel(ref, provider.AgentEffort("antigravity"))},
						edit.KV{Path: antigravitySource, Value: antigravityCatalog()})
				}
				if on {
					// back to what the user had
					if err := putRaw("gateway", unstash(stashGateway)); err != nil {
						return err
					}
					if err := putRaw(antigravityList, unstash(stashList)); err != nil {
						return err
					}
					if err := putRaw(antigravitySource, unstash(stashSource)); err != nil {
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
			// how hard its models reason (MDX-Tom, #4838). The app's own
			// picker offers a level for a model the catalog lists at
			// each of its levels (gateway.antigravityModels), and the
			// level picked there goes with the request, ahead of this
			// one. This is the level the app starts on, and what a
			// model it can't offer levels for is asked for
			// (provider.AgentEffort); with none, those go as it asks.
			Key: "effort", Label: "effort",
			Get: func() string { return provider.AgentEffort("antigravity") },
			Set: func(v string) error {
				if err := provider.SetAgentEffort("antigravity", v); err != nil {
					return err
				}
				return startOn(model(), v)
			},
			Options: func(map[string]string) []Option {
				if !wired() {
					return nil
				}
				return pickedEfforts("antigravity")
			},
		}},
	}
}

// antigravityBase is the model the app starts on without the level its
// picker starts it at (gateway.AntigravityModel).
func antigravityBase(id string) string {
	m, _ := provider.MemberEffort(id)
	return m
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
	noticeAntigravity      = newNotice("Antigravity reads its gateway at start-up — restart Antigravity to use magpie's models. On a gateway it uses no Google sign-in, so what Google turns on for your account (its experiments, and commands such as /boost, /teamwork, /plugin and /generative_ui) is gone while it's on magpie; turning it off here gives that back. An agy not connected on its own row follows it too.")
	noticeAntigravityAdmin = newNotice("Antigravity's {path} sets a gateway of its own, which wins over ~/.antigravity/settings.json, so it doesn't reach magpie")
	noticeAntigravityEnv   = newNotice("AGY_LLM_GATEWAY_URL is set to {url}, which wins over ~/.antigravity/settings.json, so Antigravity started with it doesn't reach magpie")
)
