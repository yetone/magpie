package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// upPartsBody is a Chat Completions request with every part: a system
// prompt, a tool's definition, an inline image, and a conversation whose
// one tool call read two files, its result shared between them.
func upPartsBody(model string) (body string, system, tool, image, result int) {
	sys := strings.Repeat("You are a careful agent. ", 200)
	def := `{"type":"function","function":{"name":"exec_command","description":"` + strings.Repeat("Runs a command. ", 100) + `","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}}}`
	img := "data:image/png;base64," + strings.Repeat("iVBORw0KGgo", 300)
	out := strings.Repeat("package main // the files' text\n", 300)
	q, _ := json.Marshal(out)
	body = `{"model":"` + model + `","messages":[` +
		`{"role":"system","content":"` + sys + `"},` +
		`{"role":"user","content":[{"type":"text","text":"read these"},{"type":"image_url","image_url":{"url":"` + img + `"}}]},` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"exec_command","arguments":"{\"cmd\":\"cat a.go b.go\"}"}}]},` +
		`{"role":"tool","tool_call_id":"c1","content":` + string(q) + `}],` +
		`"tools":[` + def + `]}`
	return body, len(sys), len(def), len(img), len(out)
}

// A request's body is told apart by what its bytes were, as the prompt
// is read: the system prompt, the tools' definitions and the images each
// at least their own bytes, and nothing counted twice — a tool result
// shared among the files it read counts once, so the parts never sum past
// the body (theforeveriris on X: what are 8.6 GB up made of?).
func TestPromptKnowsWhatItsBytesWere(t *testing.T) {
	body, system, tool, image, result := upPartsBody("m")
	p := promptOf(provider.Chat, []byte(body))
	if p == nil {
		t.Fatal("the body wasn't read")
	}
	read := 0
	for _, it := range partOf(p, PartFiles).Items {
		if it.Name == "a.go" || it.Name == "b.go" {
			read++
		}
	}
	if read != 2 {
		t.Fatalf("the cat's two files weren't read as files: %+v", p.Parts)
	}
	s := p.sent
	if s.system < system || s.tools < tool || s.images < image {
		t.Fatalf("parts %+v; the body holds system %d, tools %d, an image %d", s, system, tool, image)
	}
	if s.all > len(body) {
		t.Fatalf("the parts read %d bytes of a %d-byte body: something counted twice", s.all, len(body))
	}
	if rest := s.all - s.system - s.tools - s.images; rest < result {
		t.Fatalf("the conversation is %d bytes; its tool result alone is %d", rest, result)
	}
}

// What the parts are on a row is what went on the wire, each part scaled
// by it; none where the bytes up aren't the body's.
func TestUpPartsScaleToTheWire(t *testing.T) {
	p := &Prompt{sent: sentSizes{system: 100, tools: 200, images: 300, all: 1000}}
	got := upParts(p, &upBytes{wire: 500})
	if got == nil || *got != (usage.UpParts{System: 50, Tools: 100, Images: 150}) {
		t.Fatalf("got %+v", got)
	}
	for name, c := range map[string]struct {
		p *Prompt
		u *upBytes
	}{
		"not read":       {nil, &upBytes{wire: 500}},
		"nothing went":   {p, &upBytes{}},
		"a socket delta": {p, &upBytes{wire: 500, socket: true}},
		"images by file": {p, &upBytes{wire: 500, images: 2}},
		"empty body":     {&Prompt{}, &upBytes{wire: 500}},
	} {
		if got := upParts(c.p, c.u); got != nil {
			t.Errorf("%s: %+v, want none", name, got)
		}
	}
}

// A request's ledger row keeps what its bytes up were made of, the parts
// within what went up and each where the body has it.
func TestLedgerKeepsUpParts(t *testing.T) {
	fresh(t)
	got := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = len(b)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, chatOK)
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "a", Name: "A", Key: "k", Models: []string{"m"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	body, system, tool, image, _ := upPartsBody("a/m")
	s := New()
	if code, reply := postAs(t, s, "", body); code != 200 {
		t.Fatalf("%d %s", code, reply)
	}
	route := lastRoute(s).ID
	var row *usage.Record
	for deadline := time.Now().Add(10 * time.Second); row == nil && time.Now().Before(deadline); {
		for _, u := range usage.Load(time.Time{}) {
			if u.RouteID == route {
				row = &u
			}
		}
		if row == nil {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if row == nil {
		t.Fatal("no row for the request")
	}
	if row.UpBytes != got || row.UpParts == nil {
		t.Fatalf("UpBytes %d (the vendor got %d), parts %+v", row.UpBytes, got, row.UpParts)
	}
	u := row.UpParts
	if u.System+u.Tools+u.Images > row.UpBytes {
		t.Fatalf("parts %+v sum past %d bytes up", u, row.UpBytes)
	}
	// the body went on as it came, near enough: each part near its share
	near := func(part, want int) bool { return part >= want*9/10 && part <= want*13/10 }
	if !near(u.System, system) || !near(u.Tools, tool) || !near(u.Images, image) {
		t.Fatalf("parts %+v; the body holds system %d, tools %d, an image %d", u, system, tool, image)
	}
}

// Reading a large body's parts is a length added per piece the prompt
// already reads, nothing beside it: its cost is promptOf's.
func BenchmarkPromptOfLargeBody(b *testing.B) {
	var msgs []string
	for i := range 400 {
		msgs = append(msgs, `{"role":"user","content":"`+strings.Repeat("a line of the conversation ", 40)+`"}`)
		msgs = append(msgs, `{"role":"assistant","content":null,"tool_calls":[{"id":"c`+string(rune('a'+i%26))+`","type":"function","function":{"name":"exec_command","arguments":"{\"cmd\":\"cat a.go b.go\"}"}}]}`)
		msgs = append(msgs, `{"role":"tool","tool_call_id":"c`+string(rune('a'+i%26))+`","content":"`+strings.Repeat("x", 1500)+`"}`)
	}
	body := []byte(`{"model":"m","messages":[{"role":"system","content":"` + strings.Repeat("s", 40000) + `"},` + strings.Join(msgs, ",") + `]}`)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		if promptOf(provider.Chat, body) == nil {
			b.Fatal("not read")
		}
	}
}
