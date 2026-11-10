package guard

import (
	"encoding/json"
	"os"
	"testing"
)

// The embedder reads each text into the pieces Hugging Face's tokenizers
// does, and to the embedding ONNX Runtime makes of the same int8 model to
// a cosine of 0.995, as near the float32 model's as ONNX Runtime's is
// (testdata/embedder.json, from all three). Two int8 runs part a little: each
// matmul's input is rounded to 256 steps, so a float rounding apart moves
// a step now and then; ONNX Runtime's own graph optimizations move its
// result 0.9965, and its int8 is 0.988 to 0.993 from float32.
func TestEmbedderMatchesONNXRuntime(t *testing.T) {
	d := os.Getenv("MAGPIE_EMBED_DIR")
	if d == "" {
		t.Skip("MAGPIE_EMBED_DIR is not set")
	}
	e, err := LoadEmbedder(d)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("testdata/embedder.json")
	if err != nil {
		t.Fatal(err)
	}
	var want []struct {
		Text      string
		IDs       []int
		Vec, FP32 []float32
	}
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatal(err)
	}
	for _, w := range want {
		var ids []int
		for _, tk := range e.tok.encode(w.Text) {
			ids = append(ids, tk.id)
		}
		if got := append(append([]int{0}, ids...), 2); !equalInts(got, w.IDs) {
			t.Errorf("%q: pieces %v, want %v", w.Text, got, w.IDs)
			continue
		}
		v := e.Embed(w.Text)
		ort, fp, ortFP := cosine(v, w.Vec), cosine(v, w.FP32), cosine(w.Vec, w.FP32)
		t.Logf("%q: %.5f with ONNX Runtime's, %.5f with float32's (ONNX Runtime's %.5f)", w.Text, ort, fp, ortFP)
		if ort < 0.995 || fp < ortFP-0.002 {
			t.Errorf("%q: cosine %.5f with ONNX Runtime's, %.5f with float32's, where ONNX Runtime's is %.5f", w.Text, ort, fp, ortFP)
		}
	}
}

func cosine(a, b []float32) float64 {
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return dot
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
