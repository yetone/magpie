package omarchy

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The JSON below is Umbriel's own, as Umbriel 0.1.0 (noctalia-dev/umbriel
// at 0760394, src/server/ipc_commands.cpp: windowEntry, IpcCommands::outputs;
// src/server/ipc.cpp: subscribe) writes it with nlohmann::json, whose
// objects list their keys in order. Replies are {"ok": …} or {"err": "…"};
// a subscription answers with {"event": …, "data": …} lines, the first the
// state as it is.

// umbrielWindowJSON is a `windows` entry: MareDevi's panel on #7881,
// "[float 484x572+1038+451]" on HDMI-A-1.
func umbrielWindowJSON(id string, pid int, title string, floating bool, output string) string {
	return `{"active":true,"app_id":"magpie","border_effect":{"name":"","overlay":false,"pool":"","source":"default","suppressed":false},` +
		`"column":-1,"content_type":"none","floating":` + map[bool]string{true: "true", false: "false"}[floating] +
		`,"focused":true,"fullscreen":false,"h":572,"id":` + quote(id) + `,"maximized":false,"maximized_to_edges":false,` +
		`"minimizable":true,"minimized":false,"output":` + quote(output) + `,"parent":"","pid":` + itoa(pid) +
		`,"pinned":false,"region":null,"row":-1,"scratchpad":"","tab_hidden":false,"tab_index":-1,"tabbed":false,` +
		`"title":` + quote(title) + `,"title_bar":false,"urgent":false,"visible":true,"w":484,` +
		`"window_effect":{"name":"","pool":"","source":"default","suppressed":false},"workspace":"ws-1",` +
		`"x":1038,"xdg_tag":"","xwayland":false,"y":451}`
}

// umbrielOutputsReply is MareDevi's two outputs: HDMI-A-1 2560x1440 at
// scale 1 with Noctalia's bar taking 34 at the top (usable 2560x1406+0+34),
// and eDP-1 2560x1600 at 1.25 (2048x1280 logical) to its right.
const umbrielOutputsReply = `{"ok":[` +
	`{"active_workspace":"ws-1","adaptive_sync":null,"config_name":"Dell Inc./DELL U2723QE/ABC","description":"Dell Inc. DELL U2723QE","direct_scanout":null,` +
	`"enabled":true,"focused":true,"hdr_active":false,"logical_size":{"height":1440,"width":2560},"make":"Dell Inc.","model":"DELL U2723QE",` +
	`"modes":[{"current":true,"height":1440,"preferred":true,"refresh_mhz":59951,"width":2560}],"name":"HDMI-A-1",` +
	`"physical_size":{"height_mm":340,"width_mm":600},"position":{"x":0,"y":0},"powered":true,"scale":1.0,"serial":"ABC",` +
	`"transform":"normal","usable_area":{"height":1406,"width":2560,"x":0,"y":34}},` +
	`{"active_workspace":"ws-2","adaptive_sync":null,"config_name":null,"description":"BOE 0x0BCA","direct_scanout":null,` +
	`"enabled":true,"focused":false,"hdr_active":false,"logical_size":{"height":1280,"width":2048},"make":"BOE","model":"0x0BCA",` +
	`"modes":[{"current":true,"height":1600,"preferred":true,"refresh_mhz":60000,"width":2560}],"name":"eDP-1",` +
	`"physical_size":{"height_mm":190,"width_mm":300},"position":{"x":2560,"y":0},"powered":true,"scale":1.25,"serial":"",` +
	`"transform":"normal","usable_area":{"height":1246,"width":2048,"x":2560,"y":34}}]}`

func umbrielOutputs(t *testing.T) []umbrielOutput {
	t.Helper()
	var r struct{ Ok []umbrielOutput }
	if err := json.Unmarshal([]byte(umbrielOutputsReply), &r); err != nil {
		t.Fatal(err)
	}
	return r.Ok
}

