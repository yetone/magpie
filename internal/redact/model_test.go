package redact

import (
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// fakeModel finds each of values wherever a text has it, under its label,
// with a space before it as the model takes one in; asked are the texts it
// was asked about.
type fakeModel struct {
	values map[string]string // value → label
	asked  [][]string
}

func (f *fakeModel) find(texts []string) [][]Found {
	f.asked = append(f.asked, texts)
	out := make([][]Found, len(texts))
	for i, t := range texts {
		for v, label := range f.values {
			for at := 0; ; {
				j := strings.Index(t[at:], v)
				if j < 0 {
					break
				}
				a := at + j
				if a > 0 && t[a-1] == ' ' {
					a--
				}
				out[i] = append(out[i], Found{Start: a, End: at + j + len(v), Label: label})
				at += j + len(v)
			}
		}
	}
	return out
}

// What the model finds and no rule does is masked, under the switch its
// kind is under: a name or an address with personal data, a secret with
// the secrets.
func TestModelFindsWhatRulesDont(t *testing.T) {
	const name, addr, secret = "Rina Kowalczyk", "42 Larchmont Ave, Springfield", "correct horse battery staple"
	f := &fakeModel{values: map[string]string{name: "private_person", addr: "private_address", secret: "secret"}}
	s := "Ask " + name + " at " + addr + "; the vault phrase is " + secret + "."

	out, n := Mask(s, Options{Personal: true, Secrets: true, Model: f.find})
	if n != 3 || strings.Contains(out, name) || strings.Contains(out, addr) || strings.Contains(out, secret) {
		t.Fatalf("%d masked: %q", n, out)
	}
	if !strings.Contains(out, "Ask {{PERSON_") || !strings.Contains(out, " at {{ADDRESS_") || !strings.Contains(out, "is {{SECRET_") {
		t.Fatalf("kinds, or the space before each taken in: %q", out)
	}
	if back := Restore(out, false); back != s {
		t.Fatalf("round trip: %q", back)
	}

	if out, _ := Mask(s, Options{Secrets: true, Model: f.find}); !strings.Contains(out, name) || !strings.Contains(out, addr) || strings.Contains(out, secret) {
		t.Fatalf("personal data off: %q", out)
	}
	if out, _ := Mask(s, Options{Personal: true, Model: f.find}); strings.Contains(out, name) || !strings.Contains(out, secret) {
		t.Fatalf("secrets off: %q", out)
	}
	// nothing on, nothing asked
	f.asked = nil
	if out, _ := Mask(s, Options{Model: f.find}); out != s || f.asked != nil {
		t.Fatalf("all off: %q, asked %q", out, f.asked)
	}
}

// A name the model found, masked once, is a known value of personal data:
// with personal data off it goes as it is, though secrets are on.
func TestModelKnownValueIsPersonal(t *testing.T) {
	const name = "Rina Kowalczyk-Brandt"
	f := &fakeModel{values: map[string]string{name: "private_person"}}
	if out, _ := Mask("from "+name, Options{Personal: true, Model: f.find}); strings.Contains(out, name) {
		t.Fatalf("not masked: %q", out)
	}
	if out, _ := Mask("to "+name, Options{Secrets: true}); !strings.Contains(out, name) {
		t.Fatalf("a name masked as a secret with personal data off: %q", out)
	}
	if out, _ := Mask("to "+name, Options{Personal: true}); strings.Contains(out, name) {
		t.Fatalf("a known name not masked again: %q", out)
	}
}

// What the model often gets wrong is left alone: a vendor's or a tool's
// id taken for a secret or an account, a secret shorter than a rule's, a
// one-letter name, a label magpie doesn't mask (a date, a URL).
func TestModelSpansFiltered(t *testing.T) {
	keep := map[string]string{
		"req_011CSzQ8mXv3kBR2": "secret",
		"toolu_01AbCdEfGhIjKl": "account_number",
		"hunter2":              "secret",
		"J":                    "private_person",
		"2026-10-18":           "private_date",
		"https://example.org":  "private_url",
	}
	f := &fakeModel{values: keep}
	s := "see req_011CSzQ8mXv3kBR2 and toolu_01AbCdEfGhIjKl, pw hunter2, J said on 2026-10-18 at https://example.org"
	if out, n := Mask(s, Options{Personal: true, Secrets: true, Model: f.find}); out != s || n != 0 {
		t.Fatalf("%d masked: %q", n, out)
	}
	// a span outside the text, or splitting a character, is no span
	bad := func([]string) [][]Found {
		return [][]Found{{{Start: -1, End: 3, Label: "secret"}, {Start: 0, End: 99, Label: "secret"}, {Start: 1, End: 4, Label: "private_person"}}}
	}
	if out, _ := Mask("é名字abc", Options{Personal: true, Secrets: true, Model: bad}); out != "é名字abc" {
		t.Fatalf("bad spans: %q", out)
	}
}

// A span the model starts at the delimiter before a value is the value's:
// a phone number in JSON or a CSV row is masked, and the delimiter goes as
// written (#3591, from the evaluation's json-contact and tool-result-mixed).
func TestModelSpanLeavesDelimiters(t *testing.T) {
	f := &fakeModel{values: map[string]string{
		`":"+86 139 1234 5678`: "private_phone",
		`,+81 90-1234-5678`:    "private_phone",
		`,rina.k@fastmail.com`: "private_email",
		`"Wei Zhang"`:          "private_person",
	}}
	o := Options{Personal: true, Model: f.find}
	for _, c := range []struct{ in, want string }{
		{`{"name":"Wei Zhang","phone":"+86 139 1234 5678"}`, `^\{"name":"\{\{PERSON_[a-z2-7]{8}\}\}","phone":"\{\{PHONE_[a-z2-7]{8}\}\}"\}$`},
		{"Rina,rina.k@fastmail.com,+81 90-1234-5678\n", `^Rina,\{\{EMAIL_[a-z2-7]{8}\}\},\{\{PHONE_[a-z2-7]{8}\}\}\n$`},
	} {
		if out, _ := Mask(c.in, o); !regexp.MustCompile(c.want).MatchString(out) {
			t.Errorf("%q went as %q", c.in, out)
		}
	}
}

// A request's texts go to the model once, together, each once: never its
// ids, kinds or tools' definitions, which are never masked, nor what a
// vendor sealed with a signature.
func TestModelAskedOnceForRequest(t *testing.T) {
	const name = "Rina Kowalczyk"
	f := &fakeModel{values: map[string]string{name: "private_person"}}
	body := `{"model":"claude-x","tools":[{"name":"lookup","description":"find ` + name + `","input_schema":{"type":"object"}}],` +
		`"messages":[{"role":"user","content":"hi ` + name + `"},{"role":"assistant","content":[{"type":"thinking","thinking":"about ` + name + `","signature":"sig"},{"type":"text","text":"hi ` + name + `"}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01","content":"ok"}]}]}`
	out, n := MaskJSON([]byte(body), Options{Personal: true, Model: f.find})
	if len(f.asked) != 1 {
		t.Fatalf("asked %d times: %q", len(f.asked), f.asked)
	}
	got := slices.Clone(f.asked[0])
	slices.Sort(got)
	// "ok" is too short to hold anything, and the tool's description is
	// the rules' alone (TestModelNotAskedAboutDefinitions)
	if want := []string{"hi " + name}; !slices.Equal(got, want) {
		t.Fatalf("asked about %q, want %q", got, want)
	}
	// the description and the two messages; the thinking block goes back
	// as the vendor wrote it but for the value masked elsewhere
	if strings.Contains(string(out), "hi "+name) || strings.Contains(string(out), "find "+name) || n < 3 {
		t.Fatalf("%d masked: %s", n, out)
	}
}

// What an agent defines for the vendor (its tools' and schemas'
// descriptions) is never read by the model: it was most of a request's
// text, and the model reading it held a 4-core server's first Claude Code
// request past the gateway's 2 minutes (niuniu_8964 on X). On Claude Code
// 2.1.296's own request the model was asked about 67,532 bytes, 52,577 of
// them its tools' descriptions; on Codex 0.162.1's, its tools too. What
// the user and the agent say (the system prompt, the messages) still is.
func TestModelNotAskedAboutDefinitions(t *testing.T) {
	for _, p := range []string{"../gateway/testdata/workdir/claude-code-2.1.296.json", "testdata/codex-0.162.1-request.json"} {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		defs := definitions(v)
		var inDefs, said []string
		walk(raw, func(path, key, s string) string {
			if len(s) >= 3 && !kept[key] && !isID(key) {
				if under(path, defs) {
					inDefs = append(inDefs, s)
				} else {
					said = append(said, s)
				}
			}
			return s
		})
		if len(inDefs) == 0 || len(said) == 0 {
			t.Fatalf("%s: %d texts in definitions, %d said: not the request the test is about", p, len(inDefs), len(said))
		}
		var asked []string
		MaskJSON(raw, Options{Secrets: true, Personal: true, Model: func(ts []string) [][]Found {
			asked = append(asked, ts...)
			return make([][]Found, len(ts))
		}})
		n := 0
		for _, s := range asked {
			n += len(s)
		}
		for _, d := range inDefs {
			if slices.Contains(asked, d) && !slices.Contains(said, d) {
				t.Errorf("%s: the model was asked about a definition's %.80q", p, d)
			}
		}
		for _, s := range said {
			if len(s) > 200 && !slices.Contains(asked, s) {
				t.Errorf("%s: the model wasn't asked about %.80q", p, s)
			}
		}
		t.Logf("%s: %d texts, %d bytes asked", p, len(asked), n)
	}
}

// A definition is masked as it is with the model off: by the rules (an
// email, a key), and not by what the model would have found in it.
func TestDefinitionMaskedAsWithoutModel(t *testing.T) {
	const name, other = "Rina Kowalczyk", "Tomasz Brzezinski"
	f := &fakeModel{values: map[string]string{name: "private_person", other: "private_person"}}
	body := `{"model":"claude-x","tools":[{"name":"crm","description":"Ask ` + other + ` (tomasz.b@acme-corp.com) with key sk-ant-api03-` + strings.Repeat("Ab3", 30) + `",` +
		`"input_schema":{"type":"object","properties":{"who":{"type":"string","description":"a customer, e.g. ` + other + `, tel 13800138000"}}}}],` +
		`"messages":[{"role":"user","content":"call ` + name + ` today"}]}`
	without, _ := MaskJSON([]byte(body), Options{Secrets: true, Personal: true})
	with, _ := MaskJSON([]byte(body), Options{Secrets: true, Personal: true, Model: f.find})
	var a, b map[string]any
	if err := json.Unmarshal(without, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(with, &b); err != nil {
		t.Fatal(err)
	}
	ta, _ := json.Marshal(a["tools"])
	tb, _ := json.Marshal(b["tools"])
	if string(ta) != string(tb) {
		t.Fatalf("tools with the model:\n%s\nwithout:\n%s", tb, ta)
	}
	for _, v := range []string{"tomasz.b@acme-corp.com", "sk-ant-api03-", "13800138000"} {
		if strings.Contains(string(tb), v) {
			t.Errorf("%q went as written in a definition: %s", v, tb)
		}
	}
	if strings.Contains(string(with), name) {
		t.Errorf("the message's name went as written: %s", with)
	}
}

// A text sent before on a route goes as the model found it then, until the
// model is another build (ModelGeneration, a plugin's model whose files
// changed): that one is asked afresh.
func TestNewModelBuildFindsAfresh(t *testing.T) {
	// a test before may have left the name known, and a known value is
	// masked by the rules whatever the model finds
	Forget()
	t.Cleanup(Forget)
	const name = "Rina Kowalczyk"
	s := "Ask " + name + " about the release."
	blind := &fakeModel{values: map[string]string{}}
	sees := &fakeModel{values: map[string]string{name: "private_person"}}
	o := Options{Personal: true, Route: "vendor", ModelGeneration: "a"}

	o.Model = blind.find
	if out, _ := Mask(s, o); out != s {
		t.Fatalf("nothing found, yet masked: %q", out)
	}
	o.Model = sees.find
	if out, _ := Mask(s, o); out != s {
		t.Fatalf("the same build sent it again otherwise: %q", out)
	}
	o.ModelGeneration = "b"
	if out, _ := Mask(s, o); strings.Contains(out, name) {
		t.Fatalf("a new build's find went out: %q", out)
	}
}
