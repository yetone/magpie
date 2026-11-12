package provider

import "testing"

// Before the plugin host's list is read, a sync still knows which
// plugins' sign-ins rotate: every plugin whose hook says rotating
// (plugins #1027) or whose source says a refresh token is spent once.
func TestRotatingKnownBeforeHostList(t *testing.T) {
	got := rotatingPlugins()
	for _, id := range []string{
		"cline", "codearts", "factory", "minimax-code", "minimax-code-global", "nous-portal",
		"officeace", "qoder", "qoder-cn", "sensenova-token-plan", "workbuddy", "workbuddy-ai",
	} {
		if !got[id] {
			t.Errorf("%s is carried before the plugin host list is read", id)
		}
	}
}
