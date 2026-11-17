package plugin

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/yetone/magpie/internal/filememo"
)

// PrivacyLauncher is the command a plugin declares in magpie.privacyModels.
// Its model's local path is appended to Args. Only a plugin's package may
// declare the launcher; saved Options supply model versions and paths.
type PrivacyLauncher struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Version string   `json:"-"`
}

// PrivacyModels reads a plugin's launcher and whether it has no host entry.
func PrivacyModels(target string) (*PrivacyLauncher, bool) {
	type manifest struct {
		Main    string          `json:"main"`
		Exports json.RawMessage `json:"exports"`
		Version string          `json:"version"`
		Magpie  struct {
			Privacy *PrivacyLauncher `json:"privacyModels"`
		} `json:"magpie"`
	}
	pkg, err := filememo.Read("privacy-models", filepath.Join(target, "package.json"), func(data []byte) (manifest, error) {
		var m manifest
		err := json.Unmarshal(data, &m)
		return m, err
	})
	if err != nil || pkg.Magpie.Privacy == nil || strings.TrimSpace(pkg.Magpie.Privacy.Command) == "" {
		return nil, false
	}
	launcher := *pkg.Magpie.Privacy
	launcher.Command = strings.TrimSpace(launcher.Command)
	launcher.Version = pkg.Version
	return &launcher, pkg.Main == "" && (len(pkg.Exports) == 0 || string(pkg.Exports) == "null")
}
