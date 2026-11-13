package agent

// Antigravity, Google's desktop app (2.15 and later), sends its requests to
// a gateway of the user's instead of Google when its settings file turns one
// on (~/.antigravity/settings.json up to 2.29, ~/.gemini/config/config.json
// from 2.30; antigravityFiles): Google's Antigravity Enterprise Gateway, documented at
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
// The models its own sign-in gave it (Code Assist's fetchAvailableModels)
// leave its picker with it: with no account under magpie's Antigravity
// subscription, noticeAntigravityOwn says how to have them back, through
// the gateway on that account's quota (T_t__biu on X).
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

// antigravityFiles are the files Antigravity's app takes its gateway from,
// newest first: ~/.gemini/config/config.json from 2.30 on, where the app
// also keeps its userSettings and plugins, and ~/.antigravity/settings.json
// up to 2.29. Each version reads its own alone (2.30.0 and 2.29.0's
// language servers, headless: a gateway in the other file goes unread and
// the app asks for a Google sign-in), and an update moves nothing across,
// so a file magpie wired for 2.29 left 2.30 off magpie (YujunT46308 on
// X, Windows). magpie writes the same block into both.
func antigravityFiles(home string) []string {
	return []string{
		filepath.Join(home, ".gemini", "config", "config.json"),
		filepath.Join(home, ".antigravity", "settings.json"),
	}
}

// agyFile is the wiring of one of antigravityFiles.
type agyFile struct {
	path                                          string
	stashGateway, stashModel, stashList, stashSrc string
}

func newAgyFile(path string) agyFile {
	k := "antigravity:" + path + ":"
	return agyFile{path: path, stashGateway: k + "gateway", stashModel: k + "defaultModelId", stashList: k + "list", stashSrc: k + "listSource"}
}

func (f agyFile) model() string { return jsonGet(f.path, antigravityModel)() }

// wired: the file's gateway is on and carries a key magpie gave it
func (f agyFile) wired() bool {
	on, _ := edit.GetJSON(f.path, "gateway.enabled")
	key, _ := edit.GetJSON(f.path, "gateway.apiKey")
	return on == "true" && gatewayKey(key)
}

func antigravityGateway() map[string]any {
	return map[string]any{"enabled": true, "url": gateway.URL(), "apiKey": agentKey("antigravity"), "wireProtocol": "openai"}
}

// keepSource stashes a catalog of the user's that a file wired before
// magpie wrote one holds, to come back with their gateway
func (f agyFile) keepSource() {
	v, _ := edit.GetJSON(f.path, antigravitySource)
	if v != "" && !strings.Contains(v, "format="+gateway.AntigravityFormat) && unstashPeek(f.stashSrc) == "" {
		raw, _ := edit.GetJSONRaw(f.path, antigravitySource)
		stash(map[string]string{f.stashSrc: raw})
	}
}

// startOn writes the model the app starts on, cur, at the level its
// picker offers for want (the effort picked, "" for the row's), when it
// is magpie's and the file is on magpie
func (f agyFile) startOn(cur, want string) error {
	if cur == "" || !f.wired() {
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
		return edit.SetJSON(f.path, edit.KV{Path: antigravityModel, Value: to})
	}
	return nil
}

// putRaw writes back what was stashed as it was, or takes key out
func (f agyFile) putRaw(key, raw string) error {
	if raw == "" {
		return edit.DelJSON(f.path, key)
	}
	return edit.SetJSON(f.path, edit.KV{Path: key, Value: json.RawMessage(raw)})
}

// connect puts the file on magpie's gateway, starting on ref (one of
// magpie's models, "" to leave the model as it is), the user's gateway,
// model, list and catalog stashed the first time
func (f agyFile) connect(ref string) error {
	on := f.wired()
	if !on {
		g, _ := edit.GetJSONRaw(f.path, "gateway")
		m, _ := edit.GetJSONRaw(f.path, antigravityModel)
		l, _ := edit.GetJSONRaw(f.path, antigravityList)
		c, _ := edit.GetJSONRaw(f.path, antigravitySource)
		stash(map[string]string{f.stashGateway: g, f.stashModel: m, f.stashList: l, f.stashSrc: c})
	}
	// a list of the user's would hide magpie's models from the picker,
	// and it is read ahead of the catalog that gives each its window; it
	// is stashed with their gateway
	if err := edit.DelJSON(f.path, antigravityList); err != nil {
		return err
	}
	// set again on magpie, what the user added to its block stays
	block := any(antigravityGateway())
	if on {
		f.keepSource()
		block = theirsKept(f.path, "gateway", func() any { return antigravityGateway() })()
	}
	kv := []edit.KV{{Path: "gateway", Value: block}, {Path: antigravitySource, Value: antigravityCatalog()}}
	if ref != "" {
		kv = append(kv, edit.KV{Path: antigravityModel, Value: gateway.AntigravityModel(ref, provider.AgentEffort("antigravity"))})
	} else if !on {
		// a model of the user's own isn't one of the gateway's
		if err := edit.DelJSON(f.path, antigravityModel); err != nil {
			return err
		}
	}
	return edit.SetJSON(f.path, kv...)
}

