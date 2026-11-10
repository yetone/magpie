package provider

import "testing"

// A magpie's list tells another the provider serving each model
// (magpie_by): its own provider, the one at the start of a chain of
// magpies for a Remote magpie's, "routing group" for a group (lcc580 on
// X). A Remote magpie whose list said none (one from before) is named
// as ever, upstream names on or off.
func TestServedByAndUpstreamLabel(t *testing.T) {
	relay := Provider{ID: "relaya", Name: "Relay A"}
	remote := Provider{ID: "office", Name: "Office", Preset: RemoteMagpiePreset, UpstreamProviders: true}
	for _, c := range []struct {
		e         Entry
		by, label string
	}{
		{Entry{Name: "DeepSeek Chat", Provider: relay}, "Relay A", "DeepSeek Chat · Relay A"},
		{Entry{Name: "Fast", Group: "fast", Provider: relay}, "routing group", "Fast · routing group"},
		{Entry{Name: "DeepSeek Chat · Relay A", Plain: "DeepSeek Chat", By: "Relay A", Provider: remote}, "Relay A", "DeepSeek Chat · Office › Relay A"},
		{Entry{Name: "Mine", Default: "DeepSeek Chat", By: "Relay A", Provider: remote}, "Relay A", "Mine · Office › Relay A"},
		{Entry{Name: "DeepSeek Chat · Relay A", Plain: "DeepSeek Chat", Provider: remote}, "Office", "DeepSeek Chat · Relay A · Office"},
		// upstream names on a provider that isn't a Remote magpie do nothing
		{Entry{Name: "DeepSeek Chat", By: "Relay A", Provider: Provider{ID: "x", Name: "X", UpstreamProviders: true}}, "X", "DeepSeek Chat · X"},
	} {
		if got := c.e.ServedBy(); got != c.by {
			t.Errorf("%+v: served by %q, want %q", c.e, got, c.by)
		}
		if got := c.e.Label(); got != c.label {
			t.Errorf("%+v: label %q, want %q", c.e, got, c.label)
		}
	}
}
