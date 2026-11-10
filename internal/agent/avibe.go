package agent

// Avibe (avibe-bot/avibe, MIT, the avibe-os package and its `vibe` CLI) is
// a local agent OS: its own agent, Vibey, and the Claude Code, Codex and
// OpenCode it runs all take their models from its Model Hub, a gateway of
// its own (CLIProxyAPI) in front of the model sources added on its Models
// page. Its home is $AVIBE_HOME, else ~/.avibe (~/.vibe_remote before 3.0:
// config/paths.py), and the sources are model_hub.sources in
// config/config.json there.
//
// magpie is one such source, "magpie": an API-key source of the custom
// vendor at the gateway's /v1, spoken to as chat completions with an agent
// key of Avibe's own. A hub source serves every backend
// (source_eligible_for_backend), the Model Hub translating Claude's and
// Codex's requests, and Avibe lists its models from the gateway's
// /v1/models itself, so the models picked for Avibe on the Agents page
// are what it offers.
//
// Chat completions rather than Responses: for a Responses source the Model
// Hub runs Codex's executor, which names itself codex-tui and would be
// taken for Codex itself (fromCodex); for chat it passes the backend's own
// headers on.
//
// A source can't be written into config.json by hand: each is checked
// whole, its key lives in the Model Hub's store under a credential_ref, and
// Avibe adds it to its agents' route chains as it is added. So magpie adds
// and removes it as Avibe's Models page does, through Avibe's internal API
// (core/handlers/model_hub/rpc.py), while Avibe runs:
//
//	POST /internal/model-hub  {"operation": "create_source"|"delete_source"|"refresh_source", "payload": {…}}
//
// on the socket state/dispatch.sock in its home (core/control_ipc.py), or
// on Windows at the loopback port runtime/control-ipc/endpoint.json gives
// with its bearer token. The source's id is stashed, so a renamed one is
// still magpie's.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/edit"
)

// avibeStash is the stash key for the id of magpie's source in the Avibe
// at dir.
func avibeStash(dir string) string { return "avibe.source:" + dir }

// errAvibeDown is a change asked of an Avibe that isn't running.
var errAvibeDown = errors.New("Avibe adds and removes model sources only while it runs: start it (vibe start), then try again")

func avibe(home string) *Agent {
	dir := avibeHome(home)
	path := filepath.Join(dir, "config", "config.json")
	wired := func() bool { _, ok := avibeOurs(dir, path); return ok }
	return &Agent{
		ID: "avibe", Name: "Avibe", Icon: "avibe", Aliases: []string{"vibe", "vibey", "avibe-os"},
		Dir: dir, Path: path,
		detect:      func() bool { return isDir(dir) },
		ListsModels: true,
		ListOnly:    true,
		// a model picked or dropped for Avibe reaches its source when it
		// lists the gateway's models again; Avibe does that itself only
		// for a listing older than half an hour
		Sync: func() error {
			src, ok := avibeOurs(dir, path)
			if !ok {
				return nil
			}
			c := avibeLive(dir)
			if c == nil {
				return nil
			}
			if avibeMoved(src.BaseURL) {
				return avibeWire(c, dir, src, true)
			}
			avibeKeep(dir, src.ID)
			return c.confirmed("refresh_source", map[string]any{"source_id": src.ID})
		},
		Check: func() string {
			src, ok := avibeOurs(dir, path)
			if !ok {
				return ""
			}
			return wiringOff("Avibe", path, func(string) (string, bool) { return strings.TrimSuffix(src.BaseURL, "/"), true }, "base_url", gatewayV1())
		},
		Fields: []Field{{
			Key: "provider", Label: "provider",
			Get: func() string {
				if wired() {
					return magpieID
				}
				return ""
			},
			Set: func(v string) error {
				src, there := avibeOurs(dir, path)
				if v == "" && !there {
					return nil
				}
				if v != "" && there && strings.TrimSuffix(src.BaseURL, "/") == gatewayV1() {
					avibeKeep(dir, src.ID)
					return nil
				}
				c := avibeLive(dir)
				if c == nil {
					return errAvibeDown
				}
				if v == "" {
					if err := c.confirmed("delete_source", map[string]any{"source_id": src.ID}); err != nil {
						return err
					}
					forget(avibeStash(dir))
					return nil
				}
				return avibeWire(c, dir, src, there)
			},
			Options: func(map[string]string) []Option {
				return []Option{{Value: magpieID, Label: "magpie", Icon: "magpie", Note: "a model source on Avibe's Models page, for Vibey and the agents it runs"}}
			},
		}},
	}
}

