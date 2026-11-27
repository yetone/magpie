package redact

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// Charles Cao's key on X, sent as a made-up key of its form.
const shapeKey = "sk_w3axCByTQgK1PxqPhfKTrJOLBqz6sbZv"

// sameForm says f has v's head, length and separators, and the same kind
// of character in each other place.
func sameForm(f, v string) bool {
	if len(f) != len(v) || keyHead(f) != keyHead(v) {
		return false
	}
	class := func(c byte) byte {
		switch {
		case c >= 'A' && c <= 'Z':
			return 'A'
		case c >= 'a' && c <= 'z':
			return 'a'
		case c >= '0' && c <= '9':
			return '0'
		}
		return c
	}
	for i := len(keyHead(v)); i < len(v); i++ {
		if class(f[i]) != class(v[i]) {
			return false
		}
	}
	return true
}

func shapeOut(t *testing.T, s string, o Options) string {
	t.Helper()
	out, n := Mask(s, o)
	if n == 0 || strings.Contains(out, shapeKey) {
		t.Fatalf("not masked: %q", out)
	}
	return out
}

func TestShapeKeepsForm(t *testing.T) {
	o := Options{Secrets: true, Shape: true}
	out := shapeOut(t, "OPENAI_API_KEY="+shapeKey, o)
	f := strings.TrimPrefix(out, "OPENAI_API_KEY=")
	if strings.Contains(f, "{{") || !sameForm(f, shapeKey) {
		t.Fatalf("made-up key %q isn't of %q's form", f, shapeKey)
	}
	// the same made-up key every turn, as a vendor's cache needs
	if again, _ := Mask("key: "+shapeKey, o); again != "key: "+f {
		t.Fatalf("again %q, want %q", again, "key: "+f)
	}
	if got := Restore("use "+f+" here", false); got != "use "+shapeKey+" here" {
		t.Fatalf("restored %q", got)
	}
	// a hex key stays hex of its case
	for _, v := range []string{"9f2c4e1a7b3d5c8e9f2c4e1a7b3d5c8e", "ghp_Ab12Cd34Ef56Gh78Ij90Kl12Mn34Op56Qr78", "sk-proj-Xy12_ab-CD34ef56GH78ij90KL12mn34", "SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"} {
		f, ok := shapeOf("API_KEY", v)
		hex := strings.Trim(v, "0123456789abcdef") == ""
		if !ok || f == v || hex && (len(f) != len(v) || strings.Trim(f, "0123456789abcdef") != "") || !hex && !sameForm(f, v) {
			t.Fatalf("%q as %q", v, f)
		}
	}
	// a word the model took for a secret, with no key's head and no digit,
	// goes as a placeholder: as a made-up word (Illuminate as Zedxkjcbko)
	// the agent read it as a name, not as something masked (#7973)
	for _, v := range []string{"Illuminate", "PurpleMonkey", "correct-horse-battery"} {
		if f, ok := shapeOf("SECRET", v); ok {
			t.Errorf("%q is a word, yet it went as %q", v, f)
		}
		Forget()
		out, _ := Mask("password: "+v, Options{Secrets: true, Shape: true, Model: (&fakeModel{values: map[string]string{v: "secret"}}).find})
		if strings.Contains(out, v) || !strings.Contains(out, "{{") {
			t.Errorf("%q went as %q", v, out)
		}
	}
}

// Off, the default, a key goes as a placeholder still; personal data and
// short or odd values do with it on.
func TestShapeOnlyForKeys(t *testing.T) {
	out := shapeOut(t, "OPENAI_API_KEY="+shapeKey, Options{Secrets: true})
	if !strings.Contains(out, "{{") {
		t.Fatalf("off: %q", out)
	}
	for _, c := range []struct{ kind, v string }{
		{"EMAIL", "rina.kowalczyk@kowalczyk.pl"},
		{"PHONE", "13800138000123456"},
		{"PASSWORD", "short1"},
		{"SECRET", "sk-ab-cd"},  // 8, but 4 made up
		{"SECRET", "ghp_abcde"}, // 9, but 5 made up
		{"PASSWORD", "has space in it 12345"},
		{"PASSWORD", `quote"inside1234567`},
	} {
		if f, ok := shapeOf(c.kind, c.v); ok {
			t.Errorf("%s %q as %q", c.kind, c.v, f)
		}
	}
	out, _ = Mask("write to rina.k@kowalczyk.pl", Options{Personal: true, Shape: true})
	if !strings.Contains(out, "{{EMAIL_") {
		t.Fatalf("email: %q", out)
	}
}

// In a request body and back from the vendor's: a tool call's arguments,
// escaped, and a whole reply.
func TestShapeJSON(t *testing.T) {
	o := Options{Secrets: true, Shape: true}
	body := `{"messages":[{"role":"user","content":"OPENAI_API_KEY=` + shapeKey + `"}]}`
	out, n := MaskJSON([]byte(body), o)
	if n == 0 || strings.Contains(string(out), shapeKey) || strings.Contains(string(out), "{{") {
		t.Fatalf("%s", out)
	}
	f := string(out[strings.Index(string(out), "sk_"):][:len(shapeKey)])
	args, _ := json.Marshal(`{"cmd":"export OPENAI_API_KEY=` + f + `"}`)
	reply := `{"content":[{"type":"tool_use","input":` + string(args) + `},{"type":"text","text":"set ` + f + `"}]}`
	got := string(RestoreJSON([]byte(reply)))
	if strings.Contains(got, f) || strings.Count(got, shapeKey) != 2 {
		t.Fatalf("%s", got)
	}
	// what a vendor sealed is masked again as it was sent
	signed := `{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"the key ` + shapeKey + `","signature":"Eqk1"}]}]}`
	out, _ = MaskJSON([]byte(signed), o)
	if !strings.Contains(string(out), "the key "+f) {
		t.Fatalf("signed: %s", out)
	}
	// a made-up key that came back unrestored isn't masked again
	if again, _ := Mask("OPENAI_API_KEY="+f, o); again != "OPENAI_API_KEY="+f {
		t.Fatalf("masked again: %q", again)
	}
}

