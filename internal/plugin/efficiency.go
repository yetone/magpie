package plugin

import "github.com/yetone/magpie/internal/proc"

// SetEfficiencyMode puts the running plugin hosts (magpie-bun), and any
// host started while on, in Windows' efficiency mode, or hands the choice
// back to Windows. What a plugin starts in turn is left to Windows, so a
// CLI it runs isn't slowed.
func SetEfficiencyMode(on bool) {
	live.Lock()
	if live.efficient == on {
		live.Unlock()
		return
	}
	live.efficient = on
	trees := make([]*proc.Tree, 0, len(live.hosts))
	for h := range live.hosts {
		if h.tree != nil {
			trees = append(trees, h.tree)
		}
	}
	live.Unlock()
	for _, t := range trees {
		t.SetEfficiencyMode(on)
	}
}
