package pii

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/yetone/magpie/internal/lazyre"
)

// privacyVersion is one selectable model from a plugin's Options.models.
type privacyVersion struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Path     string  `json:"path"`
	Revision string  `json:"revision,omitempty"`
	MinScore float64 `json:"minScore,omitempty"`
}

var privacyID = lazyre.New(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// parsePrivacyVersions checks a plugin's saved model configurations.
func parsePrivacyVersions(options map[string]any) ([]privacyVersion, error) {
	models, ok := options["models"]
	if !ok {
		return nil, nil
	}
	data, err := json.Marshal(models)
	if err != nil {
		return nil, err
	}
	var versions []privacyVersion
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&versions); err != nil {
		return nil, fmt.Errorf("invalid privacy model options: %w", err)
	}
	seen := make(map[string]bool, len(versions))
	for i, v := range versions {
		if !privacyID.MatchString(v.ID) || seen[v.ID] {
			return nil, fmt.Errorf("privacy models[%d]: invalid or duplicate id %q", i, v.ID)
		}
		// a minScore under Floor is refused as a user's is (Threshold):
		// what any model masks by is at least that sure
		if strings.TrimSpace(v.Name) == "" || !filepath.IsAbs(v.Path) || v.MinScore != 0 && !(v.MinScore >= Floor && v.MinScore < 1) {
			return nil, fmt.Errorf("privacy models[%d]: give a name, an absolute path and a minScore of 0 (the default, %g) or from %g to under 1", i, Floor, Floor)
		}
		seen[v.ID] = true
	}
	return versions, nil
}
