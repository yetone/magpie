package agent

// Nowledge Mem (mem.nowledge.co, a Tauri app, co.nowledge.mem.desktop;
// xiaomaomiloulou on X) is a personal memory app whose own AI (search,
// distilling, the chat) runs on a remote LLM picked on its first page,
// "选择一个 AI", and in Settings > AI. Mem 0.10 has a provider of magpie's
// own among its types: "magpie", at the gateway's default address
// http://127.0.0.1:3425/v1, which sends Bearer magpie-nowledge-mem when no
// key is set. So magpie's gateway already reads its requests as the agent
// nowledge-mem; what it lacked was the setting.
//
// Mem keeps its providers in remote_llm.json in its config folder
// (~/Library/Application Support/co.nowledge.mem.desktop on a Mac,
// %APPDATA%\co.nowledge.mem.desktop on Windows, the XDG config folder on
// Linux; $NMEM_APP_CONFIG_DIR), each key encrypted there (api_key_enc and
// api_key_nonce), so the file is never written by magpie. Mem's backend,
// nmem-server, changes it through its HTTP API on 127.0.0.1:14242 (the
// nmem CLI's $NMEM_API_URL), which takes loopback requests without a key,
// as Mem's own Settings and `nmem config provider` do (read at 0.10.121):
//
//	GET  /remote-llm/config            enabled, active_provider, providers{id: provider_type, model, api_base, has_api_key, …}
//	POST /remote-llm/config            {enabled, provider, provider_type, model, api_base, api_key, temperature, max_tokens, timeout}
//	POST /remote-llm/activate          {provider}
//	POST /remote-llm/provider/remove   {provider}
//	POST /remote-llm/enabled           {enabled}
//
// POST /remote-llm/config saves a provider and always makes it the active
// one; api_base and api_key left out keep what was stored, but
// temperature, max_tokens and timeout left out go back to 0.7, 8192 and
// 60, so the provider's own are sent again with every change.
//
// Picking a magpie model for Mem saves the provider "magpie" (type magpie)
// at the gateway's /v1 with Mem's own agent key and that model, and turns
// Mem's AI on. What Mem had before is stashed: its active provider,
// whether its AI was on, and a "magpie" provider the user had made
// themselves, with its values. Stepping out activates that provider again,
// puts the user's "magpie" back as it was or removes magpie's, and turns
// the AI off again if it was off.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/edit"
)

// memBundle is Mem's bundle id, the name of its config folder.
const memBundle = "co.nowledge.mem.desktop"

// memAddr is where Mem's backend listens unless $NMEM_API_URL says.
const memAddr = "http://127.0.0.1:14242"

// memDefaultBase is where Mem's magpie type goes with no api_base.
const memDefaultBase = "http://127.0.0.1:3425/v1"

// errMemDown is a change asked of a Mem that isn't running.
var errMemDown = errors.New("Nowledge Mem changes its AI only while it runs: open Nowledge Mem, then try again")

// memConfigDir is Mem's config folder: $NMEM_APP_CONFIG_DIR, else the
// app's folder in the system's config folder.
func memConfigDir(home string) string {
	if d := strings.TrimSpace(appdir.Getenv("NMEM_APP_CONFIG_DIR")); d != "" {
		return d
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", memBundle)
	case "windows":
		if d := appdir.Getenv("APPDATA"); d != "" {
			return filepath.Join(d, memBundle)
		}
		return filepath.Join(home, "AppData", "Roaming", memBundle)
	}
	if d := appdir.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, memBundle)
	}
	return filepath.Join(home, ".config", memBundle)
}

