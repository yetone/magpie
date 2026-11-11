// Package redact is Settings → Privacy's masking: secrets, personal data
// and the user's own words go to a vendor as placeholders, and come back
// as they were. While the settings ask for any of it, it stays on.
//
// It is the masker of the gateway's middleware point (P3 of
// docs/subsystems/plugin-runtime.md): Apply serves masking.Masker, and the point
// fails closed: a request the settings say to mask is not sent while no
// masker serves it. The logs' and archives' scrubbing (redact.Scrub) is
// the core's, whatever the settings say.
package redact

import (
	"slices"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/gwmw"
	"github.com/yetone/magpie/internal/masking"
	"github.com/yetone/magpie/internal/pluginrt"
	"github.com/yetone/magpie/internal/settings"
)

func init() {
	pluginrt.Register(pluginrt.Manifest{
		ID: "redact", API: "^1", Requires: []string{"settings", "gateway"}, Essential: true,
		Name: "Privacy masking", Sub: "Secrets, personal data and your own words go to vendors as placeholders, and come back as they were",
		One: "Masking is on", Many: "Masking is on",
		InUse: InUse,
		SetAt: "privacy #redactList",
		// Insights › Masking: what masking did, request by request
		Parts:  []pluginrt.Part{{Slot: "nav.page", Key: "masking"}},
		Client: []string{"i18n.js", "redact.css", "client.js", "page.js"},
		Apply: func(c *pluginrt.Ctx) error {
			c.OnDispose(gwmw.Mask(masking.Masker{}))
			return nil
		},
	})
}

// InUse is 1 while the settings ask for anything to be masked; settings
// that can't be read are not known, so it stays on.
func InUse() (int, error) {
	st, err := settings.LoadChecked()
	if err != nil {
		return 0, err
	}
	if masking.Wanted(st) {
		return 1, nil
	}
	// a key held to masking (access.RedactOn) uses it with the setting off
	keys, err := access.List()
	if err != nil {
		return 0, err
	}
	if slices.ContainsFunc(keys, func(k access.Key) bool { return k.Redact == access.RedactOn && !k.Off }) {
		return 1, nil
	}
	return 0, nil
}