func TestUmbrielSpot(t *testing.T) {
	outs := umbrielOutputs(t)
	hdmi, edp := outs[0], outs[1]
	// a bar down the left side, 40 wide
	left := hdmi
	left.Usable.X, left.Usable.Width = 40, 2520
	for _, c := range []struct {
		name  string
		click int
		known bool
		out   umbrielOutput
		x     int
	}{
		// #7881's Activate(2500, 20): the icon at the right end of the bar
		{"icon near the right edge", 2500, true, hdmi, 2560 - 484 - 6},
		{"under the icon", 1500, true, hdmi, 1500 - 242},
		{"icon near the left edge", 30, true, hdmi, 6},
		{"not clicked: top right", 0, false, hdmi, 2560 - 484 - 6},
		{"a click off the output: top right", 3000, true, hdmi, 2560 - 484 - 6},
		// counted from the usable area's corner, which is the output's
		{"on the second output", 1000, true, edp, 1000 - 242},
		{"second output, not clicked", 0, false, edp, 2048 - 484 - 6},
		// the usable area starts after a side bar: kept off it
		{"beside a left bar, under the icon", 1500, true, left, 1500 - 242 - 40},
		{"beside a left bar, icon at its edge", 30, true, left, 6},
	} {
		x, y := umbrielSpot(c.click, c.known, 484, c.out)
		if x != c.x || y != PanelGap {
			t.Errorf("%s: %d,%d; want %d,%d", c.name, x, y, c.x, PanelGap)
		}
	}
}

func TestUmbrielOutputOf(t *testing.T) {
	outs := umbrielOutputs(t)
	for _, c := range []struct{ name, output, want string }{
		{"the window's own", "eDP-1", "eDP-1"},
		{"none named: the focused", "", "HDMI-A-1"},
		{"one gone: the focused", "DP-3", "HDMI-A-1"},
	} {
		if o, ok := umbrielOutputOf(outs, c.output); !ok || o.Name != c.want {
			t.Errorf("%s: %q %v, want %q", c.name, o.Name, ok, c.want)
		}
	}
	off := append([]umbrielOutput(nil), outs...)
	off[1].LogicalSize.Width = 0 // a disabled output reports a zero size
	off[0].Focused = false
	if o, ok := umbrielOutputOf(off, "eDP-1"); ok {
		t.Errorf("a disabled output and none focused: %q, want none", o.Name)
	}
}

// The panel is placed once as it opens, whether it opened before the
// subscription or after, and not again while it stays open; hidden (gone
// from the list) and shown again it is placed again. Other windows,
// another process's and the main window are left alone.
func TestWatchUmbriel(t *testing.T) {
	const pid = 4242
	list := func(ws ...string) string { return `{"data":[` + strings.Join(ws, ",") + `],"event":"windows"}` }
	main := umbrielWindowJSON("a1", pid, "magpie", false, "HDMI-A-1")
	panel := umbrielWindowJSON("b2", pid, "magpie panel", true, "HDMI-A-1")
	stream := strings.Join([]string{
		list(main, panel, umbrielWindowJSON("c3", 99, "magpie panel", true, "HDMI-A-1")),
		list(main, panel),
		`{"data":{"background":"#141419FF"},"event":"theme"}`,
		list(main),
		list(main, panel),
		list(main, umbrielWindowJSON("d4", pid, "magpie", true, "eDP-1")),
		list(main, umbrielWindowJSON("d4", pid, "magpie panel", true, "eDP-1")),
		list(main, umbrielWindowJSON("d4", pid, "magpie panel", true, "eDP-1")),
		list(main, umbrielWindowJSON("e5", -1, "magpie panel", true, "eDP-1")),
		``,
	}, "\n")
	var sent strings.Builder
	c := struct {
		io.Reader
		io.Writer
	}{strings.NewReader(stream), &sent}
	var placed []string
	if err := watchUmbriel(c, "magpie panel", pid, func(w umbrielWindow) { placed = append(placed, w.ID+"@"+w.Output) }); err != io.EOF {
		t.Fatalf("watchUmbriel: %v, want EOF at the stream's end", err)
	}
	if sent.String() != `{"cmd":"subscribe","events":["windows"]}`+"\n" {
		t.Fatalf("asked %q, want a windows subscription", sent.String())
	}
	if want := "b2@HDMI-A-1 b2@HDMI-A-1 d4@eDP-1 e5@eDP-1"; strings.Join(placed, " ") != want {
		t.Fatalf("placed %v, want %s", placed, want)
	}
}

