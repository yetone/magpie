package gui

import (
	"encoding/json"
	"regexp"
	"strings"
	"sync"
)

// pageWords are the page's own translations (assets/i18n.js's I18N), by
// language, read once: the words a usage card's window is named and
// counted in ("Credits", "credits", "5 hours") come from the vendor or a
// plugin in English, and the Usage page says them with t(). The tray's
// tooltip says them with the same table (pageWord), so a window reads in
// the tray as on its card, in every language the page has (yi_time on
// Discord: "Credits 0 / 500 credits · 已用 0%").
var pageWords = sync.OnceValue(func() map[string]map[string]string {
	b, err := assets.ReadFile("assets/i18n.js")
	if err != nil {
		return nil
	}
	return parsePageWords(string(b))
})

var (
	pageLangHead = regexp.MustCompile(`^  "?([\w-]+)"?: \{`)
	pagePair     = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"\s*:\s*"((?:[^"\\]|\\.)*)"`)
)

// parsePageWords reads I18N's language blocks from i18n.js's source: each
// `  zh: {` or `  "zh-TW": {` line starts one, and its entries are
// `    "English": "translation",` lines, one or more to a line. A string
// JSON can't read (a JS-only escape) is left out: that word reads in
// English, as the page's own fallback does.
func parsePageWords(src string) map[string]map[string]string {
	if i := strings.Index(src, "\n};"); i >= 0 {
		src = src[:i]
	}
	out := map[string]map[string]string{}
	var cur map[string]string
	for _, line := range strings.Split(src, "\n") {
		if m := pageLangHead.FindStringSubmatch(line); m != nil {
			cur = map[string]string{}
			out[m[1]] = cur
			continue
		}
		if cur == nil || !strings.HasPrefix(line, "    \"") {
			continue
		}
		for _, m := range pagePair.FindAllStringSubmatch(line, -1) {
			var k, v string
			if json.Unmarshal([]byte(`"`+m[1]+`"`), &k) != nil || json.Unmarshal([]byte(`"`+m[2]+`"`), &v) != nil {
				continue
			}
			cur[k] = v
		}
	}
	return out
}

// pageWord is s as the page says it in lang (its t()): s itself in
// English, or where the page has no word for it.
func pageWord(lang, s string) string {
	if v, ok := pageWords()[lang][s]; ok && v != "" {
		return v
	}
	return s
}

// trayWindowName is a window's name as its card says it in lang: the
// name whole when the page has it, else each " · " part the page has
// ("Gemini · 5 hours", a pool's window named with its pool by trayShared,
// a model's with its family by trayModels), as the card writes
// pool + " · " + t(name).
func trayWindowName(lang, name string) string {
	if lang == "en" || name == "" {
		return name
	}
	if w := pageWord(lang, name); w != name {
		return w
	}
	parts := strings.Split(name, " · ")
	for i, p := range parts {
		parts[i] = pageWord(lang, p)
	}
	return strings.Join(parts, " · ")
}
