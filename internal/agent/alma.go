package agent

// Alma (a desktop AI chat app) keeps its providers and settings in the app,
// not in a file magpie can edit: they are reached through its local REST API
// (http://localhost:23001, while Alma runs).
//
//	GET  /api/providers              [{"id","name","type","baseURL","enabled","models":["<id>",…],"availableModels":[{"id","name",…}]},…]
//	POST /api/providers              {"name","type","apiKey","baseURL","enabled"} → the provider
//	PUT  /api/providers/:id          the fields to change
//	PUT  /api/providers/:id/models   {"models":["<id>",…],"availableModels":[{"id","name","capabilityOverrides"}]}:
//	                                 models, kept as given, are the ones Alma offers; a model's
//	                                 capabilityOverrides, when sent, take the place of the ones it had
//	GET  /api/settings, PUT it back  the whole settings; chat.defaultModel is "<providerId>:<model>",
//	                                 imageGen.model the same ("" for Auto)
//
// magpie is one provider there, named magpie, of type openai at the
// gateway's /v1; choosing one of its models makes it Alma's default model,
// and the model magpie draws with Alma's image generation model when Alma
// has none picked (almaSetImage).
// Alma not running is no error: there is nothing to read or keep current.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/gateway"
)

// almaAPI is where Alma's API answers. Under test it is nothing, so no test
// reaches a real Alma; a test points it at a fake one.
var almaAPI = func() string {
	if testing.Testing() {
		return ""
	}
	return "http://localhost:23001"
}()

// almaClient gives up quickly: a local app that doesn't answer at once isn't
// running, or is stuck.
var almaClient = &http.Client{Timeout: 1500 * time.Millisecond}

// errAlmaDown is Alma not answering.
var errAlmaDown = errors.New("Alma isn't running — open Alma and try again")

// almaProvider is what magpie reads of one of Alma's providers.
type almaProvider struct {
	ID      string           `json:"id"`
	Name    string           `json:"name"`
	Type    string           `json:"type"`
	BaseURL string           `json:"baseURL"`
	APIKey  string           `json:"apiKey"`
	Enabled bool             `json:"enabled"`
	Models  almaModelIDs     `json:"models"`
	Known   []map[string]any `json:"availableModels"`
}

// almaModelIDs is a provider's models, the ids Alma offers. Alma keeps
// them as ids or, for some providers, as the models themselves
// ({"id","name","enabled","capabilityOverrides",…}), and reads either as
// its id; so does magpie — one provider listing objects left every one
// of Alma's unread ("cannot unmarshal object into Go struct field
// almaProvider.models of type string"), and Alma showed as not set.
type almaModelIDs []string

func (ids *almaModelIDs) UnmarshalJSON(b []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		*ids = nil
		return nil
	}
	out := almaModelIDs{}
	for _, r := range raw {
		var id string
		if json.Unmarshal(r, &id) != nil {
			var m struct {
				ID any `json:"id"`
			}
			if json.Unmarshal(r, &m) != nil || m.ID == nil {
				continue
			}
			id = fmt.Sprint(m.ID)
		}
		if id != "" {
			out = append(out, id)
		}
	}
	*ids = out
	return nil
}

// almaDo sends one request to Alma's API and decodes its reply into out.
func almaDo(method, path string, body, out any) error {
	return almaAsk(method, path, body, out, false)
}

// almaLook is a GET that may be answered with what Alma said a moment ago,
// for showing what Alma is on: its providers run to most of a megabyte and
// take most of a second, and one look at the Agents page asks for them
// several times over. What is changed goes by almaDo's own GETs, never
// by these.
func almaLook(path string, out any) error {
	return almaAsk("GET", path, nil, out, true)
}

func almaAsk(method, path string, body, out any, recent bool) error {
	if almaAPI == "" {
		return errAlmaDown
	}
	b, err := almaSend(method, path, body, recent)
	if err != nil {
		return err
	}
	if out == nil || len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	return json.Unmarshal(b, out)
}

// almaRead is what Alma last answered each GET with, for almaLook. Any
// change sent to Alma forgets it all.
var almaRead struct {
	sync.Mutex
	at   map[string]time.Time
	body map[string][]byte
	gen  int // changes sent so far: a GET answered across one is not kept
}

// almaReadFor is how long almaLook takes an answer as current. Tests,
// which change their fake Alma behind magpie's back, see every change.
var almaReadFor = func() time.Duration {
	if testing.Testing() {
		return 0
	}
	return 5 * time.Second
}()

