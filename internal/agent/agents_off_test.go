package agent

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Models turned off for agents on the Gateway page (辰林鸭~ on Discord)
// are left out of what magpie writes into an agent's own files, the
// routing group made of them kept; a field that doesn't offer one says
// where it was turned off.
func TestAgentsOffLeaveTheWrittenConfig(t *testing.T) {
	home, dir, web := dshRouteHome(t)
	dshKeyFixture(dir)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1", "m2", "m3"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "pool", Name: "Pool", Members: []string{"relay/m2", "relay/m3"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetAgentsOff([]string{"relay/m2", "relay/m3"}, true); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(web, []byte("# Your patch layer for this dsh profile.\n"), 0o644)
	a := dsh(home)
	if err := a.Field("model").Set("magpie/relay/m1"); err != nil {
		t.Fatal(err)
	}
	ps := dshPiProviders(t, web)
	got := dshRouteModelIDs(ps["magpie"])
	for _, id := range []string{"relay/m2", "relay/m3"} {
		if slices.Contains(got, id) {
			t.Errorf("%s written though off for agents: %v", id, got)
		}
	}
	if !slices.Contains(got, "relay/m1") || !slices.Contains(got, "group/pool") {
		t.Errorf("written: %v", got)
	}
	if err := a.notOffered("relay/m2", nil); err == nil || !strings.Contains(err.Error(), "Gateway page") {
		t.Errorf("not offered: %v", err)
	}
}