func TestWatchUmbrielRefused(t *testing.T) {
	c := struct {
		io.Reader
		io.Writer
	}{strings.NewReader(`{"err":"unknown subscription event: windows"}` + "\n"), io.Discard}
	if err := watchUmbriel(c, "magpie panel", 1, func(umbrielWindow) { t.Fatal("placed") }); err == nil || !strings.Contains(err.Error(), "unknown subscription") {
		t.Fatalf("watchUmbriel: %v, want Umbriel's error", err)
	}
}

// fakeUmbriel answers on UMBRIEL_SOCKET as Umbriel does, one request a
// line, and keeps the requests.
func fakeUmbriel(t *testing.T, answer func(req string) string) (sent func() []string) {
	dir, err := os.MkdirTemp("", "umbriel") // a socket path fits in 104 bytes
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "umbriel-wayland-1.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var got []string
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			sc := bufio.NewScanner(c)
			for sc.Scan() {
				mu.Lock()
				got = append(got, sc.Text())
				mu.Unlock()
				io.WriteString(c, answer(sc.Text())+"\n")
			}
			c.Close()
		}
	}()
	t.Cleanup(func() {
		l.Close()
		wg.Wait()
		os.RemoveAll(dir)
	})
	t.Setenv("UMBRIEL_SOCKET", sock)
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

func TestPlaceUmbrielPanel(t *testing.T) {
	answer := func(req string) string {
		if req == `{"cmd":"outputs"}` {
			return umbrielOutputsReply
		}
		return `{"ok":null}`
	}
	x := 2500
	for _, c := range []struct {
		name     string
		click    *int
		floating bool
		output   string
		want     []string
	}{
		// #7881: Activate(2500, 20) on the bar
		{"clicked, already floating", &x, true, "HDMI-A-1", []string{
			`{"cmd":"outputs"}`,
			`{"action":"window-move-to","anchor":"top_left","cmd":"action","window":"b2","x":2070,"y":6}`,
		}},
		{"opened by magpie panel, tiled, on the second output", nil, false, "eDP-1", []string{
			`{"cmd":"outputs"}`,
			`{"action":"window-toggle-floating","cmd":"action","window":"b2"}`,
			`{"action":"window-move-to","anchor":"top_left","cmd":"action","window":"b2","x":1558,"y":6}`,
		}},
	} {
		got := fakeUmbriel(t, answer)
		var w umbrielWindow
		if err := json.Unmarshal([]byte(umbrielWindowJSON("b2", 1, "magpie panel", c.floating, c.output)), &w); err != nil {
			t.Fatal(err)
		}
		var p UmbrielPanel
		p.Opening(c.click)
		p.place(w)
		if sent := strings.Join(got(), "\n"); sent != strings.Join(c.want, "\n") {
			t.Errorf("%s: sent\n%s\nwant\n%s", c.name, sent, strings.Join(c.want, "\n"))
		}
		// the click is used once: the next opening, not clicked, goes top right
		if _, known := p.take(); known {
			t.Errorf("%s: the click was kept for the next opening", c.name)
		}
	}
}

// An Umbriel that won't list its outputs gets no move at all.
func TestPlaceUmbrielPanelNoOutputs(t *testing.T) {
	got := fakeUmbriel(t, func(req string) string {
		if req == `{"cmd":"outputs"}` {
			return `{"err":"no outputs"}`
		}
		return `{"ok":null}`
	})
	var p UmbrielPanel
	p.place(umbrielWindow{ID: "b2", Output: "HDMI-A-1", W: 484})
	if sent := got(); len(sent) != 1 {
		t.Fatalf("sent %q, want only the question", sent)
	}
}
