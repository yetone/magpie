package gateway

import (
	"bytes"
	"container/list"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// The sizes Codex's own client gives images, from openai/codex's tests
// (codex-rs/utils/image/src/image_tests.rs) and a MacBook's full-screen
// screenshot as ylorn's would be.
func TestCodexImageSizeAsCodex(t *testing.T) {
	high, original := imageLimits{2048, 2500}, imageLimits{6000, 10000}
	for _, c := range []struct {
		l            imageLimits
		w, h, ow, oh int
	}{
		{high, 64, 32, 64, 32},
		{high, 2048, 2048, 1600, 1600},
		{high, 2050, 2, 2048, 2},
		{original, 6401, 100, 6000, 94},
		{original, 4096, 2048, 4096, 2048},
		{high, 3456, 2234, 1979, 1280},
	} {
		if w, h := c.l.size(c.w, c.h); w != c.ow || h != c.oh {
			t.Errorf("%v %dx%d → %dx%d, Codex gives %dx%d", c.l, c.w, c.h, w, h, c.ow, c.oh)
		}
	}
}

// screenshot is a w×h image: black on its left half, white on its right,
// with a red band 64 px tall across the middle — what has to stay where
// it was for the model to read it.
func shotImage(w, h int) *image.RGBA {
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{255, 255, 255, 255}
			if x < w/2 {
				c = color.RGBA{0, 0, 0, 255}
			}
			if y >= h/2-32 && y < h/2+32 {
				c = color.RGBA{220, 20, 20, 255}
			}
			m.SetRGBA(x, y, c)
		}
	}
	return m
}

func shotURL(t *testing.T, m image.Image, format string) string {
	t.Helper()
	var b bytes.Buffer
	if format == "jpeg" {
		jpeg.Encode(&b, m, &jpeg.Options{Quality: 95})
	} else {
		png.Encode(&b, m)
	}
	return "data:image/" + format + ";base64," + base64.StdEncoding.EncodeToString(b.Bytes())
}

func shotDecode(t *testing.T, url string) (image.Image, string) {
	t.Helper()
	comma := strings.IndexByte(url, ',')
	raw, err := base64.StdEncoding.DecodeString(url[comma+1:])
	if err != nil {
		t.Fatalf("not base64: %v", err)
	}
	m, format, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("not an image: %v", err)
	}
	return m, format
}

// legible: the halves and the band are where they were, in their colours.
func shotLegible(t *testing.T, m image.Image) {
	t.Helper()
	b := m.Bounds()
	w, h := b.Dx(), b.Dy()
	at := func(x, y int) (uint32, uint32, uint32) {
		r, g, bl, _ := m.At(x, y).RGBA()
		return r >> 8, g >> 8, bl >> 8
	}
	if r, g, bl := at(w/4, h/4); r > 16 || g > 16 || bl > 16 {
		t.Errorf("left half isn't black: %d %d %d", r, g, bl)
	}
	if r, g, bl := at(3*w/4, 3*h/4); r < 240 || g < 240 || bl < 240 {
		t.Errorf("right half isn't white: %d %d %d", r, g, bl)
	}
	if r, g, bl := at(w/4, h/2); r < 190 || g > 50 || bl > 50 {
		t.Errorf("the band isn't red: %d %d %d", r, g, bl)
	}
}

func codexAccount() provider.Provider {
	return provider.Provider{ID: "codex", Account: &provider.Account{Agent: "codex", User: "me@example.com"}}
}

func responsesWith(parts ...map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{"model": "gpt-5.5", "stream": true, "input": []any{
		map[string]any{"type": "message", "role": "user", "content": append([]any{
			map[string]any{"type": "input_text", "text": "what does this show?"}}, anys(parts)...)},
		map[string]any{"type": "function_call_output", "call_id": "c1", "output": []any{
			map[string]any{"type": "input_text", "text": "the page"}}},
	}})
	return b
}

func anys(parts []map[string]any) []any {
	out := make([]any, len(parts))
	for i, p := range parts {
		out[i] = p
	}
	return out
}

