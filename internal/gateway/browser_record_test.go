package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
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

// A recording's MP4 has a frame an action, held a second, the last three,
// every frame the first one's size, its index before its pictures.
func TestBrowserMP4(t *testing.T) {
	frames := [][]byte{
		recJPEG(t, 400, 301, image.Rectangle{}),
		recJPEG(t, 400, 301, image.Rect(100, 100, 160, 140)),
		recJPEG(t, 400, 301, image.Rect(100, 100, 160, 140)),
		recJPEG(t, 800, 600, image.Rectangle{}), // a resized window
	}
	data, err := browserMP4(frames)
	if err != nil {
		t.Fatal(err)
	}
	if string(data[4:8]) != "ftyp" || bytes.Index(data, []byte("moov")) > bytes.Index(data, []byte("mdat")) {
		t.Fatalf("starts %q: not ftyp, moov, mdat", data[:16])
	}
	i := bytes.Index(data, []byte("stts"))
	if i < 0 {
		t.Fatal("no stts")
	}
	// version/flags, 2 runs: 3 a second, 1 three seconds
	want := []byte{0, 0, 0, 0, 0, 0, 0, 2, 0, 0, 0, 3, 0, 0, 0x03, 0xe8, 0, 0, 0, 1, 0, 0, 0x0b, 0xb8}
	if got := data[i+4 : i+4+len(want)]; !bytes.Equal(got, want) {
		t.Fatalf("stts % x", got)
	}
	i = bytes.Index(data, []byte("stsd")) + bytes.Index(data[bytes.Index(data, []byte("stsd")):], []byte("avc1"))
	if w, h := int(data[i+28])<<8|int(data[i+29]), int(data[i+30])<<8|int(data[i+31]); w != 400 || h != 302 {
		t.Fatalf("avc1 %dx%d, want 400x302 (cropped to 301)", w, h)
	}
	if _, err := browserMP4(nil); err == nil {
		t.Fatal("no frames made an MP4")
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
// a frame of the MP4 at the answer's link, which Chromium plays; the ones
// that read it are not. format: gif makes a GIF instead.
func TestBrowserRecordsAVideo(t *testing.T) {
	srv, site := realBrowser(t)
	call, ok := browserCaller(t, srv, "rec")
	record := func(format string) (string, []byte, string) {
		t.Helper()
		ok("browser", map[string]any{"action": "record_start"}, "Recording")
		page := ok("browser", map[string]any{"action": "navigate", "url": site.URL}, "Magpie fixture")
		name := regexp.MustCompile(`textbox "Name"[^\n]*\[ref=(e\d+)\]`).FindStringSubmatch(page)
		if name == nil {
			t.Fatalf("no Name field in:\n%s", page)
		}
		ok("browser", map[string]any{"action": "type", "ref": name[1], "text": "hunter2"})
		ok("browser", map[string]any{"action": "snapshot"})
		ok("browser", map[string]any{"action": "navigate", "url": site.URL + "/two"}, "Page two")
		args := map[string]any{"action": "record_stop"}
		if format != "" {
			args["format"] = format
		}
		m := call("browser", args)
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
		return files[0].URL, b, resp.Header.Get("Content-Type")
	}

	u, b, mt := record("")
	if mt != "video/mp4" || len(b) < 8 || string(b[4:8]) != "ftyp" {
		t.Fatalf("%s, starts %q", mt, b[:min(len(b), 16)])
	}
	if !strings.Contains(u, ".mp4?") {
		t.Fatalf("the link %s doesn't end .mp4", u)
	}
	// a player asks for ranges
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("Range", "bytes=0-99")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	part, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || !bytes.Equal(part, b[:100]) {
		t.Fatalf("a range: %d, %d bytes", resp.StatusCode, len(part))
	}
	// Chromium plays it: three frames over five seconds, browserRecWidth
	// wide, and a picture that isn't black
	// (opened as its own page, so its picture can be read)
	ok("browser", map[string]any{"action": "navigate", "url": u})
	play := `async () => {
		const v = document.querySelector('video');
		v.muted = true;
		if (v.readyState < 2) await new Promise((ok, no) => { v.onloadeddata = ok; v.onerror = () => no(new Error('video error ' + (v.error && v.error.code))); setTimeout(() => no(new Error('not loaded: readyState ' + v.readyState)), 10000); });
		const c = document.createElement('canvas');
		c.width = v.videoWidth; c.height = v.videoHeight;
		const g = c.getContext('2d');
		g.drawImage(v, 0, 0);
		const d = g.getImageData(0, 0, c.width, c.height).data;
		let lit = 0;
		for (let i = 0; i < d.length; i += 4) if (d[i] + d[i+1] + d[i+2] > 600) lit++;
		return 'played ' + v.videoWidth + ' ' + v.duration + ' ' + (lit > d.length / 40);
	}`
	ok("browser", map[string]any{"action": "evaluate", "script": play}, fmt.Sprintf("played %d 5 true", browserRecWidth))

	mp4 := len(b)
	_, b, mt = record("gif")
	t.Logf("MP4 %d KB, GIF %d KB", mp4/1024, len(b)/1024)
	g, err := gif.DecodeAll(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("%s: %v", mt, err)
	}
	if len(g.Image) != 3 || g.Config.Width != browserRecWidth {
		t.Fatalf("%d frames, %d wide", len(g.Image), g.Config.Width)
	}
	if m := call("browser", map[string]any{"action": "record_stop"}); !m.Result.IsError {
		t.Fatalf("a second stop: %+v", m.Result)
	}
	ok("browser", map[string]any{"action": "record_start"}, "Recording")
	if m := call("browser", map[string]any{"action": "record_stop", "format": "webm"}); !m.Result.IsError || !strings.Contains(m.Result.Content[0].Text, "mp4 or gif") {
		t.Fatalf("format webm: %+v", m.Result)
	}
}
