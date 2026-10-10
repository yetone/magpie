package gateway

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestFromCodex(t *testing.T) {
	for _, c := range []struct {
		name string
		head http.Header
		want bool
	}{
		{"acp user-agent", http.Header{"User-Agent": {"acp-extension-codex/0.160.1 (Mac OS 26.2.0; arm64) unknown (acp-extension-codex; 0.9.0)"}}, true},
		{"acp originator", http.Header{"Originator": {"acp-extension-codex"}}, true},
		{"other agent", http.Header{"User-Agent": {"opencode/1.0"}}, false},
		{"host client name", http.Header{"Originator": {"lody"}}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := fromCodex(c.head); got != c.want {
				t.Errorf("fromCodex(%v) = %t, want %t", c.head, got, c.want)
			}
		})
	}
}

// A relay that serves only Codex (Discord: "This account only allows Codex
// official clients", sub2api's codex_cli_only: an official User-Agent or
// originator, and an x-codex- header) gets Codex's own headers as it sent
// them, the provider's key in place of magpie's, on the Responses API and
// on Chat Completions, relayed or translated
func TestCodexHeadersReachKeyedRelay(t *testing.T) {
	codex := http.Header{
		"User-Agent":            {"codex_exec/0.159.2 (Mac OS 26.6.0; arm64) kitty (codex_exec; 0.159.2)"},
		"Originator":            {"codex_exec"},
		"Session-Id":            {"ses-1"},
		"Thread-Id":             {"ses-1"},
		"Session_id":            {"ses-1"},
		"Version":               {"0.159.2"},
		"X-Client-Request-Id":   {"ses-1"},
		"X-Codex-Window-Id":     {"ses-1:0"},
		"X-Codex-Beta-Features": {"remote_compaction_v2"},
		"X-Codex-Turn-Metadata": {`{"session_id":"ses-1","turn_id":"t1"}`},
		"X-Openai-Subagent":     {"review"},
		"Authorization":         {"Bearer " + Token},
		"X-Oai-Attestation":     {"att"},
		"Chatgpt-Account-Id":    {"acct"},
	}
	secret := []string{"Authorization", "X-Oai-Attestation", "Chatgpt-Account-Id"}
	for _, c := range []struct {
		proto      provider.Protocol
		path, body string
		ctype      string
		reply      string
	}{
		{provider.Responses, "/v1/responses", `{"model":"fake/m1","input":"hi","stream":false}`,
			"application/json", `{"id":"r","object":"response","status":"completed","output":[]}`},
		// Codex's default web_search makes magpie build the request again
		{provider.Responses, "/v1/responses", `{"model":"fake/m1","input":"hi","stream":false,"tools":[{"type":"web_search","external_web_access":false}]}`,
			"", "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[]}}\n\n"},
		{provider.Chat, "/v1/responses", `{"model":"fake/m1","input":"hi","stream":false}`,
			"", "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"OK\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"},
	} {
		f := &fake{t: t, ctype: c.ctype, reply: c.reply}
		setup(t, c.proto, f)
		send := func(h http.Header) {
			t.Helper()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", c.path, strings.NewReader(c.body))
			for k, v := range h {
				req.Header[k] = v
			}
			New().Handler().ServeHTTP(rec, req)
			if rec.Code != 200 || f.calls == 0 {
				t.Fatalf("%s %s: %d %s", c.proto, c.body, rec.Code, rec.Body.String())
			}
		}

		send(codex)
		for k, v := range codex {
			if slices.Contains(secret, k) {
				continue
			}
			if got := f.head.Get(k); got != v[0] {
				t.Errorf("%s: %s: got %q, want %q", c.proto, k, got, v[0])
			}
		}
		if a := f.head.Get("Authorization"); a != "Bearer k" {
			t.Errorf("%s: auth %q", c.proto, a)
		}
		for _, k := range secret[1:] {
			if f.head.Get(k) != "" {
				t.Errorf("%s: %s leaked", c.proto, k)
			}
		}

		// a provider with no key of its own still never sees magpie's
		p, _ := provider.Find("fake")
		p.Key = ""
		provider.Save(*p)
		send(codex)
		if a := f.head.Get("Authorization"); a != "" {
			t.Errorf("%s: magpie's key leaked: %q", c.proto, a)
		}
		if f.head.Get("Originator") != "codex_exec" {
			t.Errorf("%s: originator: %v", c.proto, f.head)
		}

		// the desktop app is told by its originator as well
		send(http.Header{"User-Agent": {"Codex Desktop/0.162.3 (Windows 10.0.26100; x86_64) unknown"}, "Originator": {"Codex Desktop"}, "X-Codex-Window-Id": {"w"}})
		if f.head.Get("Originator") != "Codex Desktop" || !strings.HasPrefix(f.head.Get("User-Agent"), "Codex Desktop/") || f.head.Get("X-Codex-Window-Id") != "w" {
			t.Errorf("%s: desktop: %v", c.proto, f.head)
		}

		// another agent's request goes as magpie's
		send(http.Header{"User-Agent": {"opencode/1.0"}, "X-Codex-Window-Id": {"w"}, "Session-Id": {"s"}})
		if ua := f.head.Get("User-Agent"); !strings.HasPrefix(ua, "magpie/") || f.head.Get("X-Codex-Window-Id") != "" || f.head.Get("Session-Id") != "" {
			t.Errorf("%s: other agent: %q %v", c.proto, ua, f.head)
		}
	}
}