func almaSend(method, path string, body any, recent bool) ([]byte, error) {
	key := almaAPI + path
	almaRead.Lock()
	if method != "GET" {
		almaRead.at, almaRead.body = nil, nil
		almaRead.gen++
	} else if at, ok := almaRead.at[key]; ok && recent && time.Since(at) < almaReadFor {
		b := almaRead.body[key]
		almaRead.Unlock()
		return b, nil
	}
	gen := almaRead.gen
	almaRead.Unlock()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, strings.TrimRight(almaAPI, "/")+path, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := almaClient.Do(req)
	if err != nil {
		return nil, errAlmaDown
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(b))
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		return nil, fmt.Errorf("Alma: %s %s: %d %s", method, path, resp.StatusCode, msg)
	}
	if method == "GET" {
		almaRead.Lock()
		if almaRead.gen == gen {
			if almaRead.at == nil {
				almaRead.at, almaRead.body = map[string]time.Time{}, map[string][]byte{}
			}
			almaRead.at[key], almaRead.body[key] = time.Now(), b
		}
		almaRead.Unlock()
	}
	return b, nil
}

// almaProviders lists Alma's providers; seen, as almaLook has them.
func almaProviders() ([]almaProvider, error) {
	var ps []almaProvider
	err := almaDo("GET", "/api/providers", nil, &ps)
	return ps, err
}

func almaProvidersSeen() ([]almaProvider, error) {
	var ps []almaProvider
	err := almaLook("/api/providers", &ps)
	return ps, err
}

// almaMagpie is magpie's provider among Alma's: the one named magpie, or
// an OpenAI-shaped one at the gateway. Nil if there is none.
func almaMagpie(ps []almaProvider) *almaProvider {
	for i := range ps {
		if ps[i].Name == magpieID && (ps[i].Type == "openai" || ps[i].Type == "custom") {
			return &ps[i]
		}
	}
	for i := range ps {
		if (ps[i].Type == "openai" || ps[i].Type == "custom") && ps[i].BaseURL != "" && sameHost(ps[i].BaseURL, gatewayV1()) {
			return &ps[i]
		}
	}
	return nil
}

// almaModels is magpie's catalog as Alma is shown it: the ids Alma offers,
// and each one's name and what magpie knows it can do (almaCaps).
func almaModels() (ids []string, known []map[string]any) {
	ids, known = []string{}, []map[string]any{}
	for _, m := range magpieModels("alma") {
		ids = append(ids, m.ID)
		k := map[string]any{"id": m.ID, "name": m.Name}
		if caps := almaCaps(m); len(caps) > 0 {
			k["capabilityOverrides"] = caps
		}
		known = append(known, k)
	}
	// the model magpie draws with, which Alma offers for its image
	// generation, said to make images (Settings › Image Generation lists
	// a provider's models that make images, and shows one it doesn't list
	// as unavailable)
	if d := gateway.Drawer(); d != "" {
		if i := slices.Index(ids, d); i >= 0 {
			caps, _ := known[i]["capabilityOverrides"].(map[string]any)
			if caps == nil {
				caps = map[string]any{}
				known[i]["capabilityOverrides"] = caps
			}
			caps["imageOutput"] = true
		} else {
			_, name, _ := strings.Cut(d, "/")
			ids = append(ids, d)
			known = append(known, map[string]any{"id": d, "name": name, "capabilityOverrides": map[string]any{"imageOutput": true}})
		}
	}
	return ids, known
}

// almaLevels are the reasoning levels Alma knows, in its order: its own
// read of a provider's /models keeps these and drops the rest.
var almaLevels = []string{"low", "medium", "high", "xhigh", "max"}

// almaCaps is what Alma is told of m, as the model's capabilityOverrides.
// Alma works a model's capabilities out from models.dev by its id, and
// magpie's ids (codex/gpt-5.5) aren't models.dev's, so such a model came
// out with no reasoning and Alma offered no thinking for it (#730). A model
// that reasons says so, with those of its levels Alma knows, and its
// window, output limit and images when magpie knows them.
func almaCaps(m catalog.Model) map[string]any {
	caps := map[string]any{}
	if m.Reasoning || len(m.Efforts) > 0 {
		caps["reasoning"] = true
		var levels []string
		for _, l := range almaLevels {
			if slices.Contains(m.Efforts, l) {
				levels = append(levels, l)
			}
		}
		if len(levels) > 0 {
			caps["reasoningLevels"] = levels
		}
	}
	if m.Images {
		caps["vision"] = true
	}
	if m.Context > 0 {
		caps["contextWindow"] = m.Context
	}
	if n := maxTokens(m); n > 0 {
		caps["maxOutputTokens"] = n
	}
	// what a call costs, as magpie's usage pages count it: Alma prices a
	// model by models.dev's id too, so it counted every one of magpie's at
	// $0, "unpriced" (#919). Its pricing is USD per million tokens, as
	// magpie's is, with one cache write price: the 5-minute one.
	if p := m.Price; p != nil {
		pr := map[string]any{"input": p.Input, "output": p.Output}
		if p.CacheRead > 0 {
			pr["cacheRead"] = p.CacheRead
		}
		if p.CacheWrite > 0 {
			pr["cacheWrite"] = p.CacheWrite
		}
		caps["pricing"] = pr
	}
	return caps
}

