package agent

import (
	"encoding/json"
	"os"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/lazyre"
)

// piMagpieExt matches a Pi package source for the pi-magpie extension:
// npm:pi-magpie, npm:pi-magpie@0.1.6, or a path or git source ending in it.
var piMagpieExt = lazyre.New(`(^|[:/\\])pi-magpie(@[^/\\]*)?(#.*)?$`)

// piMagpieInstalled says Pi's settings.json lists the pi-magpie extension
// in "packages". It serves provider "magpie" from magpie's live catalog,
// and warns at every start while models.json also names providers.magpie:
// that block gives every model one baseUrl and a frozen list. A settings
// file that can't be read is unknown, so not installed.
func piMagpieInstalled(settings string) bool {
	raw, err := os.ReadFile(settings)
	if err != nil {
		return false
	}
	var s struct {
		Packages []json.RawMessage `json:"packages"`
	}
	if json.Unmarshal(edit.TrimBOM(raw), &s) != nil {
		return false
	}
	for _, p := range s.Packages {
		var src string
		if json.Unmarshal(p, &src) != nil {
			var o struct {
				Source string `json:"source"`
			}
			if json.Unmarshal(p, &o) != nil {
				continue
			}
			src = o.Source
		}
		if piMagpieExt.MatchString(src) {
			return true
		}
	}
	return false
}

// dropPiBlock takes magpie's own providers.magpie out of models.json, the
// rest of the file as it was, after keeping a copy of the file beside it
// (models.json.before-pi-magpie, the first one kept). off is wiringOff for
// the block: "" says it points at this gateway with magpie's key, so it is
// magpie's; any other block is the user's and stays.
func dropPiBlock(modelsPath string, off func() string) error {
	if _, has := edit.GetJSON(modelsPath, "providers."+magpieID); !has || off() != "" {
		return nil
	}
	if raw, err := os.ReadFile(modelsPath); err == nil {
		if _, e := os.Stat(modelsPath + ".before-pi-magpie"); e != nil {
			if err := edit.WriteAtomic(modelsPath+".before-pi-magpie", raw); err != nil {
				return err
			}
		}
	}
	return edit.DelJSON(modelsPath, "providers."+magpieID)
}
