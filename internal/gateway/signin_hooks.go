package gateway

// The gateway's request hooks told of an account that has to be signed in
// again (shannnnnks on X: of several Codex accounts, one is signed out now
// and then, to be pushed by the notify plugin). The alert is the one the
// app notifies (provider.WatchSignIns): every subscription the Providers
// page says to sign in again (Claude, Copilot, ChatGPT's revoked or
// refused sign-ins, a plugin's), and a Claude sign-in about to end, each
// said once until the account is usable again. The hooks' marks are their
// own (provider.SignInHooksPath), so the app's notification and the hooks
// are each told.

import (
	"context"
	"log"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
)

// signInEvent is what a hook's onSignIn is handed for a: the provider,
// the account's label and its state, never a token or the vendor's words.
func signInEvent(a provider.SignInAlert) plugin.SignInEvent {
	state := "signed-out"
	if a.Kind == "ending" {
		state = "ending"
	}
	return plugin.SignInEvent{Provider: a.Provider, ProviderName: a.Name, Account: a.User, State: state, EndsAt: a.EndsAt}
}

// tellHooksOfSignIns is one look: each alert due handed to the hooks. With
// no hook installed nothing is looked at, nor marked told, so a hook
// installed later hears of an account signed out before.
func tellHooksOfSignIns(ctx context.Context) {
	if !plugin.HasGateway() {
		return
	}
	for _, a := range provider.SignInAlertsAt(provider.SignInHooksPath()) {
		log.Printf("sign-in hook: %s %s %s", a.Kind, a.Name, a.User)
		signInToHooks(ctx, signInEvent(a))
	}
}

// keepHooksToldOfSignIns looks a minute after the gateway starts and every
// two minutes after, as the app's alert does: a mark is set as a request is
// refused, so a look reads magpie's own files and asks no vendor.
func keepHooksToldOfSignIns(ctx context.Context) {
	t := time.NewTimer(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		tellHooksOfSignIns(ctx)
		t.Reset(2 * time.Minute)
	}
}

// signInToHooks hands an event to the hooks; a test counts the calls.
var signInToHooks = plugin.GatewaySignIn