// almaWithOverrides is known with each model's overrides laid over the ones
// it has in Alma: the overrides sent take the place of a model's, so one
// the user set there that magpie says nothing of (functionCalling,
// pricing) is sent back as it was.
func almaWithOverrides(p *almaProvider, known []map[string]any) []map[string]any {
	had := map[string]map[string]any{}
	for _, k := range p.Known {
		id, _ := k["id"].(string)
		if o, ok := k["capabilityOverrides"].(map[string]any); ok && id != "" {
			had[id] = o
		}
	}
	out := make([]map[string]any, len(known))
	for i, k := range known {
		id, _ := k["id"].(string)
		o := had[id]
		mine, ok := k["capabilityOverrides"].(map[string]any)
		if len(o) == 0 || !ok {
			// nothing of magpie's to say: no overrides sent, and Alma
			// keeps the model's own
			out[i] = k
			continue
		}
		merged := map[string]any{}
		for key, v := range o {
			merged[key] = v
		}
		for key, v := range mine {
			merged[key] = v
		}
		kk := map[string]any{}
		for key, v := range k {
			kk[key] = v
		}
		kk["capabilityOverrides"] = merged
		out[i] = kk
	}
	return out
}

// almaSameModels reports whether Alma already offers these models, in this
// order, each under its name and with what magpie says of it.
func almaSameModels(p *almaProvider, ids []string, known []map[string]any) bool {
	if !slices.Equal(p.Models, ids) || len(p.Known) != len(known) {
		return false
	}
	for i, k := range known {
		if p.Known[i]["id"] != k["id"] || p.Known[i]["name"] != k["name"] {
			return false
		}
		mine, _ := k["capabilityOverrides"].(map[string]any)
		had, _ := p.Known[i]["capabilityOverrides"].(map[string]any)
		for key, v := range mine {
			if !sameJSONValue(had[key], v) {
				return false
			}
		}
	}
	return true
}

// sameJSONValue reports whether a and b read the same as JSON: a value
// read back from Alma (float64, []any) against one magpie made (int,
// []string).
func sameJSONValue(a, b any) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && bytes.Equal(x, y)
}

// almaSyncModels puts magpie's catalog into its provider in Alma, if it
// isn't there already.
func almaSyncModels(p *almaProvider) error {
	ids, known := almaModels()
	if almaSameModels(p, ids, known) {
		return nil
	}
	return almaDo("PUT", "/api/providers/"+p.ID+"/models", map[string]any{"models": ids, "availableModels": almaWithOverrides(p, known)}, nil)
}

// almaWire makes sure Alma has magpie's provider, pointed at the gateway,
// turned on and listing the catalog, and returns its id.
func almaWire() (string, error) {
	ps, err := almaProviders()
	if err != nil {
		return "", err
	}
	p := almaMagpie(ps)
	if p == nil {
		var made almaProvider
		if err := almaDo("POST", "/api/providers", map[string]any{"name": magpieID, "type": "openai",
			"apiKey": almaKey(), "baseURL": gatewayV1(), "enabled": true}, &made); err != nil {
			return "", err
		}
		if made.ID == "" {
			// a reply without the provider: look it up
			if ps, err = almaProviders(); err != nil {
				return "", err
			}
			if p = almaMagpie(ps); p == nil {
				return "", errors.New("Alma didn't keep magpie's provider")
			}
			made = *p
		}
		p = &made
	} else if p.BaseURL != gatewayV1() || !p.Enabled {
		if err := almaDo("PUT", "/api/providers/"+p.ID, map[string]any{"baseURL": gatewayV1(),
			"apiKey": almaKey(), "enabled": true}, nil); err != nil {
			return "", err
		}
	} else if err := almaKeyed(p); err != nil {
		return "", err
	}
	return p.ID, almaSyncModels(p)
}

// almaKey is the key magpie's provider in Alma is given. Alma's requests
// carry the AI SDK's User-Agent and nothing of Alma's, so the key is how
// the gateway knows them for Alma's.
func almaKey() string { return gateway.TokenFor("alma") }

