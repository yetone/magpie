package gateway

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/image/draw"
)

func recJPEG(t *testing.T, w, h int, box image.Rectangle) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), image.White, image.Point{}, draw.Src)
	draw.Draw(img, box, image.NewUniform(color.RGBA{0, 0, 0xff, 0xff}), image.Point{}, draw.Src)
	var b bytes.Buffer
	jpeg.Encode(&b, img, &jpeg.Options{Quality: 95})
	return b.Bytes()
}

// A recording's GIF has a frame an action, held a second, the last three;
// after the first, a frame holds only what changed.
func TestBrowserGIFFramesHoldWhatChanged(t *testing.T) {
	frames := [][]byte{
		recJPEG(t, 400, 300, image.Rectangle{}),
		recJPEG(t, 400, 300, image.Rect(100, 100, 160, 140)),
		recJPEG(t, 400, 300, image.Rect(100, 100, 160, 140)),
		recJPEG(t, 800, 600, image.Rectangle{}), // a resized window
	}
	data, err := browserGIF(frames)
	if err != nil {
		t.Fatal(err)
	}
	g, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Image) != 4 || g.Config.Width != 400 || g.Config.Height != 300 {
		t.Fatalf("%d frames, %dx%d", len(g.Image), g.Config.Width, g.Config.Height)
	}
	if d := g.Delay; d[0] != 100 || d[3] != 300 {
		t.Fatalf("delays %v", d)
	}
	if b := g.Image[1].Bounds(); b.Dx() > 80 || b.Dy() > 60 || !b.Overlaps(image.Rect(100, 100, 160, 140)) {
		t.Fatalf("the second frame holds %v, not the box that changed", b)
	}
	if b := g.Image[2].Bounds(); b.Dx()*b.Dy() > 4 {
		t.Fatalf("an unchanged frame holds %v", b)
	}
	if b := g.Image[3].Bounds(); !b.In(image.Rect(0, 0, 400, 300)) {
		t.Fatalf("the resized frame is %v, outside the GIF", b)
	}
}

// A frame's label says what was done to what, never what was typed.
func TestBrowserRecSayKeepsTypedTextOut(t *testing.T) {
	got := browserRecSay(3, "type", map[string]any{"ref": "e4", "text": "hunter2", "action": "type"})
	if got != "3  type  e4" {
		t.Fatalf("%q", got)
	}
	if got := browserRecSay(1, "navigate", map[string]any{"url": "https://example.com/"}); got != "1  navigate  https://example.com/" {
		t.Fatalf("%q", got)
	}
	if got := browserRecSay(2, "drag", map[string]any{"from": "e1", "to": "e2"}); got != "2  drag  e1 -> e2" {
		t.Fatalf("%q", got)
	}
}

// record_stop with nothing recorded says so, with no browser connected.
func TestBrowserRecordStopWithNothing(t *testing.T) {
	fresh(t)
	s := New()
	srv := httptest.NewServer(lanGuard(s.Handler()))
	t.Cleanup(srv.Close)
	call, _ := browserCaller(t, srv, "rec")
	if m := call("browser", map[string]any{"action": "record_stop"}); !m.Result.IsError || !strings.Contains(m.Result.Content[0].Text, "record_start") {
		t.Fatalf("%+v", m.Result)
	}
	call("browser", map[string]any{"action": "record_start"})
	if m := call("browser", map[string]any{"action": "record_stop"}); !m.Result.IsError || !strings.Contains(m.Result.Content[0].Text, "no frames") {
		t.Fatalf("%+v", m.Result)
	}
}

// Recording in a real Chromium: the actions that change the page are each
// a frame of the GIF at the answer's link, the ones that read it are not.
func TestBrowserRecordsAGIF(t *testing.T) {
	srv, site := realBrowser(t)
	call, ok := browserCaller(t, srv, "rec")
	ok("browser", map[string]any{"action": "record_start"}, "Recording")
	page := ok("browser", map[string]any{"action": "navigate", "url": site.URL}, "Magpie fixture")
	name := regexp.MustCompile(`textbox "Name"[^\n]*\[ref=(e\d+)\]`).FindStringSubmatch(page)
	if name == nil {
		t.Fatalf("no Name field in:\n%s", page)
	}
	ok("browser", map[string]any{"action": "type", "ref": name[1], "text": "hunter2"})
	ok("browser", map[string]any{"action": "snapshot"})
	ok("browser", map[string]any{"action": "navigate", "url": site.URL + "/two"}, "Page two")
	m := call("browser", map[string]any{"action": "record_stop"})
	if m.Result.IsError || !strings.Contains(m.Result.Content[0].Text, "Recorded 3 frames") {
		t.Fatalf("%+v", m.Result)
	}
	var files []MadeFile
	if json.Unmarshal([]byte(m.meta(FilesMeta)), &files) != nil || len(files) != 1 {
		t.Fatalf("files %s", m.meta(FilesMeta))
	}
	resp, err := http.Get(files[0].URL)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	g, err := gif.DecodeAll(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("%d %s: %v", resp.StatusCode, resp.Header.Get("Content-Type"), err)
	}
	if len(g.Image) != 3 || g.Config.Width != browserRecWidth {
		t.Fatalf("%d frames, %d wide", len(g.Image), g.Config.Width)
	}
	if m := call("browser", map[string]any{"action": "record_stop"}); !m.Result.IsError {
		t.Fatalf("a second stop: %+v", m.Result)
	}
}
