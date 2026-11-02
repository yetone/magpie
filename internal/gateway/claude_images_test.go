package gateway

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// tallShot is a screenshot as DeepSeek Harness sends one: a JPEG 400 px
// wide and 8192 tall, within dsh's 2048x2048 pixel budget and 8192 px
// long edge, so dsh passes it byte for byte (sean_80289's case).
func tallShot(t *testing.T, w, h int) string {
	t.Helper()
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m.Set(x, y, color.RGBA{uint8(x), uint8(y), uint8(x ^ y), 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, m, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b.Bytes())
}

func imageSize(t *testing.T, b64 string) (image.Config, string) {
	t.Helper()
	cfg, format, err := image.DecodeConfig(base64.NewDecoder(base64.StdEncoding, strings.NewReader(b64)))
	if err != nil {
		t.Fatalf("the image sent can't be read: %v", err)
	}
	return cfg, format
}

// An earlier turn's image over 8000 px a side went into the transcript a
// run resumes as it came, and the API refused the turn: "API Error: an
// image in the conversation could not be processed and was removed"
// (sean_80289, through DeepSeek Harness). It goes fitted to 2000 px a
// side, as Claude Code fits its own.
func TestClaudeReplayFitsATallImage(t *testing.T) {
	h := tuiScriptHarness(t, `[ -f "$file" ] && cp "$file" "$file.seen"`)
	img := tallShot(t, 400, 8192)
	body := `{"model":"claude-sonnet-5","max_tokens":100,"messages":[` +
		`{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"` + img + `"}},{"type":"text","text":"what is it"}]},` +
		`{"role":"assistant","content":[{"type":"text","text":"a long page"}]},` +
		`{"role":"user","content":"and its title?"}]}`
	var u Usage
	if code, msg := h.s.serveClaudeSubscription(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body)), provider.Anthropic, h.p, "claude-sonnet-5", []byte(body), &u); code != 200 {
		t.Fatalf("%d %s", code, msg)
	}
	var run string
	for _, l := range h.runs() {
		run = l
	}
	args, _, _ := strings.Cut(run, "\n")
	_, sid, ok := strings.Cut(args, "--resume ")
	if !ok {
		t.Fatalf("the run wasn't given a transcript: %s", args)
	}
	sid, _, _ = strings.Cut(sid, " ")
	seen, _ := filepath.Glob(filepath.Join(h.config, "projects", "*", sid+".jsonl.seen"))
	if len(seen) != 1 {
		t.Fatalf("no transcript %s", sid)
	}
	b, err := os.ReadFile(seen[0])
	if err != nil {
		t.Fatal(err)
	}
	images := 0
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(nil, 16<<20)
	for sc.Scan() {
		var l struct {
			Message struct {
				Content []struct {
					Type   string `json:"type"`
					Source struct {
						MediaType string `json:"media_type"`
						Data      string `json:"data"`
					} `json:"source"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue
		}
		for _, c := range l.Message.Content {
			if c.Type != "image" {
				continue
			}
			images++
			cfg, format := imageSize(t, c.Source.Data)
			if cfg.Width > claudeImageSide || cfg.Height > claudeImageSide {
				t.Fatalf("the transcript's image is %dx%d, over Claude Code's %d px", cfg.Width, cfg.Height, claudeImageSide)
			}
			if cfg.Height != 2000 || cfg.Width != 98 {
				t.Fatalf("the image wasn't scaled to its own shape: %dx%d", cfg.Width, cfg.Height)
			}
			if c.Source.MediaType != "image/"+format {
				t.Fatalf("media_type %q for a %s", c.Source.MediaType, format)
			}
		}
	}
	if images != 1 {
		t.Fatalf("%d images in the transcript", images)
	}
}

// An image that fits goes as it came, byte for byte; one that can't be
// read goes as it came too.
func TestClaudeImageThatFitsGoesAsItCame(t *testing.T) {
	small := tallShot(t, 400, 2000)
	if mt, d := claudeImage("image/jpeg", small); mt != "image/jpeg" || d != small {
		t.Fatal("an image within 2000 px was written again")
	}
	if mt, d := claudeImage("image/png", "bm90IGFuIGltYWdl"); mt != "image/png" || d != "bm90IGFuIGltYWdl" {
		t.Fatal("bytes that aren't an image were changed")
	}
}

// A PNG over the side is scaled and stays a PNG; a tool's image in an MCP
// result is fitted as a message's is.
func TestClaudeImagePNGAndToolResults(t *testing.T) {
	m := image.NewNRGBA(image.Rect(0, 0, 3000, 1000))
	var b bytes.Buffer
	if err := png.Encode(&b, m); err != nil {
		t.Fatal(err)
	}
	wide := base64.StdEncoding.EncodeToString(b.Bytes())
	res := mcpResult(Part{Text: "shot", Images: []Part{{MediaType: "image/png", Data: wide}}})
	if len(res.Content) != 2 {
		t.Fatalf("%d contents", len(res.Content))
	}
	cfg, format := imageSize(t, res.Content[1]["data"].(string))
	if cfg.Width != 2000 || cfg.Height != 667 || format != "png" || res.Content[1]["mimeType"] != "image/png" {
		t.Fatalf("the tool's image went as %dx%d %s (%v)", cfg.Width, cfg.Height, format, res.Content[1]["mimeType"])
	}
}
