package gui

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A sign-in asked to keep to its link opens nothing here (Wing8Aqua on X),
// whatever Settings say; the window then shows the link.
func TestOpenSignInLinkOnlyAsked(t *testing.T) {
	got := openSignIn(nil, provider.SignInState{URL: "https://x.example/auth"}, true)
	if !got.LinkOnly {
		t.Fatalf("a sign-in asked for its link only was not kept to it: %+v", got)
	}
}
