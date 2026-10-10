package agent

// Snow CLI (`snow`, npm snow-ai, MayDay-wpf/snow-cli) keeps its settings in
// ~/.snow ($SNOW_CONFIG_DIR when set). Each API setup is a profile,
// profiles/<name>.json, the whole of its config ({"snowcfg": {baseUrl,
// baseUrlMode, apiKey, requestMethod, advancedModel, basicModel,
// maxContextTokens, maxTokens, …}, …}); active-profile.json
// ({"activeProfile": "<name>"}) names the one in use, "default" when it is
// missing, and config.json is a copy of that one, which Snow writes again
// from the profile at every start (utils/config/configManager.ts
// initializeProfiles, switchProfile).
//
// magpie adds a profile of its own, profiles/magpie.json: a copy of the
// profile the user was on — their other settings kept, a subscription
// sign-in of theirs (snowcfg.oauth) left out — with the gateway's /v1 as
// its base URL, a key naming Snow CLI, Chat Completions as its protocol and
// the model picked as its main (advancedModel) and light (basicModel) model,
// the gateway resolving "<provider>/<model>" itself, and the model's
// window, output and image support from the catalog (snowSynced). It makes
// that profile the active one and copies it to config.json, as Snow's own
// profile switch does; the profile the user was on is stashed and made
// active again when magpie steps out, and magpie.json goes. Snow App
// (MayDay-wpf/snow-app) keeps API setups of its own: its "Sync Snow CLI API
// config" button copies these profiles, magpie's among them, into it.
//
// Snow's requests carry no User-Agent of its own (an x-snow: <version>
// header), so the key is what tells them apart: gateway.TokenFor("snow").

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

// snowProfile is the name of the profile magpie writes.
const snowProfile = magpieID

// snowContext and snowOutput are what Snow is told of a model whose limits
// magpie doesn't know: its own defaults.
const (
	snowContext = 200000
	snowOutput  = 64000
)

func snow(home string) *Agent { return snowIn(here(home)) }

