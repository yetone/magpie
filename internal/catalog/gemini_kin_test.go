package catalog

import "testing"

// stockAAAAA on X: gemini-4-carbon read no window while gemini-4-argon had
// 1M, since models.dev lists carbon a day after Google's lists name it.
func TestNewGeminiTakesItsGenerationsWindow(t *testing.T) {
	writeCatalog(t, `{"google":{"id":"google","models":{
		"gemini-4-argon":{"id":"gemini-4-argon","limit":{"context":1048576,"output":65536}},
		"gemini-2.5-pro":{"id":"gemini-2.5-pro","limit":{"context":1048576,"output":65536}},
		"gemini-2.5-flash-lite":{"id":"gemini-2.5-flash-lite","limit":{"context":1000000,"output":65536}}}}}`)
	for id, want := range map[string]int{
		"gemini-4-carbon":      1048576,
		"models/gemini-4-nova": 1048576,
		"gemini-4-argon":       1048576,
		"gemini-2.5-new":       0, // a point release is not guessed
		"gemini-3-zinc":        0, // no Gemini 3 listed
		"gemini-4":             0,
		"claude-4-carbon":      0,
	} {
		if got := ContextOf(id); got != want {
			t.Errorf("ContextOf(%q) = %d, want %d", id, got, want)
		}
	}
}