// A screenshot larger than Codex's own client sends goes to a ChatGPT
// account at the size Codex would send it, as a PNG still, and the
// picture in it is the same picture. The rest of the request is the same
// bytes, for the prompt cache.
func TestCodexScreenshotSizedAsCodex(t *testing.T) {
	big := shotURL(t, shotImage(3456, 2234), "png")
	body := responsesWith(map[string]any{"type": "input_image", "image_url": big})
	out := codexImagesFit(codexAccount(), provider.Responses, body)

	url := gjson.GetBytes(out, "input.0.content.1.image_url").String()
	if !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Fatalf("not a PNG: %.40s", url)
	}
	m, format := shotDecode(t, url)
	if format != "png" || m.Bounds().Dx() != 1979 || m.Bounds().Dy() != 1280 {
		t.Fatalf("%s %v, Codex sends 1979x1280 png", format, m.Bounds())
	}
	shotLegible(t, m)
	if len(out) >= len(body) {
		t.Errorf("%d bytes after, %d before", len(out), len(body))
	}
	if string(bytes.Replace(out, []byte(url), []byte("IMG"), 1)) != string(bytes.Replace(body, []byte(big), []byte("IMG"), 1)) {
		t.Errorf("the rest of the request changed")
	}
}

// A JPEG stays a JPEG, at Codex's quality; an image in a tool's output is
// sized too; "original" detail has Codex's larger budget.
func TestCodexImagesSizedInEveryPlace(t *testing.T) {
	photo := shotURL(t, shotImage(3000, 3000), "jpeg")
	wide := shotURL(t, shotImage(6401, 100), "png")
	body := responsesWith(
		map[string]any{"type": "input_image", "image_url": photo, "detail": "high"},
		map[string]any{"type": "input_image", "image_url": wide, "detail": "original"},
	)
	body = []byte(strings.Replace(string(body), `{"text":"the page","type":"input_text"}`,
		`{"text":"the page","type":"input_text"},{"type":"input_image","image_url":"`+photo+`"}`, 1))
	out := codexImagesFit(codexAccount(), provider.Responses, body)

	for path, want := range map[string][3]any{
		"input.0.content.1.image_url": {"jpeg", 1600, 1600},
		"input.0.content.2.image_url": {"png", 6000, 94},
		"input.1.output.1.image_url":  {"jpeg", 1600, 1600},
	} {
		url := gjson.GetBytes(out, path).String()
		if url == "" {
			t.Fatalf("%s gone: %.300s", path, out)
		}
		m, format := shotDecode(t, url)
		if format != want[0] || m.Bounds().Dx() != want[1] || m.Bounds().Dy() != want[2] {
			t.Errorf("%s: %s %v, want %v", path, format, m.Bounds(), want)
		}
		if want[1] == 1600 {
			shotLegible(t, m)
		}
	}
}

// What Codex's client sends as it came goes as it came: an image that
// fits, one with "low" detail (Codex refuses that itself), and anything
// that isn't an image a decoder reads.
func TestCodexImagesThatFitGoAsTheyCame(t *testing.T) {
	small := shotURL(t, shotImage(64, 32), "png")
	big := shotURL(t, shotImage(3000, 3000), "png")
	body := responsesWith(
		map[string]any{"type": "input_image", "image_url": small},
		map[string]any{"type": "input_image", "image_url": big, "detail": "low"},
		map[string]any{"type": "input_image", "image_url": "data:image/png;base64,bm90IGFuIGltYWdl"},
		map[string]any{"type": "input_image", "image_url": "https://example.invalid/a.png"},
	)
	if out := codexImagesFit(codexAccount(), provider.Responses, body); !bytes.Equal(out, body) {
		t.Errorf("changed")
	}
}

