package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// stableFor saves Settings' Update channel for one test and puts the one
// before back after it.
func stableFor(t *testing.T, channel string) {
	t.Helper()
	before := settings.Load()
	s := before
	s.UpdateChannel = channel
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { settings.Save(before) })
}

// The Stable channel (ssharpcc on X) and Daily (fishmagic80 on X): the
// feed is asked with channel=stable or channel=daily, and only then; every version asks it as before. The
// language goes with it either way.
func TestLatestAsksTheChannel(t *testing.T) {
	var (
		mu    sync.Mutex
		asked []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.RawQuery)
		mu.Unlock()
		v := "0.1.1600"
		switch r.URL.Query().Get("channel") {
		case "stable":
			v = "0.1.1550"
		case "daily":
			v = "0.1.1590"
		}
		json.NewEncoder(w).Encode(Release{Version: v})
	}))
	defer srv.Close()
	t.Setenv("MAGPIE_UPDATE_FEED", srv.URL+"/api/latest")
	ctx := context.Background()

	for _, c := range []struct{ channel, lang, query, version string }{
		{"", "zh", "lang=zh", "0.1.1600"},
		{"", "", "", "0.1.1600"},
		{settings.UpdateStable, "zh", "channel=stable&lang=zh", "0.1.1550"},
		{settings.UpdateStable, "", "channel=stable", "0.1.1550"},
		{settings.UpdateDaily, "ja", "channel=daily&lang=ja", "0.1.1590"},
		{settings.UpdateDaily, "", "channel=daily", "0.1.1590"},
	} {
		t.Run(fmt.Sprintf("%q %q", c.channel, c.lang), func(t *testing.T) {
			stableFor(t, c.channel)
			mu.Lock()
			asked = nil
			mu.Unlock()
			rel, err := LatestIn(ctx, c.lang)
			if err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(asked) != 1 || asked[0] != c.query {
				t.Fatalf("asked %q, want %q", asked, c.query)
			}
			if rel.Version != c.version {
				t.Fatalf("got %s, want %s", rel.Version, c.version)
			}
		})
	}
}

// What's new after a Stable update is every release since the one
// installed, read as one list: the cap of 40 is every version's only.
func TestStableNotesAreAll(t *testing.T) {
	var notes []Note
	for i := 1; i <= 120; i++ {
		notes = append(notes, Note{Version: fmt.Sprintf("0.1.%d", 1000+i), Notes: fmt.Sprintf("- change %d", i)})
	}
	stableFor(t, "")
	if got := Between(notes, "0.1.1000", "0.1.1120"); len(got) != MaxNotes || got[0].Version != "0.1.1120" {
		t.Fatalf("every version: %d notes, first %s", len(got), got[0].Version)
	}
	stableFor(t, settings.UpdateStable)
	got := Between(notes, "0.1.1000", "0.1.1120")
	if len(got) != 120 || got[0].Version != "0.1.1120" || got[119].Version != "0.1.1001" {
		t.Fatalf("stable: %d notes", len(got))
	}
	if MostNotes() != StableMaxNotes {
		t.Fatalf("MostNotes on stable: %d", MostNotes())
	}
	// Daily's update is a day of releases, read as one list too
	stableFor(t, settings.UpdateDaily)
	if got := Between(notes, "0.1.1000", "0.1.1120"); len(got) != 120 || MostNotes() != StableMaxNotes {
		t.Fatalf("daily: %d notes, MostNotes %d", len(got), MostNotes())
	}
}

// Daily's release, like Stable's, is one only the site knows: a failed
// feed doesn't go on to GitHub's newest, and GitHub only doesn't skip the
// feed, either of which would hand the newest release to someone who chose
// one a day.
func TestDailyStaysOnTheFeed(t *testing.T) {
	t.Setenv("MAGPIE_UPDATE_FEED", "")
	for _, c := range []struct {
		channel string
		picked  bool
	}{{"", false}, {settings.UpdateDaily, true}, {settings.UpdateStable, true}} {
		stableFor(t, c.channel)
		if Picked() != c.picked || Channel() != c.channel {
			t.Fatalf("%q: Picked %v, Channel %q", c.channel, Picked(), Channel())
		}
		if fallsBack(context.Background()) == c.picked {
			t.Errorf("%q: fallsBack %v", c.channel, !c.picked)
		}
	}
	stableFor(t, settings.UpdateDaily)
	s := settings.Load()
	s.UpdateSource = settings.UpdateSourceGitHub
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if githubOnly() {
		t.Error("GitHub only skipped Daily's feed")
	}
}

// Settings take "stable", "daily" or nothing; anything else is refused, and the
// value is kept as saved.
func TestUpdateChannelSaved(t *testing.T) {
	stableFor(t, settings.UpdateStable)
	if got := settings.Load().UpdateChannel; got != "stable" || !Stable() {
		t.Fatalf("saved stable, read %q", got)
	}
	stableFor(t, settings.UpdateDaily)
	if got := settings.Load().UpdateChannel; got != "daily" || Stable() || Channel() != "daily" {
		t.Fatalf("saved daily, read %q", got)
	}
	stableFor(t, settings.UpdateStable)
	for _, bad := range []string{"Stable", "beta", "every", " stable", "Daily", "weekly"} {
		s := settings.Load()
		s.UpdateChannel = bad
		if err := settings.Save(s); err == nil {
			t.Errorf("%q was saved", bad)
		}
	}
	if got := settings.Load().UpdateChannel; got != "stable" {
		t.Fatalf("a refused save changed it: %q", got)
	}
}
