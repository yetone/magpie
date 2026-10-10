package agent

// Muse Code, Meta's muse CLI, keeps its settings in
// ${XDG_CONFIG_HOME:-~/.config}/muse/settings.json, a file it refuses
// without "schema_version": 1. Where its requests go is endpoint_transport:
// base_url, the /responses it asks (OpenAI's Responses API, streamed), and
// auth, "bearer" for the Meta sign-in's token (auth.json, or META_API_KEY)
// or "none" for no Authorization at all and no sign-in asked for. The model
// sessions start on is the top-level model, one of those Muse lists from
// <base_url's host>/muse-code/models, which the gateway serves it.
//
// magpie sets endpoint_transport to the gateway with auth "none" — so the
// Meta sign-in's token, which magpie never reads, isn't sent to it either —
// and model to the catalog id; the endpoint_transport and model the user had
// are stashed and put back when magpie steps out, and the file's other keys
// are left as they are.

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/edit"
)

const (
	museTransport = "endpoint_transport"
	museModel     = "model"
)

func muse(cfg string) *Agent { return museAt(here(""), cfg) }

// museIn is Muse Code in a WSL distro (see wsl.go): ~/.config/muse, as
// XDG_CONFIG_HOME there isn't read.
func museIn(at place) *Agent { return museAt(at, filepath.Join(at.home, ".config")) }

// museAt is Muse Code with its config under cfg, reaching the gateway as
// at does.
func museAt(at place, cfg string) *Agent {
	gatewayV1 := at.v1
	dir := filepath.Join(cfg, "muse")
	path := filepath.Join(dir, "settings.json")
	keyTransport := "muse:" + path + ":" + museTransport
	keyModel := "muse:" + path + ":" + museModel
	// keyNew marks a file magpie made, which goes again once nothing but
	// the schema version magpie gave it is left
	keyNew := "muse:" + path + ":new"
	// ours: Muse's requests go to the gateway
	ours := func() bool { v, _ := edit.GetJSON(path, museTransport+".base_url"); return v == gatewayV1() }
	model := func() string { v, _ := edit.GetJSON(path, museModel); return v }
	// on: magpie's — the endpoint is the gateway's, or the model one of
	// magpie's catalog, which no other endpoint serves (Meta's have no "/")
	on := func() bool { return ours() || isMagpie(model()) }
	get := func() string {
		v := model()
		if v != "" && on() {
			return magpieID + "/" + v
		}
		return v
	}
	// put sets the top-level model, or takes it out for ""
	put := func(v string) error {
		if v == "" {
			return edit.DelJSON(path, museModel)
		}
		return edit.SetJSON(path, edit.KV{Path: museModel, Value: v})
	}
	// restore puts back the endpoint_transport the user had, or takes
	// magpie's out where they had none
	restore := func() error {
		if was := unstash(keyTransport); was != "" && json.Valid([]byte(was)) {
			return edit.SetJSON(path, edit.KV{Path: museTransport, Value: json.RawMessage(was)})
		}
		return edit.DelJSON(path, museTransport)
	}
	return atomic(&Agent{
		ID: "muse", Name: "Muse Code", Icon: "meta-color", Aliases: []string{"muse-code", "musecode"},
		// muse-build/1.4.2 (non-interactive; macos-aarch64; …)
		UA:  []string{"muse-build", "muse-code"},
		Bin: "muse", Dir: dir, Path: path,
		Notice: func() string {
			if Running(`(^|/)muse( |$)`) {
				return noticeMuse.String()
			}
			return ""
		},
		Check: func() string {
			if !on() {
				return ""
			}
			if model() == "" {
				return "Muse Code's model (settings.json) is gone, so it no longer reaches magpie"
			}
			return wiringOff("Muse Code", path, func(k string) (string, bool) { return edit.GetJSON(path, museTransport+"."+k) },
				"base_url", gatewayV1(), "auth", "none")
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: get,
			Set: func(v string) error {
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					// what the user had, unless magpie stashed it already
					// and something else has since moved the endpoint
					if !on() {
						t, _ := edit.GetJSON(path, museTransport)
						m, _ := edit.GetJSON(path, museModel)
						forget(keyTransport, keyModel, keyNew)
						stash(map[string]string{keyTransport: t, keyModel: m})
						if !isFile(path) {
							stash(map[string]string{keyNew: "1"})
						}
					}
					// Muse reads no file without its schema version
					if _, ok := edit.GetJSON(path, "schema_version"); !ok {
						if err := edit.SetJSON(path, edit.KV{Path: "schema_version", Value: 1}); err != nil {
							return err
						}
					}
					if err := edit.SetJSON(path, edit.KV{Path: museTransport, Value: map[string]any{"base_url": gatewayV1(), "auth": "none"}}); err != nil {
						return err
					}
					return put(ref)
				}
				made := false
				if on() {
					made = unstash(keyNew) != ""
					// back to what the user had
					if v == "" {
						v = unstash(keyModel)
					} else {
						forget(keyModel)
					}
					if err := restore(); err != nil {
						return err
					}
				}
				if err := put(v); err != nil {
					return err
				}
				if made {
					if raw, err := edit.Read(path); err == nil {
						if left := gjson.ParseBytes(raw).Map(); len(left) == 1 && left["schema_version"].Int() == 1 {
							return edit.Remove(path)
						}
					}
				}
				return nil
			},
			Options: func(cur map[string]string) []Option {
				var own []Option
				if c := cur["model"]; c != "" && !usesMagpie(c) {
					own = append(own, Option{Value: c, Icon: museIcon(c)})
				}
				return append(group("Muse Code", own), viaMagpie("muse", magpieID+"/")...)
			},
		}},
	}, path)
}

// museIcon is the logo of a model of Muse's own: Meta's for its Muse
// models, else the vendor's its id says.
func museIcon(id string) string {
	if strings.HasPrefix(id, "muse") {
		return "meta-color"
	}
	return modelIcon("", id)
}

// what muse says after a change (notice.go)
var (
	noticeMuse = newNotice("Muse Code reads its settings at start-up — restart open muse sessions to use this.")
)
