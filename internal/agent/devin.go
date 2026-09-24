package agent

// Devin keeps its settings in ~/.config/devin/config.json (%APPDATA%\devin on
// Windows), the model under agent.model. Devin has no endpoint magpie can
// stand in for — its models are its own — so the options are the families
// and variants `devin models list --format json` reports, not the magpie
// catalog. A family id (swe-2, claude-opus-5-5) follows the family's newest;
// a variant id (swe-2-max) pins one.

import (
	"context"
	"os"
	"path/filepath"
	"runtime"

	"github.com/yetone/magpie/internal/provider"
)

func devin(home, cfg string) *Agent {
	dir := filepath.Join(cfg, "devin")
	if runtime.GOOS == "windows" {
		if app := os.Getenv("APPDATA"); app != "" {
			dir = filepath.Join(app, "devin")
		}
	}
	path := filepath.Join(dir, "config.json")
	return &Agent{
		ID: "devin", Name: "Devin", Icon: "devin",
		Bin: "devin", Dir: dir, Path: path,
		Notice: func() string {
			if Running(`(^|/)devin( |$)`) {
				return "Devin reads its model at start-up — restart open Devin sessions to use this."
			}
			return ""
		},
		Fields: []Field{{
			Key: "model", Label: "model",
			Get: jsonGet(path, "agent.model"),
			Set: jsonSet(path, "agent.model"),
			Options: func(cur map[string]string) []Option {
				return devinOptions(cur["model"])
			},
		}},
	}
}

// providerDevinFamilies lists Devin's models; a var so tests can fake it.
var providerDevinFamilies = func(ctx context.Context) ([]provider.DevinFamily, error) {
	return provider.DevinFamilies(ctx)
}

// devinOptions offers the families first (a pick that follows the family's
// newest model), then every variant under its family.
func devinOptions(cur string) []Option {
	families, err := providerDevinFamilies(context.Background())
	if err != nil {
		if cur == "" {
			return nil
		}
		return []Option{{Value: cur}}
	}
	out := make([]Option, 0, len(families))
	for _, f := range families {
		out = append(out, Option{Value: f.UID, Label: f.Label, Note: "the family's newest", Icon: "devin", Group: f.Label})
		for _, m := range f.Models {
			out = append(out, Option{Value: m.ID, Label: m.Name, Icon: "devin", Group: f.Label})
		}
	}
	if cur != "" {
		for _, o := range out {
			if o.Value == cur {
				return out
			}
		}
		out = append([]Option{{Value: cur, Note: "current"}}, out...)
	}
	return out
}
