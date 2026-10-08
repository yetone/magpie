package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// agy's view_file gives an image back inside the functionResponse, in its
// own parts (Gemini 3's multimodal function responses), under "model" as
// agy sends it or under "user". Read only as response.output, the image
// never reached the upstream: the prompt was 96 tokens instead of 1115 and
// the model made up what the picture showed (#1038).
const agyImage = "/9j/4AAQSkZJRgABAQAAAQABAAD/2wBDAAgGBgcGBQgHBwcJCQgKDBQNDAsLDBkSEw8U"

func agyViewFile(role string, snake bool) string {
	fr := `{"functionResponse":{"id":"call_462432","name":"view_file",
		"response":{"output":"...The following is the entire, complete content of the requested file."},
		"parts":[{"inlineData":{"mimeType":"image/jpeg","data":"` + agyImage + `"}}]}}`
	if snake {
		fr = `{"function_response":{"id":"call_462432","name":"view_file",
		"response":{"output":"...The following is the entire, complete content of the requested file."},
		"parts":[{"inline_data":{"mime_type":"image/jpeg","data":"` + agyImage + `"}}]}}`
	}
	return `{"contents":[
		{"role":"user","parts":[{"text":"用一句中文说出图中底部字幕和左上角水印"}]},
		{"role":"model","parts":[{"functionCall":{"id":"call_462432","name":"view_file",
			"args":{"AbsolutePath":"/path/to/frame.jpg","toolAction":"Viewing image file","toolSummary":"View frame.jpg"}}}]},
		{"role":"` + role + `","parts":[` + fr + `]}],
	"tools":[{"functionDeclarations":[{"name":"view_file","description":"View a file",
		"parameters":{"type":"object","properties":{"AbsolutePath":{"type":"string"}}}}]}]}`
}

func TestGeminiFunctionResponseImage(t *testing.T) {
	for _, c := range []struct {
		role  string
		snake bool
	}{{"model", false}, {"user", false}, {"model", true}, {"user", true}} {
		name := c.role
		if c.snake {
			name += " snake_case"
		}
		r, err := parseGemini([]byte(agyViewFile(c.role, c.snake)))
		if err != nil {
			t.Fatal(err)
		}
		last := r.Messages[len(r.Messages)-1]
		if last.Role != "user" || len(last.Parts) != 1 || last.Parts[0].Kind != ToolResult {
			t.Fatalf("%s: the last turn: %+v", name, last)
		}
		res := last.Parts[0]
		if !strings.Contains(res.Text, "entire, complete content") || len(res.Images) != 1 ||
			res.Images[0].Kind != Image || res.Images[0].MediaType != "image/jpeg" || res.Images[0].Data != agyImage {
			t.Fatalf("%s: the result: %+v", name, res)
		}
		// every upstream is given the image
		var sent struct {
			Request struct {
				Contents []struct {
					Role  string                       `json:"role"`
					Parts []map[string]json.RawMessage `json:"parts"`
				} `json:"contents"`
			} `json:"request"`
		}
		if err := json.Unmarshal(buildCodeAssist(r, "gemini-3.8-flash", "antigravity"), &sent); err != nil {
			t.Fatal(err)
		}
		turn := sent.Request.Contents[len(sent.Request.Contents)-1]
		if turn.Role != "user" || len(turn.Parts) != 2 || turn.Parts[0]["functionResponse"] == nil ||
			!strings.Contains(string(turn.Parts[1]["inlineData"]), agyImage) {
			t.Fatalf("%s: Antigravity was sent %+v", name, turn)
		}
		for _, via := range []provider.Protocol{provider.Anthropic, provider.Chat, provider.Responses} {
			b, err := build(via, r, "m", "", false)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(b), agyImage) {
				t.Fatalf("%s: %s was not given the image: %s", name, via, b)
			}
		}
	}
}

// Through the gateway: /v1beta with agy's request reaches a Chat upstream
// with the image the tool read.
func TestGeminiFunctionResponseImageReachesUpstream(t *testing.T) {
	setHome(t, t.TempDir())
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","choices":[{"delta":{"content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, out := post(t, "/v1beta/models/fake/m1:generateContent", agyViewFile("model", false))
	if code != 200 || !strings.Contains(out, "OK") {
		t.Fatalf("%d %s", code, out)
	}
	if !strings.Contains(string(f.got), "data:image/jpeg;base64,"+agyImage) {
		t.Fatalf("the upstream was not given the image: %s", f.got)
	}
}

// A file that isn't an image (a PDF) can't sit in another API's tool
// result: it follows the result in the same turn rather than being lost.
func TestGeminiFunctionResponseFile(t *testing.T) {
	body := `{"contents":[{"role":"model","parts":[{"functionCall":{"id":"c1","name":"read","args":{}}}]},
		{"role":"user","parts":[{"functionResponse":{"id":"c1","name":"read","response":{"output":"read it"},
			"parts":[{"inlineData":{"mimeType":"application/pdf","data":"UERG"}},{"fileData":{"mimeType":"image/png","fileUri":"https://x/y.png"}}]}}]}]}`
	r, err := parseGemini([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	parts := r.Messages[1].Parts
	if len(parts) != 2 || parts[0].Kind != ToolResult || len(parts[0].Images) != 1 || parts[0].Images[0].URL != "https://x/y.png" ||
		parts[1].Kind != File || parts[1].MediaType != "application/pdf" || parts[1].Data != "UERG" {
		t.Fatalf("parts: %+v", parts)
	}
}

// The image checks see a function response's image as a tool's: a model
// that can't see is sent the request with it left out, not turned away.
func TestGeminiFunctionResponseImageIsATools(t *testing.T) {
	body := []byte(agyViewFile("user", false))
	if !hasImage(provider.Gemini, body) {
		t.Fatal("the tool's image was not seen")
	}
	out, current := textOnlyBody(provider.Gemini, body)
	if current || strings.Contains(string(out), agyImage) || !strings.Contains(string(out), imageOmitted) {
		t.Fatalf("current %v: %s", current, out)
	}
	r, err := parseGemini(out)
	if err != nil {
		t.Fatal(err)
	}
	res := r.Messages[len(r.Messages)-1].Parts[0]
	if len(res.Images) != 0 || !strings.Contains(res.Text, imageOmitted) {
		t.Fatalf("the result: %+v", res)
	}
}