// snowIn is Snow CLI at a place: this machine's home, or a WSL distro's (see
// wsl.go), whose profile names the gateway as the distro reaches it, with
// the key it takes from there.
func snowIn(at place) *Agent {
	dir := filepath.Join(at.home, ".snow")
	if d := strings.TrimSpace(at.getenv("SNOW_CONFIG_DIR")); filepath.IsAbs(d) {
		dir = filepath.Clean(d)
	}
	profiles := filepath.Join(dir, "profiles")
	ours := filepath.Join(profiles, snowProfile+".json")
	activeFile := filepath.Join(dir, "active-profile.json")
	legacyActive := filepath.Join(dir, "active-profile.txt")
	config := filepath.Join(dir, "config.json")
	defaultProfile := filepath.Join(profiles, "default.json")
	profileOf := func(name string) string { return filepath.Join(profiles, name+".json") }
	key := func() string { return agentKeyAt("snow", at.gw()) }
	stashKey := "snow:" + dir
	// keyActive is the profile the user was on, keyNone that there was no
	// active-profile.json to put back, keyHad the bytes of a magpie.json of
	// the user's own that magpie's took the place of
	keyActive, keyNone, keyHad := stashKey+":active", stashKey+":active.none", stashKey+":had"
	// keyWrote is what magpie last wrote of snowSynced (snowProfileFor)
	keyWrote := stashKey + ":wrote"

	// active is the profile Snow starts on, as it reads it
	active := func() string {
		if v, ok := edit.GetJSON(activeFile, "activeProfile"); ok {
			return v
		}
		if b, err := edit.Read(legacyActive); err == nil && strings.TrimSpace(string(b)) != "" {
			return strings.TrimSpace(string(b))
		}
		return "default"
	}
	// onOurs says Snow is on magpie's profile: the active one, and a key
	// magpie gave it, so a profile of the user's own named "magpie" is not
	// taken for it
	onOurs := func() bool {
		if active() != snowProfile {
			return false
		}
		k, ok := edit.GetJSON(ours, "snowcfg.apiKey")
		return ok && snowOurKey(k)
	}
	// model is the active profile's main model: config.json's when the
	// profile file isn't there (a Snow from before profiles)
	model := func(name string) string {
		if v, ok := edit.GetJSON(profileOf(name), "snowcfg.advancedModel"); ok {
			return v
		}
		if _, err := os.Stat(profileOf(name)); errors.Is(err, fs.ErrNotExist) {
			v, _ := edit.GetJSON(config, "snowcfg.advancedModel")
			return v
		}
		return ""
	}
	get := func() string {
		if onOurs() {
			if v := model(snowProfile); v != "" {
				return magpieID + "/" + v
			}
			return ""
		}
		return model(active())
	}

	// mirror copies a profile to config.json, as Snow's profile switch does
	mirror := func(name string) error {
		b, err := edit.Read(profileOf(name))
		if err != nil {
			return err
		}
		if b == nil {
			return nil
		}
		return edit.WriteAtomic(config, b)
	}
	// setActive names the profile Snow starts on
	setActive := func(name string) error {
		return edit.WriteAtomic(activeFile, []byte(`{
  "activeProfile": `+jsonString(name)+`
}`))
	}

	// write makes magpie.json for the model ref, from the profile the user
	// is on (base) where there is one, and points it at the gateway
	write := func(ref, base string) error {
		var raw []byte
		var wrote map[string]any
		mine := false
		if b, err := edit.Read(ours); err == nil && b != nil && snowIsOurs(b) {
			raw, mine = b, true
			if w := stashLoad()[keyWrote]; w != "" {
				json.Unmarshal([]byte(w), &wrote)
			}
		} else if base != "" {
			if b, err := edit.Read(profileOf(base)); err == nil && b != nil {
				raw = b
			} else if b, err := edit.Read(config); err == nil && b != nil {
				raw = b
			}
		}
		out, now, err := snowProfileFor(raw, mine, at.v1(), key(), ref, wrote)
		if err != nil {
			return err
		}
		if err := edit.WriteAtomic(ours, out); err != nil {
			return err
		}
		b, _ := json.Marshal(now)
		stash(map[string]string{keyWrote: string(b)})
		return nil
	}

	// leave puts Snow back on the profile the user had (to, or the stashed
	// one), takes magpie.json away (or puts back the user's own of that
	// name), and copies the profile it is on to config.json
	leave := func(to string) error {
		was, none := unstash(keyActive), stashLoad()[keyNone] != ""
		forget(keyNone, keyWrote)
		// a profile asked for by name is made the active one; else the one
		// the user was on, or none at all when Snow had no active file
		if to == "" && !none {
			to = was
		}
		switch {
		case to != "":
			if err := setActive(to); err != nil {
				return err
			}
		case none:
			if err := os.Remove(activeFile); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		default:
			if err := setActive("default"); err != nil {
				return err
			}
		}
		if had := unstash(keyHad); had != "" {
			if err := edit.WriteAtomic(ours, []byte(had)); err != nil {
				return err
			}
		} else if b, err := edit.Read(ours); err == nil && b != nil && snowIsOurs(b) {
			if err := os.Remove(ours); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		now := active()
		if _, err := os.Stat(profileOf(now)); err == nil {
			return mirror(now)
		}
		// no profile to copy: a config.json still magpie's goes, or Snow
		// would make it the user's default profile at its next start
		if b, err := edit.Read(config); err == nil && b != nil && snowIsOurs(b) {
			if err := os.Remove(config); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		return nil
	}

	return atomic(&Agent{
		ID: "snow", Name: "Snow CLI", Icon: "snow", Aliases: []string{"snow-cli", "snow-ai", "snow-app"}, Spelled: prefixed,
		Bin: "snow", Dir: dir, Path: ours,
		Sync: func() error {
			b, err := edit.Read(ours)
			if err != nil || b == nil || !snowIsOurs(b) {
				return err
			}
			ref := gjson.GetBytes(jsonc.ToJSONInPlace(b), "snowcfg.advancedModel").String()
			if err := write(ref, ""); err != nil {
				return err
			}
			if active() == snowProfile {
				return mirror(snowProfile)
			}
			return nil
		},
		Notice: func() string {
			if Running(`(^|/)snow( |$)`) {
				return noticeSnow.String()
			}
			return ""
		},
		Check: func() string {
			if active() != snowProfile {
				return ""
			}
			b, _ := edit.Read(ours)
			if b == nil {
				return ""
			}
			cfg := gjson.GetBytes(jsonc.ToJSONInPlace(b), "snowcfg")
			if !snowOurKey(cfg.Get("apiKey").String()) {
				return ""
			}
			if m := cfg.Get("requestMethod").String(); m != "" && m != "chat" {
				return "Snow CLI's requestMethod (profiles/" + snowProfile + ".json) is " + m + ", not chat, so magpie's gateway won't take its requests"
			}
			return wiringOff("Snow CLI", ours, func(k string) (string, bool) {
				g := cfg.Get(k)
				return g.String(), g.Exists()
			}, "baseUrl", at.v1(), "apiKey", key())
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: get,
			Set: func(v string) error {
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					base := active()
					if !onOurs() {
						if b, err := edit.Read(ours); err == nil && b != nil && !snowIsOurs(b) {
							stash(map[string]string{keyHad: string(b)})
						}
						if _, err := os.Stat(activeFile); errors.Is(err, fs.ErrNotExist) {
							if _, err := os.Stat(legacyActive); errors.Is(err, fs.ErrNotExist) {
								stash(map[string]string{keyNone: "1"})
							}
						}
						stash(map[string]string{keyActive: base})
						// a Snow from before profiles copies config.json
						// into a new default profile at its next start and
						// makes that the active one: done here, so it is
						// the user's config that is copied, not magpie's
						if _, err := os.Stat(defaultProfile); errors.Is(err, fs.ErrNotExist) {
							if b, err := edit.Read(config); err == nil && b != nil && !snowIsOurs(b) {
								if err := edit.WriteAtomic(defaultProfile, b); err != nil {
									return err
								}
							}
						}
					} else {
						base = ""
					}
					if err := write(ref, base); err != nil {
						return err
					}
					if err := setActive(snowProfile); err != nil {
						return err
					}
					return mirror(snowProfile)
				}
				if !onOurs() {
					// on a profile of the user's own: its model is theirs
					// to change, as Snow's own model picker does
					if v == "" {
						return nil
					}
					name := active()
					if p := snowFindProfile(profiles, v, name); p != "" && p != name {
						if err := setActive(p); err != nil {
							return err
						}
						return mirror(p)
					}
					if _, err := os.Stat(profileOf(name)); err != nil {
						return edit.SetJSON(config, edit.KV{Path: "snowcfg.advancedModel", Value: v})
					}
					if err := edit.SetJSON(profileOf(name), edit.KV{Path: "snowcfg.advancedModel", Value: v}); err != nil {
						return err
					}
					return mirror(name)
				}
				// leaving magpie's profile: for the user's profile with
				// that model, or the one they were on
				to := ""
				if v != "" {
					was := stashLoad()[keyActive]
					to = snowFindProfile(profiles, v, was)
					if to == "" {
						to = was
						if to == "" {
							to = "default"
						}
						if _, err := os.Stat(profileOf(to)); err == nil {
							if err := edit.SetJSON(profileOf(to), edit.KV{Path: "snowcfg.advancedModel", Value: v}); err != nil {
								return err
							}
						}
					}
				}
				return leave(to)
			},
			Options: func(cur map[string]string) []Option {
				return append(snowOwnOptions(profiles, cur["model"]), viaMagpie("snow", magpieID+"/")...)
			},
		}, {
			// basicModel, the light model Snow's summaries, compaction and
			// file search ask (utils/config/apiConfig.ts): on magpie's
			// profile only, where both models go to the gateway; a
			// profile of the user's is theirs to set in Snow. Empty while
			// it follows the main model.
			Key: "small", Label: "small",
			Get: func() string {
				if !onOurs() {
					return ""
				}
				b, _ := edit.GetJSON(ours, "snowcfg.basicModel")
				if b == "" || b == model(snowProfile) {
					return ""
				}
				return magpieID + "/" + b
			},
			Set: func(v string) error {
				if !onOurs() {
					if v == "" {
						return nil
					}
					return errors.New("Snow CLI's light model can be set here on magpie's profile: pick a model through magpie as its model first")
				}
				ref := model(snowProfile)
				if v != "" {
					r, ok := strings.CutPrefix(v, magpieID+"/")
					if !ok || !isMagpie(r) {
						return errors.New("Snow CLI's light model goes to the gateway with its main model: pick one through magpie")
					}
					ref = r
				}
				if err := edit.SetJSON(ours, edit.KV{Path: "snowcfg.basicModel", Value: ref}); err != nil {
					return err
				}
				return mirror(snowProfile)
			},
			Options: func(cur map[string]string) []Option {
				if !strings.HasPrefix(cur["model"], magpieID+"/") {
					return nil
				}
				return viaMagpie("snow", magpieID+"/")
			},
		}, {
			// chatThinking, the thinking magpie's profile asks for on Chat
			// Completions (api/chat.ts): enabled sends reasoning_effort,
			// unset sends thinking {type: disabled}, Snow's default. On
			// magpie's profile only; Snow's thinking.effort (Anthropic)
			// and responsesReasoning (Responses) are for protocols that
			// profile doesn't use.
			Key: "effort", Label: "thinking",
			Get: func() string {
				if !onOurs() {
					return ""
				}
				b, _ := edit.Read(ours)
				ct := gjson.GetBytes(jsonc.ToJSONInPlace(b), "snowcfg.chatThinking")
				if !ct.Get("enabled").Bool() {
					return ""
				}
				if v := ct.Get("reasoning_effort").String(); v != "" {
					return v
				}
				// on, with no level: the model's own
				return "on"
			},
			Set: func(v string) error {
				if !onOurs() {
					if v == "" {
						return nil
					}
					return errors.New("Snow CLI's thinking can be set here on magpie's profile: pick a model through magpie as its model first")
				}
				var err error
				if v == "" {
					err = edit.DelJSON(ours, "snowcfg.chatThinking")
				} else {
					err = edit.SetJSON(ours, edit.KV{Path: "snowcfg.chatThinking", Value: map[string]any{"enabled": true, "reasoning_effort": v}})
				}
				if err != nil {
					return err
				}
				return mirror(snowProfile)
			},
			Options: func(cur map[string]string) []Option {
				ref, ok := strings.CutPrefix(cur["model"], magpieID+"/")
				if !ok {
					return nil
				}
				for _, m := range magpieModels("snow") {
					if m.ID == ref && len(m.Efforts) > 0 {
						return append([]Option{{Value: "", Takes: "off"}}, static(m.Efforts...)...)
					}
				}
				// a model that lists no levels has none to pick
				return nil
			},
		}},
	}, ours, activeFile, config, defaultProfile)
}

