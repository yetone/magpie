package update

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// What changed is shown after an upgrade between releases only.
func TestShowNotes(t *testing.T) {
	for _, c := range []struct {
		name, last, current string
		existed             bool
		after               string
		show                bool
	}{
		{"upgrade", "0.1.600", "0.1.604", true, "0.1.600", true},
		{"upgrade, v kept", "v0.1.600", "0.1.604", true, "0.1.600", true},
		{"minor upgrade", "0.1.604", "0.2.0", true, "0.1.604", true},
		{"fresh install", "", "0.1.604", false, "", false},
		{"from before versions were kept", "", "0.1.604", true, "", true},
		{"same version", "0.1.604", "0.1.604", true, "", false},
		{"downgrade", "0.1.604", "0.1.600", true, "", false},
		{"source build", "0.1.600", "dev", true, "", false},
		{"source build after a release", "0.1.600", "v0.1.604-3-g0bcb2cc", true, "", false},
		{"last unreadable", "dev", "0.1.604", true, "", false},
	} {
		after, show := ShowNotes(c.last, c.current, c.existed)
		if after != c.after || show != c.show {
			t.Errorf("%s: ShowNotes(%q, %q, %v) = %q, %v; want %q, %v", c.name, c.last, c.current, c.existed, after, show, c.after, c.show)
		}
	}
}

func TestStripInstall(t *testing.T) {
	md := "## Bug Fixes\n\n- Fixed a thing. (#464)\n\n### Install\n\nDownload from [usemagpie.ai](https://usemagpie.ai).\n\n- **macOS:** `magpie.dmg`\n"
	if got, want := StripInstall(md), "## Bug Fixes\n\n- Fixed a thing. (#464)"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// a section after it at its level or above stays
	md = "## Install\n\nlinks\n\n### more links\n\n## New Features\n\n- A feature. (#1)"
	if got, want := StripInstall(md), "## New Features\n\n- A feature. (#1)"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := StripInstall("- Install the CLI faster. (#2)"); got != "- Install the CLI faster. (#2)" {
		t.Errorf("a bullet naming Install was taken: %q", got)
	}
}

func TestBetween(t *testing.T) {
	notes := []Note{
		{Version: "0.1.598", Notes: "- old"},
		{Version: "v0.1.604", Notes: "- four\n\n### Install\n\nlinks"},
		{Version: "0.1.605", Notes: "- not out here yet"},
		{Version: "0.1.601", Notes: "- one"},
		{Version: "0.1.600", Notes: "- zero"},
		{Version: "0.1.602", Notes: "### Install\n\nlinks"}, // nothing left
		{Version: "0.1.603", Notes: "- three"},
	}
	vs := func(ns []Note) (out []string) {
		for _, n := range ns {
			out = append(out, n.Version)
		}
		return out
	}
	got := Between(notes, "0.1.600", "0.1.604")
	if want := []string{"0.1.604", "0.1.603", "0.1.601"}; !reflect.DeepEqual(vs(got), want) {
		t.Fatalf("got %v, want %v", vs(got), want)
	}
	if got[0].Notes != "- four" {
		t.Errorf("Install kept: %q", got[0].Notes)
	}
	if got := Between(notes, "", "0.1.604"); !reflect.DeepEqual(vs(got), []string{"0.1.604"}) {
		t.Errorf("current alone: %v", vs(got))
	}
	if got := Between(notes, "0.1.604", "0.1.604"); len(got) != 0 {
		t.Errorf("nothing between: %v", vs(got))
	}
}

func TestNotesBetween(t *testing.T) {
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.RawQuery
		json.NewEncoder(w).Encode(map[string]any{"releases": []Note{
			{Version: "0.1.604", Notes: "- four", URL: "https://example.com/4"},
			{Version: "0.1.600", Notes: "- zero"},
			{Version: "0.1.602", Notes: "- two"},
		}})
	}))
	defer srv.Close()
	t.Setenv("MAGPIE_NOTES_FEED", srv.URL)
	got, err := NotesBetween(context.Background(), "0.1.600", "0.1.604")
	if err != nil {
		t.Fatal(err)
	}
	if asked != "after=0.1.600&upto=0.1.604" {
		t.Errorf("asked %q", asked)
	}
	if len(got) != 2 || got[0].Version != "0.1.604" || got[1].Version != "0.1.602" || got[0].URL != "https://example.com/4" {
		t.Errorf("%+v", got)
	}
	srv.Close()
	if _, err := NotesBetween(context.Background(), "0.1.600", "0.1.604"); err == nil {
		t.Error("no error offline")
	}
}