// almaKeyed gives magpie's provider the key almaKey in place of the one it
// was given before there was one, gateway.Token; a key Alma gives back
// some other way (encrypted) is left be. The key alone is sent: a baseURL,
// even the same one, would have Alma drop its models.
func almaKeyed(p *almaProvider) error {
	if p.APIKey != gateway.Token {
		return nil
	}
	return almaDo("PUT", "/api/providers/"+p.ID, map[string]any{"apiKey": almaKey()}, nil)
}

// almaSettings reads Alma's whole settings, every key kept as it is;
// seen, as almaLook has them.
func almaSettings() (map[string]any, error) { return almaSettingsOf(false) }

func almaSettingsSeen() (map[string]any, error) { return almaSettingsOf(true) }

func almaSettingsOf(recent bool) (map[string]any, error) {
	var s map[string]any
	if err := almaAsk("GET", "/api/settings", nil, &s, recent); err != nil {
		return nil, err
	}
	if s == nil {
		s = map[string]any{}
	}
	return s, nil
}

func almaDefault(s map[string]any) string {
	chat, _ := s["chat"].(map[string]any)
	v, _ := chat["defaultModel"].(string)
	return v
}

// almaSetDefault sets chat.defaultModel, putting the rest of the settings
// back as they were: Alma takes only the whole object.
func almaSetDefault(v string) error {
	s, err := almaSettings()
	if err != nil {
		return err
	}
	if almaDefault(s) == v {
		return nil
	}
	chat, ok := s["chat"].(map[string]any)
	if !ok {
		chat = map[string]any{}
		s["chat"] = chat
	}
	chat["defaultModel"] = v
	return almaDo("PUT", "/api/settings", s, nil)
}

// almaImage is Alma's image generation model, "" for Auto.
func almaImage(s map[string]any) string {
	ig, _ := s["imageGen"].(map[string]any)
	v, _ := ig["model"].(string)
	return v
}

// almaSetImage makes the model magpie draws with Alma's image generation
// model (Sorghum on Discord), on magpie's provider pid, when Alma has one
// of magpie's that magpie no longer offers it (the drawer it was given
// before, almaModels) or, with fill, none picked (Auto): fill is the user
// choosing one of magpie's models for Alma, not Sync, which leaves an Auto
// the user may have gone back to. One the user picked, theirs or magpie's,
// is left as it is. Nothing when magpie draws with none.
func almaSetImage(pid string, fill bool) error {
	d := gateway.Drawer()
	if d == "" {
		return nil
	}
	s, err := almaSettings()
	if err != nil {
		return err
	}
	cur, want := almaImage(s), pid+":"+d
	if cur == want {
		return nil
	}
	if cur == "" && !fill {
		return nil
	}
	if cur != "" {
		ref, ours := strings.CutPrefix(cur, pid+":")
		if !ours {
			return nil
		}
		if ids, _ := almaModels(); slices.Contains(ids, ref) {
			return nil
		}
	}
	return almaPutImage(s, want)
}

// almaPutImage sets imageGen.model to v, the rest of the settings put
// back as they were.
func almaPutImage(s map[string]any, v string) error {
	ig, ok := s["imageGen"].(map[string]any)
	if !ok {
		ig = map[string]any{}
		s["imageGen"] = ig
	}
	ig["model"] = v
	return almaDo("PUT", "/api/settings", s, nil)
}

// almaGet is Alma's default model, one of magpie's as magpie/<model>. With
// Alma not running it is what magpie last set there, so that isn't taken
// for something else having changed it.
func almaGet() string {
	s, err := almaSettingsSeen()
	if err != nil {
		return appliedOf("alma").Fields["model"]
	}
	v := almaDefault(s)
	pid, model, ok := strings.Cut(v, ":")
	if !ok {
		return v
	}
	ps, err := almaProvidersSeen()
	if err != nil {
		return appliedOf("alma").Fields["model"]
	}
	if p := almaMagpie(ps); p != nil && p.ID == pid {
		return magpieID + "/" + model
	}
	return v
}

