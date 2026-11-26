package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Cloudflare's clef-flash says a confidence of its own calibration, far
// below its probabilities (@VersunPan on X: 提醒我明天下午三点给 Dora 打电话
// came out light at 0.2866, the same model in MLX-Serve 0.68, its top
// choice right 15 of 15): the intent counts by its probabilities, on Jev's
// scale, so it matches its rule; a spread near even still matches none.
// The answers come in Workers AI's envelope, shaped as clef-flash's output
// schema has them (type, choice, probabilities, confidence).
func TestClefFlashSureByProbabilities(t *testing.T) {
	var mu sync.Mutex
	var spread map[string]any
	var conf float64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/client/v4/accounts/acc7/ai/run/@cf/cloudflare/clef-flash" {
			http.Error(w, `{"success":false,"errors":[{"message":"No route for that URI"}]}`, 400)
			return
		}
		var q map[string]any
		json.NewDecoder(r.Body).Decode(&q)
		mu.Lock()
		ps, c := spread, conf
		mu.Unlock()
		answers := map[string]any{}
		for id, x := range q["questions"].(map[string]any) {
			switch {
			case id == "levels":
				answers[id] = map[string]any{"type": "noul", "noul": 0.04}
			case id == "intent":
				answers[id] = map[string]any{"type": "choice", "choice": "light", "probabilities": ps, "confidence": c}
			case id == "level":
				answers[id] = map[string]any{"type": "choice", "choice": "light", "probabilities": map[string]float64{"light": 0.7, "heavy": 0.2, "text": 0.1}, "confidence": 0.3}
			case x.(map[string]any)["type"] == "score":
				answers[id] = map[string]any{"type": "score", "score": 0.3, "legend": map[string]any{"0": "low"},
					"probabilities": map[string]float64{"0": 0.75, "1": 0.2, "2": 0.05, "3": 0}, "confidence": 0.31}
			}
		}
		b, _ := json.Marshal(map[string]any{"model": "clef-flash", "answers": answers, "usage": map[string]int{"input_tokens": 212, "output_tokens": 0}})
		w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":` + string(b) + `}`))
	}))
	defer up.Close()
	s := jevAt(t, up.URL+"/client/v4/accounts/acc7/ai/run", "jv/@cf/cloudflare/clef-flash",
		provider.Rule{Use: "b/big", Intent: "light"}, provider.Rule{Use: "a/small", Intent: "heavy"}, provider.Rule{Use: "a/small", Intent: "text"})

	mu.Lock()
	spread, conf = map[string]any{"light": 0.68, "heavy": 0.14, "text": 0.1, noIntent: 0.08}, 0.2866
	mu.Unlock()
	out, r := postOK(t, s, "s1", chat("提醒我明天下午三点给 Dora 打电话", nil, 0, ""))
	if c := r.Rule.Classified; !strings.Contains(out, "from kb") || c.Intent != "light" || c.Sure < 0.57 || c.Sure > 0.58 {
		t.Fatalf("sure by its probabilities: %s %+v %+v", out, r.Rule, c)
	}

	mu.Lock()
	spread, conf = map[string]any{"light": 0.28, "heavy": 0.25, "text": 0.24, noIntent: 0.23}, 0.03
	mu.Unlock()
	out, r = postOK(t, s, "s2", chat("嗯", nil, 0, ""))
	if c := r.Rule.Classified; strings.Contains(out, "from kb") || c.Intent != "" || c.Sure > 0.05 {
		t.Fatalf("a spread near even: %s %+v %+v", out, r.Rule, c)
	}
}

// Levels go by the probabilities too: a clear new level is taken over the
// turn before's though its confidence field is low, and a torn one still
// keeps the turn before's.
func TestJevLevelSureByProbabilities(t *testing.T) {
	in := []string{"简单任务", "复杂任务"}
	clear := []byte(`{"model":"clef-flash","answers":{"level":{"type":"choice","choice":"简单任务","probabilities":{"简单任务":0.86,"复杂任务":0.14},"confidence":0.31}},"usage":{"input_tokens":180,"output_tokens":0}}`)
	if v, err := readJev(clear, in, true, before{Intent: "复杂任务"}, false); err != nil || v.Intent != "简单任务" || v.Sure < 0.71 || v.Sure > 0.73 {
		t.Fatalf("clear: %+v %v", v, err)
	}
	torn := []byte(`{"model":"clef-flash","answers":{"level":{"type":"choice","choice":"简单任务","probabilities":{"简单任务":0.54,"复杂任务":0.46},"confidence":0.02}},"usage":{"input_tokens":180,"output_tokens":0}}`)
	if v, _ := readJev(torn, in, true, before{Intent: "复杂任务"}, false); v.Intent != "复杂任务" {
		t.Fatalf("torn: %+v", v)
	}
	// an answer without probabilities is as sure as its confidence says,
	// as Jev's are
	if v, _ := readJev([]byte(`{"answers":{"intent":{"type":"choice","choice":"复杂任务","confidence":0.43,"probabilities":{}}}}`), in, false, before{}, false); v.Intent != "复杂任务" || v.Sure != 0.43 {
		t.Fatalf("no probabilities: %+v", v)
	}
}