// A stream that splits the made-up key, after one character too, reads as
// the key with the events as they came.
func TestShapeStream(t *testing.T) {
	o := Options{Secrets: true, Shape: true}
	out := shapeOut(t, "OPENAI_API_KEY="+shapeKey, o)
	f := strings.TrimPrefix(out, "OPENAI_API_KEY=")
	chat := func(s string) string {
		b, _ := json.Marshal(s)
		return `data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":` + string(b) + `}}]}` + "\n\n"
	}
	pieces := []string{"key s", f[1:9], f[9:20], f[20:], " ok; ", f[:1], f[1:], "", "again " + f[:len(f)-1], f[len(f)-1:], "."}
	var events []string
	for _, p := range pieces {
		events = append(events, chat(p))
	}
	events = append(events, "data: [DONE]\n\n")
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "text/event-stream")
	w := NewWriter(rec)
	w.WriteHeader(200)
	for _, e := range events {
		w.Write([]byte(e))
	}
	w.Finish()
	var text strings.Builder
	for _, ev := range strings.Split(rec.Body.String(), "\n\n") {
		d, ok := strings.CutPrefix(ev, "data: ")
		if !ok || d == "[DONE]" {
			continue
		}
		var e struct {
			Choices []struct{ Delta struct{ Content string } }
		}
		if err := json.Unmarshal([]byte(d), &e); err != nil {
			t.Fatalf("event %q: %v", d, err)
		}
		text.WriteString(e.Choices[0].Delta.Content)
	}
	// a made-up key ending as one starts, in some of them, isn't held at
	// its end once it has come whole
	mu.Lock()
	sk := "sk_Ab12Cd34Ef56Gh78s"
	values[sk], shapes[sk] = "sk_Zy98Xw76Vu54Ts32r", "SECRET"
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		delete(values, sk)
		delete(shapes, sk)
		mu.Unlock()
	})
	if n := shapeTail("got " + sk); n != 0 {
		t.Fatalf("held %d of a whole key", n)
	}
	if n := shapeTail("got " + sk + " and s"); n != 1 {
		t.Fatalf("held %d of the next one's start", n)
	}
	want := "key " + shapeKey + " ok; " + shapeKey + "again " + shapeKey + "."
	if text.String() != want {
		t.Fatalf("text:\n got %q\nwant %q\n%s", text.String(), want, rec.Body.String())
	}
	if strings.Count(rec.Body.String(), "\n\n") != len(events) {
		t.Fatalf("events changed:\n%s", rec.Body.String())
	}
}

// A short key keeps its form too, and the settings' Try it shows what a
// request would send: the made-up key, not a placeholder (the owner's
// sk_abcdefg showed {{SECRET_…}} with the setting on).
func TestShapeShortAndPreview(t *testing.T) {
	f, ok := shapeOf("SECRET", "sk_abcdefg")
	if !ok || len(f) != len("sk_abcdefg") || !strings.HasPrefix(f, "sk_") || f == "sk_abcdefg" {
		t.Fatalf("sk_abcdefg as %q %v", f, ok)
	}
	const key = "sk_prevw9ZqX1mT3vB7nK2pLr4"
	o := Options{Secrets: true, Shape: true}
	text := "OPENAI_API_KEY=" + key
	got := Preview(text, o)
	want, _ := shapeOf("SECRET", key)
	mu.RLock()
	_, kept := values[want]
	mu.RUnlock()
	if kept {
		t.Fatal("preview kept the made-up key for Restore")
	}
	if !strings.Contains(got, want) || strings.Contains(got, "{{") || strings.Contains(got, key) {
		t.Fatalf("preview with Shape: %q, want %q in it", got, want)
	}
	if got := Preview(text, Options{Secrets: true}); !strings.Contains(got, "{{") {
		t.Fatalf("preview without Shape: %q", got)
	}
}

// 密钥 OPENAI_API_KEY=sk_… is found with its name: the name stays, the
// key after it is made up, and the key alone is put back, as a reply that
// names only the key has it.
func TestShapeKeepsAName(t *testing.T) {
	const key = "sk_nmdv8QpX2rT6yW4uZ1oKs"
	got := shaped("SECRET", "OPENAI_API_KEY="+key)
	f := strings.TrimPrefix(got, "OPENAI_API_KEY=")
	if f == got || !strings.HasPrefix(f, "sk_") || len(f) != len(key) || f == key {
		t.Fatalf("%q", got)
	}
	if back := Restore("the key is "+f, false); back != "the key is "+key {
		t.Fatalf("%q", back)
	}
	if got := shaped("SECRET", "OPENAI_API_KEY=a b c d e f"); !strings.HasPrefix(got, "{{") {
		t.Fatalf("a value with spaces made up: %q", got)
	}
}
