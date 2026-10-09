package agent

// Qwen Code (`qwen`, QwenLM/qwen-code, a Gemini CLI fork) keeps its
// settings in ~/.qwen/settings.json ($QWEN_HOME when set). Models of the
// user's own are the modelProviders.<protocol> arrays, each
// {baseUrl, envKey, id, name}: id is the model asked for, name what its
// /model picker shows, and envKey names a variable in the same file's env
// object (or ~/.qwen/.env) with the key. The model sessions start on is
// settings.model {name, baseUrl}, baseUrl telling two entries of one id
// apart, and security.auth.selectedType "openai" skips the /auth prompt
// on start.
//
// magpie appends one entry per catalog model to modelProviders.openai,
// the wire id the gateway takes ("<provider>/<model>") and the name
// "magpie/<provider>/<model>", every one on the same env key of its own,
// which is how an entry of magpie's is told from the user's own, and
// points settings.model and security.auth.selectedType at a pick. The key
// itself lives in the settings' env object — a shell-exported or
// ~/.qwen/.env variable of the same name would win over it, which the
// qwen-specific name makes unlikely. The user's entries stay as they were
// and in front, so their own ids don't move, and the default, auth type
// and any key of theirs they had are stashed and put back when magpie
// steps out.

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"

	"github.com/yetone/magpie/internal/edit"
)

const (
	qwenModels  = "modelProviders.openai"
	qwenModel   = "model.name"
	qwenModelAt = "model.baseUrl"
	qwenAuth    = "security.auth.selectedType"
	qwenEnv     = "env." + qwenEnvKey
	// qwenEnvKey names the variable magpie's provider entries take their
	// key from: qwen-specific so a shell or ~/.qwen/.env value of the
	// name is unlikely to shadow the settings' (their priority over it is
	// why "MAGPIE_API_KEY" is not used), and set in the settings' env
	// object while magpie is wired in, which names one of magpie's
	// entries wherever it appears.
	qwenEnvKey = "MAGPIE_QWEN_API_KEY"
)

// qwenEntry is one modelProviders.openai member magpie writes, fields
// ordered as Qwen Code orders its own (alphabetically).
type qwenEntry struct {
	BaseURL    string   `json:"baseUrl"`
	EnvKey     string   `json:"envKey"`
	Generation *qwenGen `json:"generationConfig,omitempty"`
	ID         string   `json:"id"`
	Name       string   `json:"name"`
}

// qwenGen is the generationConfig a model's entry may carry.
type qwenGen struct {
	ContextWindowSize int `json:"contextWindowSize,omitempty"`
}

// qwenEntriesAt are magpie's entries for a qwen reaching the gateway at
// v1.
func qwenEntriesAt(v1 string) []qwenEntry {
	var out []qwenEntry
	for _, m := range magpieModels("qwen") {
		e := qwenEntry{ID: m.ID, Name: magpieID + "/" + m.ID, BaseURL: v1, EnvKey: qwenEnvKey}
		if m.Context > 0 {
			e.Generation = &qwenGen{ContextWindowSize: m.Context}
		}
		out = append(out, e)
	}
	return out
}

// qwenCustom is one modelProviders.openai member as it is in the file:
// its raw JSON, and what of it the picker needs.
type qwenCustom struct {
	raw          json.RawMessage
	id, name     string
	base, envKey string
	ours         bool
}

// qwenCustoms reads modelProviders.openai of a settings.json.
func qwenCustoms(path string) []qwenCustom {
	raw, err := edit.Read(path)
	if err != nil || len(raw) == 0 {
		return nil
	}
	var out []qwenCustom
	all := gjson.GetBytes(jsonc.ToJSONInPlace(raw), qwenModels)
	if !all.IsArray() {
		// no modelProviders (or none of openai): none of magpie's and
		// none of the user's, rather than an error ForEach gives for null
		return nil
	}
	all.ForEach(func(_, v gjson.Result) bool {
		c := qwenCustom{
			raw:    json.RawMessage(v.Raw),
			id:     v.Get("id").String(),
			name:   v.Get("name").String(),
			base:   v.Get("baseUrl").String(),
			envKey: v.Get("envKey").String(),
		}
		c.ours = c.envKey == qwenEnvKey
		out = append(out, c)
		return true
	})
	return out
}

// qwenModelOf reads settings.model: the model's id, and the baseUrl
// telling one of its two entries apart ("" when unset).
func qwenModelOf(path string) (name, base string) {
	name, _ = edit.GetJSON(path, qwenModel)
	base, _ = edit.GetJSON(path, qwenModelAt)
	return name, base
}

// qwenPrune takes a parent key out once the last thing in it is gone: the
// "env": {} or "modelProviders": {} that emptying magpie's writes would
// leave was never the user's.
func qwenPrune(path, parent string) error {
	if v, ok := edit.GetJSON(path, parent); ok && strings.TrimSpace(v) == "{}" {
		return edit.DelJSON(path, parent)
	}
	return nil
}

