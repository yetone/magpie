package update

import (
	"context"
	"encoding/json"
	"fmt"
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
	// the Chinese notes' section is 安装 (#649), Japanese ones' インストール
	for _, h := range []string{"安装", "インストール"} {
		md = "## Bug Fixes\n\n- 修复了一件事。\n\n### " + h + "\n\n从 [usemagpie.ai](https://usemagpie.ai) 下载。"
		if got, want := StripInstall(md), "## Bug Fixes\n\n- 修复了一件事。"; got != want {
			t.Errorf("%s: got %q, want %q", h, got, want)
		}
	}
	if got := StripInstall("- 安装更快了。"); got != "- 安装更快了。" {
		t.Errorf("a bullet naming 安装 was taken: %q", got)
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
	got, err := NotesBetween(context.Background(), "0.1.600", "0.1.604", "")
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
	if _, err := NotesBetween(context.Background(), "0.1.600", "0.1.604", ""); err == nil {
		t.Error("no error offline")
	}
}

// sujingshen on X: v0.1.2606 to v0.1.2762 showed the newest MaxNotes and
// read "40 releases". The total counts every release in the range, so the
// list cut short can say how many there are.
func TestNotesBetweenTotalPastTheCap(t *testing.T) {
	var rel []Note
	for i := 1; i <= MaxNotes+16; i++ {
		rel = append(rel, Note{Version: fmt.Sprintf("0.1.%d", 600+i), Notes: "- x"})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"releases": rel})
	}))
	defer srv.Close()
	t.Setenv("MAGPIE_NOTES_FEED", srv.URL)
	upto := fmt.Sprintf("0.1.%d", 600+MaxNotes+16)
	got, total, err := NotesBetweenTotal(context.Background(), "0.1.600", upto, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != MaxNotes || got[0].Version != upto || total != MaxNotes+16 {
		t.Fatalf("kept %d of %d, newest %s", len(got), total, got[0].Version)
	}
	got, total, err = NotesBetweenTotal(context.Background(), "0.1.650", upto, "")
	if err != nil || len(got) != 6 || total != 6 {
		t.Fatalf("under the cap: kept %d of %d (%v)", len(got), total, err)
	}
}

// Settings' What's new can show this version and the 10, 20, 50 or 100
// before it (Linka on Discord): the site is asked for that many alone, and
// a site that sends more (from before it took a limit) is cut to them.
func TestNotesBack(t *testing.T) {
	versions := func(ns []Note) []string {
		out := []string{}
		for _, n := range ns {
			out = append(out, n.Version)
		}
		return out
	}
	var asked []string
	var list []Note
	for v := 660; v >= 590; v-- {
		if v == 607 {
			continue // never released
		}
		list = append(list, Note{Version: fmt.Sprintf("0.1.%d", v), Notes: fmt.Sprintf("- %d\n\n## Install\n\nDownload it.", v)})
	}
	list[660-617].Notes = "## Install\n\nonly links" // 0.1.617: nothing left once Install is out
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.RawQuery)
		// a site from before limit: every release, whatever was asked
		json.NewEncoder(w).Encode(map[string]any{"releases": list})
	}))
	defer srv.Close()
	t.Setenv("MAGPIE_NOTES_FEED", srv.URL)
	got, err := NotesBack(context.Background(), "0.1.612", 10, "zh")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"lang=zh&limit=11&upto=0.1.612"}; !reflect.DeepEqual(asked, want) {
		t.Errorf("asked %q, want %q", asked, want)
	}
	// 0.1.612 and the ten before it, 0.1.607 never released
	want := []string{"0.1.612", "0.1.611", "0.1.610", "0.1.609", "0.1.608", "0.1.606", "0.1.605", "0.1.604", "0.1.603", "0.1.602", "0.1.601"}
	if !reflect.DeepEqual(versions(got), want) {
		t.Errorf("got %v, want %v", versions(got), want)
	}
	if got[0].Notes != "- 612" {
		t.Errorf("Install kept: %q", got[0].Notes)
	}
	// newer than upto isn't counted; an empty one counts but isn't shown
	if got, want := versions(Back(list, "0.1.619", 3)), []string{"0.1.619", "0.1.618", "0.1.616"}; !reflect.DeepEqual(got, want) {
		t.Errorf("back 3 from 0.1.619: %v, want %v", got, want)
	}
	// fewer than asked: what there is
	if got, want := versions(Back(list, "0.1.592", 10)), []string{"0.1.592", "0.1.591", "0.1.590"}; !reflect.DeepEqual(got, want) {
		t.Errorf("short list: %v, want %v", got, want)
	}
	// more than MaxNotes: the 100 asked for are all kept
	if got := Back(list, "0.1.660", 100); len(got) != len(list)-1 {
		t.Errorf("back 100: %d releases, want %d", len(got), len(list)-1)
	}
	srv.Close()
	if _, err := NotesBack(context.Background(), "0.1.612", 10, ""); err == nil {
		t.Error("no error offline")
	}
}
