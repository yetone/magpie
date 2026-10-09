package agent

// Pencil (pen.dev, the design canvas whose agent runs models) takes a
// custom provider of one's own in ~/.pencil/models.json, in Pi's format
// (徐小小 on Discord):
//
//	{"providers":{"<id>":{"name":…,"baseUrl":…,"api":"openai-responses",
//	  "models":[{"id":…,"name":…,"reasoning":true,"input":["text","image"],
//	    "cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0},
//	    "contextWindow":…,"maxTokens":…,"compat":{}}]}}}
//
// Its settings offer a custom provider on OpenAI Completions, OpenAI
// Responses or Anthropic Messages (docs.pen.dev, Authentication) — Pi's
// openai-completions, openai-responses and anthropic-messages — set for the
// provider as a whole. So magpie's models are Pi's entries (piModelJSON)
// with every model on the provider's openai-completions at the gateway's
// /v1, which the gateway takes for every model: a model's own api and
// baseUrl, which Pi reads but Pencil's own editor never writes, are left
// out. Each model carries every field Pencil writes for one. The model is
// picked in Pencil's agent composer, not in a file magpie knows of, so what
// magpie sets is whether its models are in that picker; the user's other
// providers stay as they are. Its requests carry magpie-pencil, as its
// User-Agent isn't known.

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

func pencil(home string) *Agent {
	dir := filepath.Join(home, ".pencil")
	path := filepath.Join(dir, "models.json")
	key := "providers." + magpieID
	wired := func() bool { _, ok := edit.GetJSON(path, key); return ok }
	return &Agent{
		ID: "pencil", Name: "Pencil", Icon: "generic", Aliases: []string{"pen.dev", "pen-dev", "pendev"},
		Dir: dir, Path: path,
		// ~/.pencil is the app's (and pen.dev's CLI's) once it has run; the
		// app installed and never opened is found by its bundle on a Mac.
		// No command: `pencil` and `pen` are common names.
		detect: func() bool { return isDir(dir) || pencilApp(home) },
		Notice: func() string {
			if runtime.GOOS == "windows" || Running(`Pencil\.app/`, `pen\.dev\.app/`, `(^|/)[Pp]encil( |$)`) {
				return "Pencil reads its custom models at start-up — restart Pencil to see magpie's models in its model picker."
			}
			return ""
		},
		Sync: func() error {
			return syncJSON(path, key, theirsKept(path, key, func() any { return pencilProviderJSON(gateway.URL()) }, "models"))
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
				if v == "" {
					return edit.DelJSON(path, key)
				}
				return edit.SetJSON(path, edit.KV{Path: key, Value: theirsKept(path, key, func() any { return pencilProviderJSON(gateway.URL()) }, "models")()})
			},
			Options: func(map[string]string) []Option {
				return []Option{{Value: magpieID, Label: "magpie", Icon: "magpie", Note: "every magpie model in Pencil's model picker"}}
			},
		}},
	}
}

// pencilApp reports whether Pencil's app is installed where a Mac keeps
// apps, under its name before and after the pen.dev rename.
func pencilApp(home string) bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	for _, d := range []string{"/Applications", filepath.Join(home, "Applications")} {
		for _, n := range []string{"Pencil.app", "pen.dev.app"} {
			if _, err := os.Stat(filepath.Join(d, n)); err == nil {
				return true
			}
		}
	}
	return false
}

// pencilProviderJSON is magpie's provider in Pencil's models.json: Pi's
// entries (piModelJSON), on the provider's openai-completions throughout,
// each with the fields Pencil writes for a model — Pi's defaults where
// magpie knows no better (a 128K window, 16K out) and no cost, magpie's
// spend being counted by magpie.
func pencilProviderJSON(gw string) map[string]any {
	ms := []map[string]any{}
	for _, m := range magpieModels("pencil") {
		e := piModelJSON(m, gw, false)
		if _, ok := e["input"]; !ok {
			e["input"] = []string{"text"}
		}
		if _, ok := e["contextWindow"]; !ok {
			e["contextWindow"] = 128000
		}
		if _, ok := e["maxTokens"]; !ok {
			e["maxTokens"] = min(16384, e["contextWindow"].(int))
		}
		e["cost"] = map[string]any{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0}
		e["compat"] = map[string]any{}
		ms = append(ms, e)
	}
	return map[string]any{"name": "magpie", "baseUrl": gw + "/v1", "api": "openai-completions",
		"apiKey": gateway.TokenFor("pencil"), "models": ms}
}
