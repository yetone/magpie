package agent

// Devin keeps its settings in ~/.config/devin/config.json (%APPDATA%\devin on
// Windows), the model under agent.model. Devin has no endpoint magpie can
// stand in for — its models are its own — so the options are the families
// `devin models list --format json` reports, not the magpie catalog, offered
// as the provider offers them: each family one model (and its fast run one
// more, claude-opus-5-5-fast), with an effort beside it. Devin's config has
// no effort of its own — a variant id is the effort it runs at — so the pair
// is written as the one id: a family's id with no effort (it follows the
// family's newest), its variant at the effort picked (claude-opus-5-5-high,
// claude-opus-5-5-high-fast) otherwise, and read back the same way.

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/provider"
)

func devin(home, cfg string) *Agent {
	dir := filepath.Join(cfg, "devin")
	if runtime.GOOS == "windows" {
		if app := appdir.Getenv("APPDATA"); app != "" {
			dir = filepath.Join(app, "devin")
		}
	}
	path := filepath.Join(dir, "config.json")
	get, set := jsonGet(path, "agent.model"), jsonSet(path, "agent.model")
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
			// the family the id Devin has is of, or the id itself when no
			// family has it at an effort
			Get: func() string {
				m, _ := devinSplit(get())
				return m
			},
			// a family picked keeps the effort the old id was at, as near
			// as the family has it; with none, its own id; an id that is no
			// family's (swe-2-max, typed in) goes as it is
			Set: func(v string) error {
				if v == "" {
					return set("")
				}
				families, err := providerDevinFamilies(context.Background())
				if err != nil {
					return set(v)
				}
				_, effort := provider.DevinSplit(families, get())
				return set(provider.DevinPick(families, v, effort))
			},
			Options: func(cur map[string]string) []Option {
				return devinOptions(cur["model"])
			},
		}, {
			// not a key of Devin's: the effort the model's id is at, and
			// picking one writes the model's variant at it
			Key: "effort", Label: "effort",
			Get: func() string {
				_, e := devinSplit(get())
				return e
			},
			Set: func(v string) error {
				cur := get()
				if cur == "" {
					return errors.New("pick Devin's model first: its effort is part of the model's id")
				}
				families, err := providerDevinFamilies(context.Background())
				if err != nil {
					return err
				}
				model, _ := provider.DevinSplit(families, cur)
				return set(provider.DevinPick(families, model, v))
			},
			Options: func(cur map[string]string) []Option {
				return static(devinEfforts(cur["model"])...)
			},
		}},
	}
}

// providerDevinFamilies lists Devin's models; a var so tests can fake it.
var providerDevinFamilies = func(ctx context.Context) ([]provider.DevinFamily, error) {
	return provider.DevinFamilies(ctx)
}

// devinSplit is the id Devin's config holds as the picker shows it: the
// model and the effort it is at (provider.DevinSplit), or the id at none
// while Devin's list can't be read.
func devinSplit(id string) (model, effort string) {
	if id == "" {
		return "", ""
	}
	families, err := providerDevinFamilies(provider.DevinNoWait(context.Background()))
	if err != nil {
		return id, ""
	}
	return provider.DevinSplit(families, id)
}

// devinEfforts are the levels the model the picker shows has, none for one
// at no effort (glm-5-2-1m) or one Devin's list doesn't have.
func devinEfforts(model string) []string {
	families, err := providerDevinFamilies(provider.DevinNoWait(context.Background()))
	if err != nil {
		return nil
	}
	for _, m := range provider.DevinOffered(families) {
		if m.ID == model {
			return m.Efforts
		}
	}
	return nil
}

// devinOptions offers the families as the provider lists them — each one
// model, its fast run another, the effort picked beside it — and the
// variants at no effort (glm-5-2-1m). They are Devin's own models, not
// borrowed ones, so like every agent's own list they sit flat — a group
// each would only crowd the picker's rail with one icon per family.
func devinOptions(cur string) []Option {
	families, err := providerDevinFamilies(provider.DevinNoWait(context.Background()))
	if err != nil {
		if cur == "" {
			return nil
		}
		return []Option{{Value: cur}}
	}
	ms := provider.DevinOffered(families)
	out := make([]Option, 0, len(ms))
	for _, m := range ms {
		out = append(out, Option{Value: m.ID, Label: m.Name, Icon: "devin", Context: m.Context})
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