// Only a ChatGPT account's requests are sized: another vendor, an API
// key, or another protocol is sent the image as the agent sent it.
func TestCodexImagesOnlyForChatGPT(t *testing.T) {
	body := responsesWith(map[string]any{"type": "input_image", "image_url": shotURL(t, shotImage(3456, 2234), "png")})
	for _, c := range []struct {
		p  provider.Provider
		to provider.Protocol
	}{
		{provider.Provider{ID: "openai"}, provider.Responses},
		{provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude"}}, provider.Responses},
		{codexAccount(), provider.Chat},
	} {
		if out := codexImagesFit(c.p, c.to, body); !bytes.Equal(out, body) {
			t.Errorf("%s %v: changed", c.p.ID, c.to)
		}
	}
}

// An agent other than Codex — Pi's openai-codex, as ylorn's — asking a
// ChatGPT account through magpie: the backend gets the screenshot at
// Codex's size, which takes the turn from past 1 MiB to under it.
func TestCodexAccountGetsScreenshotAtCodexSize(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	var got []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.5"}}`,
			`data: {"type":"response.output_text.delta","delta":"a screenshot"}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":2}}}`))
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })

	// a photo along its top, which no PNG makes small: the turn past
	// 1 MiB, as ylorn's was
	shot := shotImage(3456, 2234)
	noise := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < 160*shot.Stride; i++ {
		if i%4 != 3 {
			shot.Pix[i] = byte(noise.Uint32())
		}
	}
	body := string(responsesWith(map[string]any{"type": "input_image", "image_url": shotURL(t, shot, "png")}))
	body = strings.Replace(body, `"model":"gpt-5.5"`, `"model":"codex/gpt-5.5"`, 1)
	if len(body) < 1<<20 {
		t.Fatalf("the turn is %d bytes, not past 1 MiB", len(body))
	}
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body)))
	if rec.Code != 200 || got == nil {
		t.Fatalf("status %d: %.300s", rec.Code, rec.Body.String())
	}
	m, _ := shotDecode(t, gjson.GetBytes(got, "input.0.content.1.image_url").String())
	if m.Bounds().Dx() != 1979 || m.Bounds().Dy() != 1280 {
		t.Errorf("sent %v, Codex sends 1979x1280", m.Bounds())
	}
	t.Logf("%d bytes from the agent, %d to ChatGPT", len(body), len(got))
}

// freshFitted gives a test a cache of fitted images of its own, with
// budget bytes, and counts the images made again from here.
func freshFitted(t *testing.T, budget int) func() int64 {
	t.Helper()
	was, wasBudget := fitted, fittedBudget
	fitted, fittedBudget = &fitCache{m: map[[32]byte]*list.Element{}}, budget
	t.Cleanup(func() { fitted, fittedBudget = was, wasBudget })
	from := refits.Load()
	return func() int64 { return refits.Load() - from }
}

// computerUseRequest is a Responses turn with each of shots as a
// Computer Use tool's screenshot, as Codex sends a conversation's every
// past screenshot again on each turn.
func computerUseRequest(shots []string) []byte {
	var input []any
	for i, shot := range shots {
		id := fmt.Sprintf("call_%d", i)
		input = append(input,
			map[string]any{"type": "function_call", "call_id": id, "name": "computer", "arguments": `{"action":"screenshot"}`},
			map[string]any{"type": "function_call_output", "call_id": id, "output": []any{
				map[string]any{"type": "input_image", "image_url": shot}}})
	}
	b, _ := json.Marshal(map[string]any{"model": "gpt-5.5", "stream": true, "input": input})
	return b
}

// wideShots are n distinct PNGs wider than Codex sends, each made
// smaller once it is fitted: cheap to make, as a test needs.
func wideShots(t *testing.T, n int) []string {
	t.Helper()
	out := make([]string, n)
	for i := range out {
		m := image.NewRGBA(image.Rect(0, 0, 2100, 4))
		for x := 0; x < 2100; x++ {
			m.SetRGBA(x, i%4, color.RGBA{byte(i), byte(i >> 8), byte(x), 255})
		}
		out[i] = shotURL(t, m, "png")
	}
	return out
}

// A long Computer Use conversation (fflake33, Discord: hundreds of
// screenshots) sends every past screenshot again on each turn. Each is
// decoded, resized and written again once; the next turn makes only its
// new one, past the 64 images the cache held, and sends the others as the
// same bytes as before, which the prompt cache needs.
func TestCodexScreenshotsAreNotFittedAgainEachTurn(t *testing.T) {
	made := freshFitted(t, fittedBudget)
	shots := wideShots(t, 101)
	first := codexImagesFit(codexAccount(), provider.Responses, computerUseRequest(shots[:100]))
	if n := made(); n != 100 {
		t.Fatalf("the first turn made %d images, want 100", n)
	}
	next := codexImagesFit(codexAccount(), provider.Responses, computerUseRequest(shots))
	if n := made() - 100; n != 1 {
		t.Errorf("the next turn made %d images again, want only its new one", n)
	}
	for i := range 100 {
		path := fmt.Sprintf("input.%d.output.0.image_url", 2*i+1)
		was, now := gjson.GetBytes(first, path).String(), gjson.GetBytes(next, path).String()
		if was == shots[i] || now != was {
			t.Fatalf("screenshot %d: not the same fitted bytes on the next turn", i)
		}
	}
	m, _ := shotDecode(t, gjson.GetBytes(next, "input.201.output.0.image_url").String())
	if m.Bounds().Dx() != 2048 {
		t.Errorf("the new screenshot went at %v", m.Bounds())
	}
}