// leave takes the file off magpie onto v, a model of the user's own, or
// what they had (""); a file not there is made for v only when always
func (f agyFile) leave(v string, always bool) error {
	if f.wired() {
		// back to what the user had
		if err := f.putRaw("gateway", unstash(f.stashGateway)); err != nil {
			return err
		}
		if err := f.putRaw(antigravityList, unstash(f.stashList)); err != nil {
			return err
		}
		if err := f.putRaw(antigravitySource, unstash(f.stashSrc)); err != nil {
			return err
		}
		if v == "" {
			if err := f.putRaw(antigravityModel, unstash(f.stashModel)); err != nil {
				return err
			}
			return antigravityTidy(f.path)
		}
		forget(f.stashModel)
	} else if !always && !isFile(f.path) {
		// nothing of the user's to change, and no file to make for it
		return nil
	}
	if v == "" {
		if err := edit.DelJSON(f.path, antigravityModel); err != nil {
			return err
		}
	} else if err := edit.SetJSON(f.path, edit.KV{Path: antigravityModel, Value: v}); err != nil {
		return err
	}
	return antigravityTidy(f.path)
}

// sync brings a wired file's gateway and catalog to the gateway's current
// address, and the model it starts on to a level the catalog lists
func (f agyFile) sync() error {
	if err := syncJSON(f.path, "gateway", theirsKept(f.path, "gateway", func() any { return antigravityGateway() })); err != nil {
		return err
	}
	// the catalog at the gateway's address, and on a file wired before
	// magpie wrote one, so its windows reach it too
	if v, _ := edit.GetJSON(f.path, antigravitySource); v != antigravityCatalog() {
		f.keepSource()
		if err := edit.SetJSON(f.path, edit.KV{Path: antigravitySource, Value: antigravityCatalog()}); err != nil {
			return err
		}
	}
	// the model it starts on as its catalog lists it now: a model with
	// levels is listed once a level, and an id the catalog doesn't list
	// starts the app on its first model instead
	return f.startOn(f.model(), "")
}

func antigravity(home string) *Agent {
	paths := antigravityFiles(home)
	files := make([]agyFile, len(paths))
	for i, p := range paths {
		files[i] = newAgyFile(p)
	}
	legacy := files[len(files)-1]
	dir, path := filepath.Dir(legacy.path), legacy.path
	// on is the first file on magpie, the one the model is read from
	on := func() (agyFile, bool) {
		for _, f := range files {
			if f.wired() {
				return f, true
			}
		}
		return agyFile{}, false
	}
	wired := func() bool { _, ok := on(); return ok }
	// model is the model the app starts on: the wired file's, else the
	// first one's that names one
	model := func() string {
		if f, ok := on(); ok {
			return f.model()
		}
		for _, f := range files {
			if v := f.model(); v != "" {
				return v
			}
		}
		return ""
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
			if !wired() {
				return ""
			}
			if len(provider.Logins("antigravity")) == 0 {
				return noticeAntigravity.String() + " " + noticeAntigravityOwn.String()
			}
			return noticeAntigravity.String()
		},
		Sync: func() error {
			w, ok := on()
			if !ok {
				return nil
			}
			cur := w.model()
			for _, f := range files {
				if f.wired() {
					if err := f.sync(); err != nil {
						return err
					}
					continue
				}
				// a file wired by a magpie before it knew this one, or
				// for the app's other version: the same block in it, at
				// the level the wired file starts on
				ref := ""
				if b := antigravityBase(cur); isMagpie(b) {
					ref = b
				}
				if err := f.connect(ref); err != nil {
					return err
				}
				if ref != "" {
					if err := edit.SetJSON(f.path, edit.KV{Path: antigravityModel, Value: cur}); err != nil {
						return err
					}
				}
				if err := f.startOn(f.model(), ""); err != nil {
					return err
				}
			}
			return nil
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
			for _, f := range files {
				if s := wiringOff("Antigravity", f.path, func(k string) (string, bool) { return edit.GetJSON(f.path, "gateway."+k) },
					"url", gateway.URL(), "apiKey", agentKey("antigravity")); s != "" {
					return s
				}
			}
			return ""
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
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					for _, f := range files {
						if err := f.connect(ref); err != nil {
							return err
						}
					}
					return nil
				}
				for _, f := range files {
					if err := f.leave(v, f.path == legacy.path); err != nil {
						return err
					}
				}
				return nil
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
				for _, f := range files {
					if err := f.startOn(f.model(), v); err != nil {
						return err
					}
				}
				return nil
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
	noticeAntigravityOwn   = newNotice("The models your own Google sign-in gives Antigravity leave its list too; sign that Google account in under magpie's Antigravity subscription and they come back, on that account's quota.")
	noticeAntigravityAdmin = newNotice("Antigravity's {path} sets a gateway of its own, which wins over ~/.antigravity/settings.json, so it doesn't reach magpie")
	noticeAntigravityEnv   = newNotice("AGY_LLM_GATEWAY_URL is set to {url}, which wins over ~/.antigravity/settings.json, so Antigravity started with it doesn't reach magpie")
)
