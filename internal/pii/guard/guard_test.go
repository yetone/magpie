package guard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
)

func tag(s string) int {
	for i, l := range labels {
		if l == s {
			return i
		}
	}
	panic(s)
}

func TestDecodeBIO(t *testing.T) {
	// "Mail a@b.co or ring 138 0013 8000, Bob" in tokens.
	toks := []token{{0, 0, 4}, {0, 5, 6}, {0, 6, 7}, {0, 7, 11}, {0, 12, 14},
		{0, 15, 19}, {0, 19, 19}, {0, 20, 23}, {0, 24, 28}, {0, 29, 33}, {0, 35, 38}}
	lab := []int{tag("O"), tag("B-email"), tag("I-email"), tag("I-email"), tag("O"),
		tag("O"), tag("B-phone"), tag("I-phone"), tag("I-phone"), tag("I-phone"),
		tag("I-private_person")}
	prob := []float64{1, .99, .9, .95, 1, 1, .2, .97, .85, .9, .7}
	got := decode(toks, lab, prob)
	want := []Span{{5, 11, "email", .9}, {20, 33, "phone", .85}, {35, 38, "private_person", .7}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decode = %v, want %v", got, want)
	}
	// One under Floor is dropped; masking drops those under the
	// threshold set, MinScore or the user's, from what is left.
	prob[10] = Floor - .01
	if got := decode(toks, lab, prob); !reflect.DeepEqual(got, want[:2]) {
		t.Fatalf("under Floor: %v", got)
	}
	// A B tag right after a span of its kind starts another.
	lab[3] = tag("B-email")
	if got := decode(toks, lab, prob); len(got) != 3 || got[0].End != 7 || got[1].Start != 7 {
		t.Fatalf("B after a span: %v", got)
	}
}

// dir is the model's folder, given in MAGPIE_GUARD_DIR: the tests that
// need the model run only then, as no test downloads it.
func dir(t *testing.T) string {
	d := os.Getenv("MAGPIE_GUARD_DIR")
	if d == "" {
		t.Skip("MAGPIE_GUARD_DIR is not set")
	}
	return d
}

// The tokenizer gives what Hugging Face's tokenizers 0.22.2 gives for the
// model's tokenizer.json (testdata/tokens.json: ids, and offsets in
// characters).
func TestTokenizerMatchesHF(t *testing.T) {
	tk, err := loadTokenizer(filepath.Join(dir(t), "tokenizer.json"), vocabSize)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("testdata/tokens.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Text   string
		Tokens [][3]int
	}
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		var got [][3]int
		for _, k := range tk.encode(c.Text) {
			got = append(got, [3]int{k.id, utf8.RuneCountInString(c.Text[:k.start]), utf8.RuneCountInString(c.Text[:k.end])})
		}
		if len(got) == 0 && len(c.Tokens) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, c.Tokens) {
			t.Errorf("%q:\n got %v\nwant %v", c.Text, got, c.Tokens)
		}
	}
}

func TestModelFinds(t *testing.T) {
	m, err := Load(dir(t))
	if err != nil {
		t.Fatal(err)
	}
	// bert.go's own; TestCoreMLMatchesGo holds CoreML to it. CoreML gives
	// the module path .81, the publisher's fp32 model .785.
	m.ml = nil
	text := "联系人：张三，电话 13800138000，邮箱 zhangsan@example.com。\n" +
		"go 1.26\nrequire github.com/clipperhouse/uax29/v2 v2.2.0\n"
	// what masking keeps at the publisher's threshold: Find gives down
	// to Floor (the module path is a common_secret at .79)
	have := map[string]string{}
	for _, s := range m.Find(text) {
		if s.Score >= MinScore {
			have[text[s.Start:s.End]] = s.Label
		}
	}
	want := map[string]string{"张三": "private_person", "13800138000": "phone", "zhangsan@example.com": "email"}
	if !reflect.DeepEqual(have, want) {
		t.Fatalf("found %v, want %v", have, want)
	}
}

// The model reads text that isn't valid UTF-8, as an embeddings input
// with a stray byte came to it (#4553: "hello \xff" panicked in the
// tokenizer and ended magpie), and finds in the valid text around it
// what it finds without the byte, with every span inside the text.
func TestModelFindsAroundMalformedBytes(t *testing.T) {
	m, err := Load(dir(t))
	if err != nil {
		t.Fatal(err)
	}
	m.ml = nil
	for _, text := range []string{
		"hello \xff", "\xff", "\xc3", "\xed\xa0\x80", "\xf0\x9f",
		"联系人：张三\xff，电话 13800138000\xc3。",
	} {
		have := map[string]string{}
		phone := false
		for _, s := range m.Find(text) {
			if s.Start < 0 || s.End > len(text) || s.Start >= s.End {
				t.Fatalf("Find(%q): span %+v is out of the text", text, s)
			}
			if s.Score >= MinScore {
				have[text[s.Start:s.End]] = s.Label
				// a stray byte against the number is a character of its
				// word, so the span may take it too
				phone = phone || s.Label == "phone" && strings.Contains(text[s.Start:s.End], "13800138000")
			}
		}
		if strings.Contains(text, "13800138000") && !phone {
			t.Errorf("Find(%q) = %v, want the phone number", text, have)
		}
	}
}

// TestWindowsAtOnceReadAsOneByOne: a text of many windows, read with its
// windows at once, finds what it does read a window at a time.
func TestWindowsAtOnceReadAsOneByOne(t *testing.T) {
	m, err := Load(dir(t))
	if err != nil {
		t.Fatal(err)
	}
	m.ml = nil
	var b strings.Builder
	for i := range 200 {
		fmt.Fprintf(&b, "第%d位联系人：张三，电话 138%08d，邮箱 user%d@example.com，住在北京市海淀区中关村大街%d号。\n", i, i*7919, i, i)
	}
	text := b.String()
	if n := len(m.tok.encode(text)); n < 4*window {
		t.Fatalf("%d tokens, want several windows", n)
	}
	was := runtime.GOMAXPROCS(1)
	one := m.Find(text)
	runtime.GOMAXPROCS(max(4, was))
	at := m.Find(text)
	runtime.GOMAXPROCS(was)
	if len(one) == 0 {
		t.Fatal("found nothing")
	}
	if !reflect.DeepEqual(at, one) {
		t.Fatalf("windows at once found %d spans, one by one %d", len(at), len(one))
	}
}

// The model reads on half the cores at most, never more than 4: on all
// of them a long history unread kept every core busy (Cores).
func TestReadsOnFewCores(t *testing.T) {
	was := runtime.GOMAXPROCS(32)
	defer runtime.GOMAXPROCS(was)
	if n := Cores(); n != 4 {
		t.Fatalf("32 cores: reads on %d", n)
	}
	runtime.GOMAXPROCS(1)
	if n := Cores(); n != 1 {
		t.Fatalf("1 core: reads on %d", n)
	}
}
