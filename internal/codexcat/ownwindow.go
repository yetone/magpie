package codexcat

import (
	"maps"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// bundledWindows is the context_window of each model in Codex's own
// bundled catalog (rust-v0.166.0, models-manager/models.json), by slug;
// TestBundledWindowsAreCodexOwn checks it against that file in testdata.
// Codex holds most of OpenAI's models to 272K, and GPT-6.1 Terra to 500K
// (@flower__daisy on X).
var bundledWindows = map[string]int{
	"gpt-6-astra":              272000,
	"gpt-6.1-sol":              272000,
	"gpt-6.1-terra":            500000,
	"gpt-6-sol":                272000,
	"gpt-6-luna":               272000,
	"gpt-5.6-sol":              272000,
	"gpt-5.6-terra":            272000,
	"gpt-5.6-luna":             272000,
	"gpt-daybreak-blue-latest": 272000,
	"gpt-daybreak-red-latest":  372000,
	"gpt-5.5":                  272000,
	"codex-auto-review":        272000,
}

// OwnWindow is the context window official Codex holds the OpenAI model
// that id serves to (mycpa/gpt-6.1-terra, a ChatGPT account's
// codex/gpt-6.1-terra, a found auto/gpt-6.1-terra), matched as Codex
// matches a slug (longestPrefix): the window the ChatGPT account Codex last
// asked with lists (models_cache.json, which is this Codex version's),
// else Codex's bundled catalog's. 0 for a routing group, a model of
// another vendor's, or one Codex has no entry for.
func OwnWindow(id string) int {
	if strings.HasPrefix(id, "group/") || !openAIModel(id) {
		return 0
	}
	slug := strings.ToLower(id[strings.LastIndex(id, "/")+1:])
	listed := map[string]int{}
	for _, m := range catalog.CodexShared() {
		if m.Context > 0 {
			listed[strings.ToLower(m.ID)] = m.Context
		}
	}
	if s := longestPrefix(slug, maps.Keys(listed)); s != "" {
		return listed[s]
	}
	if s := longestPrefix(slug, maps.Keys(bundledWindows)); s != "" {
		return bundledWindows[s]
	}
	return 0
}

// Working is where magpie has Codex compact a conversation on model id, of
// a window of c tokens, when neither the model nor its provider sets a
// threshold: Long conversations' (st.Working). Its default, the working
// window, is the 272K Codex holds OpenAI's models to, so an OpenAI model
// Codex holds to more (GPT-6.1 Terra's 500K) is held to that window, never
// above c. A size the user set (CompactAt) or Full window is as they set it.
func Working(st settings.Settings, id string, c int) int {
	w := st.Working(c)
	if st.FullContext || st.CompactAt > 0 {
		return w
	}
	if n := OwnWindow(id); n > w {
		return min(c, n)
	}
	return w
}

// unknownWorking is Working for a model whose window magpie doesn't know:
// Long conversations' threshold, which by default is the window Codex's
// own catalog holds an OpenAI model to when that is more.
func unknownWorking(st settings.Settings, id string) int {
	w := st.Compact()
	if w > 0 && st.CompactAt == 0 {
		w = max(w, OwnWindow(id))
	}
	return w
}
