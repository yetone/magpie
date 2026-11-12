package agent

// WorkBuddy (Tencent's CodeBuddy desktop app) takes models of one's own
// from ~/.workbuddy/models.json ($WORKBUDDY_CONFIG_DIR if set): a list of
// OpenAI chat-completions models, or {"models":[…],"availableModels":[…]}
// as CodeBuddy Code's CLI writes it. Its own "local models" are entries of
// the same list (local: true).
//
//	[{"id":"deepseek/pro","name":…,"vendor":"magpie","apiKey":"magpie-workbuddy",
//	  "url":"http://127.0.0.1:3425/v1/chat/completions",
//	  "maxInputTokens":…,"maxOutputTokens":…,
//	  "contextWindow":{"supportedLengths":[200000,…],"defaultLength":…},
//	  "supportsToolCall":true,"supportsImages":…,"supportsReasoning":true,
//	  "reasoning":{"supportedEfforts":[…],"canDisableThinking":…,"defaultEffort":…}}]
//
// WorkBuddy shows them in its picker as "custom-local:<id>", and asks url
// for model <id>, with reasoning_effort, under Bearer apiKey. 5.6's
// app.asar watches the file only when it was there at start, and the watch
// is on the file itself, so a rewrite can go unseen until a restart; the
// Agents row offers one (apprestart.go). The model is picked per
// task in WorkBuddy's window, not in a file, so what magpie sets is whether
// its models are in that picker. Its requests say "CLI/<ver> WorkBuddy/<ver>",
// so they are told apart by their key.
//
// WorkBuddy AI, the international build (www.workbuddy.ai, bundle
// com.workbuddy.workbuddy-ai), is the same app with its data in
// ~/.workbuddy-ai: its cli/product.json sets dataFolderName ".workbuddy-ai",
// which resolveWorkbuddyConfigDir (5.6.2's app.asar) takes when
// WORKBUDDY_CONFIG_DIR isn't set. magpie wires it as its own agent, its
// models under its own key, so each build's picker is written in its own
// folder (#1494).