// Past what the cache may hold, each turn still finds the screenshots kept
// since the last and makes only the rest again. Keeping each image as it
// is made, in a conversation whose images come in the same order each
// turn, would drop each one just before it is sent again and find none.
// What is held stays within the budget.
func TestCodexFittedImagesPastTheBudget(t *testing.T) {
	shots := wideShots(t, 30)
	one := len(fitKeyEntry(t, shots[0]).data)
	made := freshFitted(t, 10*one+one/2)
	body := computerUseRequest(shots)
	was := int64(0)
	kept := 0
	for turn := 1; turn <= 3; turn++ {
		codexImagesFit(codexAccount(), provider.Responses, body)
		n := made() - was
		was += n
		if want := int64(len(shots) - kept); n != want {
			t.Errorf("turn %d made %d of %d images, with %d kept", turn, n, len(shots), kept)
		}
		fitted.Lock()
		held := fitted.bytes
		// PNG sizes differ across screenshots and encoder versions; a byte
		// budget based on one image does not always hold exactly ten.
		kept = len(fitted.m)
		fitted.Unlock()
		if kept == 0 || kept >= len(shots) {
			t.Fatalf("turn %d kept %d images: need a partially cached conversation", turn, kept)
		}
		if held > fittedBudget {
			t.Errorf("turn %d: %d bytes held, past the budget of %d", turn, held, fittedBudget)
		}
	}
}

// fitKeyEntry is shot fitted to Codex's high limits, made directly.
func fitKeyEntry(t *testing.T, shot string) fitEntry {
	t.Helper()
	e := codexImageRefit(shot[strings.IndexByte(shot, ',')+1:], imageLimits{2048, 2500})
	if e.data == nil {
		t.Fatal("the screenshot fits as it came")
	}
	return e
}

// pngHeader is a PNG whose header declares w by h at depth bits a channel
// and colour, with a valid zlib stream of no pixels after it: what a
// decode reads the size from, before it reads a pixel. Go's png decodes it
// as far as allocating the image the header declares, which is where a
// header alone costs its bytes.
func pngHeader(w, h uint32, depth, colour byte) []byte {
	var b bytes.Buffer
	b.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	be := func(v any) { _ = binary.Write(&b, binary.BigEndian, v) }
	var ihdr bytes.Buffer
	_ = binary.Write(&ihdr, binary.BigEndian, w)
	_ = binary.Write(&ihdr, binary.BigEndian, h)
	ihdr.Write([]byte{depth, colour, 0, 0, 0})
	for _, c := range []struct {
		kind string
		body []byte
	}{
		{"IHDR", ihdr.Bytes()},
		// a valid zlib stream of no pixel data, so that a decode reads
		// the header and allocates what it says
		{"IDAT", []byte{0x78, 0x9c, 0x03, 0x00, 0x00, 0x00, 0x00, 0x01}},
		{"IEND", nil},
	} {
		be(uint32(len(c.body)))
		head := append([]byte(c.kind), c.body...)
		b.Write(head)
		be(crc32.ChecksumIEEE(head))
	}
	return b.Bytes()
}

func bombURL(w, h uint32, depth, colour byte) string {
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngHeader(w, h, depth, colour))
}

