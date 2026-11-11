package provider

// A model's own speed (kdajenks on X): the service tier the user gave one
// model in its Model settings, kept in settings' ModelTiers by "<provider
// id>/<model id>": "priority" (Codex's Fast) or "ultrafast" (its Ultra
// Fast, which a plan that doesn't offer it is sent priority in place of). The
// gateway sends every request for the model so, for each agent that has no
// pick of its own for it (an agent's fast pick, FastPicks, is that agent's
// and goes first). A routing group's member is sent as the group says.

import (
	"fmt"
	"strings"

	"github.com/yetone/magpie/internal/settings"
)

// ModelTier is the tier the user gave provider pid's model, "" for none. It
// reads the held settings, as it is asked on every request.
func ModelTier(pid, model string) string {
	if pid == "" || model == "" {
		return ""
	}
	return heldSettings().ModelTiers[pid+"/"+model]
}

// CleanModelTier reads the words people use for a tier: "" (also off,
// usual, standard) for none, "priority" (fast), "ultrafast" (ultra,
// ultra-fast).
func CleanModelTier(v string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "off", "none", "usual", "standard", "default":
		return "", nil
	case "priority", "fast":
		return "priority", nil
	case "ultrafast", "ultra", "ultra-fast", "ultra_fast":
		return "ultrafast", nil
	}
	return "", fmt.Errorf("a model's speed is one of usual, fast or ultrafast, not %q", v)
}

// SetModelTier sends a provider's model, spelt "provider/model", at tier:
// "" the usual speed, "priority" or "ultrafast" (see CleanModelTier). Only
// a model with a fast mode magpie can ask for (CanFast) is switched on.
func SetModelTier(ref, tier string) error {
	_, err := setModelTier(ref, tier)
	return err
}

func setModelTier(ref, tier string) (bool, error) {
	p, model, err := splitRef(ref)
	if err != nil {
		return false, err
	}
	if tier, err = CleanModelTier(tier); err != nil {
		return false, err
	}
	key := p.ID + "/" + model
	if tier != "" {
		if !CanFast(*p, model) {
			return false, fmt.Errorf("%s has no fast mode magpie can ask for", key)
		}
		if tier == "ultrafast" && !CanUltra(*p, model) {
			return false, fmt.Errorf("no account offers Ultrafast on %s", key)
		}
	}
	s := settings.Load()
	if s.ModelTiers[key] == tier {
		return false, nil
	}
	if tier == "" {
		delete(s.ModelTiers, key)
	} else {
		if s.ModelTiers == nil {
			s.ModelTiers = map[string]string{}
		}
		s.ModelTiers[key] = tier
	}
	if err := settings.Save(s); err != nil {
		return false, err
	}
	return true, nil
}
