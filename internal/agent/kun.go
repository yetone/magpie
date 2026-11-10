package agent

// Kun (KunAgent/Kun, kun-agent.com; zhongxingyuyes on X) is a local-first
// AI workbench: a desktop app (Code, Work, chats, agents) and a `kun` TUI,
// both clients of one runtime, `kun serve`. Its models come from the
// provider connections in the runtime's registry,
// model-connections.v2.json in its data folder: $KUN_DATA_DIR, else
// ~/.kun/data (kun/src/cli/kun-data-dir.ts; read at v0.4.5). The registry
// is the authority; the desktop's kun-settings.json and the runtime's
// snapshots are projections of it, and a connection's key lives in Kun's
// protected credential store under a reference, so a connection can't be
// written into a file by hand.
//
// So magpie adds and removes its connection as Kun's own Settings >
// Providers and its `kun provider` CLI do, through the runtime's admin API,
// while Kun runs:
//
//	GET    /v1/model-connections                  the snapshot, with its revision
//	POST   /v1/model-connections/connect          {expectedRevision, id, name, baseUrl, credential, models, selectedModel, …}
//	PATCH  /v1/model-connections/:id             {expectedRevision, models}
//	POST   /v1/model-connections/select           {expectedRevision, providerId, model}
//	DELETE /v1/model-connections/:id?expected_revision=N
//	POST   /v1/provider-config/transactions/preview, then commit   the default's own connection
//
// at the baseUrl of runtime.json in the data folder, with its runtimeToken
// as a bearer (server/runtime-discovery.ts, cli/gateway-cli.ts). magpie's
// connection, "magpie", is an HTTP one at the gateway's /v1 spoken to as
// chat completions with Kun's own agent key, its models the ones picked
// for Kun on the Agents page (at most 500, contracts/model-connections.ts).
// Picking a magpie model makes it Kun's default; the default Kun had is
// stashed and picked again when magpie steps out, before its connection
// is removed. The connection's id is stashed too, so one Kun renamed to
// magpie-2 is still magpie's.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/edit"
)

// kunRegistry is the file of Kun's provider connections in its data folder.
const kunRegistry = "model-connections.v2.json"

// kunMaxModels is how many models a Kun connection takes.
const kunMaxModels = 500

// errKunDown is a change asked of a Kun that isn't running.
var errKunDown = errors.New("Kun adds and changes model providers only while it runs: open Kun, then try again")

// kunStash is the stash key of what magpie keeps for the Kun at dir: the
// id of its connection, and the default Kun had before.
func kunStash(dir, what string) string { return "kun." + what + ":" + dir }

// kunDataDir is Kun's data folder: $KUN_DATA_DIR, else ~/.kun/data.
func kunDataDir(home string) string {
	if d := strings.TrimSpace(appdir.Getenv("KUN_DATA_DIR")); d != "" {
		if abs, err := filepath.Abs(d); err == nil {
			return abs
		}
		return d
	}
	return filepath.Join(home, ".kun", "data")
}

// kunProvider is a connection as the snapshot and the registry give it.
type kunProvider struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Kind          string   `json:"kind"`
	BaseURL       string   `json:"baseUrl"`
	Models        []string `json:"models"`
	SelectedModel string   `json:"selectedModel"`
}

// kunState is the runtime's snapshot: its connections and its default.
type kunState struct {
	Revision          int           `json:"revision"`
	Providers         []kunProvider `json:"providers"`
	DefaultProviderID string        `json:"defaultProviderId"`
	DefaultAccountID  string        `json:"defaultAccountId"`
	DefaultModel      string        `json:"defaultModel"`
}

func (s kunState) provider(id string) (kunProvider, bool) {
	for _, p := range s.Providers {
		if p.ID == id {
			return p, true
		}
	}
	return kunProvider{}, false
}

// kunOurs is magpie's connection in s: the one whose id magpie stashed,
// else an HTTP one at the gateway (added by hand in Kun's Settings).
func kunOurs(dir string, s kunState) (kunProvider, bool) {
	if id := unstashPeek(kunStash(dir, "id")); id != "" {
		if p, ok := s.provider(id); ok {
			return p, true
		}
	}
	for _, p := range s.Providers {
		if (p.Kind == "" || p.Kind == "http") && sameGateway(p.BaseURL, gatewayV1()) {
			return p, true
		}
	}
	return kunProvider{}, false
}

