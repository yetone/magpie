package omarchy

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"runtime"
	"time"
)

// Umbriel is whether magpie runs under Umbriel (github.com/noctalia-dev/
// umbriel), which sets UMBRIEL_SOCKET for what it starts. As under
// Hyprland and niri, a Wayland window can't place itself, so the tray
// panel opened where Umbriel floats a new window: the middle of the usable
// area (#7881).
func Umbriel() bool {
	return runtime.GOOS == "linux" && os.Getenv("UMBRIEL_SOCKET") != ""
}

// UmbrielPanel puts the panel under the bar, beside the icon clicked, each
// time it opens, as NiriPanel does under niri. Umbriel has no rule a
// program can add while it runs (window rules are its config file's) and
// doesn't tell where the pointer is, so the panel is found by its title
// among the windows Umbriel announces and moved with its window-move-to
// action. It still opens in the middle and slides up. A click on another
// window takes the focus from it, and Wails' HideOnFocusLost closes it
// then.
type UmbrielPanel struct{ panelClick }

// umbrielWindow is what magpie reads of a `windows` entry
// (src/server/ipc_commands.cpp, windowEntry).
type umbrielWindow struct {
	ID       string `json:"id"` // its ext-foreign-toplevel identifier
	Title    string `json:"title"`
	PID      int    `json:"pid"` // -1 when unknown
	Floating bool   `json:"floating"`
	Output   string `json:"output"`
	W        int    `json:"w"`
}

// umbrielOutput is what magpie reads of an `outputs` entry, in layout
// coordinates.
type umbrielOutput struct {
	Name     string `json:"name"`
	Focused  bool   `json:"focused"`
	Position struct {
		X int `json:"x"`
	} `json:"position"`
	LogicalSize struct {
		Width int `json:"width"`
	} `json:"logical_size"`
	Usable struct {
		X     int `json:"x"`
		Width int `json:"width"`
	} `json:"usable_area"`
}

// Watch places each window of this process titled title as it opens, for
// as long as magpie runs, reconnecting when Umbriel restarts.
func (p *UmbrielPanel) Watch(title string) {
	for {
		if c, err := umbrielDial(); err == nil {
			_ = watchUmbriel(c, title, os.Getpid(), p.place)
			c.Close()
		}
		time.Sleep(2 * time.Second)
	}
}

// watchUmbriel subscribes to Umbriel's windows on c and calls place once
// for each window of pid titled title: in the first snapshot (the panel
// may have opened before it) and in each later one it is new to. Each
// event is the whole list of mapped windows, so one that has gone (a hidden
// panel is unmapped) is forgotten, and one dragged stays where it went.
func watchUmbriel(c io.ReadWriter, title string, pid int, place func(umbrielWindow)) error {
	if _, err := io.WriteString(c, `{"cmd":"subscribe","events":["windows"]}`+"\n"); err != nil {
		return err
	}
	sc := bufio.NewScanner(c)
	sc.Buffer(nil, 16<<20)
	placed := map[string]bool{}
	for sc.Scan() {
		var ev struct {
			Event string          `json:"event"`
			Data  []umbrielWindow `json:"data"`
			Err   *string         `json:"err"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		if ev.Err != nil {
			return errors.New("umbriel: " + *ev.Err)
		}
		if ev.Event != "windows" {
			continue
		}
		open := map[string]bool{}
		for _, w := range ev.Data {
			open[w.ID] = true
		}
		for id := range placed {
			if !open[id] {
				delete(placed, id)
			}
		}
		for _, w := range ev.Data {
			if w.ID != "" && w.Title == title && (w.PID <= 0 || w.PID == pid) && !placed[w.ID] {
				placed[w.ID] = true
				place(w)
			}
		}
	}
	return cmpErr(sc.Err(), io.EOF)
}

// place moves the panel w, just opened, under the bar of the output it
// opened on.
func (p *UmbrielPanel) place(w umbrielWindow) {
	click, known := p.take()
	var outs []umbrielOutput
	if umbrielRequest(map[string]any{"cmd": "outputs"}, &outs) != nil {
		return
	}
	out, ok := umbrielOutputOf(outs, w.Output)
	if !ok {
		return
	}
	x, y := umbrielSpot(click, known, w.W, out)
	if !w.Floating {
		_ = umbrielRequest(map[string]any{"cmd": "action", "action": "window-toggle-floating", "window": w.ID}, nil)
	}
	_ = umbrielRequest(map[string]any{"cmd": "action", "action": "window-move-to", "anchor": "top_left", "x": x, "y": y, "window": w.ID}, nil)
}

// umbrielOutputOf is the output named name, or the focused one when no
// output is named (or the named one is gone).
func umbrielOutputOf(outs []umbrielOutput, name string) (umbrielOutput, bool) {
	var focused *umbrielOutput
	for i, o := range outs {
		if o.LogicalSize.Width <= 0 {
			continue // disabled
		}
		if name != "" && o.Name == name {
			return o, true
		}
		if o.Focused && focused == nil {
			focused = &outs[i]
		}
	}
	if focused == nil {
		return umbrielOutput{}, false
	}
	return *focused, true
}

// umbrielSpot is window-move-to's x, y from the usable area's top left
// (the output less the bar) for a panel width wide: panelSpot's place,
// which counts the click across the output, as a tray host on Wayland
// knows it, taken into the usable area and kept PanelGap inside it.
func umbrielSpot(click int, known bool, width int, out umbrielOutput) (x, y int) {
	ox, _ := panelSpot(click, known, float64(width), float64(out.LogicalSize.Width))
	x = out.Position.X + int(ox) - out.Usable.X
	x = max(PanelGap, min(x, out.Usable.Width-width-PanelGap))
	return x, PanelGap
}

func umbrielDial() (net.Conn, error) {
	sock := os.Getenv("UMBRIEL_SOCKET")
	if sock == "" {
		return nil, errors.New("umbriel: UMBRIEL_SOCKET is not set")
	}
	return net.DialTimeout("unix", sock, time.Second)
}

// umbrielRequest sends Umbriel one request, a line of JSON, and reads its
// reply, {"ok": …} or {"err": "…"}, into v.
func umbrielRequest(req, v any) error {
	c, err := umbrielDial()
	if err != nil {
		return err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(time.Second))
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if _, err := c.Write(append(b, '\n')); err != nil {
		return err
	}
	sc := bufio.NewScanner(c)
	sc.Buffer(nil, 16<<20)
	if !sc.Scan() {
		return cmpErr(sc.Err(), io.EOF)
	}
	var r struct {
		Ok  json.RawMessage `json:"ok"`
		Err *string         `json:"err"`
	}
	if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
		return err
	}
	if r.Err != nil {
		return errors.New("umbriel: " + *r.Err)
	}
	if v == nil || r.Ok == nil {
		return nil
	}
	return json.Unmarshal(r.Ok, v)
}