// avibeWire adds magpie's source to the Avibe c serves, in place of src
// when there is one: its key is sealed in the Model Hub and goes with the
// gateway's address, so a source on an old address is made again rather
// than patched.
func avibeWire(c *avibeClient, dir string, src avibeSource, there bool) error {
	if there {
		if err := c.confirmed("delete_source", map[string]any{"source_id": src.ID}); err != nil {
			return err
		}
	}
	id, err := c.create()
	if err != nil {
		return err
	}
	stash(map[string]string{avibeStash(dir): id})
	return nil
}

// avibeStashed is the id of the source magpie added to the Avibe at dir.
func avibeStashed(dir string) string {
	stashMu.Lock()
	defer stashMu.Unlock()
	return stashShared()[avibeStash(dir)]
}

// avibeKeep stashes id as magpie's source in the Avibe at dir, one found
// by its address (added by hand, or before the stash was lost), so it is
// still known once the gateway moves.
func avibeKeep(dir, id string) {
	if avibeStashed(dir) != id {
		stash(map[string]string{avibeStash(dir): id})
	}
}

// avibeHome is Avibe's home: $AVIBE_HOME, else ~/.avibe, else the
// ~/.vibe_remote of an Avibe from before the move (paths.py's
// get_vibe_remote_dir).
func avibeHome(home string) string {
	if d := strings.TrimSpace(appdir.Getenv("AVIBE_HOME")); d != "" {
		if rest, ok := strings.CutPrefix(d, "~"); ok && (rest == "" || rest[0] == '/' || rest[0] == filepath.Separator) {
			d = home + rest
		}
		if abs, err := filepath.Abs(d); err == nil {
			d = abs
		}
		return d
	}
	d := filepath.Join(home, ".avibe")
	if _, err := os.Lstat(d); err != nil {
		if legacy := filepath.Join(home, ".vibe_remote"); isDir(legacy) {
			return legacy
		}
	}
	return d
}

// avibeSource is a model source as config.json keeps it.
type avibeSource struct {
	ID          string `json:"id"`
	Vendor      string `json:"vendor"`
	DisplayName string `json:"display_name"`
	BaseURL     string `json:"base_url"`
}

// avibeOurs is magpie's source in the config.json at path: the one whose
// id magpie stashed when it added it, else one of the custom vendor at the
// gateway (added by hand on Avibe's Models page).
func avibeOurs(dir, path string) (avibeSource, bool) {
	raw, ok := edit.GetJSON(path, "model_hub.sources")
	if !ok {
		return avibeSource{}, false
	}
	var srcs []avibeSource
	if json.Unmarshal([]byte(raw), &srcs) != nil {
		return avibeSource{}, false
	}
	if id := avibeStashed(dir); id != "" {
		for _, s := range srcs {
			if s.ID == id {
				return s, true
			}
		}
	}
	for _, s := range srcs {
		if s.ID != "" && s.Vendor == "custom" && strings.TrimSuffix(s.BaseURL, "/") == gatewayV1() {
			return s, true
		}
	}
	return avibeSource{}, false
}

// avibeProbe gives up quickly: an Avibe that doesn't answer at once isn't
// running.
const avibeProbe = 1500 * time.Millisecond

// avibeWait is how long a change may take: adding a source lists its
// models, and removing one rewrites every route chain it is in.
const avibeWait = 30 * time.Second

// avibeClient is a running Avibe's internal API.
type avibeClient struct {
	http  *http.Client
	base  string
	token string
}

// avibeMoved says base, magpie's source's address, is the gateway's own
// on an older port, which a sync moves to where the gateway is now. One
// the user pointed at magpie on another machine (a NAS's, the LAN
// address), through a local proxy's route (a path before /v1), or the way
// a WSL distro reaches Windows stays where it is, as every other agent's
// sync keeps it (kept.away).
func avibeMoved(base string) bool {
	base = strings.TrimSuffix(base, "/")
	if base == gatewayV1() {
		return false
	}
	root := strings.TrimSuffix(base, "/v1")
	return root != base && !onAnotherMachine(root) && !loopbackWithPath(root) && !wslHost(root)
}

// avibeLive is avibeConnect, for the tests to stand in for.
var avibeLive = avibeConnect

