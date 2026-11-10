package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// heldTokens is each kept sign-in's refresh token against its user and
// identity, to compare two homes.
func heldTokens(agent string) map[string]string {
	out := map[string]string{}
	for _, l := range readLogins() {
		if l.Agent != agent {
			continue
		}
		who := l.User
		if agent == "claude" {
			e, org := claudeWho(l.Profile)
			who = e + "/" + org
		} else {
			_, ws := codexWho(l.Auth)
			who += "/" + ws
		}
		out[loginRefreshToken(l)] = who
	}
	return out
}

func equalMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// A Claude account exported in either format is imported again, here, as
// the same two seats of one email (an organization apart), and the files
// carry what Sub2API and CLIProxyAPI's own say.
func TestExportClaudeRoundTrip(t *testing.T) {
	claudeHome(t)
	seats := `{"type":"sub2api-data","version":1,"proxies":[],"accounts":[
 {"name":"seat-a","platform":"anthropic","type":"oauth",
  "credentials":{"access_token":"sk-ant-oat01-a","expires_at":1760000000,"refresh_token":"sk-ant-ort01-a","scope":"user:inference user:profile","org_uuid":"org-a","account_uuid":"acct-1","email_address":"team@example.com"}},
 {"name":"seat-b","platform":"anthropic","type":"oauth",
  "credentials":{"access_token":"sk-ant-oat01-b","refresh_token":"sk-ant-ort01-b"},
  "extra":{"org_uuid":"org-b","account_uuid":"acct-1","email_address":"team@example.com"}}]}`
	if _, err := ImportLogins(context.Background(), "claude", []string{seats}); err != nil {
		t.Fatal(err)
	}
	before := heldTokens("claude")
	if len(before) != 2 {
		t.Fatalf("held %v", before)
	}

	s2a, err := ExportLogins("claude", ExportSub2API, nil)
	if err != nil || len(s2a.Files) != 1 || len(s2a.Accounts) != 2 {
		t.Fatalf("%+v %v", s2a, err)
	}
	var doc struct {
		Type     string
		Accounts []struct {
			Platform, Type string
			Credentials    map[string]any
		}
	}
	if err := json.Unmarshal(s2a.Files[0].Data, &doc); err != nil || doc.Type != "sub2api-data" || len(doc.Accounts) != 2 ||
		doc.Accounts[0].Platform != "anthropic" || doc.Accounts[0].Type != "oauth" || doc.Accounts[0].Credentials["refresh_token"] == "" {
		t.Fatalf("%s %v", s2a.Files[0].Data, err)
	}
	cpa, err := ExportLogins("claude", ExportCLIProxy, nil)
	if err != nil || len(cpa.Files) != 2 {
		t.Fatalf("%+v %v", cpa, err)
	}
	var one map[string]any
	if json.Unmarshal(cpa.Files[0].Data, &one) != nil || one["type"] != "claude" || one["email"] != "team@example.com" ||
		!strings.HasPrefix(cpa.Files[0].Name, "claude-team@example.com") || cpa.Files[0].Name == cpa.Files[1].Name {
		t.Fatalf("%s %s", cpa.Files[0].Name, cpa.Files[0].Data)
	}

	for name, files := range map[string][]ExportFile{"sub2api": s2a.Files, "cliproxyapi": cpa.Files} {
		claudeHome(t) // a new computer
		var texts []string
		for _, f := range files {
			texts = append(texts, string(f.Data))
		}
		rs, err := ImportLogins(context.Background(), "claude", texts)
		if err != nil {
			t.Fatal(name, err)
		}
		if got := heldTokens("claude"); !equalMaps(got, before) {
			t.Fatalf("%s: back as %v, was %v (%+v)", name, got, before, rs)
		}
	}
}

// A ChatGPT account makes the round trip too, each file refreshed on the
// way in as any import is.
func TestExportCodexRoundTrip(t *testing.T) {
	claudeHome(t)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		rt := body["refresh_token"]
		who, ok := map[string][2]string{"r1": {"a@example.com", "acct-1"}, "r2": {"b@example.com", "acct-2"}}[strings.TrimPrefix(rt, "n")]
		if !ok {
			w.WriteHeader(400)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id_token": fakeJWT(map[string]any{"email": who[0],
				"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus", "chatgpt_account_id": who[1]}}),
			"access_token":  fakeJWT(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix())}),
			"refresh_token": "n" + strings.TrimPrefix(rt, "n"),
		})
	}))
	defer fake.Close()
	t.Cleanup(CodexTokenURLForTest(fake.URL))
	in := `[{"type":"codex","email":"a@example.com","id_token":"x","access_token":"y","refresh_token":"r1","account_id":"acct-1"},
	{"type":"codex","email":"b@example.com","id_token":"x","access_token":"y","refresh_token":"r2","account_id":"acct-2"}]`
	if _, err := ImportLogins(context.Background(), "codex", []string{in}); err != nil {
		t.Fatal(err)
	}
	before := heldTokens("codex")
	if len(before) != 2 {
		t.Fatalf("held %v", before)
	}

	// one account picked by name
	one, err := ExportLogins("codex", ExportSub2API, []string{"B@example.com"})
	if err != nil || len(one.Accounts) != 1 || !strings.Contains(one.Accounts[0], "b@example.com") {
		t.Fatalf("%+v %v", one, err)
	}
	if _, err := ExportLogins("codex", ExportSub2API, []string{"nobody@example.com"}); err == nil {
		t.Fatal("a name no account has was exported")
	}
	s2a, err := ExportLogins("codex", ExportSub2API, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(s2a.Files[0].Data), `"platform": "openai"`) || !strings.Contains(string(s2a.Files[0].Data), "chatgpt_account_id") {
		t.Fatalf("%s", s2a.Files[0].Data)
	}
	cpa, err := ExportLogins("codex", ExportCLIProxy, nil)
	if err != nil || len(cpa.Files) != 2 || !strings.HasSuffix(cpa.Files[0].Name, "-plus.json") {
		t.Fatalf("%+v %v", cpa, err)
	}
	for name, files := range map[string][]ExportFile{"sub2api": s2a.Files, "cliproxyapi": cpa.Files} {
		claudeHome(t)
		var texts []string
		for _, f := range files {
			texts = append(texts, string(f.Data))
		}
		rs, err := ImportLogins(context.Background(), "codex", texts)
		if err != nil {
			t.Fatal(name, err)
		}
		got := heldTokens("codex")
		if len(got) != 2 {
			t.Fatalf("%s: back as %v (%+v)", name, got, rs)
		}
		for _, who := range got {
			if !strings.Contains(who, "example.com") || !strings.HasSuffix(who, "/acct-1") && !strings.HasSuffix(who, "/acct-2") {
				t.Fatalf("%s: %v", name, got)
			}
		}
	}
}

// An account with only an access token, or a setup-token, can't be taken
// out: said, never written as a file that imports as nothing.
func TestExportSkipsWhatHasNoRefreshToken(t *testing.T) {
	claudeHome(t)
	if _, err := ExportLogins("claude", ExportSub2API, nil); err == nil {
		t.Fatal("an export of no accounts")
	}
	if _, err := ExportLogins("gemini", ExportSub2API, nil); err == nil {
		t.Fatal("gemini exported")
	}
	if _, err := ExportLogins("claude", "csv", nil); err == nil {
		t.Fatal("csv exported")
	}
}