// allocDuring is the bytes f allocates, from before it runs to after,
// which is what a decode of a size it was given costs whether or not it
// then finds the pixels.
func allocDuring(f func()) uint64 {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// A header of a few dozen bytes is all it takes to ask a decode for
// hundreds of megabytes: it declares the size, and png allocates that
// before it reads a pixel, so the decode is paid for even though it then
// fails. 110 bytes declaring 8000x8000 at 16 bits a channel held 488 MB,
// and four such images in one request held 1.7 GB: a process the system
// kills, off 260 bytes of an upload. What a decode may hold is judged
// from the declaration now, before it decodes, and it is the same measure
// for every format (codexImageMaxBytes, four bytes a pixel for anything
// with colour, as image/png allocates it).
func TestCodexAHeaderAloneCantHoldPastTheBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("measures the bytes a decode holds")
	}
	for _, c := range []struct {
		w, h   uint32
		depth  byte
		colour byte
		what   string
	}{
		{8000, 8000, 8, 6, "8000x8000 at 8 bits a channel: 256 MB of pixels"},
		{8000, 8000, 16, 6, "8000x8000 at 16 bits a channel: 512 MB of pixels"},
		{70000, 70000, 8, 6, "70000x70000: more than an int holds"},
	} {
		url := bombURL(c.w, c.h, c.depth, c.colour)
		if n := len(url); n > 1024 {
			t.Fatalf("%s: the upload is %d bytes, not a header alone", c.what, n)
		}
		b64 := url[strings.IndexByte(url, ',')+1:]
		held := allocDuring(func() {
			codexImageRefit(b64, imageLimits{2048, 2500})
		})
		if held > codexImageMaxBytes {
			t.Errorf("%s: %d bytes of upload held %d MB, past the %d MB a decode may hold",
				c.what, len(url), held>>20, codexImageMaxBytes>>20)
		}
	}
}

// The same, in one request: four headers each declaring a size it may not
// hold, which codexImagesFit used to make side by side.
func TestCodexFourHeadersInOneRequestCantHoldTheHeap(t *testing.T) {
	const allowed = 128 << 20
	if testing.Short() {
		t.Skip("measures the bytes a decode holds")
	}
	// four *distinct* images: one entry is made per image, so the same data
	// URL four times is made once
	urls := make([]string, 4)
	for i := range urls {
		urls[i] = bombURL(7000+uint32(i), 8000, 16, 6)
	}
	body := computerUseRequest(urls)
	held := allocDuring(func() {
		codexImagesFit(codexAccount(), provider.Responses, body)
	})
	if held > allowed {
		t.Errorf("%d bytes of upload, sent as 4 images in one request, held %d MB, past %d MB",
			len(body), held>>20, allowed>>20)
	}
}

