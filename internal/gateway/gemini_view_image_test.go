package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestVertexViewImageTurn(t *testing.T) {
	v := &vertexUp{}
	v.reply = vertexTurnsAway
	s := vertexServe(t, v, vertexLevels)
	const img = "data:image/png;base64,iVBORw0KGgo="
	hist := `{"type":"message","role":"user","content":[{"type":"input_text","text":"look at the screenshot"}]},` +
		`{"type":"function_call","name":"view_image","arguments":"{\"path\":\"/tmp/a.png\"}","call_id":"call_1"},`
	for name, tail := range map[string]string{
		"array":         `{"type":"function_call_output","call_id":"call_1","output":[{"type":"input_text","text":"ok"},{"type":"input_image","image_url":"` + img + `","detail":"high"}]}`,
		"imageonly":     `{"type":"function_call_output","call_id":"call_1","output":[{"type":"input_image","image_url":"` + img + `"}]}`,
		"separate":      `{"type":"function_call_output","call_id":"call_1","output":"ok"},{"type":"message","role":"user","content":[{"type":"input_image","image_url":"` + img + `"}]}`,
		"separate-asst": `{"type":"function_call_output","call_id":"call_1","output":"ok"},{"type":"message","role":"assistant","content":[{"type":"input_image","image_url":"` + img + `"}]}`,
	} {
		rec := codexPostTo(s, `{"model":"google-vertex/gemini-3.8-flash","instructions":"You are Codex.","stream":true,"tools":[{"type":"function","name":"view_image","parameters":{"type":"object","properties":{}}}],"input":[`+hist+tail+`]}`)
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "model turn") {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
			continue
		}
		b, _ := json.Marshal(v.last(t).body["contents"])
		if !strings.Contains(string(b), "iVBORw0KGgo=") {
			t.Errorf("%s: the image was lost: %s", name, b)
		}
		if role, _ := lastTurn(t, v.last(t).body["contents"]); role != "user" {
			t.Errorf("%s: ends with %s: %s", name, role, b)
		}
	}
}