// snowOurKey says k is a key magpie gives Snow CLI.
func snowOurKey(k string) bool {
	return k != "" && (ourKey(k) || strings.HasPrefix(k, gateway.Token+"-"))
}

// snowIsOurs says a profile (or config.json) is magpie's: its key is one
// magpie gave.
func snowIsOurs(b []byte) bool {
	return snowOurKey(gjson.GetBytes(jsonc.ToJSONInPlace(append([]byte(nil), b...)), "snowcfg.apiKey").String())
}

// snowSynced are the settings of magpie's profile that follow its main
// model: its window and output, from the catalog, and whether it takes
// images. What magpie last wrote of each is kept (the stash's
// "snow:<dir>:wrote"), so one the user changed since, in Snow's own
// settings, is theirs and stays as they set it.
var snowSynced = []string{"maxContextTokens", "maxTokens", "supportsVision"}

// snowVision are the settings of Snow's own vision model, which describes
// images to a main model that takes none (supportsVision false).
var snowVision = []string{"visionModel", "visionBaseUrl", "visionBaseUrlMode", "visionApiKey", "visionRequestMethod"}

// snowProfileFor is the profile magpie writes for ref: from (a profile of
// the user's, or magpie's own already when ours) with the gateway set in,
// the user's sign-in left out; Snow's own defaults where from is empty.
// wrote is what magpie last wrote of snowSynced into its own profile (nil
// for none known), and the second result what it writes now.
//
// A vision model of the user's own comes along only with an endpoint and
// key of its own: one that took the profile's would go to the gateway, which doesn't know it, or send
// magpie's key to the user's endpoint. Images reach a model that can't
// see through magpie's own Vision, which describes them at the gateway
// (provider.Described): the catalog then says the model takes them, and
// Snow is told so.
func snowProfileFor(from []byte, ours bool, v1, key, ref string, wrote map[string]any) ([]byte, map[string]any, error) {
	cfg := map[string]any{}
	if len(from) > 0 {
		if err := json.Unmarshal(jsonc.ToJSON(from), &cfg); err != nil {
			return nil, nil, err
		}
	}
	sc, _ := cfg["snowcfg"].(map[string]any)
	if sc == nil {
		sc = map[string]any{}
	}
	delete(sc, "oauth")
	if !ours {
		snowOwnVision(sc)
	}
	ctx, out := snowContext, snowOutput
	sees := false
	for _, m := range magpieModels("snow") {
		if m.ID == ref {
			if m.Context > 0 {
				ctx = m.Context
			}
			if n := maxTokens(m); n > 0 {
				out = n
			}
			// as every agent is told (fx, Copilot): a model the catalog
			// doesn't say sees is text-only to the gateway (blindTo)
			sees = m.Images && (m.ImageInput == nil || *m.ImageInput)
			break
		}
	}
	// an output above the window Snow is told is cut to it, as for a
	// model whose window isn't known
	out = min(out, ctx)
	want := map[string]any{"maxContextTokens": ctx, "maxTokens": out, "supportsVision": sees}
	now := map[string]any{}
	for _, k := range snowSynced {
		if ours && wrote != nil {
			if w, known := wrote[k]; known && !snowSame(sc[k], w) {
				// the user's own since magpie wrote it
				now[k] = w
				continue
			}
		}
		now[k], sc[k] = want[k], want[k]
	}
	sc["baseUrl"] = v1
	sc["baseUrlMode"] = "base"
	sc["apiKey"] = key
	sc["requestMethod"] = "chat"
	// the light model follows the main one, unless picked apart from it
	// (the small field): a magpie model of its own stays through a new
	// main model and a sync
	small := ref
	if ours {
		was, _ := sc["advancedModel"].(string)
		if b, _ := sc["basicModel"].(string); b != "" && b != was && isMagpie(b) {
			small = b
		}
	}
	sc["advancedModel"] = ref
	sc["basicModel"] = small
	cfg["snowcfg"] = sc
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	return b, now, nil
}