// memProvider is one of Mem's providers, as its API (provider_type) and
// its file (type) give it.
type memProvider struct {
	Type        string   `json:"provider_type,omitempty"`
	FileType    string   `json:"type,omitempty"`
	Model       string   `json:"model"`
	APIBase     string   `json:"api_base,omitempty"`
	HasKey      bool     `json:"has_api_key,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	MaxTokens   *int     `json:"max_tokens,omitempty"`
	Timeout     *float64 `json:"timeout,omitempty"`
}

func (p memProvider) kind() string {
	if p.Type != "" {
		return p.Type
	}
	return p.FileType
}

// memState is Mem's remote LLM setting.
type memState struct {
	Enabled   bool                   `json:"enabled"`
	Active    string                 `json:"active_provider"`
	Providers map[string]memProvider `json:"providers"`
}

// memPrev is what Mem had before magpie, as stashed.
type memPrev struct {
	Active  string       `json:"active,omitempty"`
	Enabled bool         `json:"enabled"`
	Had     *memProvider `json:"had,omitempty"`
}

// memOn says magpie's provider is the one Mem's AI uses. One named
// magpie at another machine's address is this magpie's when the address
// answers as this magpie (magpieAt), as a proxy in front of this gateway
// does (#1985), and never when it answers as another: a magpie on a
// server the user added or imported over magpie's own (Alex on Discord,
// 11-25 and 11-27: the cloud magpie, whose deepseek/pro this magpie
// serves too, still read as connected here while what magpie stashed was
// kept), as in Alma (almaMagpie). Until the address has answered it goes
// by what magpie set up (set: what Mem had is stashed) and the model, so
// a proxy isn't told replaced, and Reconnect offered over it, before it
// is asked.
func memOn(s memState, set bool) (memProvider, bool) {
	p, ok := s.Providers[magpieID]
	if !ok || !s.Enabled || s.Active != magpieID {
		return p, false
	}
	if !onAnotherMachine(memBase(p)) {
		return p, true
	}
	base, ok := proxyBase(memBase(p), gatewayV1())
	if !ok {
		return p, false
	}
	if yes, known := magpieAt(base); known {
		return p, yes
	}
	return p, set && isMagpie(p.Model)
}

// memAsked waits for magpie's answer at the address of Mem's provider
// named magpie, on another machine, before a write goes by memOn
// (magpieAsked).
func memAsked(s memState) {
	if p, ok := s.Providers[magpieID]; ok && onAnotherMachine(memBase(p)) {
		if base, ok := proxyBase(memBase(p), gatewayV1()); ok {
			magpieAsked(base)
		}
	}
}

// memElsewhere says p, Mem's provider named magpie, sends Mem's requests
// to another machine than magpie wrote: the user pointed it there since
// (a magpie on a server imported over magpie's, or a proxy of theirs that
// doesn't answer as this magpie), and it is theirs, never removed or
// written over on the way out.
func memElsewhere(p memProvider) bool {
	b := memBase(p)
	if !onAnotherMachine(b) || sameHost(b, gatewayV1()) {
		return false
	}
	// a proxy in front of this gateway is magpie's still
	base, ok := proxyBase(b, gatewayV1())
	yes, known := magpieAt(base)
	return !ok || !(yes && known)
}

// memBase is where p sends Mem's requests.
func memBase(p memProvider) string {
	if p.APIBase == "" && p.kind() == magpieID {
		return memDefaultBase
	}
	return strings.TrimSuffix(p.APIBase, "/")
}

// memOwnValue is the model field's value for p, Mem's own provider id: its
// model, or for one named magpie that isn't this magpie's (memOn), a
// magpie on a server, host/model, which is never read as a model of this
// magpie's (Alex on Discord: the cloud magpie's deepseek/pro showed Mem
// as connected here).
func memOwnValue(id string, p memProvider) string {
	if id == magpieID && p.Model != "" {
		if u, err := url.Parse(memBase(p)); err == nil && u.Host != "" {
			return u.Host + "/" + p.Model
		}
	}
	return p.Model
}

// memOwnModel is the model a value of memOwnValue's names on p.
func memOwnModel(id string, p memProvider, v string) string {
	if own := memOwnValue(id, p); own != p.Model && v == own {
		return p.Model
	}
	return v
}

// memRead is Mem's setting as remote_llm.json has it, for a Mem that
// isn't running.
func memRead(path string) (memState, bool) {
	raw, err := edit.Read(path)
	if err != nil {
		return memState{}, false
	}
	var s memState
	if json.Unmarshal(raw, &s) != nil {
		return memState{}, false
	}
	return s, true
}

func nowledgeMem(home string) *Agent {
	dir := memConfigDir(home)
	path := filepath.Join(dir, "remote_llm.json")
	keyPrev := "nowledge-mem.prev:" + dir
	now := func() (memState, *memClient) {
		if c := memLive(dir); c != nil {
			if s, err := c.config(); err == nil {
				return s, c
			}
		}
		s, _ := memRead(path)
		return s, nil
	}
	// wire makes magpie's provider Mem's AI, with model m
	wire := func(c *memClient, s memState, m string) error {
		had, there := s.Providers[magpieID]
		if unstashPeek(keyPrev) == "" {
			prev := memPrev{Enabled: s.Enabled}
			if s.Active != magpieID {
				prev.Active = s.Active
			}
			if there {
				prev.Had = &had
			}
			b, _ := json.Marshal(prev)
			stash(map[string]string{keyPrev: string(b)})
		}
		body := memBody(had, magpieID, m)
		body["api_base"] = gatewayV1()
		var prev memPrev
		json.Unmarshal([]byte(unstashPeek(keyPrev)), &prev)
		if prev.Had == nil {
			// a key on the user's own "magpie" provider stays theirs
			body["api_key"] = agentKey("nowledge-mem")
		}
		return c.post("/remote-llm/config", body)
	}
	// unwire takes magpie's provider out of Mem's AI, putting back what Mem
	// had, with model v on it where v isn't ""
	unwire := func(c *memClient, s memState, v string) error {
		raw := unstashPeek(keyPrev)
		var prev memPrev
		if raw == "" {
			// magpie never stashed: a "magpie" the user made by hand is
			// theirs to keep; Mem goes to another provider of its own
			prev.Had, prev.Enabled = &memProvider{}, true
			for _, id := range slices.Sorted(maps.Keys(s.Providers)) {
				if id != magpieID {
					prev.Active = id
					break
				}
			}
			if prev.Active == "" {
				prev.Enabled = false
			}
		} else {
			json.Unmarshal([]byte(raw), &prev)
		}
		if p, ok := s.Providers[magpieID]; ok && prev.Had != nil && prev.Had.Model != "" {
			back := memBody(p, magpieID, prev.Had.Model)
			back["api_base"] = prev.Had.APIBase
			for k, val := range map[string]any{"temperature": prev.Had.Temperature, "max_tokens": prev.Had.MaxTokens, "timeout": prev.Had.Timeout} {
				if val != nil {
					back[k] = val
				}
			}
			if err := c.post("/remote-llm/config", back); err != nil {
				return err
			}
		}
		// a provider the user picked in Mem since stays picked
		ours := s.Active == magpieID
		if p, ok := s.Providers[prev.Active]; ok && prev.Active != magpieID && (ours || v != "") {
			if v != "" && v != p.Model {
				if err := c.post("/remote-llm/config", memBody(p, prev.Active, v)); err != nil {
					return err
				}
			} else if err := c.post("/remote-llm/activate", map[string]any{"provider": prev.Active}); err != nil {
				return err
			}
		} else if v != "" && ours {
			return errors.New("Nowledge Mem had no AI of its own to use " + v + " with: pick one in its Settings > AI")
		}
		if _, ok := s.Providers[magpieID]; ok && prev.Had == nil {
			if err := c.post("/remote-llm/provider/remove", map[string]any{"provider": magpieID}); err != nil {
				return err
			}
		}
		if !prev.Enabled && ours {
			if err := c.post("/remote-llm/enabled", map[string]any{"enabled": false}); err != nil {
				return err
			}
		}
		forget(keyPrev)
		return nil
	}
	return &Agent{
		ID: "nowledge-mem", Name: "Nowledge Mem", Icon: "nowledge-mem",
		Dir: dir, Path: path,
		detect: func() bool {
			return isDir(dir) || (&Agent{ID: "nowledge-mem"}).HasApp()
		},
		// a gateway on a new port reaches magpie's provider
		Sync: func() error {
			c := memLive(dir)
			if c == nil {
				return nil
			}
			s, err := c.config()
			if err != nil {
				return err
			}
			p, on := memOn(s, unstashPeek(keyPrev) != "")
			if !on || !avibeMoved(memBase(p)) {
				return nil
			}
			body := memBody(p, magpieID, p.Model)
			body["api_base"] = gatewayV1()
			return c.post("/remote-llm/config", body)
		},
		Check: func() string {
			s, _ := now()
			p, on := memOn(s, unstashPeek(keyPrev) != "")
			if !on {
				return ""
			}
			return wiringOff("Nowledge Mem", path, func(string) (string, bool) { return memBase(p), true }, "api_base", gatewayV1())
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: func() string {
				s, _ := now()
				if p, on := memOn(s, unstashPeek(keyPrev) != ""); on {
					return magpieID + "/" + p.Model
				}
				if p, ok := s.Providers[s.Active]; ok && s.Enabled {
					return memOwnValue(s.Active, p)
				}
				return ""
			},
			Set: func(v string) error {
				s, c := now()
				memAsked(s)
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					if c == nil {
						return errMemDown
					}
					return wire(c, s, ref)
				}
				// magpie's provider the user pointed at another machine
				// since magpie set it up is theirs (memElsewhere): Alex on
				// Discord imported the magpie on their server over it, and
				// Disconnect removed it, as it wasn't there before magpie.
				// What magpie stashed goes, and Mem is left as the user has
				// it, as for a magpie on a server never set up here.
				if p, ok := s.Providers[magpieID]; ok && memElsewhere(p) {
					forget(keyPrev)
				}
				_, on := memOn(s, unstashPeek(keyPrev) != "")
				if p, ok := s.Providers[s.Active]; ok && !on {
					// the user's magpie on a server, as Get spells it
					v = memOwnModel(s.Active, p, v)
				}
				_, made := s.Providers[magpieID]
				stashed := unstashPeek(keyPrev) != ""
				if !on && !(stashed && made) {
					if v == "" {
						return nil
					}
					if p, ok := s.Providers[s.Active]; ok && p.Model == v {
						return nil
					}
				}
				if c == nil {
					if !on && !stashed {
						return nil
					}
					return errMemDown
				}
				if on || stashed {
					return unwire(c, s, v)
				}
				p, ok := s.Providers[s.Active]
				if !ok {
					return errors.New("Nowledge Mem has no AI of its own to use " + v + " with: pick one in its Settings > AI")
				}
				return c.post("/remote-llm/config", memBody(p, s.Active, v))
			},
			Options: func(cur map[string]string) []Option {
				var own []Option
				if c := cur["model"]; c != "" && !usesMagpie(c) {
					own = append(own, Option{Value: c, Icon: modelIcon("", c)})
				}
				return withVia(group("Nowledge Mem", own), "nowledge-mem", magpieID+"/")
			},
		}},
	}
}

// memBody is the POST /remote-llm/config that saves provider id as p is,
// with model m, its parameters sent again (left out, Mem resets them).
func memBody(p memProvider, id, m string) map[string]any {
	kind := p.kind()
	if kind == "" {
		kind = id
	}
	body := map[string]any{"enabled": true, "provider": id, "provider_type": kind, "model": m}
	if p.Temperature != nil {
		body["temperature"] = *p.Temperature
	}
	if p.MaxTokens != nil {
		body["max_tokens"] = *p.MaxTokens
	}
	if p.Timeout != nil {
		body["timeout"] = *p.Timeout
	}
	return body
}

// memProbe gives up quickly: a backend that doesn't answer at once isn't
// running.
const memProbe = 1500 * time.Millisecond

// memWait is how long a change may take.
const memWait = 20 * time.Second

// memClient is a running Mem's backend API.
type memClient struct {
	http *http.Client
	base string
}

// memLive is memConnect, for the tests to stand in for.
var memLive = memConnect

// memConnect is the backend of the Mem whose config folder is dir if it
// answers, nil if none runs: $NMEM_API_URL when it names this computer,
// else 127.0.0.1:14242.
func memConnect(dir string) *memClient {
	base := memAddr
	if u := strings.TrimSpace(appdir.Getenv("NMEM_API_URL")); u != "" {
		p, err := url.Parse(u)
		if err != nil || (p.Scheme != "http" && p.Scheme != "https") || !kunLoopback(p.Hostname()) {
			return nil
		}
		base = strings.TrimRight(u, "/")
	}
	c := &memClient{http: &http.Client{}, base: base}
	ctx, cancel := context.WithTimeout(context.Background(), memProbe)
	defer cancel()
	if _, err := c.do(ctx, "GET", "/remote-llm/status", nil); err != nil {
		return nil
	}
	return c
}

func (c *memClient) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, r)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Nowledge Mem: %w", err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("Nowledge Mem: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Detail any `json:"detail"`
		}
		json.Unmarshal(out, &e)
		if s, ok := e.Detail.(string); ok && s != "" {
			return nil, errors.New("Nowledge Mem: " + s)
		}
		return nil, fmt.Errorf("Nowledge Mem: %s %s: HTTP %d", method, path, resp.StatusCode)
	}
	return out, nil
}

func (c *memClient) config() (memState, error) {
	ctx, cancel := context.WithTimeout(context.Background(), memWait)
	defer cancel()
	b, err := c.do(ctx, "GET", "/remote-llm/config", nil)
	if err != nil {
		return memState{}, err
	}
	var s memState
	if err := json.Unmarshal(b, &s); err != nil {
		return memState{}, fmt.Errorf("Nowledge Mem: /remote-llm/config: %w", err)
	}
	return s, nil
}

func (c *memClient) post(path string, body any) error {
	ctx, cancel := context.WithTimeout(context.Background(), memWait)
	defer cancel()
	_, err := c.do(ctx, "POST", path, body)
	return err
}
