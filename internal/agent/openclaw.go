package agent

// OpenClaw (openclaw/openclaw, a personal assistant that answers on
// WhatsApp, Telegram, Discord and the rest) keeps its settings in one file,
// openclaw.json. Where it is follows OpenClaw's own resolveConfigPath
// (src/config/paths.ts) and resolveStateDir (src/config/state-dir.ts):
// $OPENCLAW_CONFIG_PATH, else openclaw.json in $OPENCLAW_STATE_DIR, else in
// ~/.openclaw-<profile> for a named $OPENCLAW_PROFILE, else in ~/.openclaw,
// where ~ is $OPENCLAW_HOME when it is set.
//
// A provider of the user's own is an entry of models.providers
// (src/config/zod-schema.core.ts, ModelProviderSchema): a baseUrl, an
// apiKey, an api (openai-completions is OpenAI's chat completions) and its
// models, each an id and a name, with input, reasoning and contextWindow
// (ModelDefinitionSchema). The schema is strict: a key OpenClaw doesn't
// know makes the file invalid, so magpie writes only those. A model is
// named provider/model, split at the first slash
// (src/agents/model-selection-normalize.ts), so magpie's
// magpie/<provider>/<model> is OpenClaw's ref as it is. The model OpenClaw
// runs on is agents.defaults.model, a ref or {primary, fallbacks}
// (docs/concepts/models.md); `openclaw models set` sets primary and keeps
// the fallbacks, and adds the ref to agents.defaults.models
// (src/commands/models/shared.ts), which a config from before 2026.8.1
// still reads as the models allowed. A non-empty
// agents.defaults.modelPolicy.allow is the models allowed: magpie adds
// "magpie/*" to it when nothing of magpie's is in it.
//
// magpie adds itself as the provider "magpie", the gateway's /v1 with a key
// naming OpenClaw and one model entry per catalog model, and makes the model
// picked the primary. What was the primary is stashed and put back on
// Disconnect, while magpie's is still the primary. The user's own keys stay
// as written, in magpie's provider and its model entries too (headers,
// timeoutSeconds, a model's maxTokens). OpenClaw writes the file as JSON
// (src/config/io.write.ts) but reads JSON5; a file that isn't JSON magpie
// can read is left alone. OpenClaw's gateway takes a change to models and
// the default model without a restart (docs/gateway/configuration/
// hot-reload.md), but a chat already pinned to a model keeps it until
// /model default.
//
// Not wired: a per-agent model (agents.entries.*.model) stays the user's,
// and so does a config split with $include.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"

	"github.com/yetone/magpie/internal/capre"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/filememo"
)

// what OpenClaw says after a change (notice.go)
var noticeOpenClaw = newNotice("OpenClaw takes this up without a restart. A chat that already has a model keeps it: send /model default in that chat to move it onto this one.")

// openClawContext is what OpenClaw is told of a model whose context magpie
// doesn't know.
const openClawContext = 128000

// the key paths magpie writes in openclaw.json
const (
	openClawProvider = "models.providers." + magpieID
	openClawModel    = "agents.defaults.model"
	openClawModels   = "agents.defaults.models"
	openClawAllow    = "agents.defaults.modelPolicy.allow"
	openClawAll      = magpieID + "/*"
)

// openClawProfile is a profile name OpenClaw takes (profile-utils.ts).
var openClawProfile = capre.MustCompile(`(?i)^[a-z0-9][a-z0-9_-]*$`, 64)

// openClawHome is OpenClaw's ~: $OPENCLAW_HOME, a leading ~ in it the
// system's home (resolveEffectiveHomeDir).
func openClawHome(at place) string {
	h := strings.TrimSpace(at.getenv("OPENCLAW_HOME"))
	if h == "" || h == "undefined" || h == "null" {
		return at.home
	}
	return openClawTilde(h, at.home)
}

