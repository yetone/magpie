package gateway

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// lc on Discord: a model on this machine or the local network the user set
// to go unmasked (provider.Unredacted) gets the request as the agent wrote
// it, with Settings' redaction on; set so, a chat, a token count and an
// embedding go to it whole. Unset, or able to fall back to a vendor, or in
// a group with one, it stays masked.
func TestLocalUnredactedProviderGetsTheRequestWhole(t *testing.T) {
	const key = "sk-proj-abcdEFGH1234ijklMNOP5678qrst"
	chat := `{"model":"fake/m1","max_tokens":10,"messages":[{"role":"user","content":"my key is ` + key + `"}]}`
	count := `{"model":"fake/m1","messages":[{"role":"user","content":"my key is ` + key + `"}]}`
	vendor := provider.Provider{ID: "vendor", Name: "Vendor", Key: "k", Models: []string{"m9"}, Anthropic: "https://api.vendor.example"}
	cases := []struct {
		name  string
		local func(*provider.Provider)
		group bool
		more  []provider.Provider
		whole bool
	}{
		{name: "set", local: func(p *provider.Provider) { p.Unredacted = true }, whole: true},
		{name: "unset", local: func(p *provider.Provider) {}},
		{name: "falls back to a vendor", local: func(p *provider.Provider) { p.Unredacted = true; p.Fallback = []string{"vendor/m9"} }, more: []provider.Provider{vendor}},
		{name: "falls back to another set", local: func(p *provider.Provider) { p.Unredacted = true; p.Fallback = []string{"lan/m2"} },
			more: []provider.Provider{{ID: "lan", Name: "LAN", Key: "k", Models: []string{"m2"}, Chat: "http://192.168.1.20:8000/v1", Unredacted: true}}, whole: true},
		{name: "a group of it alone", local: func(p *provider.Provider) { p.Unredacted = true }, group: true, whole: true},
		{name: "a group with a vendor", local: func(p *provider.Provider) { p.Unredacted = true }, group: true, more: []provider.Provider{vendor}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fake{ctype: "application/json", reply: `{"id":"msg_1","type":"message","role":"assistant","model":"m1","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`}
			setup(t, provider.Anthropic, f)
			p, err := provider.Find("fake")
			if err != nil {
				t.Fatal(err)
			}
			tc.local(p)
			for _, q := range append([]provider.Provider{*p}, tc.more...) {
				if err := provider.Save(q); err != nil {
					t.Fatal(err)
				}
			}
			body := chat
			if tc.group {
				members := []string{"fake/m1"}
				if len(tc.more) > 0 {
					members = append(members, "vendor/m9")
				}
				if err := provider.SaveGroup(provider.Group{ID: "mine", Name: "mine", Members: members, Routing: "order"}); err != nil {
					t.Fatal(err)
				}
				body = strings.Replace(chat, "fake/m1", "group/mine", 1)
			}
			if err := settings.Save(settings.Settings{Redact: true}); err != nil {
				t.Fatal(err)
			}
			if code, reply := post(t, "/v1/messages", body); code != 200 {
				t.Fatalf("%d %s", code, reply)
			}
			if whole := strings.Contains(string(f.got), key); whole != tc.whole {
				t.Fatalf("sent whole %v, want %v: %s", whole, tc.whole, f.got)
			}
			if tc.group {
				return
			}
			f.reply = `{"input_tokens":17}`
			if code, reply := post(t, "/v1/messages/count_tokens", count); code != 200 || f.path != "/v1/messages/count_tokens" {
				t.Fatalf("count: %d %s to %s", code, reply, f.path)
			}
			if whole := strings.Contains(string(f.got), key); whole != tc.whole {
				t.Fatalf("counted whole %v, want %v: %s", whole, tc.whole, f.got)
			}
		})
	}
}

// Embeddings likewise: to a local model set to go unmasked, whole.
func TestLocalUnredactedEmbeddings(t *testing.T) {
	s, l := shelved(t)
	p, err := provider.Find("lib")
	if err != nil {
		t.Fatal(err)
	}
	p.Unredacted = true
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	if err := settings.Save(settings.Settings{Redact: true}); err != nil {
		t.Fatal(err)
	}
	secret := "sk-ant-api03-" + strings.Repeat("Ab3x", 20)
	if code, raw := postRetrieval(t, s, "/v1/embeddings", `{"model":"lib/embed-1","input":"key `+secret+`"}`); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	if sent := l.got("/v1/embeddings"); len(sent) != 1 || !strings.Contains(sent[0], secret) {
		t.Fatalf("the local model was sent %v", sent)
	}
}
