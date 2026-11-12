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

// SetCodexOpen sets what Codex opens in from its Agents row menu: "",
// "app" or "terminal" (Yink88564391 on X).
func SetCodexOpen(v string) error {
	v = strings.TrimSpace(v)
	if !slices.Contains(settings.CodexOpens, v) {
		return fmt.Errorf("Open Codex with must be one of %q, not %q", settings.CodexOpens[1:], v)
	}
	s := settings.Load()
	if s.CodexOpen == v {
		return nil
	}
	s.CodexOpen = v
	return settings.Save(s)
}

// CodexClient is the Codex client magpie presents to the ChatGPT backend
// (settings.CodexClient): "" for Codex CLI, "desktop" for the Codex app. A
// value this magpie doesn't know (a newer one's, a file edited by hand) is
// read as "" and changes nothing.
func CodexClient() string {
	v := strings.TrimSpace(heldSettings().CodexClient)
	if !slices.Contains(settings.CodexClients, v) {
		return ""
	}
	return v
}

// SetCodexClient sets settings.CodexClient.
func SetCodexClient(v string) error {
	v = strings.TrimSpace(v)
	if !slices.Contains(settings.CodexClients, v) {
		return fmt.Errorf("the Codex client must be one of %q, not %q", settings.CodexClients[1:], v)
	}
	s := settings.Load()
	if s.CodexClient == v {
		return nil
	}
	s.CodexClient = v
	return settings.Save(s)
}