func almaSet(v string) error {
	if v == "" {
		// back to Alma's own: its default model is its to pick, and
		// magpie's provider comes out
		s, err := almaSettings()
		if err != nil {
			return err
		}
		ps, err := almaProviders()
		if err != nil {
			return err
		}
		p := almaMagpie(ps)
		if p == nil {
			return nil
		}
		if pid, _, _ := strings.Cut(almaDefault(s), ":"); pid == p.ID {
			if err := almaSetDefault(""); err != nil {
				return err
			}
		}
		// an image generation model of magpie's goes back to Auto too
		if pid, _, _ := strings.Cut(almaImage(s), ":"); pid == p.ID {
			if s, err = almaSettings(); err != nil {
				return err
			}
			if err := almaPutImage(s, ""); err != nil {
				return err
			}
		}
		return almaDo("DELETE", "/api/providers/"+p.ID, nil, nil)
	}
	if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok {
		id, err := almaWire()
		if err != nil {
			return err
		}
		if err := almaSetDefault(id + ":" + ref); err != nil {
			return err
		}
		return almaSetImage(id, true)
	}
	if _, _, ok := strings.Cut(v, ":"); !ok {
		return fmt.Errorf("expected providerId:model or magpie/<model>, got %q", v)
	}
	return almaSetDefault(v)
}

// almaOwn lists the models Alma reaches through its other providers.
func almaOwn() []Option {
	var ms []struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Provider   string `json:"provider"`
		ProviderID string `json:"providerId"`
	}
	if almaLook("/api/models", &ms) != nil {
		return nil
	}
	ps, _ := almaProvidersSeen()
	mine := ""
	if p := almaMagpie(ps); p != nil {
		mine = p.ID
	}
	var out []Option
	for _, m := range ms {
		pid, model, _ := strings.Cut(m.ID, ":")
		if m.ProviderID != "" {
			pid = m.ProviderID
		}
		if pid == mine {
			continue
		}
		out = append(out, Option{Value: m.ID, Label: m.Name, Group: m.Provider, Icon: modelIcon("", model)})
	}
	return out
}

// almaDir is where Alma keeps its data, there once Alma was installed and
// opened: ~/Library/Application Support/alma on a Mac, the user config
// folder elsewhere. On Linux alma-server, Alma without a desktop, keeps it
// in ALMA_DATA_DIR, else $XDG_DATA_HOME/alma (~/.local/share/alma), and
// answers on the same port: with only the desktop's folder looked for it
// was never found, so its magpie provider never got Alma's key and its
// requests read as the AI SDK's (Lutra.x on Discord). The desktop's folder
// comes first; "" when neither is there.
func almaDir(goos string) string {
	var dirs []string
	if d, err := os.UserConfigDir(); err == nil {
		dirs = append(dirs, filepath.Join(d, "alma"))
	}
	if goos != "darwin" && goos != "windows" {
		if d := appdir.Getenv("ALMA_DATA_DIR"); d != "" {
			dirs = append(dirs, d)
		}
		if d := appdir.Getenv("XDG_DATA_HOME"); d != "" {
			dirs = append(dirs, filepath.Join(d, "alma"))
		} else if h, err := os.UserHomeDir(); err == nil {
			dirs = append(dirs, filepath.Join(h, ".local", "share", "alma"))
		}
	}
	for _, d := range dirs {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return d
		}
	}
	if len(dirs) > 0 {
		return dirs[0]
	}
	return ""
}

// almaOS is the system almaDir looks for Alma's data on; a test sets it.
var almaOS = runtime.GOOS

func alma() *Agent {
	dir := almaDir(almaOS)
	return &Agent{
		ID: "alma", Name: "Alma", Icon: "alma",
		UA:  []string{"alma"},
		Dir: dir, Path: almaAPI,
		Check: func() string {
			s, err := almaSettingsSeen()
			if err != nil {
				return ""
			}
			ps, err := almaProvidersSeen()
			if err != nil {
				return ""
			}
			p := almaMagpie(ps)
			if p == nil {
				return ""
			}
			if pid, _, _ := strings.Cut(almaDefault(s), ":"); pid != p.ID {
				return ""
			}
			if !p.Enabled {
				return "Alma's magpie provider is turned off, so Alma won't use its models"
			}
			return wiringOff("Alma", "providers", func(k string) (string, bool) { return p.BaseURL, p.BaseURL != "" },
				"baseURL", gatewayV1())
		},
		Sync: func() error {
			if _, err := os.Stat(dir); almaAPI == "" || dir == "" || err != nil {
				return nil
			}
			ps, err := almaProviders()
			if err != nil {
				return nil // not running: nothing to keep current
			}
			p := almaMagpie(ps)
			if p == nil {
				return nil
			}
			if err := almaKeyed(p); err != nil {
				return err
			}
			if err := almaSyncModels(p); err != nil {
				return err
			}
			return almaSetImage(p.ID, false)
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: almaGet,
			Set: almaSet,
			Options: func(map[string]string) []Option {
				return append(almaOwn(), viaMagpie("alma", magpieID+"/")...)
			},
		}},
	}
}
