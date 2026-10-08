package settings

import "testing"

func TestGatewayConversationConsentIsMachineLocal(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if Load().GatewayConversations {
		t.Fatal("recording on by default")
	}
	from := Settings{GatewayConversations: true}
	from.KeepOwn(Settings{})
	if from.GatewayConversations {
		t.Fatal("sync enabled recording")
	}
	from.KeepOwn(Settings{GatewayConversations: true})
	if !from.GatewayConversations {
		t.Fatal("sync removed local consent")
	}
}

func TestGatewayConversationGenericSaveCannotEnable(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Settings{GatewayConversations: true}); err != nil {
		t.Fatal(err)
	}
	if Load().GatewayConversations {
		t.Fatal("generic settings save enabled recording")
	}
}

func TestGatewayConversationConsentSurvivesStaleSettings(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := SetGatewayConversations(true); err != nil {
		t.Fatal(err)
	}
	staleOn := Load()
	if !staleOn.GatewayConversations {
		t.Fatal("explicit consent was not saved")
	}
	if err := SetGatewayConversations(false); err != nil {
		t.Fatal(err)
	}
	staleOn.Theme = "dark"
	if err := Save(staleOn); err != nil {
		t.Fatal(err)
	}
	if got := Load(); got.GatewayConversations || got.Theme != "dark" {
		t.Fatal("stale settings save enabled recording or lost its theme change")
	}
	staleOff := Load()
	if err := SetGatewayConversations(true); err != nil {
		t.Fatal(err)
	}
	staleOff.Theme = "light"
	if err := Save(staleOff); err != nil {
		t.Fatal(err)
	}
	if got := Load(); !got.GatewayConversations || got.Theme != "light" {
		t.Fatal("stale settings save removed consent or lost its theme change")
	}
	if err := SetGatewayConversations(false); err != nil {
		t.Fatal(err)
	}
	if got := Load(); got.GatewayConversations || got.Theme != "light" {
		t.Fatal("explicit consent change overwrote other settings")
	}
}
