// Package warmups is warm-up and check-in: start a subscription's usage
// window at a time the user picks, and claim vendors' daily check-in
// credits. While one is set, it stays on.
package warmups

import (
	"github.com/yetone/magpie/internal/pluginrt"
	"github.com/yetone/magpie/internal/settings"
)

func init() {
	pluginrt.Register(pluginrt.Manifest{
		ID: "warmups", API: "^1", Requires: []string{"window", "settings"},
		Name: "Warm-up and check-in", Sub: "Start a subscription's usage window at a time you pick, and claim vendors' daily check-in credits",
		Opens: "providers",
		One:   "1 warm-up or check-in on", Many: "{n} warm-ups and check-ins on",
		InUse: func() (int, error) {
			s, err := settings.LoadChecked()
			if err != nil {
				return 0, err
			}
			n := count(s.CodexWarmup != "", s.ClaudeWarmup != "", s.CodexWarmAt != "" || len(s.CodexWarmAts) > 0,
				s.ClaudeWarmAt != "" || len(s.ClaudeWarmAts) > 0, s.WorkBuddyCheckin, s.TraeCheckin, s.MiniMaxCheckin, s.QoderCheckin)
			n += len(s.CodexWarmAtOf)
			for _, on := range s.PluginCheckins {
				n += count(on)
			}
			return n, nil
		},
		Parts: []pluginrt.Part{{Slot: "part", Key: "warmups"}},
	})
}

func count(on ...bool) int {
	n := 0
	for _, b := range on {
		if b {
			n++
		}
	}
	return n
}
