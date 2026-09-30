package agent

// OpenHanako (HanaAgent, a desktop personal agent) keeps what it knows under
// $HANA_HOME, ~/.hanako by default:
//
//	provider-catalog.json                  {"catalogVersion":2,"providers":{"<id>":{…}},…}: the providers it reaches
//	provider-plugins/<id>/providers/<id>.json  a provider added by hand, moved out of the catalog at start-up
//	                                       (all but its key and headers, which stay in the catalog)
//	user/preferences.json                  primaryAgent: the agent it opens on
//	agents/<id>/config.yaml                that agent's settings; models.chat is {id, provider}
//	server-info.json                       {port, token, …}, while it runs
//
// magpie is one provider there, magpie, spoken to as chat completions at the
// gateway's /v1 with the catalog as its models; choosing one of them makes it
// the primary agent's chat model. While OpenHanako runs, both go through its
// local API, which saves them and reloads its models at once:
//
//	PUT /api/config               {"providers":{"magpie":{…}|null}}
//	PUT /api/agents/:id/config    {"models":{"chat":{"id","provider"}|null}}
//
// Otherwise magpie writes the files, which it reads when it starts. The
// model the agent had is stashed and put back when magpie steps out.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

// hanakoProbe gives up quickly, as OpenHanako's own liveness probe does:
// a server that doesn't answer at once isn't running.
var hanakoProbe = &http.Client{Timeout: 1500 * time.Millisecond}

// hanakoClient waits longer: a provider change reloads every model.
var hanakoClient = &http.Client{Timeout: 15 * time.Second}

func hanakoHome(home string) string {
	dir := os.Getenv("HANA_HOME")
	if dir == "" {
		return filepath.Join(home, ".hanako")
	}
	if rest, ok := strings.CutPrefix(dir, "~"); ok && (rest == "" || rest[0] == '/' || rest[0] == filepath.Separator) {
		dir = home + rest
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	return dir
}

func hanako(home string) *Agent {
	dir := hanakoHome(home)
	path := filepath.Join(dir, "provider-catalog.json")
	// cfg is the primary agent's config, "" when there is no agent yet
	cfg := func() string {
		if id := hanakoAgent(dir); id != "" {
			return filepath.Join(dir, "agents", id, "config.yaml")
		}
		return ""
	}
	chat := func() (provider, id string) {
		c := cfg()
		if c == "" {
			return "", ""
		}
		provider, _ = edit.GetYAML(c, "models.chat.provider")
		if id, _ = edit.GetYAML(c, "models.chat.id"); id == "" {
			id, _ = edit.GetYAML(c, "models.chat") // before OpenHanako's migration #5, a plain id
		}
		return provider, id
	}
	onMagpie := func() bool { p, _ := chat(); return p == magpieID }
	key := func() string { return "hanako:" + dir + ":" + hanakoAgent(dir) + ":chat" }
	return &Agent{
		ID: "hanako", Name: "OpenHanako", Icon: "hanako", Aliases: []string{"openhanako", "hana", "hanaagent"},
		// what its provider client sends, though the key names it first
		UA:  []string{"hanaagent"},
		Dir: dir, Path: path,
		Sync: func() error {
			cur, ok := hanakoCurrent(dir)
			if !ok || hanakoSame(cur, hanakoProvider()) {
				return nil
			}
			return hanakoSave(dir, hanakoProvider())
		},
		Check: func() string {
			if !onMagpie() {
				return ""
			}
			cur, _ := hanakoCurrent(dir)
			return wiringOff("OpenHanako", path, func(k string) (string, bool) {
				v, ok := cur[k].(string)
				return v, ok
			}, "base_url", gatewayV1(), "api_key", gateway.TokenFor("hanako"))
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: func() string {
				p, id := chat()
				if p == "" || id == "" {
					return id
				}
				return p + "/" + id
			},
			Set: func(v string) error {
				agent := hanakoAgent(dir)
				if v == "" {
					if onMagpie() {
						if err := hanakoChat(dir, agent, unstash(key())); err != nil {
							return err
						}
					}
					return hanakoRemove(dir)
				}
				if agent == "" {
					return errors.New("OpenHanako has no agent yet — open it once to make one")
				}
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					if p, id := chat(); p != magpieID && id != "" {
						prev := id
						if p != "" {
							prev = p + "/" + id
						}
						stash(map[string]string{key(): prev})
					}
					if err := hanakoSave(dir, hanakoProvider()); err != nil {
						return err
					}
					return hanakoChat(dir, agent, magpieID+"/"+ref)
				}
				forget(key())
				return hanakoChat(dir, agent, v)
			},
			Options: func(cur map[string]string) []Option {
				return append(hanakoOwn(dir, cur["model"]), viaMagpie("hanako", magpieID+"/")...)
			},
		}},
	}
}

