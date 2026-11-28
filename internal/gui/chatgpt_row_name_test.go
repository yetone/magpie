package gui

import "testing"

// tmr11235 on X: the ChatGPT subscription, added from the Add sheet's
// ChatGPT tile, was listed on the Providers page as "Codex". The page
// lists it as ChatGPT now, by the id codex it always had, and names the
// agent signed in to it Codex still (its "{agent} is signed in" lines).
func TestProvidersListTheChatGPTPlanAsChatGPT(t *testing.T) {
	codexSignedInSandbox(t)
	for _, p := range providersState().Providers {
		if p.ID != "codex" {
			continue
		}
		if p.Name != "ChatGPT" {
			t.Fatalf("the ChatGPT plan's row is named %q", p.Name)
		}
		if p.Account == nil || p.Account.Agent != "codex" || p.Account.Name != "Codex" {
			t.Fatalf("the agent signed in to it: %+v", p.Account)
		}
		return
	}
	t.Fatal("the ChatGPT plan isn't listed")
}
