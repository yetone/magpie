package provider

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

func countsLine(cs []PartnerCount) string {
	var out []string
	for _, c := range cs {
		out = append(out, fmt.Sprintf("%s %s %d", c.ID, c.What, c.Count))
	}
	return strings.Join(out, ",")
}

// A partner listed now is counted by the day, each kind up to its cap; a
// provider of the user's own, or one no longer listed, never is. The days
// ended are what is sent, and once sent they are gone.
func TestCountPartner(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("DO_NOT_TRACK", "")
	t.Setenv("MAGPIE_NO_STATS", "")
	resetPartners()
	t.Cleanup(resetPartners)
	var body atomic.Value
	body.Store(`{"partners":[{"id":"acme","name":"Acme","chat":"https://a.example/v1"}]}`)
	t.Setenv("MAGPIE_PARTNERS", partnerFeedServer(t, &body).URL)
	fetchPartnersNow()

	for range 25 {
		CountPartner(PartnerShown, "acme", "acme", "deepseek", "mine")
	}
	CountPartner(PartnerOpened, "acme")
	CountPartner("clicked", "acme")
	if _, err := Add(Provider{Name: "Acme", Preset: "acme", Chat: "https://a.example/v1", Key: "sk-acme"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Add(Provider{Name: "Mine", Chat: "https://mine.example/v1", Key: "sk-mine"}); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	if got := PartnerCounts(now); len(got) != 0 {
		t.Fatalf("today's counts are sent before the day ends: %v", got)
	}
	tomorrow := now.Add(24 * time.Hour)
	if got, want := countsLine(PartnerCounts(tomorrow)), "acme added 1,acme opened 1,acme shown 20"; got != want {
		t.Fatalf("counts = %q, want %q", got, want)
	}
	if d := PartnerCounts(tomorrow)[0].Day; d != now.UTC().Format("2006-01-02") {
		t.Fatalf("day %q", d)
	}
	PartnerCountsSent(tomorrow)
	if got := PartnerCounts(tomorrow); len(got) != 0 {
		t.Fatalf("sent days kept: %v", got)
	}

	// what magpie is used with turned off, or all stats: nothing counted
	settings.Save(settings.Settings{NoUsageStats: true})
	CountPartner(PartnerShown, "acme")
	settings.Save(settings.Settings{NoStats: true})
	CountPartner(PartnerShown, "acme")
	settings.Save(settings.Settings{})
	t.Setenv("DO_NOT_TRACK", "1")
	CountPartner(PartnerShown, "acme")
	t.Setenv("DO_NOT_TRACK", "")
	// a partner that left the list
	body.Store(`{"partners":[]}`)
	fetchPartnersNow()
	CountPartner(PartnerShown, "acme")
	if got := PartnerCounts(tomorrow); len(got) != 0 {
		t.Fatalf("counted while off or unlisted: %v", got)
	}
}

// The Plugins page's openings are counted by the day up to their cap,
// sent the day after and then gone; a page not counted, or the switches
// off, add nothing. A partner's count says where it is listed.
func TestCountPage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("DO_NOT_TRACK", "")
	t.Setenv("MAGPIE_NO_STATS", "")
	resetPartners()
	t.Cleanup(resetPartners)
	var body atomic.Value
	body.Store(`{"partners":[{"id":"acme","name":"Acme","chat":"https://a.example/v1"},{"id":"lens","name":"Lens","plugin":"@lens/opencode-lens"}]}`)
	t.Setenv("MAGPIE_PARTNERS", partnerFeedServer(t, &body).URL)
	fetchPartnersNow()

	for range 25 {
		CountPage(PagePlugins)
	}
	CountPage("settings")
	t.Setenv("DO_NOT_TRACK", "1")
	CountPage(PagePlugins)
	t.Setenv("DO_NOT_TRACK", "")
	CountPartner(PartnerShown, "acme", "lens")

	now := time.Now()
	if got := PageCounts(now); len(got) != 0 {
		t.Fatalf("today's page counts are sent before the day ends: %v", got)
	}
	tomorrow := now.Add(24 * time.Hour)
	got := PageCounts(tomorrow)
	if len(got) != 1 || got[0].Page != PagePlugins || got[0].Count != 20 || got[0].Day != now.UTC().Format("2006-01-02") {
		t.Fatalf("page counts = %+v, want plugins 20 today", got)
	}
	slots := map[string]string{}
	for _, c := range PartnerCounts(tomorrow) {
		slots[c.ID] = c.Slot
	}
	if slots["acme"] != "providers" || slots["lens"] != "plugins" {
		t.Fatalf("slots = %v, want acme providers, lens plugins", slots)
	}
	PartnerCountsSent(tomorrow)
	if got := PageCounts(tomorrow.Add(24 * time.Hour)); len(got) != 0 {
		t.Fatalf("page counts sent again: %v", got)
	}
}