// hanakoAgent is the agent OpenHanako opens on: primaryAgent, if it has a
// config, else the first agent that does, as its engine picks at start-up.
func hanakoAgent(dir string) string {
	agents := filepath.Join(dir, "agents")
	has := func(id string) bool {
		if id == "" || strings.ContainsAny(id, `/\`) {
			return false
		}
		_, err := os.Stat(filepath.Join(agents, id, "config.yaml"))
		return err == nil
	}
	if id, _ := edit.GetJSON(filepath.Join(dir, "user", "preferences.json"), "primaryAgent"); has(id) {
		return id
	}
	es, _ := os.ReadDir(agents)
	for _, e := range es {
		if e.IsDir() && has(e.Name()) {
			return e.Name()
		}
	}
	return ""
}

type hanakoModel struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	Context   int    `json:"context,omitempty"`
	MaxOutput int    `json:"maxOutput,omitempty"`
	Image     bool   `json:"image"`
	Reasoning bool   `json:"reasoning"`
	XHigh     bool   `json:"xhigh,omitempty"`
}

type hanakoProviderEntry struct {
	DisplayName string        `json:"display_name"`
	BaseURL     string        `json:"base_url"`
	API         string        `json:"api"`
	APIKey      string        `json:"api_key"`
	Models      []hanakoModel `json:"models"`
}

// hanakoProvider is magpie's entry in the catalog. OpenHanako runs on Pi's
// SDK, whose chat completions carry the effort as reasoning_effort, as for
// Pi itself; image and reasoning are always said, since a model entry saved
// over an older one keeps what the new one leaves out.
func hanakoProvider() hanakoProviderEntry {
	ms := []hanakoModel{}
	for _, m := range magpieModels("hanako") {
		ms = append(ms, hanakoModel{ID: m.ID, Name: m.Name, Context: m.Context, MaxOutput: maxTokens(m),
			Image: m.Images, Reasoning: len(m.Efforts) > 0, XHigh: slices.Contains(m.Efforts, "xhigh")})
	}
	return hanakoProviderEntry{DisplayName: magpieID, BaseURL: gatewayV1(), API: "openai-completions",
		APIKey: gateway.TokenFor("hanako"), Models: ms}
}

func hanakoPlugin(dir string) string { return filepath.Join(dir, "provider-plugins", magpieID) }

// hanakoCurrent is magpie's provider as OpenHanako sees it: the definition
// in its plugin file, if it has moved there, under what the catalog says.
func hanakoCurrent(dir string) (map[string]any, bool) {
	cur := map[string]any{}
	found := false
	if b, err := os.ReadFile(filepath.Join(hanakoPlugin(dir), "providers", magpieID+".json")); err == nil {
		var p struct {
			DisplayName string `json:"displayName"`
			BaseURL     string `json:"defaultBaseUrl"`
			API         string `json:"defaultApi"`
			Models      []any  `json:"models"`
		}
		if json.Unmarshal(b, &p) == nil {
			cur["display_name"], cur["base_url"], cur["api"] = p.DisplayName, p.BaseURL, p.API
			if p.Models != nil {
				cur["models"] = p.Models
			}
			found = true
		}
	}
	if raw, ok := edit.GetJSON(filepath.Join(dir, "provider-catalog.json"), "providers."+magpieID); ok {
		var o map[string]any
		if json.Unmarshal([]byte(raw), &o) == nil {
			for k, v := range o {
				cur[k] = v
			}
			found = true
		}
	}
	return cur, found
}

// hanakoSame reports whether the provider says all that want does; keys
// the user added (headers, say) don't count.
func hanakoSame(cur map[string]any, want hanakoProviderEntry) bool {
	b, _ := json.Marshal(want)
	var w map[string]any
	json.Unmarshal(b, &w)
	for k, v := range w {
		c := cur[k]
		if l, _ := v.([]any); k == "models" && len(l) == 0 {
			if l, _ := c.([]any); len(l) == 0 {
				continue
			}
		}
		if !reflect.DeepEqual(c, v) {
			return false
		}
	}
	return true
}

// hanakoSave writes magpie's provider: through OpenHanako's API while it
// runs, else into the catalog, whence it moves the definition into a plugin
// of its own at start-up. An old plugin goes, or its models would be kept
// beside the new ones.
func hanakoSave(dir string, p hanakoProviderEntry) error {
	if s := hanakoLive(dir); s != nil {
		return s.put("/api/config", map[string]any{"providers": map[string]any{magpieID: p}})
	}
	path := filepath.Join(dir, "provider-catalog.json")
	raw, err := edit.Read(path)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		// it holds keys: only its owner reads it, and a catalog without
		// its version is one OpenHanako refuses
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte("{\n  \"catalogVersion\": 2,\n  \"providers\": {}\n}\n"), 0o600); err != nil {
			return err
		}
	}
	kvs := []edit.KV{{Path: "providers." + magpieID, Value: p}}
	if ids, ok := hanakoDeleted(path); ok && slices.Contains(ids, magpieID) {
		kvs = append(kvs, edit.KV{Path: "meta.deletedProviders", Value: slices.DeleteFunc(ids, func(id string) bool { return id == magpieID })})
	}
	if err := edit.SetJSON(path, kvs...); err != nil {
		return err
	}
	return os.RemoveAll(hanakoPlugin(dir))
}

// hanakoRemove takes magpie's provider out, if it is there. It is marked
// deleted, as OpenHanako marks one it removes: else its start-up would
// bring it back from its models.json, which still has it.
func hanakoRemove(dir string) error {
	if _, ok := hanakoCurrent(dir); !ok {
		return nil
	}
	if s := hanakoLive(dir); s != nil {
		return s.put("/api/config", map[string]any{"providers": map[string]any{magpieID: nil}})
	}
	path := filepath.Join(dir, "provider-catalog.json")
	if err := edit.DelJSON(path, "providers."+magpieID); err != nil {
		return err
	}
	// with no catalog there is nothing to mark: OpenHanako starts a new one
	if ids, _ := hanakoDeleted(path); !slices.Contains(ids, magpieID) && hanakoExists(path) {
		if err := edit.SetJSON(path, edit.KV{Path: "meta.deletedProviders", Value: append(ids, magpieID)}); err != nil {
			return err
		}
	}
	return os.RemoveAll(hanakoPlugin(dir))
}

func hanakoExists(path string) bool { _, err := os.Stat(path); return err == nil }

// hanakoDeleted is the catalog's meta.deletedProviders.
func hanakoDeleted(path string) ([]string, bool) {
	v, ok := edit.GetJSON(path, "meta.deletedProviders")
	if !ok {
		return nil, false
	}
	var ids []string
	return ids, json.Unmarshal([]byte(v), &ids) == nil
}

// hanakoChat sets an agent's chat model, "provider/id" split at the first
// slash as OpenHanako splits it; "" takes it out.
func hanakoChat(dir, agent, v string) error {
	if agent == "" {
		return nil
	}
	var chat any
	if p, id, ok := strings.Cut(v, "/"); ok {
		chat = map[string]string{"id": id, "provider": p}
	} else if v != "" {
		chat = v
	}
	if s := hanakoLive(dir); s != nil {
		return s.put("/api/agents/"+agent+"/config", map[string]any{"models": map[string]any{"chat": chat}})
	}
	path := filepath.Join(dir, "agents", agent, "config.yaml")
	if chat == nil {
		return edit.DelYAML(path, "models.chat")
	}
	return edit.SetYAML(path, edit.KV{Path: "models.chat", Value: chat})
}

// hanakoServer is a running OpenHanako's API, as server-info.json gives it.
type hanakoServer struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
}

// hanakoLive is OpenHanako's API if it answers there with its own token,
// nil if it isn't running: server-info.json outlives a crash.
func hanakoLive(dir string) *hanakoServer {
	b, err := os.ReadFile(filepath.Join(dir, "server-info.json"))
	if err != nil {
		return nil
	}
	var s hanakoServer
	if json.Unmarshal(b, &s) != nil || s.Port <= 0 || s.Token == "" {
		return nil
	}
	req, err := http.NewRequest("GET", s.url("/api/server/identity"), nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	resp, err := hanakoProbe.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var id struct {
		ServerID string `json:"serverId"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&id) != nil || id.ServerID == "" {
		return nil
	}
	return &s
}

func (s *hanakoServer) url(path string) string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", s.Port, path)
}