// qwenFind finds the entry settings.model points at: by id and baseUrl,
// or the first of the id when no baseUrl is set.
func qwenFind(cs []qwenCustom, name, base string) *qwenCustom {
	for i := range cs {
		if cs[i].id == name && (base == "" || cs[i].base == base) {
			return &cs[i]
		}
	}
	return nil
}

func qwen(home string) *Agent { return qwenIn(here(home)) }

// qwenIn is Qwen Code at a place: this machine's home, or a WSL distro's
// (see wsl.go), whose entries name the gateway as the distro reaches it,
// with the key it takes from there.
func qwenIn(at place) *Agent {
	dir := filepath.Join(at.home, ".qwen")
	if d := at.getenv("QWEN_HOME"); d != "" {
		dir = d
	}
	path := filepath.Join(dir, "settings.json")
	entries := func() []qwenEntry { return qwenEntriesAt(at.v1()) }
	keyName := "qwen:" + path + ":" + qwenModel
	keyBase := "qwen:" + path + ":" + qwenModelAt
	keyAuth := "qwen:" + path + ":" + qwenAuth
	// keyAuthSeen marks the user's own auth type already stashed (or
	// known absent), so a later wire doesn't take magpie's own "openai"
	// for it
	keyAuthSeen := keyAuth + ".seen"
	keyEnv := "qwen:" + path + ":" + qwenEnv

	// get spells an entry of magpie's as the catalog does
	get := func() string {
		name, base := qwenModelOf(path)
		if name == "" {
			return ""
		}
		if c := qwenFind(qwenCustoms(path), name, base); c != nil && c.ours {
			return magpieID + "/" + c.id
		}
		return name
	}

	// setEnvKey writes env.MAGPIE_API_KEY as the gateway's key; with=false
	// puts back what was stashed, or takes the key out where it still is
	// magpie's. A value of the user's own under that name is stashed before
	// it is replaced and never removed: magpie only removes what it wrote.
	setEnvKey := func(with bool) error {
		if with {
			if v, ok := edit.GetJSON(path, qwenEnv); ok && v != at.gwKey() {
				stash(map[string]string{keyEnv: v})
			}
			return edit.SetJSON(path, edit.KV{Path: qwenEnv, Value: at.gwKey()})
		}
		if was := unstash(keyEnv); was != "" {
			return edit.SetJSON(path, edit.KV{Path: qwenEnv, Value: was})
		}
		if v, ok := edit.GetJSON(path, qwenEnv); ok && v == at.gwKey() {
			if err := edit.DelJSON(path, qwenEnv); err != nil {
				return err
			}
			return qwenPrune(path, "env")
		}
		return nil
	}

	// setAuth sets security.auth.selectedType to openai while magpie's
	// models are the pick (skipping the /auth prompt), stashing the user's
	// own; with=false puts it back, or takes the key out where it never
	// was, pruning the empty security.auth and security it would leave.
	setAuth := func(with bool) error {
		if with {
			// the user's own value is stashed once, whatever it is —
			// "openai" of their own too, or it can't be told from
			// magpie's on a later wire or Sync and comes off with it
			if _, seen := stashLoad()[keyAuthSeen]; !seen {
				kv := map[string]string{keyAuthSeen: "1"}
				if v, ok := edit.GetJSON(path, qwenAuth); ok {
					kv[keyAuth] = v
				}
				stash(kv)
			}
			return edit.SetJSON(path, edit.KV{Path: qwenAuth, Value: "openai"})
		}
		forget(keyAuthSeen)
		if was := unstash(keyAuth); was != "" {
			return edit.SetJSON(path, edit.KV{Path: qwenAuth, Value: was})
		}
		if err := edit.DelJSON(path, qwenAuth); err != nil {
			return err
		}
		if err := qwenPrune(path, "security.auth"); err != nil {
			return err
		}
		return qwenPrune(path, "security")
	}

	// setProviders writes modelProviders.openai with the user's own and,
	// when with, magpie's after them; none left takes the array out. A
	// with=false when none were magpie's changes nothing.
	setProviders := func(with bool) error {
		var ms []any
		var had bool
		for _, c := range qwenCustoms(path) {
			if c.ours {
				had = true
				continue
			}
			ms = append(ms, c.raw)
		}
		if with {
			if err := setEnvKey(true); err != nil {
				return err
			}
			if err := setAuth(true); err != nil {
				return err
			}
			for _, e := range entries() {
				ms = append(ms, e)
			}
		} else if !had {
			// nothing of magpie's left: a key it left behind, and the
			// auth type it set, still go
			if err := setEnvKey(false); err != nil {
				return err
			}
			return setAuth(false)
		}
		if len(ms) > 0 {
			if err := edit.SetJSON(path, edit.KV{Path: qwenModels, Value: ms}); err != nil {
				return err
			}
		} else if err := edit.DelJSON(path, qwenModels); err != nil {
			return err
		}
		if !with {
			if err := setEnvKey(false); err != nil {
				return err
			}
			if err := setAuth(false); err != nil {
				return err
			}
			return qwenPrune(path, "modelProviders")
		}
		return nil
	}

	// put sets settings.model to (name, base), or takes it out for "":
	// the "model": {} that Qwen Code never wrote is removed with its last
	// member.
	put := func(name, base string) error {
		if name != "" {
			if base != "" {
				if err := edit.SetJSON(path, edit.KV{Path: qwenModelAt, Value: base}); err != nil {
					return err
				}
			} else if err := edit.DelJSON(path, qwenModelAt); err != nil {
				// baseUrl unset stays unset: qwen treats "" as a real
				// endpoint of its own in the composite match
				return err
			}
			return edit.SetJSON(path, edit.KV{Path: qwenModel, Value: name})
		}
		if err := edit.DelJSON(path, qwenModelAt); err != nil {
			return err
		}
		if err := edit.DelJSON(path, qwenModel); err != nil {
			return err
		}
		if v, ok := edit.GetJSON(path, "model"); ok && strings.TrimSpace(v) == "{}" {
			return edit.DelJSON(path, "model")
		}
		return nil
	}

	return atomic(&Agent{
		ID: "qwen", Name: "Qwen Code", Icon: "qwen-color", Aliases: []string{"qwen-code", "qwen-cli"}, Spelled: prefixed,
		// qwen's requests carry QwenCode/<version> (packages/core/src/core/
		// openaiContentGenerator/provider/default.ts buildHeaders)
		UA:  []string{"qwencode"},
		Bin: "qwen", Dir: dir, Path: path,
		Sync: func() error {
			for _, c := range qwenCustoms(path) {
				if c.ours {
					return setProviders(true)
				}
			}
			return nil
		},
		Notice: func() string {
			if Running(`(^|/)qwen( |$)`) {
				return "Qwen Code reads a session's model at start-up — start a new session, or /model anew, to use this."
			}
			return ""
		},
		Check: func() string {
			// settings.model is magpie's by the entry it names, wherever
			// that entry points: another address is drift to report, not
			// a model of the user's
			name, _ := qwenModelOf(path)
			if name == "" {
				return ""
			}
			var c *qwenCustom
			cs := qwenCustoms(path)
			for i := range cs {
				if cs[i].ours && cs[i].id == name {
					c = &cs[i]
					break
				}
			}
			if c == nil {
				return ""
			}
			if v, ok := edit.GetJSON(path, qwenEnv); !ok || v != at.gwKey() {
				return "Qwen Code's " + qwenEnv + " (settings.json) no longer is magpie's key, so magpie's models have none"
			}
			if v, ok := edit.GetJSON(path, qwenAuth); !ok || v != "openai" {
				return "Qwen Code's security.auth.selectedType (settings.json) no longer is openai, so it asks /auth instead of using magpie"
			}
			return wiringOff("Qwen Code", path, func(k string) (string, bool) {
				g := gjson.GetBytes(c.raw, k)
				return g.String(), g.Exists()
			}, "baseUrl", at.v1())
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: get,
			Set: func(v string) error {
				curName, curBase := qwenModelOf(path)
				ours := qwenFind(qwenCustoms(path), curName, curBase)
				onOurs := ours != nil && ours.ours
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					if !onOurs {
						kv := map[string]string{keyBase: curBase}
						if curName != "" {
							kv[keyName] = curName
						} else {
							forget(keyName)
						}
						stash(kv)
					}
					if err := setProviders(true); err != nil {
						return err
					}
					return put(ref, at.v1())
				}
				base := ""
				fromStash := false
				if onOurs {
					// back to what the user had, name and its baseUrl
					if v == "" {
						v, base, fromStash = unstash(keyName), unstash(keyBase), true
					} else {
						forget(keyName, keyBase)
					}
				}
				if v != "" && !fromStash {
					if c := qwenFind(qwenCustoms(path), v, ""); c != nil {
						base = c.base
					}
				}
				if err := put(v, base); err != nil {
					return err
				}
				return setProviders(false)
			},
			Options: func(cur map[string]string) []Option {
				return append(qwenOwnOptions(path, cur["model"]), viaMagpie("qwen", magpieID+"/")...)
			},
		}},
	}, path)
}

// qwenOwnOptions are the models of the user's own the settings file has:
// every modelProviders.openai entry that isn't magpie's, and the current
// pick.
func qwenOwnOptions(path, cur string) []Option {
	seen := map[string]bool{}
	var out []Option
	for _, c := range qwenCustoms(path) {
		if c.ours || seen[c.id] {
			continue
		}
		seen[c.id] = true
		label := c.name
		if label == "" || label == c.id {
			label = ""
		}
		out = append(out, Option{Value: c.id, Label: label, Icon: modelIcon("", c.id)})
	}
	if cur != "" && !seen[cur] && !strings.HasPrefix(cur, magpieID+"/") {
		out = append([]Option{{Value: cur, Icon: modelIcon("", cur)}}, out...)
	}
	return group("Qwen Code", out)
}