// kunRead is the Kun at dir's connections as its registry file has them,
// for a Kun that isn't running: the registry keeps its connections as a
// map of profiles.
func kunRead(dir string) (kunState, bool) {
	raw, err := edit.Read(filepath.Join(dir, kunRegistry))
	if err != nil {
		return kunState{}, false
	}
	var doc struct {
		Revision          int                    `json:"revision"`
		Profiles          map[string]kunProvider `json:"profiles"`
		DefaultProviderID string                 `json:"defaultProviderId"`
		DefaultAccountID  string                 `json:"defaultAccountId"`
		DefaultModel      string                 `json:"defaultModel"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return kunState{}, false
	}
	s := kunState{Revision: doc.Revision, DefaultProviderID: doc.DefaultProviderID, DefaultAccountID: doc.DefaultAccountID, DefaultModel: doc.DefaultModel}
	for id, p := range doc.Profiles {
		if p.ID == "" {
			p.ID = id
		}
		s.Providers = append(s.Providers, p)
	}
	slices.SortFunc(s.Providers, func(a, b kunProvider) int { return strings.Compare(a.ID, b.ID) })
	return s, true
}

// kunNow is the Kun at dir's state: its registry file's, which Kun
// writes with every change, else the running one's snapshot. Reading the
// file asks nothing of a Kun that isn't running.
func kunNow(dir string) (kunState, bool) {
	if s, ok := kunRead(dir); ok {
		return s, true
	}
	if c := kunLive(dir); c != nil {
		if s, err := c.snapshot(); err == nil {
			return s, true
		}
	}
	return kunState{}, false
}

// kunModels is the models picked for Kun, as many as a connection takes,
// with want among them.
func kunModels(want string) []string {
	var out []string
	for _, m := range sharedModels("kun") {
		if m.ID == "" || len(m.ID) > 512 || slices.Contains(out, m.ID) {
			continue
		}
		out = append(out, m.ID)
	}
	if want != "" && !slices.Contains(out, want) {
		out = append([]string{want}, out...)
	}
	if len(out) > kunMaxModels {
		if i := slices.Index(out, want); i >= kunMaxModels {
			out[kunMaxModels-1] = want
		}
		out = out[:kunMaxModels]
	}
	return out
}

// kunPrev is the default Kun had before magpie's, as stashed.
type kunPrev struct {
	Provider string `json:"provider"`
	Account  string `json:"account,omitempty"`
	Model    string `json:"model"`
}

func kun(home string) *Agent {
	dir := kunDataDir(home)
	path := filepath.Join(dir, kunRegistry)
	keyID, keyPrev := kunStash(dir, "id"), kunStash(dir, "prev")
	// wire makes magpie's connection Kun's default with model m, made
	// where there is none or it was made for a gateway elsewhere
	wire := func(c *kunClient, s kunState, m string) error {
		ours, there := kunOurs(dir, s)
		if there && kunMoved(ours.BaseURL) {
			next, err := c.remove(s, ours.ID)
			if err != nil {
				return err
			}
			s, there = next, false
		}
		if !there {
			if s.DefaultProviderID != "" {
				b, _ := json.Marshal(kunPrev{Provider: s.DefaultProviderID, Account: s.DefaultAccountID, Model: s.DefaultModel})
				if _, had := kunOurs(dir, s); !had && unstashPeek(keyPrev) == "" {
					stash(map[string]string{keyPrev: string(b)})
				}
			}
			next, err := c.connect(s, m)
			if err != nil {
				return err
			}
			for _, p := range next.Providers {
				if _, was := s.provider(p.ID); !was {
					stash(map[string]string{keyID: p.ID})
					break
				}
			}
			return nil
		}
		if unstashPeek(keyID) != ours.ID {
			stash(map[string]string{keyID: ours.ID})
		}
		if models := kunModels(m); !slices.Equal(models, ours.Models) {
			next, err := c.patchModels(s, ours.ID, models)
			if err != nil {
				return err
			}
			s = next
		}
		if s.DefaultProviderID == ours.ID && s.DefaultModel == m {
			return nil
		}
		return c.selectModel(s, ours.ID, m)
	}
	// unwire takes magpie's connection out of Kun, its default going back
	// to what it was, with model v where v isn't ""
	unwire := func(c *kunClient, s kunState, ours kunProvider, v string) error {
		var prev kunPrev
		json.Unmarshal([]byte(unstashPeek(keyPrev)), &prev)
		if _, ok := s.provider(prev.Provider); !ok || prev.Provider == ours.ID {
			prev = kunPrev{}
		}
		if v != "" && prev.Provider == "" {
			return errors.New("Kun had no provider of its own to use " + v + " with: add one in Kun's Settings > Providers")
		}
		if prev.Provider != "" {
			if v == "" {
				v = prev.Model
			}
			if v != "" {
				if err := c.selectModel(s, prev.Provider, v); err != nil {
					return err
				}
				next, err := c.snapshot()
				if err != nil {
					return err
				}
				s = next
			}
		}
		if _, err := c.remove(s, ours.ID); err != nil {
			return err
		}
		forget(keyID, keyPrev)
		return nil
	}
	return &Agent{
		ID: "kun", Name: "Kun", Icon: "kun",
		Dir: dir, Path: path,
		detect: func() bool {
			return isDir(dir) || (&Agent{ID: "kun"}).HasApp()
		},
		ListsModels: true,
		ListOnly:    true,
		// a model picked or dropped for Kun reaches its connection's list,
		// and a gateway on a new port its address
		Sync: func() error {
			c := kunLive(dir)
			if c == nil {
				return nil
			}
			s, err := c.snapshot()
			if err != nil {
				return err
			}
			ours, ok := kunOurs(dir, s)
			if !ok {
				return nil
			}
			m := ours.SelectedModel
			if s.DefaultProviderID == ours.ID && s.DefaultModel != "" {
				m = s.DefaultModel
			}
			if kunMoved(ours.BaseURL) {
				return wire(c, s, m)
			}
			models := kunModels(m)
			if slices.Equal(models, ours.Models) {
				return nil
			}
			_, err = c.patchModels(s, ours.ID, models)
			return err
		},
		Check: func() string {
			s, ok := kunRead(dir)
			if !ok {
				return ""
			}
			ours, ok := kunOurs(dir, s)
			if !ok {
				return ""
			}
			return wiringOff("Kun", path, func(string) (string, bool) { return strings.TrimSuffix(ours.BaseURL, "/"), true }, "baseUrl", gatewayV1())
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: func() string {
				s, ok := kunNow(dir)
				if !ok {
					return ""
				}
				if ours, on := kunOurs(dir, s); on && s.DefaultProviderID == ours.ID && s.DefaultModel != "" {
					return magpieID + "/" + s.DefaultModel
				}
				return s.DefaultModel
			},
			Set: func(v string) error {
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					c := kunLive(dir)
					if c == nil {
						return errKunDown
					}
					s, err := c.snapshot()
					if err != nil {
						return err
					}
					return wire(c, s, ref)
				}
				s, known := kunRead(dir)
				ours, on := kunOurs(dir, s)
				if known && !on && (v == "" || v == s.DefaultModel) {
					return nil
				}
				c := kunLive(dir)
				if c == nil {
					if !known {
						return nil
					}
					return errKunDown
				}
				if s, err := c.snapshot(); err == nil {
					ours, on = kunOurs(dir, s)
					if on {
						return unwire(c, s, ours, v)
					}
					if v == "" || v == s.DefaultModel {
						return nil
					}
					if s.DefaultProviderID == "" {
						return errors.New("Kun has no provider of its own to use " + v + " with: add one in Kun's Settings > Providers")
					}
					return c.selectModel(s, s.DefaultProviderID, v)
				} else {
					return err
				}
			},
			Options: func(cur map[string]string) []Option {
				var own []Option
				if c := cur["model"]; c != "" && !usesMagpie(c) {
					own = append(own, Option{Value: c, Icon: modelIcon("", c)})
				}
				return withVia(group("Kun", own), "kun", magpieID+"/")
			},
		}},
	}
}

// kunMoved says base, magpie's connection's address, is the gateway's own
// on an older port, which a sync moves to where the gateway is now; one
// the user pointed elsewhere (another machine, a proxy's route, the way a
// WSL distro reaches Windows) stays, as avibeMoved keeps it.
func kunMoved(base string) bool { return avibeMoved(base) }

// kunProbe gives up quickly: a runtime that doesn't answer at once isn't
// running.
const kunProbe = 1500 * time.Millisecond

// kunWait is how long a change may take: a connection's credential is
// written to Kun's protected store as it is made.
const kunWait = 30 * time.Second

// kunClient is a running Kun runtime's admin API.
type kunClient struct {
	http  *http.Client
	base  string
	token string
}

// kunLive is kunConnect, for the tests to stand in for.
var kunLive = kunConnect

// kunConnect is the admin API of the Kun runtime serving dir if it
// answers, nil if none runs: runtime.json outlives a crash.
func kunConnect(dir string) *kunClient {
	c := kunAt(dir)
	if c == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), kunProbe)
	defer cancel()
	if _, err := c.do(ctx, "GET", "/v1/model-connections", nil); err != nil {
		return nil
	}
	return c
}

// kunAt is where the runtime serving dir listens, as runtime.json there
// says, or nil with none: Kun's own CLI finds it the same way.
func kunAt(dir string) *kunClient {
	b, err := os.ReadFile(filepath.Join(dir, "runtime.json"))
	if err != nil {
		return nil
	}
	var d struct {
		BaseURL string `json:"baseUrl"`
		Token   string `json:"runtimeToken"`
	}
	if json.Unmarshal(b, &d) != nil || d.BaseURL == "" {
		return nil
	}
	u, err := url.Parse(d.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !kunLoopback(u.Hostname()) {
		return nil
	}
	return &kunClient{http: &http.Client{}, base: strings.TrimRight(d.BaseURL, "/"), token: d.Token}
}

// kunLoopback says h names this computer: the runtime's token goes
// nowhere else.
func kunLoopback(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// kunError is a refusal from Kun's runtime.
type kunError struct {
	Status        int
	Code, Message string
}

func (e *kunError) Error() string {
	if e.Message != "" {
		return "Kun: " + e.Message
	}
	return fmt.Sprintf("Kun: %s (HTTP %d)", e.Code, e.Status)
}

func (c *kunClient) do(ctx context.Context, method, path string, body any) ([]byte, error) {
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
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Kun: %w", err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("Kun: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		json.Unmarshal(out, &e)
		if e.Code == "" {
			e.Code = resp.Status
		}
		return nil, &kunError{Status: resp.StatusCode, Code: e.Code, Message: e.Message}
	}
	return out, nil
}

// call is a change, answered with the snapshot it leaves.
func (c *kunClient) call(method, path string, body any) (kunState, error) {
	ctx, cancel := context.WithTimeout(context.Background(), kunWait)
	defer cancel()
	b, err := c.do(ctx, method, path, body)
	if err != nil {
		return kunState{}, err
	}
	var s kunState
	if err := json.Unmarshal(b, &s); err != nil {
		return kunState{}, fmt.Errorf("Kun: %s %s: %w", method, path, err)
	}
	return s, nil
}

func (c *kunClient) snapshot() (kunState, error) {
	return c.call("GET", "/v1/model-connections", nil)
}

// connect adds magpie's connection with model m picked, as Kun's default.
// Kun's own probe is off: it can't tell the gateway's protocols apart (it
// serves them all), and the models are the ones picked for Kun.
func (c *kunClient) connect(s kunState, m string) (kunState, error) {
	return c.call("POST", "/v1/model-connections/connect", map[string]any{
		"expectedRevision": s.Revision,
		"id":               magpieID, "name": "magpie",
		"kind": "http", "authType": "api-key",
		"baseUrl": gatewayV1(), "endpointFormat": "chat_completions",
		"credential": agentKey("kun"),
		"models":     kunModels(m), "selectedModel": m,
		"probe": false, "select": true,
	})
}

func (c *kunClient) patchModels(s kunState, id string, models []string) (kunState, error) {
	return c.call("PATCH", "/v1/model-connections/"+url.PathEscape(id), map[string]any{
		"expectedRevision": s.Revision, "models": models,
	})
}

func (c *kunClient) selectModel(s kunState, id, m string) error {
	_, err := c.call("POST", "/v1/model-connections/select", map[string]any{
		"expectedRevision": s.Revision, "providerId": id, "model": m,
	})
	return err
}

// remove takes connection id out of Kun. Kun refuses to delete the
// connection its default names ("Deleted connection still has references:
// default.providerId"), so that one goes as Kun's own Settings retires it:
// one provider-config transaction that clears the default and removes the
// connection, previewed and then committed
// (server/routes/provider-configuration.ts,
// services/provider-configuration-operations.ts).
func (c *kunClient) remove(s kunState, id string) (kunState, error) {
	if s.DefaultProviderID != id {
		return c.call("DELETE", "/v1/model-connections/"+url.PathEscape(id)+"?expected_revision="+fmt.Sprint(s.Revision), nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), kunWait)
	defer cancel()
	b, err := c.do(ctx, "POST", "/v1/provider-config/transactions/preview", map[string]any{
		"expectedRevision": s.Revision,
		"operations": []any{
			map[string]any{"kind": "set-default-selection"},
			map[string]any{"kind": "remove-connection", "connectionId": id},
		},
	})
	if err != nil {
		return kunState{}, err
	}
	var p struct {
		PreviewID        string `json:"previewId"`
		ExpectedRevision int    `json:"expectedRevision"`
	}
	if err := json.Unmarshal(b, &p); err != nil || p.PreviewID == "" {
		return kunState{}, fmt.Errorf("Kun: no preview of removing %s", id)
	}
	if _, err := c.do(ctx, "POST", "/v1/provider-config/transactions/commit", map[string]any{
		"expectedRevision": p.ExpectedRevision, "previewId": p.PreviewID,
		"idempotencyKey": "magpie:remove:" + id + ":" + p.PreviewID,
	}); err != nil {
		return kunState{}, err
	}
	return c.snapshot()
}