// openClawTilde is p with a leading ~ as home.
func openClawTilde(p, home string) string {
	if p == "~" {
		return home
	}
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		return filepath.Join(home, rest)
	}
	if rest, ok := strings.CutPrefix(p, `~\`); ok {
		return filepath.Join(home, rest)
	}
	return p
}

// openClawPaths are OpenClaw's state folder and its openclaw.json.
func openClawPaths(at place) (dir, path string) {
	home := openClawHome(at)
	dir = filepath.Join(home, ".openclaw")
	if p := strings.TrimSpace(at.getenv("OPENCLAW_PROFILE")); p != "" && !strings.EqualFold(p, "default") && openClawProfile.MatchString(p) {
		dir = filepath.Join(home, ".openclaw-"+p)
	}
	if d := strings.TrimSpace(at.getenv("OPENCLAW_STATE_DIR")); d != "" {
		dir = openClawTilde(d, home)
	}
	path = filepath.Join(dir, "openclaw.json")
	if p := strings.TrimSpace(at.getenv("OPENCLAW_CONFIG_PATH")); p != "" {
		path = openClawTilde(p, home)
	}
	return dir, path
}

// openClawRead is openclaw.json as JSON with its comments blanked out, nil
// for a missing file; one that isn't JSON (JSON5's bare keys) is an error,
// never taken as empty.
func openClawRead(path string) ([]byte, error) {
	b, err := edit.Read(path)
	if err != nil {
		return nil, err
	}
	return openClawPlain(path, b)
}

// openClawLook is openClawRead for a look that writes nothing (the
// model's Get, Check), which every drift poll makes: the file is made
// plain JSON and checked once a change, and the bytes are shared, so read
// only.
func openClawLook(path string) ([]byte, error) {
	s, err := filememo.Read("openclaw json", path, func(b []byte) ([]byte, error) {
		return openClawPlain(path, edit.TrimBOM(b))
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return s, err
}

// openClawPlain is b, OpenClaw's file, made plain JSON in a copy of its
// own, or nil for an empty file.
func openClawPlain(path string, b []byte) ([]byte, error) {
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, nil
	}
	s := jsonc.ToJSONInPlace(append([]byte(nil), b...))
	if !json.Valid(s) || !gjson.ParseBytes(s).IsObject() {
		return nil, fmt.Errorf("OpenClaw's %s isn't JSON magpie can read (JSON5?); it was left as it is", path)
	}
	return s, nil
}

// openClawKey is a key as one part of a key path.
func openClawKey(k string) string { return strings.ReplaceAll(k, ".", `\.`) }

// openClawEntry is one of magpie's models as OpenClaw is told it.
type openClawEntry struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Reasoning     bool     `json:"reasoning"`
	Input         []string `json:"input"`
	ContextWindow int      `json:"contextWindow"`
}

// openClawWant is magpie's models as OpenClaw is told them, each laid over
// the entry of the same id there was (raw), so the user's own keys in it
// stay.
func openClawWant(raw gjson.Result) []any {
	was := map[string]map[string]any{}
	for _, m := range raw.Array() {
		var e map[string]any
		if json.Unmarshal([]byte(m.Raw), &e) == nil {
			if id, _ := e["id"].(string); id != "" {
				was[id] = e
			}
		}
	}
	out := []any{}
	for _, m := range sharedModels("openclaw") {
		ctx := m.Context
		if ctx <= 0 {
			ctx = unknownWindow(openClawContext)
		}
		e := openClawEntry{ID: m.ID, Name: m.ID, Input: []string{"text"}, ContextWindow: ctx}
		if m.Images {
			e.Input = append(e.Input, "image")
		}
		e.Reasoning = slices.ContainsFunc(m.Efforts, func(l string) bool { return l != "none" })
		var mine map[string]any
		b, _ := json.Marshal(e)
		json.Unmarshal(b, &mine)
		kept := was[m.ID]
		if kept == nil {
			out = append(out, mine)
			continue
		}
		for k, v := range mine {
			kept[k] = v
		}
		out = append(out, kept)
	}
	return out
}

// openClawPrimary is the model a value of agents.defaults.model names: the
// string, or an object's primary.
func openClawPrimary(r gjson.Result) string {
	if r.IsObject() {
		return r.Get("primary").String()
	}
	if r.Type == gjson.String {
		return r.String()
	}
	return ""
}

// openClawWas is what agents.defaults.model was before magpie, as written,
// and which of the objects holding magpie's keys magpie made.
type openClawWas struct {
	Had   bool            `json:"had,omitempty"`
	Model json.RawMessage `json:"model,omitempty"`
	// Made are the key paths of objects that weren't there before magpie
	Made []string `json:"made,omitempty"`
	// File: the file wasn't there before magpie
	File bool `json:"file,omitempty"`
}

// openClawProviderAt is magpie's provider entry for a gateway at gw, as
// it is written (models aside).
func openClawProviderAt(gw string) map[string]string {
	return map[string]string{"baseUrl": gw + "/v1", "apiKey": agentKeyAt("openclaw", gw), "api": "openai-completions"}
}

func openClaw(home string) *Agent { return openClawAt(here(home)) }

func openClawAt(at place) *Agent {
	dir, path := openClawPaths(at)
	keyWas := at.key("openclaw:" + path + ":model")
	read := func() gjson.Result {
		s, _ := openClawLook(path)
		return gjson.ParseBytes(s)
	}
	ours := func(c gjson.Result) bool { return prefixed(openClawPrimary(c.Get(openClawModel))) }
	// patch applies sets and deletes to the file as it is, in one write
	patch := func(set []edit.KV, del []string) error {
		raw, err := edit.Read(path)
		if err != nil {
			return err
		}
		out, err := edit.PatchJSON(raw, set, del)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if raw == nil {
			// OpenClaw keeps its folder to its user (io.write.ts), and the
			// file holds keys
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				return err
			}
		}
		return edit.WriteAtomic(path, out)
	}
	// wire writes magpie's provider and its models, the user's own keys in
	// it left as they are, and with model, if given, makes it the primary;
	// it writes nothing when the file has it all already
	wire := func(c gjson.Result, model string) ([]edit.KV, []string) {
		var set []edit.KV
		var del []string
		p := c.Get(openClawProvider)
		for k, v := range openClawProviderAt(at.gw()) {
			if p.Get(k).String() != v || p.Get(k).Type != gjson.String {
				set = append(set, edit.KV{Path: openClawProvider + "." + k, Value: v})
			}
		}
		want := openClawWant(p.Get("models"))
		if !sameJSON(p.Get("models").Raw, want) {
			set = append(set, edit.KV{Path: openClawProvider + ".models", Value: want})
		}
		if model == "" {
			return set, del
		}
		ref := magpieID + "/" + model
		m := c.Get(openClawModel)
		if m.IsObject() {
			if m.Get("primary").String() != ref {
				set = append(set, edit.KV{Path: openClawModel + ".primary", Value: ref})
			}
		} else if m.String() != ref || m.Type != gjson.String {
			set = append(set, edit.KV{Path: openClawModel, Value: ref})
		}
		// as `openclaw models set` does; the ref of a model magpie put
		// there before goes, as it had nothing of the user's in it
		if ms := c.Get(openClawModels); ms.IsObject() {
			ms.ForEach(func(k, v gjson.Result) bool {
				if prefixed(k.String()) && k.String() != ref && v.IsObject() && len(v.Map()) == 0 {
					del = append(del, openClawModels+"."+openClawKey(k.String()))
				}
				return true
			})
			if !ms.Get(openClawKey(ref)).Exists() {
				set = append(set, edit.KV{Path: openClawModels + "." + openClawKey(ref), Value: map[string]any{}})
			}
		}
		if allow := c.Get(openClawAllow); allow.IsArray() && len(allow.Array()) > 0 && !slices.ContainsFunc(allow.Array(), func(a gjson.Result) bool {
			s := a.String()
			return s == "*" || s == ref || s == openClawAll || s == magpieID+"/"+strings.SplitN(model, "/", 2)[0]+"/*"
		}) {
			var list []any
			json.Unmarshal([]byte(allow.Raw), &list)
			set = append(set, edit.KV{Path: openClawAllow, Value: append(list, openClawAll)})
		}
		return set, del
	}
	// unwire takes magpie's provider out, and OpenClaw back to the model it
	// ran on before — when it still runs on magpie's: a pick the user made
	// since is theirs and stands. own, if given, is the model to run on.
	unwire := func(own string) error {
		s, err := openClawRead(path)
		if err != nil {
			return err
		}
		c := gjson.ParseBytes(s)
		var was openClawWas
		json.Unmarshal([]byte(unstash(keyWas)), &was)
		var set []edit.KV
		del := []string{openClawProvider}
		m := c.Get(openClawModel)
		switch {
		case own != "":
			if m.IsObject() {
				set = append(set, edit.KV{Path: openClawModel + ".primary", Value: own})
			} else {
				set = append(set, edit.KV{Path: openClawModel, Value: own})
			}
		case !ours(c):
		case !was.Had:
			if m.IsObject() && len(m.Map()) > 1 {
				del = append(del, openClawModel+".primary")
			} else {
				del = append(del, openClawModel)
			}
		case m.IsObject():
			if p := openClawPrimary(gjson.ParseBytes(was.Model)); p != "" {
				set = append(set, edit.KV{Path: openClawModel + ".primary", Value: p})
			} else {
				del = append(del, openClawModel+".primary")
			}
		default:
			set = append(set, edit.KV{Path: openClawModel, Value: was.Model})
		}
		if ms := c.Get(openClawModels); ms.IsObject() {
			ms.ForEach(func(k, v gjson.Result) bool {
				if prefixed(k.String()) && v.IsObject() && len(v.Map()) == 0 {
					del = append(del, openClawModels+"."+openClawKey(k.String()))
				}
				return true
			})
		}
		if allow := c.Get(openClawAllow); allow.IsArray() && slices.Contains(was.Made, openClawAllow+"[]") {
			var list []any
			for _, a := range allow.Array() {
				if a.String() != openClawAll {
					list = append(list, a.Value())
				}
			}
			if len(list) < len(allow.Array()) {
				if list == nil {
					list = []any{}
				}
				set = append(set, edit.KV{Path: openClawAllow, Value: list})
			}
		}
		if err := patch(set, del); err != nil {
			return err
		}
		// the objects magpie made, where nothing else is in them now,
		// deepest first; and the file, where magpie made it and it is
		// left empty
		for i := len(was.Made) - 1; i >= 0; i-- {
			k := was.Made[i]
			if strings.HasSuffix(k, "[]") {
				continue
			}
			if r := read().Get(k); r.IsObject() && len(r.Map()) == 0 {
				if err := edit.DelJSON(path, k); err != nil {
					return err
				}
			}
		}
		if was.File {
			if r := read(); r.IsObject() && len(r.Map()) == 0 {
				return os.Remove(path)
			}
		}
		return nil
	}
	// connect makes model, one of magpie's, the primary, stashing what was
	// there first
	connect := func(model string) error {
		s, err := openClawRead(path)
		if err != nil {
			return err
		}
		c := gjson.ParseBytes(s)
		var was openClawWas
		if b := stashShared()[keyWas]; b != "" {
			json.Unmarshal([]byte(b), &was)
		} else if !ours(c) {
			m := c.Get(openClawModel)
			was = openClawWas{Had: m.Exists(), File: s == nil}
			if m.Exists() {
				was.Model = json.RawMessage(m.Raw)
			}
		}
		for _, k := range []string{"models", "models.providers", "agents", "agents.defaults"} {
			if !c.Get(k).Exists() && !slices.Contains(was.Made, k) {
				was.Made = append(was.Made, k)
			}
		}
		set, del := wire(c, model)
		for _, kv := range set {
			if kv.Path == openClawAllow && !slices.Contains(was.Made, openClawAllow+"[]") {
				was.Made = append(was.Made, openClawAllow+"[]")
			}
		}
		b, _ := json.Marshal(was)
		stash(map[string]string{keyWas: string(b)})
		return patch(set, del)
	}
	return atomic(&Agent{
		ID: "openclaw", Name: "OpenClaw", Icon: "openclaw", Spelled: prefixed, NoNames: true,
		Bin: "openclaw", Dir: dir, Path: path,
		Notice:   func() string { return noticeOpenClaw.String() },
		ListOnly: true,
		Sync: func() error {
			s, err := openClawRead(path)
			if err != nil {
				return err
			}
			c := gjson.ParseBytes(s)
			if !c.Get(openClawProvider).Exists() {
				return nil
			}
			set, del := wire(c, "")
			if len(set) == 0 && len(del) == 0 {
				return nil
			}
			return patch(set, del)
		},
		Check: func() string {
			s, err := openClawLook(path)
			if err != nil {
				return err.Error()
			}
			c := gjson.ParseBytes(s)
			if !ours(c) {
				return ""
			}
			p := c.Get(openClawProvider)
			if !p.Exists() {
				return "OpenClaw's " + openClawProvider + " (openclaw.json) is gone, so it no longer reaches magpie"
			}
			want := openClawProviderAt(at.gw())
			return wiringOff("OpenClaw", path, func(k string) (string, bool) {
				v := p.Get(k)
				return v.String(), v.Exists()
			}, "baseUrl", want["baseUrl"], "apiKey", want["apiKey"])
		},
		Fields: []Field{{
			// the model OpenClaw runs on: one of magpie's is
			// magpie/<provider>/<model>, OpenClaw's ref for it as it is
			Key: "model", Label: "model",
			Get: func() string { return openClawPrimary(read().Get(openClawModel)) },
			Set: func(v string) error {
				s, err := openClawRead(path)
				if err != nil {
					return err
				}
				c := gjson.ParseBytes(s)
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					return connect(ref)
				}
				if c.Get(openClawProvider).Exists() || ours(c) {
					return unwire(v)
				}
				if v == "" {
					return nil
				}
				if m := c.Get(openClawModel); m.IsObject() {
					return patch([]edit.KV{{Path: openClawModel + ".primary", Value: v}}, nil)
				}
				return patch([]edit.KV{{Path: openClawModel, Value: v}}, nil)
			},
			Options: func(cur map[string]string) []Option {
				return withVia(openClawOwn(keyWas, cur["model"]), "openclaw", magpieID+"/")
			},
		}},
	}, path)
}

// openClawOwn is the model OpenClaw ran on before magpie, or runs on now,
// for the picker: OpenClaw's own providers are its sign-ins and plugins,
// which magpie can't list.
func openClawOwn(keyWas, cur string) []Option {
	var was openClawWas
	json.Unmarshal([]byte(stashShared()[keyWas]), &was)
	var out []Option
	for _, m := range []string{openClawPrimary(gjson.ParseBytes(was.Model)), cur} {
		if m == "" || prefixed(m) || slices.ContainsFunc(out, func(o Option) bool { return o.Value == m }) {
			continue
		}
		p, id, _ := strings.Cut(m, "/")
		out = append(out, Option{Value: m, Icon: modelIcon(p, id), Note: "OpenClaw's own"})
	}
	return group("openclaw", out)
}
