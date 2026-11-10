package provider

import (
	"fmt"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/settings"
)

// CodexServiceTier is the service tier set for every request Codex sends
// (settings.CodexServiceTier): "" as Codex asks it, settings.CodexTierStandard
// for none, "priority" or "ultrafast". A value this magpie doesn't know
// (a newer one's, a file edited by hand) is read as "" and changes nothing.
func CodexServiceTier() string {
	v := strings.TrimSpace(heldSettings().CodexServiceTier)
	if !slices.Contains(settings.CodexServiceTiers, v) {
		return ""
	}
	return v
}

// SetCodexServiceTier sets settings.CodexServiceTier.
func SetCodexServiceTier(tier string) error {
	tier = strings.TrimSpace(tier)
	if !slices.Contains(settings.CodexServiceTiers, tier) {
		return fmt.Errorf("Codex's service tier must be one of %q, not %q", settings.CodexServiceTiers[1:], tier)
	}
	s := settings.Load()
	if s.CodexServiceTier == tier {
		return nil
	}
	s.CodexServiceTier = tier
	return settings.Save(s)
}
