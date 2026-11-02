package redact

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A user name's made-up name comes back as the user's in any case the
// model writes it. Its login name was the user's own (yaobin), and a model
// took the made-up one for the user's name and wrote it as a name is
// written, capitalised; it came back to the user as letters that are no
// one's, with no braces (yapechan on Discord).
func TestUserNameStandInPutBackInAnyCase(t *testing.T) {
	Forget()
	t.Cleanup(Forget)
	o := Options{Personal: true, Kinds: map[string]bool{"home": true}}
	out, n := Mask("cd /Users/yaobin/workspace/report && git log", o)
	f := standInOf("yaobin")
	if n != 1 || !strings.Contains(out, "/Users/"+f+"/") {
		t.Fatalf("sent %q, want the user name as %q", out, f)
	}
	title := strings.ToUpper(f[:1]) + f[1:]
	upper := strings.ToUpper(f)
	for _, c := range []struct{ s, want string }{
		{"作者：" + title + "，路径 /Users/" + f + "/workspace", "作者：yaobin，路径 /Users/yaobin/workspace"},
		{"Hi " + title + ", the report is done.", "Hi yaobin, the report is done."},
		{"OWNER=" + upper, "OWNER=yaobin"},
		{"/Users/" + title + "/x", "/Users/yaobin/x"},
	} {
		if got := Restore(c.s, false); got != c.want {
			t.Errorf("Restore(%q) = %q, want %q", c.s, got, c.want)
		}
	}
	if !hasShapes([]byte(`{"text":"` + title + `"}`)) {
		t.Errorf("a reply with %q is not looked at", title)
	}
	if got := string(RestoreJSON([]byte(`{"text":"致 ` + title + `"}`))); got != `{"text":"致 yaobin"}` {
		t.Errorf("RestoreJSON = %s", got)
	}

	// streamed, cut at every place in the capitalised name
	whole := "报告作者：" + title + "。"
	for cut := strings.Index(whole, title); cut <= strings.Index(whole, title)+len(title); cut++ {
		ev := func(text string) string {
			b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": text}}}})
			return "data: " + string(b) + "\n\n"
		}
		rec := httptest.NewRecorder()
		rec.Header().Set("Content-Type", "text/event-stream")
		w := NewWriter(rec)
		w.WriteHeader(200)
		for _, e := range []string{ev(whole[:cut]), ev(whole[cut:]), "data: [DONE]\n\n"} {
			w.Write([]byte(e))
		}
		w.Finish()
		var got strings.Builder
		for _, e := range strings.Split(rec.Body.String(), "\n\n") {
			d, ok := strings.CutPrefix(e, "data: ")
			if !ok || d == "[DONE]" {
				continue
			}
			var x struct {
				Choices []struct{ Delta struct{ Content string } }
			}
			if json.Unmarshal([]byte(d), &x) == nil && len(x.Choices) > 0 {
				got.WriteString(x.Choices[0].Delta.Content)
			}
		}
		if got.String() != "报告作者：yaobin。" {
			t.Errorf("cut at %d: streamed %q", cut, got.String())
		}
	}
}

// An address's stand-in is a host's name, the same in any case, and comes
// back so; still only where it is a whole name.
func TestIPStandInPutBackInAnyCase(t *testing.T) {
	Forget()
	t.Cleanup(Forget)
	const ip = "47.96.123.45"
	out, _ := Mask("服务器 "+ip+" 连不上", Options{Personal: true, Kinds: map[string]bool{"ip": true}})
	f := strings.Fields(out)[1]
	if !isIPStandIn(f) {
		t.Fatalf("sent %q", out)
	}
	up := strings.ToUpper(f)
	for _, c := range []struct{ s, want string }{
		{"ssh root@" + up, "ssh root@" + ip},
		{"Host " + strings.ToUpper(f[:1]) + f[1:] + ":22", "Host " + ip + ":22"},
		{"my-" + up, "my-" + up},
		{up + ".lan", up + ".lan"},
	} {
		if got := Restore(c.s, false); got != c.want {
			t.Errorf("Restore(%q) = %q, want %q", c.s, got, c.want)
		}
	}
}

// A key's made-up form keeps its case: one that differs from it in case
// is another key, and goes back as written.
func TestShapedKeyPutBackOnlyAsWritten(t *testing.T) {
	Forget()
	t.Cleanup(Forget)
	const key = "sk_w3axCByTQgK1Px9bLmQr7Zt"
	out, _ := Mask("OPENAI_API_KEY="+key, Options{Secrets: true, Shape: true})
	f := strings.TrimPrefix(out, "OPENAI_API_KEY=")
	if f == key || strings.Contains(f, "{{") {
		t.Fatalf("sent %q", out)
	}
	if got := Restore(f, false); got != key {
		t.Errorf("Restore(%q) = %q", f, got)
	}
	if got := Restore(strings.ToLower(f), false); got != strings.ToLower(f) {
		t.Errorf("a key differing in case put back as %q", got)
	}
}

// What magpie remembers of placeholders is in memory, but a placeholder is
// made from its value and a key kept on disk (SetKeyPath), so after a
// restart (an update in the background) the same value goes as the same
// placeholder, and the conversation the agent sends again has its values
// put back in the reply (yapechan on Discord asked). Names from the
// reporter: 姚斌 as one of the user's own words.
func TestPlaceholdersOutliveARestart(t *testing.T) {
	Forget()
	mu.Lock()
	oldKey, oldPath := keyOf, keyPath
	keyOf, keyPath = nil, filepath.Join(t.TempDir(), "redact.key")
	mu.Unlock()
	t.Cleanup(func() {
		Forget()
		mu.Lock()
		keyOf, keyPath = oldKey, oldPath
		mu.Unlock()
	})
	path := keyPath
	o := Options{Words: []string{"姚斌"}}
	history := "请把周报发给姚斌，抄送姚斌的主管。"
	before, n := Mask(history, o)
	if n != 2 || strings.Contains(before, "姚斌") {
		t.Fatalf("sent %q", before)
	}
	p := placeholderRe.FindString(before)
	reply := "好的，已发给 " + p + "，并抄送 " + p + " 的主管。"

	// magpie starts again: nothing in memory, the key read from disk
	Forget()
	mu.Lock()
	keyOf = nil
	mu.Unlock()
	SetKeyPath(path)
	if b, err := os.ReadFile(path); err != nil || len(b) < 32 {
		t.Fatalf("key not kept: %v", err)
	}
	after, _ := Mask(history, o)
	if after != before {
		t.Errorf("after a restart the history went as %q, before as %q", after, before)
	}
	want := "好的，已发给 姚斌，并抄送 姚斌 的主管。"
	if got := Restore(reply, false); got != want {
		t.Errorf("the reply put back as %q, want %q", got, want)
	}
	// as a model writes it back: without its braces, in another case
	bare := strings.Trim(p, "{}")
	for _, s := range []string{bare, strings.ToLower(bare), "{{ " + bare + " }}", "{" + bare + "}"} {
		if got := Restore("发给 "+s+"。", false); got != "发给 姚斌。" {
			t.Errorf("Restore(%q) = %q", s, got)
		}
	}
	// nothing puts back a name that sounds like it: 姚兵 is the vendor's
	// own text, and goes to the user as the vendor wrote it
	if got := Restore("发给姚兵。", false); got != "发给姚兵。" {
		t.Errorf("Restore(姚兵) = %q", got)
	}
}
