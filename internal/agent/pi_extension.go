package agent

import (
	"bytes"
	"cmp"
	_ "embed"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// Pi's codemode calls classifier and image models (models.classify,
// models.generateImages; Pi's docs/models.md "Use classifier models" and
// "Use image models"), wangbinyq on Discord. Pi's models.json defines chat
// models only: its model definitions have no type, and a custom provider's
// classifier or image models come from an extension alone
// (docs/custom-provider.md, "Register mixed-operation models"). So magpie
// writes one, extensions/magpie.ts in Pi's agent folder, which Pi loads by
// itself. It lists magpie's decision models as provider magpie-systemone,
// on Pi's own System One client against magpie's /v1/systemone, and its
// image models as magpie-images, drawn by /v1/images/generations. It is
// there while models.json has providers.magpie, and gone with it.

//go:embed pi_extension.ts
var piExtensionSource string

// piExtensionMark opens the file magpie writes. A file at that path
// without it is the user's own, and is never written over or removed.
const piExtensionMark = "// magpie: written by magpie"

// piExtensionPath is magpie's extension in Pi's agent folder dir.
func piExtensionPath(dir string) string {
	return filepath.Join(dir, "extensions", "magpie.ts")
}

// piExtensionModel is a model as the extension registers it.
type piExtensionModel struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Input []string `json:"input,omitempty"`
}

// piInput is a model's input as Pi takes it. pi-ai's
// MODEL_INPUT_MODALITIES are "text" and "image" alone, so audio or video
// a model also takes has no word there, as in models.json's list.
func piInput(images bool) []string {
	if images {
		return []string{"text", "image"}
	}
	return []string{"text"}
}

// piExtensionFor is the extension's source for a gateway at baseURL (its
// /v1) and key; "" when magpie has no decision or image model to list.
func piExtensionFor(baseURL, key string) string {
	ds := provider.OfferedDeciders(nil)
	for i := range ds {
		ds[i].Name = cmp.Or(ds[i].Name, ds[i].Model)
	}
	labels := provider.LabelsFor("pi", ds)
	classifiers := make([]piExtensionModel, 0, len(ds))
	for i, e := range ds {
		// Pi sends a classifier the images of its context only when its
		// input has "image" (pi-ai's ClassifierContext.images): one that
		// takes them (Google's gemini-decision-1, Cloudflare's Clefs) is
		// told so where the gateway carries them to it (#7521)
		images := e.Images && gateway.SystemOneTakesImages(*e.Provider, e.Model)
		classifiers = append(classifiers, piExtensionModel{ID: e.ID, Name: labels[i], Input: piInput(images)})
	}
	images := []piExtensionModel{}
	for _, m := range gateway.ImageModels() {
		images = append(images, piExtensionModel{ID: m.ID, Name: m.Name, Input: piInput(m.Images)})
	}
	if len(classifiers) == 0 && len(images) == 0 {
		return ""
	}
	data, _ := json.Marshal(map[string]any{
		"baseUrl": baseURL, "apiKey": key,
		"classifiers": classifiers, "images": images,
	})
	return strings.Replace(piExtensionSource, "__MAGPIE__", string(data), 1)
}

// piExtensionSynced brings magpie's extension in Pi's agent folder dir
// to what models.json says: written while it has providers.magpie and
// magpie has models to list, else gone. A file there magpie didn't write
// is left as it is.
func piExtensionSynced(dir, modelsPath, baseURL, key string) error {
	path := piExtensionPath(dir)
	cur, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		cur = nil
	case err != nil:
		return err // unknown: neither written over nor removed
	case !bytes.HasPrefix(edit.TrimBOM(cur), []byte(piExtensionMark)):
		return nil // the user's own
	}
	want := ""
	if _, on := edit.GetJSON(modelsPath, "providers."+magpieID); on {
		want = piExtensionFor(baseURL, key)
	}
	if want == "" {
		if cur == nil {
			return nil
		}
		return os.Remove(path)
	}
	if string(cur) == want {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return edit.WriteAtomic(path, []byte(want))
}