// avibeConnect is the internal API of the Avibe at dir if it answers, nil
// if it isn't running: its socket, or on Windows its endpoint file,
// outlives a crash.
func avibeConnect(dir string) *avibeClient {
	c := avibeAt(dir)
	if c == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), avibeProbe)
	defer cancel()
	if _, err := c.callCtx(ctx, "list_sources", map[string]any{}); err != nil {
		return nil
	}
	return c
}

// avibeAt is where the Avibe at dir listens, as its own CLI finds it
// (control_ipc.py's resolve_client_endpoint), or nil with nothing there.
func avibeAt(dir string) *avibeClient {
	if runtime.GOOS == "windows" {
		b, err := os.ReadFile(filepath.Join(dir, "runtime", "control-ipc", "endpoint.json"))
		if err != nil {
			return nil
		}
		var d struct {
			Transport string `json:"transport"`
			Host      string `json:"host"`
			Port      int    `json:"port"`
			Token     string `json:"bearer_token"`
		}
		if json.Unmarshal(b, &d) != nil || d.Transport != "tcp" || d.Host != "127.0.0.1" || d.Port <= 0 || d.Port > 65535 || d.Token == "" {
			return nil
		}
		return &avibeClient{http: &http.Client{}, base: fmt.Sprintf("http://127.0.0.1:%d", d.Port), token: d.Token}
	}
	sock := filepath.Join(dir, "state", "dispatch.sock")
	if fi, err := os.Stat(sock); err != nil || fi.Mode()&os.ModeSocket == 0 {
		return nil
	}
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}
	return &avibeClient{http: &http.Client{Transport: tr}, base: "http://localhost"}
}

// avibeError is a refusal from Avibe's Model Hub.
type avibeError struct {
	Code, Detail string
	// what it would take away, for a change it asks to have confirmed
	Hops, Interrupts json.RawMessage
}

func (e *avibeError) Error() string {
	msg := "Avibe: " + e.Code
	if e.Detail != "" && e.Detail != "modelHub.errors."+e.Code {
		msg += " (" + e.Detail + ")"
	}
	return msg
}

func (c *avibeClient) call(op string, payload map[string]any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), avibeWait)
	defer cancel()
	return c.callCtx(ctx, op, payload)
}

func (c *avibeClient) callCtx(ctx context.Context, op string, payload map[string]any) (json.RawMessage, error) {
	b, err := json.Marshal(map[string]any{"operation": op, "payload": payload})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.base+"/internal/model-hub", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Avibe: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		OK         bool            `json:"ok"`
		Result     json.RawMessage `json:"result"`
		Error      string          `json:"error"`
		Detail     string          `json:"detail"`
		Hops       json.RawMessage `json:"would_remove_hops"`
		Interrupts json.RawMessage `json:"would_interrupt"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("Avibe: %s %s: %w", resp.Status, op, err)
	}
	if !out.OK {
		if out.Error == "" {
			out.Error = resp.Status
		}
		return nil, &avibeError{Code: out.Error, Detail: out.Detail, Hops: out.Hops, Interrupts: out.Interrupts}
	}
	return out.Result, nil
}

// confirmed runs a change Avibe may ask to have confirmed first, as its
// Models page does when a source leaves routes behind: asked again with
// what it said it would take away. The user asked for it in magpie.
func (c *avibeClient) confirmed(op string, payload map[string]any) error {
	payload["force"] = true
	_, err := c.call(op, payload)
	var e *avibeError
	if !errors.As(err, &e) || (len(e.Hops) == 0 && len(e.Interrupts) == 0) {
		return err
	}
	if len(e.Hops) > 0 {
		payload["would_remove_hops"] = e.Hops
	}
	if len(e.Interrupts) > 0 {
		payload["would_interrupt"] = e.Interrupts
	}
	_, err = c.call(op, payload)
	return err
}

// create adds magpie's source and gives its id. It is saved without
// Avibe's protocol probe, which can't tell the gateway's protocols apart
// (it serves them all), and lists its models from the gateway at once.
func (c *avibeClient) create() (string, error) {
	res, err := c.call("create_source", map[string]any{"source": map[string]any{
		"kind": "api_key", "vendor": "custom", "display_name": magpieID,
		"base_url": gatewayV1(), "key": agentKey("avibe"),
		"protocol": "openai_chat", "supply_channel": "hub", "billing": "metered",
		"save_unverified": true,
	}})
	if err != nil {
		return "", err
	}
	var out struct {
		Source struct {
			ID string `json:"id"`
		} `json:"source"`
	}
	if json.Unmarshal(res, &out) != nil || out.Source.ID == "" {
		return "", errors.New("Avibe added no source")
	}
	return out.Source.ID, nil
}
