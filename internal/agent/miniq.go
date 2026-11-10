package agent

// miniQ (LeadingThink/miniQ; xiancrong on X) is a desktop AI coworker and a
// `miniq` terminal client, both clients of one miniq-daemon. Its provider is
// one OpenAI- or Anthropic-compatible endpoint the user sets in Settings or
// with `miniq configure --base-url`: settings.json's "provider",
// {baseUrl, apiKey, model, apiProtocol, reasoningEffort?} (miniq-models
// ProviderConfig; read at 2e97b359, 0.1.213). settings.json sits in the
// daemon's data folder: $MINIQ_DATA_DIR, else the folder named by
// miniq-data-dir.json beside the default, else %LOCALAPPDATA%\miniq or
// ~/.local/share/miniq, macOS too (miniq-local data_dir).
//
// So magpie writes the provider as the gateway's /v1 with miniQ's own key
// (it sends no User-Agent of its own, so the key names it), the model's
// catalog id and apiProtocol chat_completions: "auto" would guess Responses
// or Anthropic Messages from a model's name. The user's provider is stashed
// whole and put back when magpie steps out; the rest of settings.json (MCP
// servers, approvals, hooks, remote access) stays as it is.
//
// The daemon reads settings.json once, at its start, and writes its own
// copy back on every settings change, so a running one is told too: its
// settings.update RPC over the local WebSocket daemon.json names, as the
// desktop's Settings does, which also moves open chats onto the new model.
// A daemon that can't be told keeps the old provider until it restarts.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/edit"
)

// miniqDataDir is miniQ's data folder at the place, as miniq-local's
// data_dir finds it; ~/.local/share/miniq in a WSL distro, whose variables
// and pointer magpie doesn't read.
func miniqDataDir(at place) string {
	if d := at.getenv("MINIQ_DATA_DIR"); d != "" {
		return d
	}
	def := at.join(".local", "share", "miniq")
	if app := at.getenv("LOCALAPPDATA"); app != "" {
		def = filepath.Join(app, "miniq")
	}
	// miniq-data-dir.json, beside the default, names a folder the user
	// moved it to in Settings; on this machine only, as one in a distro
	// names a path of the distro's
	if at.spell != nil {
		return def
	}
	if b, err := os.ReadFile(filepath.Join(filepath.Dir(def), "miniq-data-dir.json")); err == nil {
		var c struct {
			DataDir string `json:"data_dir"`
		}
		if json.Unmarshal(b, &c) == nil && c.DataDir != "" {
			return c.DataDir
		}
	}
	return def
}

func miniq(home string) *Agent { return miniqAt(here(home)) }

// miniqIn is miniQ in a WSL distro (see wsl.go).
func miniqIn(at place) *Agent { return miniqAt(at) }

// miniqProtocol is the apiProtocol magpie's provider is written with.
const miniqProtocol = "chat_completions"

