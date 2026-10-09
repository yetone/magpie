package provider

import "net/url"

// SkipsRedaction says requests to p go unmasked: the user set Unredacted,
// and every address p has is on this machine or the local network. One
// edited to a vendor's afterwards is masked again. A signed-in account, a
// plugin's provider and another magpie are always masked: each reaches a
// vendor.
func (p Provider) SkipsRedaction() bool {
	if !p.Unredacted || p.Account != nil || p.IsRemoteMagpie() {
		return false
	}
	return LocalAddresses(p.Chat, p.Responses, p.Anthropic, p.Gemini, p.Decide)
}

// LocalAddresses says every non-empty base URL among bases is on this
// machine or the local network (localhost, *.local, a loopback or private
// IP), and there is at least one.
func LocalAddresses(bases ...string) bool {
	n := 0
	for _, b := range bases {
		if b == "" {
			continue
		}
		u, err := url.Parse(b)
		if err != nil || u.Host == "" || !local(u.Hostname()) {
			return false
		}
		n++
	}
	return n > 0
}