func (s *hanakoServer) put(path string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("PUT", s.url(path), bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.Token)
	resp, err := hanakoClient.Do(req)
	if err != nil {
		return fmt.Errorf("OpenHanako: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 == 2 {
		return nil
	}
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	msg := strings.TrimSpace(string(out))
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(out, &e) == nil && e.Error != "" {
		msg = e.Error
	}
	return fmt.Errorf("OpenHanako: %s %s: %s", resp.Status, path, msg)
}

// hanakoOwn lists the models of OpenHanako's own providers, those the user
// added in it: the catalog's and its plugins'. The providers built into it
// list theirs in the app, out of magpie's sight.
func hanakoOwn(dir, cur string) []Option {
	models := map[string][]string{}
	add := func(p string, ms []any) {
		if p == magpieID {
			return
		}
		for _, m := range ms {
			id, _ := m.(string)
			if o, ok := m.(map[string]any); ok {
				id, _ = o["id"].(string)
			}
			if id != "" && !slices.Contains(models[p], id) {
				models[p] = append(models[p], id)
			}
		}
	}
	if raw, ok := edit.GetJSON(filepath.Join(dir, "provider-catalog.json"), "providers"); ok {
		var ps map[string]struct {
			Models []any `json:"models"`
		}
		json.Unmarshal([]byte(raw), &ps)
		for p, e := range ps {
			add(p, e.Models)
		}
	}
	files, _ := filepath.Glob(filepath.Join(dir, "provider-plugins", "*", "providers", "*.json"))
	for _, f := range files {
		var p struct {
			ID     string `json:"id"`
			Models []any  `json:"models"`
		}
		if b, err := os.ReadFile(f); err == nil && json.Unmarshal(b, &p) == nil {
			add(p.ID, p.Models)
		}
	}
	if p, id, ok := strings.Cut(cur, "/"); ok && p != magpieID {
		add(p, []any{id})
	}
	providers := make([]string, 0, len(models))
	for p := range models {
		providers = append(providers, p)
	}
	sort.Strings(providers)
	var out []Option
	for _, p := range providers {
		name := catalog.ProviderName(p)
		if name == "" {
			name = p
		}
		for _, id := range models[p] {
			out = append(out, Option{Value: p + "/" + id, Label: id, Icon: modelIcon(p, id), Group: name})
		}
	}
	return out
}
