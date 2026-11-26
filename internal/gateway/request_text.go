package gateway

// Show request content (llxth1 on X): the Routing page showed what the
// last requests said — the context window card's text and reply, and the
// Request and response of the last 40 calls, which the Gateway page's
// Recent calls show too. With Settings › Privacy's Show request content
// off (settings' HideRequestText) magpie keeps none of it in memory: the
// trace keeps no request's body for its text (keepBody), the Recent-calls
// ring keeps a call's metadata and headers but not its bodies (record),
// and what was kept before the switch went off goes at the next request or
// read. The request archive is its own switch and does as it did.

import "github.com/yetone/magpie/internal/settings"

// HidesRequestText reports whether Show request content is off. A
// settings file that can't be read is unknown, and hides it: a privacy
// switch the user turned off isn't taken for on.
func HidesRequestText() bool {
	s, err := settings.SharedChecked()
	return err != nil || s.HideRequestText
}

// dropBodies lets go of every request body the trace keeps for its text.
func (t *trace) dropBodies() {
	t.mu.Lock()
	defer t.mu.Unlock()
	clear(t.bodies)
	t.bodies = nil
}

// withoutText is c without what its request and reply said.
func withoutText(c Call) Call {
	c.RequestBody, c.ResponseBody = "", ""
	c.RequestTruncated, c.ResponseTruncated = false, false
	c.maskIn = nil
	return c
}

// forgetRingText takes the bodies out of every call in the Recent-calls
// ring; s.mu is held.
func (s *Server) forgetRingText() {
	for i := range s.recent {
		s.recent[i] = withoutText(s.recent[i])
	}
}

// forgetRequestText lets go of every request's content magpie keeps in
// memory: the trace's bodies and the ring's.
func (s *Server) forgetRequestText() {
	s.trace.dropBodies()
	s.mu.Lock()
	s.forgetRingText()
	s.mu.Unlock()
}
