package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/maskrec"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// checkedOf is agent's count in the summary's checked turns.
func checkedOf(agent string) maskrec.Looked {
	for _, c := range maskrec.Sum(1).Checked {
		if c.Name == agent {
			return c
		}
	}
	return maskrec.Looked{Name: agent}
}

// Pi's turns, on each API it asks magpie's models on (pi_protocol.go),
// are masked as any agent's are: one carrying a key is recorded under
// Pi, and one with nothing to mask is counted as checked, which the
// Masking page shows, so an agent with no records reads as "checked,
// nothing found" rather than as not masked (eiyuuzaki2333 on X: only
// dsh had records, Pi none). A turn to a model the user set to go
// unmasked is counted as sent as written.
func TestPiTurnsCountedAsChecked(t *testing.T) {
	const key = "sk-proj-abcdEFGH1234ijklMNOP5678qrst"
	var got []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, string(b))
		if strings.Contains(string(b), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"id\":\"c1\",\"model\":\"m1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"}}]}\n\n"+
				"data: {\"id\":\"c1\",\"model\":\"m1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
	}))
	defer up.Close()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "mine", Name: "Mine", Key: "k", Models: []string{"m1"}, Chat: up.URL + "/v1", Unredacted: true}); err != nil {
		t.Fatal(err)
	}
	if err := settings.Save(settings.Settings{Redact: true}); err != nil {
		t.Fatal(err)
	}
	s := New()
	send := func(path, body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		// as Pi sends it: magpie's token, its own name, session affinity
		req.Header.Set("Authorization", "Bearer "+Token)
		req.Header.Set("User-Agent", "pi/0.70.2")
		req.Header.Set("X-Session-Affinity", "pi-session-1")
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
	chat := func(model, text string) string {
		return `{"model":"` + model + `","stream":false,"messages":[{"role":"system","content":"You are pi."},{"role":"user","content":"` + text + `"}]}`
	}
	before := checkedOf("pi")
	send("/v1/chat/completions", chat("fake/m1", "hello there"))
	send("/v1/responses", `{"model":"fake/m1","stream":false,"input":[{"role":"user","content":[{"type":"input_text","text":"hello again"}]}]}`)
	send("/v1/messages", `{"model":"fake/m1","max_tokens":64,"messages":[{"role":"user","content":"and once more"}]}`)
	if c := checkedOf("pi"); c.Looked-before.Looked != 3 || c.Masked != before.Masked || c.Whole != before.Whole {
		t.Fatalf("three turns with nothing to mask: %+v, before %+v", c, before)
	}
	if rs, _ := maskrec.List(0, 5, maskrec.Filter{Agent: "pi"}); len(rs) != 0 {
		t.Fatalf("a record with nothing masked: %+v", rs)
	}

	send("/v1/chat/completions", chat("fake/m1", "my key is "+key))
	if strings.Contains(got[len(got)-1], key) {
		t.Fatalf("Pi's key went out: %s", got[len(got)-1])
	}
	if rs, _ := maskrec.List(0, 5, maskrec.Filter{Agent: "pi"}); len(rs) != 1 || rs[0].Provider != "fake" {
		t.Fatalf("Pi's masked turn: %+v", rs)
	}
	if c := checkedOf("pi"); c.Looked-before.Looked != 4 || c.Masked-before.Masked != 1 {
		t.Fatalf("a turn masked: %+v", c)
	}

	// to a model set to go unmasked: as written, and counted so
	send("/v1/chat/completions", chat("mine/m1", "my key is "+key))
	if !strings.Contains(got[len(got)-1], key) {
		t.Fatalf("an unmasked provider got it masked: %s", got[len(got)-1])
	}
	if c := checkedOf("pi"); c.Looked-before.Looked != 4 || c.Whole-before.Whole != 1 {
		t.Fatalf("a turn sent as written: %+v", c)
	}

	// masking off: nothing is counted, nothing is said to be checked
	if err := settings.Save(settings.Settings{}); err != nil {
		t.Fatal(err)
	}
	send("/v1/chat/completions", chat("fake/m1", "hello there"))
	if c := checkedOf("pi"); c.Looked-before.Looked != 4 || c.Whole-before.Whole != 1 {
		t.Fatalf("masking off, counted: %+v", c)
	}
}