// miniqRPC calls method on the miniQ daemon whose data folder is dir,
// over the local WebSocket its daemon.json names, and returns the result;
// ok false with no error is no daemon to ask (none running, or a
// daemon.json a daemon that didn't exit cleanly left behind).
func miniqRPC(dir, method string, params any) (res gjson.Result, ok bool, err error) {
	b, err := os.ReadFile(filepath.Join(dir, "daemon.json"))
	if err != nil {
		return res, false, nil
	}
	var d struct {
		Port  int    `json:"port"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(b, &d); err != nil || d.Port == 0 || d.Token == "" {
		return res, false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	u := "ws://127.0.0.1:" + strconv.Itoa(d.Port) + "/ws?token=" + url.QueryEscape(d.Token)
	c, _, err := websocket.Dial(ctx, u, nil)
	if err != nil {
		return res, false, nil
	}
	defer c.CloseNow()
	c.SetReadLimit(8 << 20)
	req, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err := c.Write(ctx, websocket.MessageText, req); err != nil {
		return res, false, err
	}
	for {
		_, msg, err := c.Read(ctx)
		if err != nil {
			return res, false, err
		}
		r := gjson.ParseBytes(msg)
		if !r.Get("jsonrpc").Exists() || r.Get("id").Raw != "1" {
			continue // an event the daemon sends every client
		}
		c.Close(websocket.StatusNormalClosure, "")
		if e := r.Get("error"); e.Exists() {
			return res, true, fmt.Errorf("miniQ refused %s: %s", method, e.Get("message").String())
		}
		return r.Get("result"), true, nil
	}
}

// miniqAt is miniQ with its home at at.home, reaching the gateway as at
// does.
func miniqAt(at place) *Agent {
	dir := miniqDataDir(at)
	path := filepath.Join(dir, "settings.json")
	key := "miniq:" + path + ":"
	keyProvider := key + "provider"
	// keyNew marks a settings.json magpie made
	keyNew := key + "new"
	prov := func() gjson.Result {
		raw, err := edit.Read(path)
		if err != nil {
			return gjson.Result{}
		}
		return gjson.GetBytes(raw, "provider")
	}
	// on: magpie's provider is miniQ's, known by magpie's key for miniQ,
	// which stays where the gateway's port moved or the user put a proxy
	// in front of it (Check tells the address apart), or by the gateway's
	// own address with a key of the user's
	on := func() bool {
		p := prov()
		return keyHolds(p.Get("apiKey").String(), agentKeyAt("miniq", at.gw())) ||
			sameGateway(p.Get("baseUrl").String(), at.v1())
	}
	// push tells a running daemon the provider written; one that can't
	// be told is left to Notice
	push := func(p map[string]any) {
		if _, _, err := miniqRPC(dir, "settings.update", map[string]any{"provider": p}); err != nil {
			log.Printf("miniQ: %v", err)
		}
	}
	// pushOf is settings.update's provider params for a provider as
	// settings.json has it
	pushOf := func(p gjson.Result) map[string]any {
		m := map[string]any{
			"baseUrl": p.Get("baseUrl").String(), "model": p.Get("model").String(),
			"apiProtocol": "auto",
		}
		if v := p.Get("apiProtocol").String(); v != "" {
			m["apiProtocol"] = v
		}
		if k := p.Get("apiKey").String(); k != "" {
			m["apiKey"] = k
		} else {
			m["clearApiKey"] = true
		}
		return m
	}
	// write writes settings.json's provider, the file made private first
	// where there is none: it holds keys, and miniQ writes it 0600
	write := func(v any) error {
		if !isFile(path) {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			if f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err == nil {
				f.WriteString("{}\n")
				f.Close()
			}
		}
		return edit.SetJSON(path, edit.KV{Path: "provider", Value: v})
	}
	// restore puts back the provider the user had, with v as its model if
	// v isn't ""; none, where they had none, and the file goes where magpie
	// made it
	restore := func(v string) error {
		if v != "" && !gjson.Valid(unstashPeek(keyProvider)) {
			return errors.New("miniQ had no provider of its own to use " + v + " with: set one in miniQ's Settings")
		}
		was := unstash(keyProvider)
		made := unstash(keyNew) != ""
		if was != "" && gjson.Valid(was) {
			var p map[string]any
			json.Unmarshal([]byte(was), &p)
			if v != "" {
				p["model"] = v
			}
			if err := write(p); err != nil {
				return err
			}
			b, _ := json.Marshal(p)
			push(pushOf(gjson.ParseBytes(b)))
			return nil
		}
		if !isFile(path) {
			return nil
		}
		// none of theirs: miniQ goes back to its own default (settings.update
		// can't take a provider out, so a running daemon keeps magpie's
		// until it restarts, which Notice says)
		if err := edit.DelJSON(path, "provider"); err != nil {
			return err
		}
		if made {
			if raw, err := edit.Read(path); err == nil && len(gjson.ParseBytes(raw).Map()) == 0 {
				return edit.Remove(path)
			}
		}
		return nil
	}
	return atomic(&Agent{
		ID: "miniq", Name: "miniQ", Icon: "miniq-color", Spelled: prefixed, NoNames: true,
		Bin: "miniq", Dir: dir, Path: path,
		// the daemon's provider, where one runs, is the one written: one
		// that couldn't be told keeps what it started with
		Notice: func() string {
			res, ok, _ := miniqRPC(dir, "settings.get", map[string]any{})
			if !ok {
				return ""
			}
			d, f := res.Get("provider"), prov()
			if strings.TrimRight(d.Get("baseUrl").String(), "/") != strings.TrimRight(f.Get("baseUrl").String(), "/") ||
				d.Get("model").String() != f.Get("model").String() {
				return noticeMiniq.String()
			}
			return ""
		},
		Check: func() string {
			if !on() {
				return ""
			}
			p := prov()
			return wiringOff("miniQ", path, func(k string) (string, bool) {
				r := p.Get(k)
				return r.String(), r.Exists()
			}, "baseUrl", at.v1(), "apiKey", agentKeyAt("miniq", at.gw()))
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: func() string {
				m := prov().Get("model").String()
				if m != "" && on() {
					return magpieID + "/" + m
				}
				return m
			},
			Set: func(v string) error {
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					if !on() {
						// what the user had, unless magpie stashed it already
						forget(keyProvider, keyNew)
						kv := map[string]string{keyProvider: prov().Raw}
						if !isFile(path) {
							kv[keyNew] = "1"
						}
						stash(kv)
					}
					p := map[string]any{
						"baseUrl": at.v1(), "apiKey": agentKeyAt("miniq", at.gw()),
						"model": ref, "apiProtocol": miniqProtocol,
					}
					if err := write(p); err != nil {
						return err
					}
					push(p)
					return nil
				}
				if on() {
					return restore(v)
				}
				p := prov()
				if !p.Exists() {
					if v == "" {
						return nil
					}
					return errors.New("miniQ has no provider of its own to use " + v + " with: set one in miniQ's Settings")
				}
				if v == "" || v == p.Get("model").String() {
					return nil
				}
				if err := edit.SetJSON(path, edit.KV{Path: "provider.model", Value: v}); err != nil {
					return err
				}
				m := pushOf(p)
				m["model"] = v
				push(m)
				return nil
			},
			Options: func(cur map[string]string) []Option {
				var own []Option
				if c := cur["model"]; c != "" && !usesMagpie(c) {
					own = append(own, Option{Value: c, Icon: modelIcon("", c)})
				}
				return withVia(group("miniQ", own), "miniq", magpieID+"/")
			},
		}},
	}, path)
}

// what miniQ says after a change (notice.go)
var (
	noticeMiniq = newNotice("Restart miniQ to use this: its running daemon keeps the provider it started with.")
)
