package omarchy

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"os"
	"runtime"
	"sync"
	"time"
)

// Niri is whether magpie runs under niri. As under Hyprland, a Wayland
// window can't place itself, so the tray panel opened where niri floats a
// new window: the middle of the screen (#1670).
func Niri() bool {
	return runtime.GOOS == "linux" && os.Getenv("NIRI_SOCKET") != ""
}

// NiriPanel puts the panel under niri's bar, beside the icon clicked, each
// time it opens. niri has no rule a program can add while it runs and
// doesn't tell where the pointer is, so the panel is found by its title
// among the windows niri announces, made floating and moved there; it
// still opens in the middle and slides up. A click on another window takes
// niri's focus from it, and Wails' HideOnFocusLost closes it then.
type NiriPanel struct{ panelClick }

// panelClick is the click that is opening the panel, which a compositor's
// watch reads as the panel appears.
type panelClick struct {
	mu    sync.Mutex
	iconX int  // where the icon was clicked across its bar
	known bool // whether iconX is known: not when `magpie panel` opened it
}

// Opening says the panel is about to open, from a click at x across the
// tray's bar (the tray host's Activate(x, y)), or with x nil from anything
// else, when it goes to the top right.
func (p *panelClick) Opening(x *int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.known = x != nil
	if x != nil {
		p.iconX = *x
	}
}

// take is the click Opening was told of, once.
func (p *panelClick) take() (x int, known bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	x, known = p.iconX, p.known
	p.known = false
	return x, known
}

// Watch places each window of this process titled title as it opens, for
// as long as magpie runs, reconnecting when niri restarts.
func (p *NiriPanel) Watch(title string) {
	for {
		if c, err := niriDial(); err == nil {
			_ = watchWindows(c, title, os.Getpid(), p.place)
			c.Close()
		}
		time.Sleep(2 * time.Second)
	}
}

// niriWindow is what magpie reads of niri-ipc's Window.
type niriWindow struct {
	ID         uint64 `json:"id"`
	Title      string `json:"title"`
	PID        int    `json:"pid"`
	IsFloating bool   `json:"is_floating"`
	Layout     struct {
		TileSize [2]float64 `json:"tile_size"` // the window and its border, as placed
	} `json:"layout"`
}

// watchWindows reads niri's event stream on c and calls place once for
// each window of pid titled title: when the stream starts (the panel may
// have opened before it did) and whenever one opens or takes the title.
// A window placed isn't placed again, so one dragged stays where it went.
func watchWindows(c io.ReadWriter, title string, pid int, place func(niriWindow)) error {
	if _, err := io.WriteString(c, `"EventStream"`+"\n"); err != nil {
		return err
	}
	sc := bufio.NewScanner(c)
	sc.Buffer(nil, 16<<20) // the first event lists every window
	if !sc.Scan() {
		return cmpErr(sc.Err(), io.EOF)
	}
	if err := niriReply(sc.Bytes(), nil); err != nil {
		return err
	}
	placed := map[uint64]bool{}
	see := func(w niriWindow) {
		if w.Title == title && (w.PID == 0 || w.PID == pid) && !placed[w.ID] {
			placed[w.ID] = true
			place(w)
		}
	}
	for sc.Scan() {
		var ev struct {
			WindowsChanged *struct {
				Windows []niriWindow `json:"windows"`
			}
			WindowOpenedOrChanged *struct {
				Window niriWindow `json:"window"`
			}
			WindowClosed *struct {
				ID uint64 `json:"id"`
			}
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch {
		case ev.WindowsChanged != nil:
			open := map[uint64]bool{}
			for _, w := range ev.WindowsChanged.Windows {
				open[w.ID] = true
			}
			for id := range placed {
				if !open[id] {
					delete(placed, id)
				}
			}
			for _, w := range ev.WindowsChanged.Windows {
				see(w)
			}
		case ev.WindowOpenedOrChanged != nil:
			see(ev.WindowOpenedOrChanged.Window)
		case ev.WindowClosed != nil:
			delete(placed, ev.WindowClosed.ID)
		}
	}
	return cmpErr(sc.Err(), io.EOF)
}

// place moves the panel w, just opened, under the bar of the output it
// opened on (the focused one: it has just taken the focus).
func (p *NiriPanel) place(w niriWindow) {
	x, known := p.take()
	var out struct {
		FocusedOutput *struct {
			Logical *struct {
				Width float64 `json:"width"`
			} `json:"logical"`
		}
	}
	if niriRequest("FocusedOutput", &out) != nil || out.FocusedOutput == nil || out.FocusedOutput.Logical == nil {
		return
	}
	px, py := panelSpot(x, known, w.Layout.TileSize[0], out.FocusedOutput.Logical.Width)
	if !w.IsFloating {
		_ = niriRequest(niriAction("MoveWindowToFloating", map[string]any{"id": w.ID}), nil)
	}
	_ = niriRequest(moveTo(w.ID, px, py), nil)
}

// panelSpot is where a panel width wide goes in niri's working area (the
// output less the bar), whose top left a floating window's position counts
// from: PanelGap under the bar, its middle at the click, kept PanelGap
// inside the output's sides; at the top right when the click isn't known.
// A tray host on Wayland knows only its own bar's coordinates, so a click
// counts across the output; one off it (a host giving coordinates across
// every output) is taken as not known. outW is the output's logical width.
func panelSpot(clickX int, known bool, width, outW float64) (x, y float64) {
	right := outW - width - PanelGap
	x = right
	if cx := float64(clickX); known && cx >= 0 && cx < outW {
		x = min(right, cx-width/2)
	}
	return math.Round(max(PanelGap, x)), PanelGap
}

// moveTo is niri-ipc's MoveFloatingWindow, to x, y in the working area.
func moveTo(id uint64, x, y float64) any {
	return niriAction("MoveFloatingWindow", map[string]any{
		"id": id,
		"x":  map[string]float64{"SetFixed": x},
		"y":  map[string]float64{"SetFixed": y},
	})
}

func niriAction(name string, args map[string]any) any {
	return map[string]any{"Action": map[string]any{name: args}}
}

func niriDial() (net.Conn, error) {
	sock := os.Getenv("NIRI_SOCKET")
	if sock == "" {
		return nil, errors.New("niri: NIRI_SOCKET is not set")
	}
	return net.DialTimeout("unix", sock, time.Second)
}

// niriRequest sends niri one request, a line of JSON, and reads its reply
// into v, the Ok side of niri-ipc's Reply.
func niriRequest(req, v any) error {
	c, err := niriDial()
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
	return niriReply(sc.Bytes(), v)
}

// niriReply reads niri-ipc's Reply, {"Ok": …} or {"Err": "…"}, into v.
func niriReply(line []byte, v any) error {
	var r struct {
		Ok  json.RawMessage
		Err *string
	}
	if err := json.Unmarshal(line, &r); err != nil {
		return err
	}
	if r.Err != nil {
		return errors.New("niri: " + *r.Err)
	}
	if r.Ok == nil {
		return errors.New("niri: a reply with neither Ok nor Err")
	}
	if v == nil {
		return nil
	}
	return json.Unmarshal(r.Ok, v)
}

func cmpErr(err, otherwise error) error {
	if err != nil {
		return err
	}
	return otherwise
}
