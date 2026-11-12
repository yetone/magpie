package catalog

import "testing"

// maohy915 on X: a model's name that says its window ("Qwen-3.8-27b-128k")
// is the window when nothing else says one. Only a last part of the id that
// is a context size alone counts; a model's size, version, date or quant
// is not read as one.
func TestNameWindow(t *testing.T) {
	for id, want := range map[string]int{
		"Qwen-3.8-27b-128k":       128_000,
		"qwen-3.8-27b-128K":       128_000,
		"llama3-8b-32k":           32_000,
		"gpt-4-32k":               32_000,
		"my_model_200k":           200_000,
		"model-1m":                1_000_000,
		"llama-4-scout-10m":       10_000_000,
		"qwen3 256k":              256_000,
		"local/qwen3-27b-128k":    128_000,
		"qwen:27b-128k":           128_000,
		"qwen3-128k:latest":       128_000,
		"qwen3-128k:8b":           128_000,
		"gpt-4o-2024":             0,
		"gpt-4o-2024-08-06":       0,
		"qwen-3.8-27b":            0,
		"pythia-14m":              0,
		"smollm-135m":             0,
		"gpt2-124m":               0,
		"llama3-q4_k_m":           0,
		"llama3:8b-instruct-q4_0": 0,
		"128k":                    0,
		"-128k":                   0,
		"model-128k-instruct":     0,
		"model-0k":                0,
		"model-1k":                0,
		"model-4096k":             0,
		"model-3m":                0,
		"model-012k":              0,
		"model-1.5m":              0,
		"model-k":                 0,
		"model-":                  0,
		"qwen3:8b-instruct":       0,
		"":                        0,
	} {
		if got := NameWindow(id); got != want {
			t.Errorf("%q: %d, want %d", id, got, want)
		}
	}
}
