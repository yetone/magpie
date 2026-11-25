package redact

import (
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"
)

// The reporter's placeholder (onkashiko on X): a file's name started with
// a value masked as {{SECRET_eidxomnu}}. Gemini 4 Neon, through
// Antigravity's agy CLI, wrote the file's link with the placeholder
// percent-encoded in its target; Paseo showed the link's text restored
// and its path as {{SECRET_eidxomnu}}_own.conf:343-392, a file not found.
// The name itself is pixelated in the report, so the value is made up.
const (
	linkPlaceholder = "{{SECRET_eidxomnu}}"
	linkValue       = "k9Xq2mZpL7"
	// a value a URL can't carry as written: a space, and CJK
	spacedPlaceholder = "{{SECRET_aaaabbbb}}"
	spacedValue       = "我的 conf"
)

func holdLinkValues(t *testing.T) {
	t.Helper()
	mu.Lock()
	values[linkPlaceholder] = linkValue
	values[spacedPlaceholder] = spacedValue
	kinds["SECRET"] = true
	mu.Unlock()
}

// The reply as the model wrote it: the link's text with the placeholder
// as given, its target percent-encoded.
const linkReply = "已同步修改 [{{SECRET_eidxomnu}}_own.conf](file:///Users/u/proxy/%7B%7BSECRET_eidxomnu%7D%7D_own.conf#L343-L392)、[love1017best_own.yaml](file:///Users/u/proxy/love1017best_own.yaml)"

const linkWant = "已同步修改 [k9Xq2mZpL7_own.conf](file:///Users/u/proxy/k9Xq2mZpL7_own.conf#L343-L392)、[love1017best_own.yaml](file:///Users/u/proxy/love1017best_own.yaml)"

// A placeholder a model writes back encoded, as a link's target, an HTML
// page or escaped Markdown has it, is the value again, encoded the same
// way; what isn't one of magpie's stays as it is.
func TestRestoreEncodedPlaceholder(t *testing.T) {
	holdLinkValues(t)
	body := linkPlaceholder[2 : len(linkPlaceholder)-2]
	for _, c := range []struct{ in, want string }{
		{linkReply, linkWant},
		{"/p/%7b%7b" + body + "%7d%7d_own.conf", "/p/" + linkValue + "_own.conf"},
		{"/p/%7B%7B%20" + body + "%20%7D%7D_own.conf", "/p/" + linkValue + "_own.conf"},
		{"/p/%7B" + body + "%7D_own.conf", "/p/" + linkValue + "_own.conf"},
		{"/p/%7B%7B" + strings.ToLower(body) + "%7D%7D.conf", "/p/" + linkValue + ".conf"},
		{"/p/" + body + "_own.conf", "/p/" + body + "_own.conf"}, // inside a longer word: not one
		{`\{\{` + body + `\}\}_own.conf`, linkValue + "_own.conf"},
		{"&#123;&#123;" + body + "&#125;&#125;_own.conf", linkValue + "_own.conf"},
		{"&lbrace;&lbrace;" + body + "&rbrace;&rbrace;_own.conf", linkValue + "_own.conf"},
		{"&#x7b;&#x7b;" + body + "&#x7d;&#x7d;_own.conf", linkValue + "_own.conf"},
		// the value goes in encoded as its placeholder came
		{"/p/%7B%7BSECRET_aaaabbbb%7D%7D_own.conf", "/p/%E6%88%91%E7%9A%84%20conf_own.conf"},
		{"&#123;&#123;SECRET_aaaabbbb&#125;&#125;", "我的 conf"},
		// not one of magpie's: another hash
		{"/p/%7B%7BSECRET_zzzzzzzz%7D%7D_own.conf", "/p/%7B%7BSECRET_zzzzzzzz%7D%7D_own.conf"},
		// a lone encoded brace on one side is the text's own
		{"a %7B" + body + " b", "a %7B" + linkValue + " b"},
		// percent signs that are no brace stay
		{"50%20off %7Bx%7D", "50%20off %7Bx%7D"},
	} {
		if got := Restore(c.in, false); got != c.want {
			t.Errorf("Restore(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
	// a tool call's arguments: JSON text, the target percent-encoded
	args := `{"file_path":"/Users/u/proxy/%7B%7BSECRET_eidxomnu%7D%7D_own.conf"}`
	if got, want := Restore(args, true), `{"file_path":"/Users/u/proxy/`+linkValue+`_own.conf"}`; got != want {
		t.Errorf("args: got %s, want %s", got, want)
	}
}

// The reporter's reply, as every protocol sends it whole and streams it
// split anywhere, reads with the value in the link's text and its target,
// and nothing of the placeholder left.
func TestEncodedPlaceholderOnEveryReply(t *testing.T) {
	holdLinkValues(t)
	for _, c := range []struct{ name, in string }{
		{"anthropic", `{"type":"message","content":[{"type":"text","text":` + j(linkReply) + `}]}`},
		{"chat", `{"choices":[{"message":{"content":` + j(linkReply) + `}}]}`},
		{"responses", `{"output":[{"type":"message","content":[{"type":"output_text","text":` + j(linkReply) + `,"annotations":[]}]}]}`},
		{"gemini", `{"candidates":[{"content":{"parts":[{"text":` + j(linkReply) + `}],"role":"model"}}]}`},
		{"anthropic tool", `{"type":"message","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/Users/u/proxy/%7B%7BSECRET_eidxomnu%7D%7D_own.conf"}}]}`},
	} {
		got := string(RestoreJSON([]byte(c.in)))
		if strings.Contains(got, "SECRET_") || !strings.Contains(got, linkValue+"_own.conf") {
			t.Errorf("%s: %s", c.name, got)
		}
	}

	args := j(map[string]any{"file_path": "/Users/u/proxy/%7B%7BSECRET_eidxomnu%7D%7D_own.conf#L343-L392"})
	wantArgs := j(map[string]any{"file_path": "/Users/u/proxy/" + linkValue + "_own.conf#L343-L392"})
	r := rand.New(rand.NewSource(1))
	for _, p := range protos(r) {
		in, out := linkReply, linkWant
		if strings.HasSuffix(p.name, "tool") {
			in, out = args, wantArgs
		}
		fails := 0
		check := func(how string, pieces []string) {
			if got := runStream(p, pieces); got != out {
				fails++
				if fails <= 3 {
					t.Errorf("%s %s:\n got %q\nwant %q", p.name, how, got, out)
				}
			}
		}
		for k := 1; k <= 7; k++ {
			check("runes of "+string(rune('0'+k)), runeSplits(in, k))
		}
		for i := 1; i < len(in); i++ {
			if utf8.RuneStart(in[i]) {
				check("split at "+in[:i], []string{in[:i], in[i:]})
			}
		}
	}
}