// snowOwnVision keeps, of the user's profile's settings, a vision model
// with an endpoint and key of its own, on the protocol and URL mode it
// was asked with there; any other goes.
func snowOwnVision(sc map[string]any) {
	model, _ := sc["visionModel"].(string)
	url, _ := sc["visionBaseUrl"].(string)
	key, _ := sc["visionApiKey"].(string)
	if strings.TrimSpace(model) == "" || strings.TrimSpace(url) == "" || strings.TrimSpace(key) == "" {
		for _, k := range snowVision {
			delete(sc, k)
		}
		return
	}
	for own, profile := range map[string]string{"visionRequestMethod": "requestMethod", "visionBaseUrlMode": "baseUrlMode"} {
		if v, _ := sc[own].(string); v == "" {
			if v, _ := sc[profile].(string); v != "" {
				sc[own] = v
			}
		}
	}
}

// snowSame says two JSON values are the same: a number read back from a
// file is a float64, the one magpie wrote an int.
func snowSame(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// snowProfiles are the profiles of the user's own: their names, and the
// main model each is on.
func snowProfiles(dir string) (names []string, models map[string]string) {
	models = map[string]string{}
	es, err := os.ReadDir(dir)
	if err != nil {
		return nil, models
	}
	for _, e := range es {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || e.IsDir() {
			continue
		}
		b, err := edit.Read(filepath.Join(dir, e.Name()))
		if err != nil || b == nil || snowIsOurs(b) {
			continue
		}
		names = append(names, name)
		models[name] = gjson.GetBytes(jsonc.ToJSONInPlace(b), "snowcfg.advancedModel").String()
	}
	sort.Strings(names)
	return names, models
}

// snowFindProfile is the user's profile on model v: prefer, when it is on
// it, else the first by name; "" for none.
func snowFindProfile(dir, v, prefer string) string {
	names, models := snowProfiles(dir)
	if prefer != "" && models[prefer] == v {
		return prefer
	}
	for _, n := range names {
		if models[n] == v {
			return n
		}
	}
	return ""
}

// snowOwnOptions are the models of the user's own profiles, and the
// current pick.
func snowOwnOptions(dir, cur string) []Option {
	names, models := snowProfiles(dir)
	seen := map[string]bool{}
	var out []Option
	for _, n := range names {
		m := models[n]
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, Option{Value: m, Icon: modelIcon("", m)})
	}
	if cur != "" && !seen[cur] && !strings.HasPrefix(cur, magpieID+"/") {
		out = append([]Option{{Value: cur, Icon: modelIcon("", cur)}}, out...)
	}
	return group("Snow CLI", out)
}

func jsonString(s string) string { b, _ := json.Marshal(s); return string(b) }

// what snow says after a change (notice.go)
var (
	noticeSnow = newNotice("Snow CLI reads its profile at start-up — restart open snow sessions to use this.")
)
