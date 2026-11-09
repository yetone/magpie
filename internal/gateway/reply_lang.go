package gateway

import (
	"strings"
	"unicode"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// A text the gateway answers with itself, as the tool loop's notice, is
// read by the person at the agent, in the conversation's own language
// (tenacious_eagle_96548 on Discord: a Chinese conversation got an English
// notice). conversationLang tells the language from what the user wrote,
// by its script: kana is Japanese, Hangul Korean, Han Chinese (Traditional
// when its common characters are), and Latin German when its common words
// are, else English. Those are the GUI's languages, and Korean.

// Languages a gateway text is written in.
const (
	langEN   = "en"
	langZH   = "zh"
	langZHTW = "zh-TW"
	langJA   = "ja"
	langKO   = "ko"
	langDE   = "de"
)

// langSample is how much of the user's newest words are read: enough for
// a few asks, little next to a request that carries a whole conversation.
const langSample = 4000

// conversationLang is the language the user writes body's conversation
// in, from the user's own words, newest first: not the agent's prompt, a
// tool's output (a file read, a log) or what the agent adds to a user turn
// in a tag of its own (<system-reminder>, <environment_context>).
func conversationLang(from provider.Protocol, body []byte) string {
	return langOf(langWords(from, body))
}

// langWords is up to langSample runes of the user's words in body, the
// newest first.
func langWords(from provider.Protocol, body []byte) string {
	list := getIn(body, conversationKey[from])
	if list.Type == gjson.String { // Responses' input as one string
		return keptWords(list.String())
	}
	if !list.IsArray() {
		return ""
	}
	items := list.Array()
	var b strings.Builder
	n := 0
	add := func(s string) {
		s = keptWords(s)
		if s == "" || n >= langSample {
			return
		}
		if r := []rune(s); n+len(r) > langSample {
			s = string(r[:langSample-n])
		}
		n += len([]rune(s))
		b.WriteString(s)
		b.WriteByte('\n')
	}
	for i := len(items) - 1; i >= 0 && n < langSample; i-- {
		it := items[i]
		if it.Get("role").String() != "user" {
			continue
		}
		parts := it.Get("content")
		if from == provider.Gemini {
			parts = it.Get("parts")
		}
		if parts.Type == gjson.String {
			add(parts.String())
			continue
		}
		parts.ForEach(func(_, p gjson.Result) bool {
			switch p.Get("type").String() {
			case "", "text", "input_text":
				add(p.Get("text").String())
			}
			return n < langSample
		})
	}
	return b.String()
}

// keptWords is s unless it is all one tagged block an agent adds to a user
// turn (<system-reminder>…</system-reminder>), which isn't the user's.
func keptWords(s string) string {
	t := strings.TrimSpace(s)
	if strings.HasPrefix(t, "<") && strings.HasSuffix(t, ">") {
		return ""
	}
	return t
}

// Characters only one of Chinese's two scripts writes, among its most
// common: 这 and 這 are the same word.
const (
	simplifiedOnly  = "这个们说来为会时对过还后么样没问题请帮发现开关应该实际导吗"
	traditionalOnly = "這個們說來為會時對過還後麼樣沒問題請幫發現開關應該實際導嗎"
)

// germanWords and englishWords are each language's most common words, for
// Latin text to be told apart by.
var (
	germanWords  = wordSet("der die das und nicht ist ich du wir mit ein eine einen zu auf für bitte kannst warum wie noch auch es den dem sich aber oder wenn dass")
	englishWords = wordSet("the and is not i you we with a an to on for please can why how still also it this that of or if but what")
)

func wordSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(s) {
		m[w] = true
	}
	return m
}

// langOf is the language s is written in, by its script.
func langOf(s string) string {
	var han, kana, hangul, simp, trad int
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Hiragana, r), unicode.Is(unicode.Katakana, r) && r != 'ー' && r != '・':
			kana++
		case unicode.Is(unicode.Hangul, r):
			hangul++
		case unicode.Is(unicode.Han, r):
			han++
			if strings.ContainsRune(simplifiedOnly, r) {
				simp++
			} else if strings.ContainsRune(traditionalOnly, r) {
				trad++
			}
		}
	}
	// a word or two of another script (a name, a quoted error) doesn't
	// make the conversation that language
	switch {
	case kana >= 2:
		return langJA
	case hangul >= 2:
		return langKO
	case han >= 2:
		if trad > simp {
			return langZHTW
		}
		return langZH
	}
	de, en := 0, 0
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) }) {
		if germanWords[w] {
			de++
		}
		if englishWords[w] {
			en++
		}
		if strings.ContainsAny(w, "äöüß") {
			de++
		}
	}
	if de >= 2 && de > en {
		return langDE
	}
	return langEN
}
