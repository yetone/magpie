package gateway

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/color/palette"
	"image/gif"
	"image/jpeg"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/h264"
	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// A recording is a video (MP4, or a GIF when asked) of what an agent did in
// its tab: between the
// browser tool's record_start and record_stop, the gateway takes a picture
// of the window after each action that can change the page, with the
// action written under it. The gateway does it all, so any extension
// version records.

const (
	// browserRecWidth is a frame's width: a window, readable, at a size a
	// few hundred frames of keep small.
	browserRecWidth = 800
	// browserRecMost is the most frames one recording keeps.
	browserRecMost = 300
	// browserRecIdle is how long a recording nobody stops is kept.
	browserRecIdle = 30 * time.Minute
	// browserRecLabel is the height of the strip each frame's action is
	// written in.
	browserRecLabel = 22
)

type browserRec struct {
	frames  [][]byte // JPEG, browserRecWidth wide, its label drawn on
	full    bool     // browserRecMost frames were taken; later ones weren't
	started time.Time
	last    time.Time
}

type browserRecs struct {
	mu sync.Mutex
	m  map[string]*browserRec // session → its recording
}

// start begins session's recording, over one it had.
func (rs *browserRecs) start(session string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	now := time.Now()
	for k, r := range rs.m {
		if now.Sub(r.last) > browserRecIdle {
			delete(rs.m, k)
		}
	}
	if rs.m == nil {
		rs.m = map[string]*browserRec{}
	}
	rs.m[session] = &browserRec{started: now, last: now}
}

func (rs *browserRecs) on(session string) bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	_, ok := rs.m[session]
	return ok
}

// add keeps a frame for session's recording, if it has one.
func (rs *browserRecs) add(session string, frame []byte) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	r := rs.m[session]
	if r == nil {
		return
	}
	r.last = time.Now()
	if len(r.frames) >= browserRecMost {
		r.full = true
		return
	}
	r.frames = append(r.frames, frame)
}

// count is how many frames session's recording holds.
func (rs *browserRecs) count(session string) int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if r := rs.m[session]; r != nil {
		return len(r.frames)
	}
	return 0
}

// stop ends session's recording and is it, nil when there was none.
func (rs *browserRecs) stop(session string) *browserRec {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	r := rs.m[session]
	delete(rs.m, session)
	return r
}

// browserRecSkips are the actions after which nothing on the page can have
// changed, or a picture would open a tab (tab_close's tab is gone).
var browserRecSkips = map[string]bool{
	"browser_snapshot": true, "browser_text": true, "browser_console": true,
	"browser_screenshot": true,
}

// browserRecWorth: a picture after tool, with the tabs action tabs, goes
// in a recording.
func browserRecWorth(tool, tabs string) bool {
	if tool == "browser_tabs" {
		return tabs == "new" || tabs == "select"
	}
	return !browserRecSkips[tool]
}

// browserRecSay is what a frame says was done: the action and what it was
// done to, never what was typed (a password, a message).
func browserRecSay(n int, action string, args map[string]any) string {
	s := fmt.Sprintf("%d  %s", n, action)
	for _, k := range []string{"url", "ref", "key", "direction", "from"} {
		if v, ok := args[k].(string); ok && v != "" {
			if len(v) > 80 {
				v = v[:77] + "..."
			}
			s += "  " + v
			if k == "from" {
				if to, ok := args["to"].(string); ok {
					s += " -> " + to
				}
			}
			break
		}
	}
	return s
}

// browserRecFrame is the picture data (base64) as a frame: browserRecWidth
// wide, with label in a strip under it.
func browserRecFrame(data, label string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	r := img.Bounds()
	if r.Dx() == 0 || r.Dy() == 0 {
		return nil, errors.New("an empty picture")
	}
	h := max(1, r.Dy()*browserRecWidth/r.Dx())
	dst := image.NewRGBA(image.Rect(0, 0, browserRecWidth, h+browserRecLabel))
	draw.ApproxBiLinear.Scale(dst, image.Rect(0, 0, browserRecWidth, h), img, r, draw.Src, nil)
	draw.Draw(dst, image.Rect(0, h, browserRecWidth, h+browserRecLabel), image.NewUniform(color.RGBA{0x22, 0x22, 0x22, 0xff}), image.Point{}, draw.Src)
	d := font.Drawer{Dst: dst, Src: image.White, Face: basicfont.Face7x13, Dot: fixed.P(8, h+browserRecLabel-6)}
	d.DrawString(label)
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 80}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// browserRecQP is the MP4's quantizer: small text stays sharp (about 43 dB
// on a page of text) at a fraction of the GIF's size.
const browserRecQP = 23

// browserMP4 is frames as an H.264 MP4: a second a frame, the last held
// three, every frame the first one's size.
func browserMP4(frames [][]byte) ([]byte, error) {
	var e *h264.Encoder
	var size image.Rectangle
	for i, f := range frames {
		img, err := jpeg.Decode(bytes.NewReader(f))
		if err != nil {
			return nil, err
		}
		b := img.Bounds()
		if e == nil {
			size = image.Rect(0, 0, b.Dx(), b.Dy())
			if e, err = h264.NewEncoder(b.Dx(), b.Dy(), browserRecQP); err != nil {
				return nil, err
			}
		} else if b.Dx() != size.Dx() || b.Dy() != size.Dy() {
			// a window resized: every frame is the first one's size
			dst := image.NewRGBA(size)
			draw.ApproxBiLinear.Scale(dst, size, img, b, draw.Src, nil)
			img = dst
		}
		ms := 1000
		if i == len(frames)-1 {
			ms = 3000
		}
		e.Add(img, ms)
	}
	if e == nil {
		return nil, errors.New("no frames")
	}
	return e.MP4(), nil
}

