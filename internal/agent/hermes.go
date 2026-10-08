package agent

// Hermes Agent (Nous Research) keeps its settings in $HERMES_HOME/config.yaml,
// ~/.hermes/config.yaml by default: the model under model.default, served by
// model.provider, which may name an entry of providers. magpie adds itself
// there as the provider "magpie" (the gateway, spoken to as chat completions,
// with the catalog as its models) and points model.provider at it; a model
// through magpie is "magpie/<provider>/<model>" here and model.default holds
// "<provider>/<model>". The provider and model the user had are stashed and
// put back when magpie steps out. A model of the user's own picked after
// that, in Hermes or on the Agents page, leaves magpie's provider in
// providers: Hermes stays connected, its session model picker still offers
// magpie's models beside its own (lijiho96940561 on X: Hermes went to Not
// connected and magpie's models left its picker), and only Disconnect
// takes the provider out.

import (
	"path/filepath"
	"strings"

	"github.com/yetone/magpie/internal/edit"
)

func hermes(home string) *Agent { return hermesIn(here(home)) }

// hermesIn is Hermes at a place: this machine's home, or a WSL distro's
// (see wsl.go), where HERMES_HOME isn't read and its provider names the
// gateway as the distro reaches it.
func hermesIn(at place) *Agent {
	dir := at.getenv("HERMES_HOME")
	if dir == "" {
		dir = filepath.Join(at.home, ".hermes")
	}
	path := filepath.Join(dir, "config.yaml")
	hermesProvider := func() hermesProviderEntry { return hermesProviderAt(at.gw()) }
	key := "hermes:" + path + ":"
	getKey := func(k string) string { v, _ := edit.GetYAML(path, k); return v }
	onMagpie := func() bool { return getKey("model.provider") == magpieID }
	// joined: magpie's provider is in providers, though the model is one of
	// Hermes's own
	joined := func() bool { _, ok := edit.GetYAML(path, "providers."+magpieID+".base_url"); return ok }
	// restore puts back the provider and model the user had before magpie,
	// magpie's provider left beside them
	restore := func() error {
		var del []string
		var kvs []edit.KV
		for _, k := range []string{"model.provider", "model.default"} {
			if v := unstash(key + k); v != "" {
				kvs = append(kvs, edit.KV{Path: k, Value: v})
			} else {
				del = append(del, k)
			}
		}
		if len(del) > 0 {
			if err := edit.DelYAML(path, del...); err != nil {
				return err
			}
		}
		if len(kvs) == 0 {
			return nil
		}
		return edit.SetYAML(path, kvs...)
	}
	return &Agent{
		ID: "hermes", Name: "Hermes Agent", Icon: "hermes", Aliases: []string{"hermes-agent"}, Spelled: prefixed,
		UA:  []string{"hermes-agent"},
		Bin: "hermes", Dir: dir, Path: path,
		Sync: func() error {
			return syncYAML(path, "providers."+magpieID, func() any { return hermesProvider() })
		},
		Notice: func() string {
			if Running(`(^|/)hermes( |$)`) {
				return "Hermes reads its settings at start-up — restart open Hermes sessions to use this."
			}
			return ""
		},
		Joined: joined,
		// Disconnect takes magpie's provider out; the model, when it is
		// magpie's, goes back by the field
		Unwire: func() error {
			if !joined() {
				return nil
			}
			return edit.DelYAML(path, "providers."+magpieID)
		},
		Check: func() string {
			if !onMagpie() {
				return ""
			}
			return wiringOff("Hermes", path, func(k string) (string, bool) { return edit.GetYAML(path, "providers."+magpieID+"."+k) },
				"base_url", at.v1(), "api_key", at.gwKey())
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: func() string {
				v := getKey("model.default")
				if v == "" {
					v = getKey("model.model")
				}
				if v != "" && onMagpie() {
					return magpieID + "/" + v
				}
				return v
			},
			Set: func(v string) error {
				if v == "" {
					if onMagpie() {
						return restore()
					}
					return edit.DelYAML(path, "model.default")
				}
				if ref, ok := strings.CutPrefix(v, magpieID+"/"); ok && isMagpie(ref) {
					if !onMagpie() {
						stash(map[string]string{
							key + "model.provider": getKey("model.provider"),
							key + "model.default":  getKey("model.default"),
						})
					}
					return edit.SetYAML(path,
						edit.KV{Path: "providers." + magpieID, Value: hermesProvider()},
						edit.KV{Path: "model.provider", Value: magpieID},
						edit.KV{Path: "model.default", Value: ref},
					)
				}
				if onMagpie() {
					if err := restore(); err != nil {
						return err
					}
				}
				return edit.SetYAML(path, edit.KV{Path: "model.default", Value: v})
			},
			Options: func(cur map[string]string) []Option {
				return append(ownOptions("", cur["model"]), viaMagpie("hermes", magpieID+"/")...)
			},
		}, {
			// agent.reasoning_effort, which Hermes's /reasoning saves; none
			// turns reasoning off, and unset Hermes asks for medium
			Key: "effort", Label: "effort",
			Get: func() string { return getKey("agent.reasoning_effort") },
			Set: func(v string) error {
				if v == "" {
					return edit.DelYAML(path, "agent.reasoning_effort")
				}
				return edit.SetYAML(path, edit.KV{Path: "agent.reasoning_effort", Value: v})
			},
			Options: func(map[string]string) []Option {
				return static("none", "minimal", "low", "medium", "high", "xhigh")
			},
		}},
	}
}

type hermesProviderEntry struct {
	Name    string            `yaml:"name"`
	BaseURL string            `yaml:"base_url"`
	APIKey  string            `yaml:"api_key"`
	APIMode string            `yaml:"api_mode"`
	Headers map[string]string `yaml:"extra_headers"`
	Models  []string          `yaml:"models"`
}

// hermesProviderAt is magpie's entry under providers, for a Hermes reaching
// the gateway at gw. Hermes sends its own User-Agent only from a recent
// release on, so the header names it for the gateway's usage view.
func hermesProviderAt(gw string) hermesProviderEntry {
	ms := []string{}
	for _, m := range magpieModels("hermes") {
		ms = append(ms, m.ID)
	}
	return hermesProviderEntry{
		Name: magpieID, BaseURL: gw + "/v1", APIKey: keyAt(gw), APIMode: "chat_completions",
		Headers: map[string]string{"User-Agent": "hermes-agent"}, Models: ms,
	}
}