// The budget the bytes above are counted against is the one a real
// screenshot of the size Codex sends still fits under: what it holds once
// decoded, so that a genuine image and a header alone are judged by the
// same measure.
func TestCodexRealScreenshotsFitBytesTheyHold(t *testing.T) {
	for _, c := range []struct{ w, h int }{
		{3456, 2234}, // ylorn's, the size Codex's own client sends
		{4032, 3024},
		{6000, 4000},
	} {
		for _, format := range []string{"png", "jpeg"} {
			url := shotURL(t, shotImage(c.w, c.h), format)
			raw := rawOf(t, url)
			cfg, f, err := image.DecodeConfig(bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			holds, ok := imageHolds(cfg, f)
			if !ok || len(raw)+holds > codexImageMaxBytes {
				t.Fatalf("%dx%d %s holds %d MB: a real screenshot is turned away", c.w, c.h, format, holds>>20)
			}
			e := codexImageRefit(url[strings.IndexByte(url, ',')+1:], imageLimits{2048, 2500})
			if e.data == nil {
				t.Fatalf("%dx%d %s went as it came", c.w, c.h, format)
			}
			if m, _ := shotDecode(t, "data:"+e.mime+";base64,"+base64.StdEncoding.EncodeToString(e.data)); m == nil {
				t.Fatalf("%dx%d %s: what was made isn't an image", c.w, c.h, format)
			}
		}
	}
}

func rawOf(t *testing.T, url string) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(url[strings.IndexByte(url, ',')+1:])
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// What one request may hold is batched: no more than fitBudget is made at
// once, however many images the request carries, so that a request of 100
// screenshots holds what four do and not what a hundred do.
func TestCodexFittingHoldsTheBudget(t *testing.T) {
	const mb = 1 << 20
	for _, c := range []struct {
		what   string
		holds  []int
		budget int
		want   [][2]int
	}{
		{"four small ones, all at once", []int{10 * mb, 10 * mb, 10 * mb, 10 * mb}, 100 * mb, [][2]int{{0, 4}}},
		{"one more than the budget, alone", []int{10 * mb, 250 * mb, 10 * mb}, 100 * mb, [][2]int{{0, 1}, {1, 2}, {2, 3}}},
		{"the second waits for the first", []int{60 * mb, 60 * mb}, 100 * mb, [][2]int{{0, 1}, {1, 2}}},
		{"a batch breaks where the next wouldn't fit", []int{40 * mb, 40 * mb, 40 * mb, 40 * mb, 10 * mb}, 100 * mb, [][2]int{{0, 2}, {2, 5}}},
		{"a batch breaks where the next wouldn't fit, exactly", []int{40 * mb, 40 * mb, 40 * mb, 30 * mb}, 100 * mb, [][2]int{{0, 2}, {2, 4}}},
		{"one past the budget on its own, alone", []int{400 * mb, 10 * mb}, 100 * mb, [][2]int{{0, 1}, {1, 2}}},
		{"nothing to make", nil, 100 * mb, nil},
	} {
		got := fitInHand(c.holds, c.budget)
		if len(got) != len(c.want) {
			t.Errorf("%s: %d batches %v, want %d %v", c.what, len(got), got, len(c.want), c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: batch %d is %v, want %v", c.what, i, got[i], c.want[i])
			}
		}
		// and what any batch holds is the budget, or one image alone
		at := 0
		for _, b := range got {
			if b[0] != at {
				t.Errorf("%s: batch %v leaves a gap after %d", c.what, b, at)
			}
			at = b[1]
			sum := 0
			for _, h := range c.holds[b[0]:b[1]] {
				sum += h
			}
			if sum > c.budget && b[1]-b[0] != 1 {
				t.Errorf("%s: batch %v holds %d MB past the budget of %d MB", c.what, b, sum/mb, c.budget/mb)
			}
		}
	}
}

// A request whose images fit the budget is made as it was: the batching
// changes nothing for the screenshots a real turn carries.
func TestCodexFittingLeavesATurnThatFitsAsItWas(t *testing.T) {
	made := freshFitted(t, fittedBudget)
	shots := wideShots(t, 8)
	if out := codexImagesFit(codexAccount(), provider.Responses, computerUseRequest(shots)); len(out) == 0 {
		t.Fatal("no reply body")
	}
	if n := made(); n != 8 {
		t.Errorf("%d of 8 images made", n)
	}
}

// What no request has sent for fittedIdle is let go.
func TestCodexFittedImagesLetGoWhenIdle(t *testing.T) {
	made := freshFitted(t, fittedBudget)
	shots := wideShots(t, 2)
	codexImagesFit(codexAccount(), provider.Responses, computerUseRequest(shots[:1]))
	fitted.Lock()
	for el := fitted.order.Front(); el != nil; el = el.Next() {
		el.Value.(*fitItem).used = time.Now().Add(-fittedIdle - time.Minute)
	}
	fitted.Unlock()
	codexImagesFit(codexAccount(), provider.Responses, computerUseRequest(shots[1:]))
	fitted.Lock()
	n, held := len(fitted.m), fitted.bytes
	fitted.Unlock()
	if n != 1 || made() != 2 {
		t.Errorf("%d kept after the first went idle, %d made", n, made())
	}
	if want := len(fitKeyEntry(t, shots[1]).data); held != want {
		t.Errorf("%d bytes held, want %d", held, want)
	}
}

// A JPEG's frame header can sit behind an EXIF segment of up to 64 KB, and
// a phone photo's often does. fitHolds read only the first 4 KB of base64
// for it, counted such an image as holding nothing, and put it in a batch
// with three others each holding up to codexImageMaxBytes. It is counted
// now for what it holds, wherever its header is.
func TestCodexAHeaderBehindExifIsCounted(t *testing.T) {
	var shot bytes.Buffer
	if err := jpeg.Encode(&shot, shotImage(3456, 2234), &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}
	raw := shot.Bytes()
	// SOI, then an APP1 of the most a segment holds, then the rest
	exif := make([]byte, 0xffff-2)
	copy(exif, "Exif\x00\x00")
	withExif := append([]byte{0xff, 0xd8, 0xff, 0xe1, 0xff, 0xff}, exif...)
	withExif = append(withExif, raw[2:]...)
	url := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(withExif)
	if _, _, err := image.DecodeConfig(bytes.NewReader(withExif)); err != nil {
		t.Fatalf("the photo isn't a JPEG: %v", err)
	}
	cfg := image.Config{ColorModel: color.YCbCrModel, Width: 3456, Height: 2234}
	pixels, _ := imageHolds(cfg, "jpeg")
	if got, want := fitHolds(url), len(withExif)+pixels; got != want {
		t.Errorf("a JPEG with its header behind %d KB of EXIF holds %d MB, want %d MB", len(exif)>>10, got>>20, want>>20)
	}
}
