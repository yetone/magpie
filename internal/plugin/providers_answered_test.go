package plugin

import (
	"testing"
)

// An empty providers list means "the plugins haven't been asked yet" or "they
// answered, and this plugin signs in to nothing", and only the second is what
// the Installed list may say out loud (#1112). Answered tells them apart.
func TestAnsweredSaysWhetherTheListIsThePluginsOwn(t *testing.T) {
	sandbox(t)

	// what is kept from before they were last asked: not the answer
	UseCached(nil)
	if Answered() {
		t.Error("Answered = true with nothing asked: an empty list is not the plugins' answer")
	}
	provMu.Lock()
	provGood, provTried = false, false
	provMu.Unlock()
	if Answered() {
		t.Error("Answered = true after the list was forgotten, want false until the plugins answer")
	}

	// one asked about, and it has none: the answer is in, so a page may say so
	UseCached([]Provider{})
	provMu.Lock()
	provGood, provTried = true, true
	provMu.Unlock()
	if !Answered() {
		t.Error("Answered = false with an answer in hand, want true")
	}

	// asked, then the list forgotten again (a sign-in or the plugins changed)
	forgetProviders()
	if Answered() {
		t.Error("Answered = true once the list was invalidated, want false until the plugins answer again")
	}
}