// browserGIF is frames as an animated GIF: a second a frame, the last held
// three. Each frame after the first holds only the rectangle that changed.
func browserGIF(frames [][]byte) ([]byte, error) {
	pal := color.Palette(palette.Plan9)
	near := make([]uint8, 1<<15) // 5 bits a channel → its nearest colour, +1
	index := func(c color.RGBA) uint8 {
		k := int(c.R>>3)<<10 | int(c.G>>3)<<5 | int(c.B>>3)
		if near[k] == 0 {
			near[k] = uint8(pal.Index(color.RGBA{c.R&^7 | 4, c.G&^7 | 4, c.B&^7 | 4, 0xff})) + 1
		}
		return near[k] - 1
	}
	g := &gif.GIF{}
	var prev *image.Paletted
	for i, f := range frames {
		img, err := jpeg.Decode(bytes.NewReader(f))
		if err != nil {
			return nil, err
		}
		b := img.Bounds()
		if prev != nil && b != prev.Bounds() {
			// a window resized: every frame is the first one's size
			dst := image.NewRGBA(prev.Bounds())
			draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
			img, b = dst, dst.Bounds()
		}
		rgba, ok := img.(*image.RGBA)
		if !ok {
			rgba = image.NewRGBA(b)
			draw.Draw(rgba, b, img, b.Min, draw.Src)
		}
		p := image.NewPaletted(b, pal)
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				o := rgba.PixOffset(x, y)
				p.Pix[p.PixOffset(x, y)] = index(color.RGBA{rgba.Pix[o], rgba.Pix[o+1], rgba.Pix[o+2], 0xff})
			}
		}
		out := p
		if prev != nil {
			ch := changed(prev, p)
			if ch.Empty() {
				ch = image.Rect(b.Min.X, b.Min.Y, b.Min.X+1, b.Min.Y+1)
			}
			out = p.SubImage(ch).(*image.Paletted)
		}
		delay := 100
		if i == len(frames)-1 {
			delay = 300
		}
		g.Image = append(g.Image, out)
		g.Delay = append(g.Delay, delay)
		g.Disposal = append(g.Disposal, gif.DisposalNone)
		if prev == nil {
			g.Config = image.Config{ColorModel: pal, Width: b.Dx(), Height: b.Dy()}
		}
		prev = p
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// changed is the smallest rectangle in which a and b, of one size, differ.
func changed(a, b *image.Paletted) image.Rectangle {
	r := a.Bounds()
	x0, y0, x1, y1 := r.Max.X, r.Max.Y, r.Min.X, r.Min.Y
	for y := r.Min.Y; y < r.Max.Y; y++ {
		row := a.PixOffset(r.Min.X, y)
		ra, rb := a.Pix[row:row+r.Dx()], b.Pix[row:row+r.Dx()]
		if bytes.Equal(ra, rb) {
			continue
		}
		y0, y1 = min(y0, y), max(y1, y+1)
		for x := range ra {
			if ra[x] != rb[x] {
				x0, x1 = min(x0, r.Min.X+x), max(x1, r.Min.X+x+1)
				break
			}
		}
		for x := len(ra) - 1; x >= 0; x-- {
			if ra[x] != rb[x] {
				x1 = max(x1, r.Min.X+x+1)
				break
			}
		}
	}
	if x0 >= x1 || y0 >= y1 {
		return image.Rectangle{}
	}
	return image.Rect(x0, y0, x1, y1)
}

// browserRecord answers record_start and record_stop; format is stop's
// "gif" or "mp4" (the default).
func (s *Server) browserRecord(r *http.Request, session string, stop bool, format string) map[string]any {
	if !stop {
		s.browser.recs.start(session)
		return toolResult("Recording. After each action that can change the page a picture of the window is kept, with the action under it; record_stop makes them a video.", false)
	}
	name, mt, encode := "MP4", "video/mp4", browserMP4
	switch format {
	case "", "mp4":
	case "gif":
		name, mt, encode = "GIF", "image/gif", browserGIF
	default:
		return toolResult("record_stop's format is mp4 or gif.", true)
	}
	rec := s.browser.recs.stop(session)
	if rec == nil {
		return toolResult("Nothing is being recorded: record_start begins.", true)
	}
	if len(rec.frames) == 0 {
		return toolResult("The recording has no frames: no action that changes the page was taken after record_start.", true)
	}
	data, err := encode(rec.frames)
	if err != nil {
		return toolResult("The "+name+" couldn't be made: "+err.Error(), true)
	}
	u := imageMCPBase(r) + ImageMCPFiles + s.imageMCPKeepFile(data, mt, access.Caller(r.Context()).KeyID)
	var b strings.Builder
	fmt.Fprintf(&b, "Recorded %d frames over %s, a %d KB %s. Download it within the hour (the link opens without a header; whoever has it can open it), e.g. curl -o recording%s '%s'\n%s\n",
		len(rec.frames), time.Since(rec.started).Round(time.Second), (len(data)+1023)/1024, name, extOf(mt), u, u)
	if rec.full {
		fmt.Fprintf(&b, "It holds the first %d frames; later actions weren't kept.\n", browserRecMost)
	}
	return withMeta(map[string]any{"content": []any{map[string]any{"type": "text", "text": b.String()}}, "isError": false}, FilesMeta, []MadeFile{{URL: u}})
}
