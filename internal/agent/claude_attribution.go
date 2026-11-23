package agent

import (
	"fmt"
	"path/filepath"

	"github.com/yetone/magpie/internal/edit"
)

// Claude Code adds a Co-Authored-By trailer to the commits it makes
// (9_bishi on X wanted it off). Its own setting for that is
// attribution.commit in settings.json: unset keeps the trailer, "" hides
// it, other text replaces it; `attribution: false` hides all attribution,
// and the deprecated includeCoAuthoredBy: false does until attribution
// sets commit or pr. The pull request text (attribution.pr) is left alone.
const (
	AttributionOn     = "on"
	AttributionOff    = "off"
	AttributionCustom = "custom" // the user's own trailer text: magpie never writes over it
)

type ClaudeSettings struct {
	path   string
	distro *distro
}

func (a *Agent) ClaudeSettings() (*ClaudeSettings, error) {
	if a.unreached != nil {
		return nil, a.unreached
	}
	if a.WSL != "" && (a.claudeSettings == nil || a.claudeSettings.distro == nil || !a.claudeSettings.distro.live()) {
		return nil, fmt.Errorf("Claude Code settings are unavailable in WSL %s", a.WSL)
	}
	if a.claudeSettings == nil {
		return nil, fmt.Errorf("%s has no Claude Code settings", a.Name)
	}
	return a.claudeSettings, nil
}

func localClaudeSettings(home string) ClaudeSettings {
	return ClaudeSettings{path: filepath.Join(here(tuneHome(home)).claudeDir(), "settings.json")}
}

// ClaudeAttribution reads whether Claude Code adds its commit trailer.
func ClaudeAttribution(home string) string {
	return localClaudeSettings(home).Attribution()
}

func (settings ClaudeSettings) Attribution() string {
	p := settings.path
	if raw, ok := edit.GetJSONRaw(p, "attribution"); ok && raw == "false" {
		return AttributionOff
	}
	if v, ok := edit.GetJSON(p, "attribution.commit"); ok {
		if v == "" {
			return AttributionOff
		}
		return AttributionCustom
	}
	if raw, ok := edit.GetJSONRaw(p, "includeCoAuthoredBy"); ok && raw == "false" {
		return AttributionOff
	}
	return AttributionOn
}

// ClaudeAttributionSet turns Claude Code's commit trailer on (Claude Code's
// own default: what hid it is taken out) or off (attribution.commit = "").
// A trailer text the user wrote is left as it is, whatever is asked.
func ClaudeAttributionSet(home string, on bool) error {
	return localClaudeSettings(home).SetAttribution(on)
}

func (settings ClaudeSettings) SetAttribution(on bool) error {
	p := settings.path
	cur := settings.Attribution()
	if cur == AttributionCustom {
		return fmt.Errorf("attribution.commit holds your own text in Claude Code's settings.json: change it there")
	}
	if on == (cur == AttributionOn) {
		return nil
	}
	if !on {
		return edit.SetJSON(p, edit.KV{Path: "attribution.commit", Value: ""})
	}
	// everything that hides the trailer is taken out: the user clicked On
	var del []string
	if raw, ok := edit.GetJSONRaw(p, "attribution"); ok && raw == "false" {
		del = append(del, "attribution")
	}
	if v, ok := edit.GetJSON(p, "attribution.commit"); ok && v == "" {
		del = append(del, "attribution.commit")
	}
	if raw, ok := edit.GetJSONRaw(p, "includeCoAuthoredBy"); ok && raw == "false" {
		del = append(del, "includeCoAuthoredBy")
	}
	return edit.DelJSON(p, del...)
}