import (
	"bytes"
	"cmp"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

func workbuddy(home string) *Agent {
	dir := appdir.Getenv("WORKBUDDY_CONFIG_DIR")
	if dir == "" {
		dir = filepath.Join(home, ".workbuddy")
	}
	return workbuddyBuild("workbuddy", "WorkBuddy", dir, []string{"work-buddy"}, workbuddyApp)
}

// workbuddyAI is WorkBuddy AI, the international build, in ~/.workbuddy-ai.
// WORKBUDDY_CONFIG_DIR is WorkBuddy's (workbuddy above): both builds would
// read a folder it names, which that agent writes already.
func workbuddyAI(home string) *Agent {
	return workbuddyBuild("workbuddy-ai", "WorkBuddy AI", filepath.Join(home, ".workbuddy-ai"), []string{"workbuddy-intl", "workbuddyai"}, workbuddyAIApp)
}

// workbuddyApp and workbuddyAIApp are the two builds' bundles.
var (
	workbuddyApp   = desktopApp{id: "com.tencent.workbuddy.mac", names: []string{"WorkBuddy.app"}}
	workbuddyAIApp = desktopApp{id: "com.workbuddy.workbuddy-ai", names: []string{"WorkBuddy AI.app"}}
)

// workbuddyOnMac says whether WorkBuddy is found by its app bundle, as on a
// Mac; a var so a test forces either OS.
var workbuddyOnMac = func() bool { return runtime.GOOS == "darwin" }

// workbuddyHere reports whether a WorkBuddy build is installed. On a Mac it
// is its app bundle (app): its data folder is no sign of it. Dragging
// WorkBuddy.app to the Trash leaves ~/.workbuddy behind, and magpie writes
// there itself (models.json when wired, mcp.json and skills/ from the
// library), so the folder alone kept an uninstalled WorkBuddy on every
// library row for good (@jiahao_jayden on X: 没安装的 WorkBuddy 也在技能
// 那一排). Elsewhere, where magpie doesn't know its install folder, the
// folder still counts, as before.
func workbuddyHere(dir, path string, app desktopApp) bool {
	if Taken(dir) {
		return false
	}
	if workbuddyOnMac() {
		return macBundle(app)
	}
	if _, err := os.Stat(path); err == nil {
		return true
	}
	return agentDir(dir)
}

// workbuddyBuild is one of WorkBuddy's builds, its models.json in dir and
// app its macOS bundle.
func workbuddyBuild(id, name, dir string, aliases []string, app desktopApp) *Agent {
	path := filepath.Join(dir, "models.json")
	write := func(on bool) error { return buddyWrite(path, id, on) }
	return &Agent{
		ID: id, Name: name, Icon: "workbuddy-color", Aliases: aliases,
		UA:  []string{id},
		Dir: dir, Path: path,
		detect: func() bool { return workbuddyHere(dir, path, app) },
		Sync: func() error {
			if !workbuddyWired(path) {
				return nil
			}
			return write(true)
		},
		// its picker lists the models picked for it (#1508)
		ListsModels: true,
		Fields: []Field{{
			Key: "provider", Label: "provider",
			Get: func() string {
				if workbuddyWired(path) {
					return magpieID
				}
				return ""
			},
			Set: func(v string) error { return write(v != "") },
			Options: func(map[string]string) []Option {
				return []Option{{Value: magpieID, Label: "magpie", Icon: "magpie", Note: "every magpie model in " + name + "'s picker"}}
			},
		}},
	}
}

// workbuddyDoc is models.json as read: its entries, and the rest of the
// object when it is one (nil for a bare list).
type workbuddyDoc struct {
	models []json.RawMessage
	rest   map[string]json.RawMessage
}

func workbuddyRead(path string) (workbuddyDoc, error) {
	var d workbuddyDoc
	b, err := edit.Read(path)
	if err != nil || len(bytes.TrimSpace(b)) == 0 {
		return d, err
	}
	if bytes.TrimSpace(b)[0] == '[' {
		return d, json.Unmarshal(b, &d.models)
	}
	if err := json.Unmarshal(b, &d.rest); err != nil {
		return d, err
	}
	if raw, ok := d.rest["models"]; ok {
		if err := json.Unmarshal(raw, &d.models); err != nil {
			return d, err
		}
	}
	return d, nil
}

// workbuddyEntry is what magpie reads of an entry.
type workbuddyEntry struct {
	ID        string `json:"id"`
	Vendor    string `json:"vendor"`
	Disabled  *bool  `json:"disabled"`
	URL       string `json:"url"`
	APIKey    string `json:"apiKey"`
	Reasoning *struct {
		DefaultEffort string `json:"defaultEffort"`
		Effort        string `json:"effort"` // the older key WorkBuddy still reads
	} `json:"reasoning"`
}

func workbuddyMine(raw json.RawMessage) (workbuddyEntry, bool) {
	var e workbuddyEntry
	return e, json.Unmarshal(raw, &e) == nil && e.Vendor == magpieID
}

// workbuddyPart is magpie's part of models.json, in one form whoever wrote
// the file: magpie's entries, each with its keys sorted and without its
// apiKey. WorkBuddy rewrites models.json itself seconds after every start
// (it purges local models a computer can't run, and saves the file again
// even when nothing was purged): without magpie's trailing newline, as a
// bare list on 5.6, and with the apiKey encrypted where its credential
// policy says so. Its own write is no change of magpie's, so its row is not
// told to restart WorkBuddy for it. The file as it is when it can't be
// read as models.json.
func workbuddyPart(path string) string {
	d, err := workbuddyRead(path)
	if err != nil {
		c, _ := edit.Read(path)
		return string(c)
	}
	var b strings.Builder
	for _, raw := range d.models {
		if _, ok := workbuddyMine(raw); !ok {
			continue
		}
		var e map[string]any
		if json.Unmarshal(raw, &e) != nil {
			continue
		}
		delete(e, "apiKey")
		c, _ := json.Marshal(e)
		b.Write(c)
		b.WriteByte('\n')
	}
	return b.String()
}

// workbuddyWired reports whether models.json has magpie's models.
func workbuddyWired(path string) bool {
	d, _ := workbuddyRead(path)
	return slices.ContainsFunc(d.models, func(raw json.RawMessage) bool { _, ok := workbuddyMine(raw); return ok })
}

// workbuddyWrite puts magpie's models into models.json (on) or takes them
// out, leaving every other entry and key as WorkBuddy wrote it. A model
// turned off in WorkBuddy stays off, and models the user pointed at a magpie
// on another machine (a NAS's) stay there with its key, as ZCode's do
// (zcodeAddress): a sync brings the models up to date, not the address.
func workbuddyWrite(path string, on bool) error { return buddyWrite(path, "workbuddy", on) }

// buddyWrite is workbuddyWrite for an agent that reads this models.json:
// WorkBuddy, or CodeBuddy Code (codebuddy.go), whose models go under the
// agent's own key. A new file is a bare list for WorkBuddy, as it writes
// one, and {"models":[…]} for CodeBuddy Code, as its docs give it.
func buddyWrite(path, agent string, on bool) error { return buddyWriteAt(path, agent, on, place{}) }

// buddyWriteAt is buddyWrite for the agent at a place. One in a WSL distro
// (where.base set) is written the gateway as the distro reaches it, with the
// key it takes from there, at every write: its address is the distro's
// view of Windows, which moves, so it is never kept as a magpie of the
// user's on another machine.
func buddyWriteAt(path, agent string, on bool, where place) error {
	away := where.base != nil
	d, err := workbuddyRead(path)
	if err != nil {
		return err
	}
	if d.rest == nil && d.models == nil && agent != "workbuddy" && agent != "workbuddy-ai" {
		d.rest = map[string]json.RawMessage{}
	}
	// the keys magpie's models are asked with: the gateway's, or that of a
	// magpie on another machine the user pointed them at
	keys := map[string]bool{gateway.TokenFor(agent): true, agentKey(agent): true}
	if away {
		keys[agentKeyAt(agent, where.gw())] = true
	}
	for _, raw := range d.models {
		if e, ok := workbuddyMine(raw); ok && e.APIKey != "" {
			keys[e.APIKey] = true
		}
	}
	var kept []json.RawMessage
	var ours []string
	off := map[string]bool{}
	effort := map[string]string{} // the default effort each model was given in WorkBuddy, "" for its Auto
	var remote, remoteKey string
	at := -1 // where magpie's were, which they keep
	for _, raw := range d.models {
		e, ok := workbuddyMine(raw)
		// WorkBuddy's model settings save an edited entry under the vendor
		// of the provider it matches, "Custom" for magpie's: still magpie's
		// model by its key, which a sync must not add a second time
		if !ok && e.ID != "" && keys[e.APIKey] {
			ok = true
		}
		if ok {
			if at < 0 {
				at = len(kept)
			}
			if !away && remote == "" && onAnotherMachine(e.URL) {
				remote, remoteKey = e.URL, e.APIKey
			}
			ours = append(ours, e.ID)
			if e.Disabled != nil && *e.Disabled {
				off[e.ID] = true
			}
			if r := e.Reasoning; r != nil {
				effort[e.ID] = cmp.Or(r.DefaultEffort, r.Effort)
			}
			continue
		}
		kept = append(kept, raw)
	}
	if !on && len(ours) == 0 {
		return nil
	}
	var add []json.RawMessage
	var ids []string
	if on {
		for _, m := range sharedModels(agent) {
			e := buddyModel(agent, m.ID, m.Name, m.Context, outputOr(m, zcodeOutput(maxTokens(m))), m.Images, m.Efforts)
			if off[m.ID] {
				e["disabled"] = true
			}
			// the default effort set in WorkBuddy's model settings (or by
			// hand) stays while the model still has it, Auto included
			if r, ok := e["reasoning"].(map[string]any); ok {
				if was, set := effort[m.ID]; set {
					if was == "" {
						delete(r, "defaultEffort")
					} else {
						r["defaultEffort"] = keptEffort(was, r["supportedEfforts"].([]string), r["defaultEffort"].(string))
					}
				}
			}
			if away {
				e["url"], e["apiKey"] = where.v1()+"/chat/completions", agentKeyAt(agent, where.gw())
			}
			if remote != "" {
				e["url"] = remote
				if remoteKey != "" {
					e["apiKey"] = remoteKey
				}
			}
			b, err := json.Marshal(e)
			if err != nil {
				return err
			}
			add = append(add, b)
			ids = append(ids, m.ID)
		}
	}
	if at < 0 {
		at = len(kept)
	}
	models := slices.Concat(kept[:at], add, kept[at:])
	if models == nil {
		models = []json.RawMessage{}
	}

	var out any = models
	if d.rest != nil {
		// a list of the models to show keeps showing magpie's
		if raw, ok := d.rest["availableModels"]; ok {
			var avail []string
			if json.Unmarshal(raw, &avail) == nil && len(avail) > 0 {
				avail = slices.DeleteFunc(avail, func(id string) bool { return slices.Contains(ours, id) })
				avail = append(avail, ids...)
				b, _ := json.Marshal(avail)
				d.rest["availableModels"] = b
			}
		}
		b, err := json.Marshal(models)
		if err != nil {
			return err
		}
		d.rest["models"] = b
		out = d.rest
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return edit.WriteAtomic(path, append(b, '\n'))
}

// buddySmallWindow is the smaller context WorkBuddy offers beside a
// model's whole window, the size its own models offer.
const buddySmallWindow = 200000

// buddyModel is one of magpie's models as a models.json entry, under
// agent's key.
func buddyModel(agent, id, name string, context, output int, images bool, efforts []string) map[string]any {
	if context == 0 {
		context = unknownWindow(200000)
	}
	e := map[string]any{
		"id": id, "name": name, "vendor": magpieID,
		"apiKey": agentKey(agent), "url": gatewayV1() + "/chat/completions",
		"maxInputTokens":   context,
		"supportsToolCall": true, "supportsImages": images, "supportsReasoning": false,
	}
	// WorkBuddy offers a choice of context in its model menu only for a
	// model whose contextWindow lists two sizes or more (its own Opus has
	// 200K and 1M); the session's budget is defaultLength, or the smallest
	// size when it is missing, so it is set to the whole window as before
	if context > buddySmallWindow {
		e["contextWindow"] = map[string]any{"supportedLengths": []int{buddySmallWindow, context}, "defaultLength": context}
	}
	// without it WorkBuddy caps every reply at its own default; output
	// comes already trimmed (zcodeOutput) unless the user set it (outputOr)
	if out := output; out > 0 {
		e["maxOutputTokens"] = out
	}
	var levels []string
	for _, l := range efforts {
		if l != "none" && !slices.Contains(levels, l) {
			levels = append(levels, l)
		}
	}
	if len(levels) > 0 {
		e["supportsReasoning"] = true
		e["reasoning"] = map[string]any{"supportedEfforts": levels,
			"canDisableThinking": slices.Contains(efforts, "none"), "defaultEffort": zcodeDefaultLevel(levels)}
	}
	return e
}

// keptEffort is the default effort a sync leaves on one of magpie's models:
// the one the user set (in the agent's own settings, or by hand) while the
// model still offers it, else magpie's def. magpie's default never
// overrides the user's own.
func keptEffort(was string, levels []string, def string) string {
	if was != "" && slices.Contains(levels, was) {
		return was
	}
	return def
}
