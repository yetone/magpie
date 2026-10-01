package usage

import (
	"bytes"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/sessions"
)

func TestCodexSessionAttributionRequiresRecordedIdentity(t *testing.T) {
	r := &sessionResolver{identities: []provider.SessionIdentity{
		{Agent: "codex", AccountID: "a-old", UserID: "u-old", User: "old@example.com"},
		{Agent: "codex", AccountID: "a-now", UserID: "u-now", User: "now@example.com"},
	}}
	for _, tc := range []struct {
		name string
		call sessions.Call
		want string
	}{
		{"official metadata", sessions.Call{Agent: "codex", Upstream: "openai", AccountID: "a-old", UserID: "u-old"}, "old@example.com"},
		{"named provider never establishes historical route", sessions.Call{Agent: "codex", Upstream: "custom", AccountID: "a-old", UserID: "u-old"}, "old@example.com"},
		{"named provider on the ChatGPT sign-in, no address", sessions.Call{Agent: "codex", Upstream: "chatgpt", AccountID: "a-old", UserID: "u-old"}, "old@example.com"},
		{"ChatGPT sign-in sent to a relay", sessions.Call{Agent: "codex", Upstream: "oauth-relay", AccountID: "a-old", UserID: "u-old"}, "old@example.com"},
		{"named provider with neither address nor sign-in", sessions.Call{Agent: "codex", Upstream: "bare"}, ""},
		{"named provider no longer configured", sessions.Call{Agent: "codex", Upstream: "gone"}, ""},
		{"third party despite OAuth login", sessions.Call{Agent: "codex", Upstream: "relay", AccountID: "a-old", UserID: "u-old"}, "old@example.com"},
		{"older file is not current account", sessions.Call{Agent: "codex", Upstream: "custom"}, ""},
		{"another user", sessions.Call{Agent: "codex", Upstream: "custom", AccountID: "a-old", UserID: "u-other"}, ""},
		{"unrecorded provider", sessions.Call{Agent: "codex", AccountID: "a-now", UserID: "u-now"}, "now@example.com"},
		{"provider without account", sessions.Call{Agent: "codex", Upstream: "openai"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := r.resolve(tc.call); got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
	r.identities = append(r.identities, provider.SessionIdentity{Agent: "codex", AccountID: "a-old", UserID: "u-other", User: "other@example.com"})
	if got := r.codexUser(sessions.Call{AccountID: "a-old"}); got != "" {
		t.Fatal("an ambiguous workspace must not pick an email")
	}
}

func TestDesktopSessionAccountMetadata(t *testing.T) {
	home := t.TempDir()
	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	root := sessions.DesktopDataDirs()[0]
	root3p := sessions.DesktopDataDirs()[1]
	account, org := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	write := func(path, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, "local-agent-mode-sessions", account, org, "local_cowork.json"), `{"cliSessionId":"cowork","emailAddress":"historical@example.com"}`)
	write(filepath.Join(root, "claude-code-sessions", account, org, "local_code.json"), `{"cliSessionId":"desktop-code"}`)
	write(filepath.Join(root3p, "local-agent-mode-sessions", account, org, "local_third.json"), `{"cliSessionId":"third","emailAddress":"third@example.com"}`)
	// Today's unrelated login must not replace the session's recorded email.
	write(filepath.Join(home, ".claude", ".claude.json"), `{"oauthAccount":{"accountUuid":"now","emailAddress":"now@example.com"}}`)
	call := sessions.Call{Time: time.Now(), Agent: "claude-desktop", Session: "cowork", File: filepath.Join(root, "local-agent-mode-sessions", account, org, "local_cowork", ".claude", "projects", "p", "s.jsonl")}
	r := newSessionResolver([]sessions.Call{call})
	if got := r.resolve(call); got != "historical@example.com" {
		t.Fatalf("Cowork identity %+v", got)
	}
	call.File, call.Session = "", "desktop-code"
	if got := r.resolve(call); got != "historical@example.com" {
		t.Fatalf("Code tab account mapping %+v", got)
	}
	call.Session = "third"
	if got := r.resolve(call); got != "third@example.com" {
		t.Fatalf("third-party Desktop misattributed %+v", got)
	}
	call.Agent, call.Session = "claude", "unrelated-cli"
	if got := r.resolve(call); got != "" {
		t.Fatalf("CLI history assigned current login %+v", got)
	}
}

func TestLedgerResolvesOAuthIdentityAndExportsIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	if err := os.MkdirAll(sessions.CodexDir(), 0700); err != nil {
		t.Fatal(err)
	}
	claims := `{"email":"matched@example.com","https://api.openai.com/auth":{"chatgpt_account_id":"a","chatgpt_user_id":"u"}}`
	jwt := "h." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".sig"
	auth, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]string{"account_id": "a", "id_token": jwt}})
	os.WriteFile(filepath.Join(sessions.CodexDir(), "auth.json"), auth, 0600)
	os.WriteFile(filepath.Join(sessions.CodexDir(), "config.toml"), []byte("[model_providers.custom]\nrequires_openai_auth = true\n"), 0600)
	call := sessions.Call{Time: time.Now(), Agent: "codex", Model: "gpt-6-sol", Upstream: "custom", AccountID: "a", UserID: "u", Tokens: sessions.Tokens{Input: 10, Output: 2}}
	rows, sum, _, providers := ledgerWith(time.Time{}, Filter{}, nil, []sessions.Call{call})
	if len(rows) != 1 || rows[0].Provider != UnknownProvider || rows[0].Host != "" || rows[0].SessionAccount != "matched@example.com" || sum.Calls != 1 || len(providers) != 1 {
		t.Fatalf("ledger attribution %+v", rows)
	}
	filtered, _, _, _ := ledgerWith(time.Time{}, Filter{Provider: UnknownProvider}, nil, []sessions.Call{call})
	if len(filtered) != 1 {
		t.Fatal("provider filter lost attributed session")
	}
	var out bytes.Buffer
	if err := WriteCSV(&out, rows); err != nil {
		t.Fatal(err)
	}
	data, err := csv.NewReader(&out).ReadAll()
	if err != nil || len(data) != 2 || data[1][3] != UnknownProvider || data[1][4] != "" || data[1][len(data[1])-3] != "custom" || data[1][len(data[1])-2] != "matched@example.com" || data[1][len(data[1])-1] != "true" {
		t.Fatal("CSV lost the resolved provider/account")
	}
}

func TestModelsNeverEstablishProviderOrAccount(t *testing.T) {
	home := t.TempDir()
	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	at := time.Now()
	logs := []sessions.Call{
		{Time: at, Agent: "claude", Model: "claude-opus-5", Requested: "claude-opus-5[1m]"},
		{Time: at, Agent: "codex", Model: "gpt-6-astra", Upstream: "relay"},
		{Time: at, Agent: "claude", Model: "deepseek-review", Requested: "claude-sonnet-5"},
	}
	rows, _, _, providers := ledgerWith(time.Time{}, Filter{}, nil, logs)
	if len(rows) != len(logs) || len(providers) != 1 || providers[0] != UnknownProvider {
		t.Fatalf("models changed actual provider groups: %v", providers)
	}
	for _, row := range rows {
		if row.Provider != UnknownProvider || row.Host != "" || row.SessionAccount != "" {
			t.Fatalf("model falsely attributed route/account: %+v", row)
		}
	}
	if rows[1].SessionProvider != "relay" || rows[2].Served != "deepseek-review" {
		t.Fatal("recorded upstream or returned model lost")
	}
	for _, official := range []string{"session-openai", "session-anthropic", "codex", "claude"} {
		if known, _, _, _ := ledgerWith(time.Time{}, Filter{Provider: official}, nil, logs); len(known) != 0 {
			t.Fatalf("model-only calls counted as official %s calls", official)
		}
	}
	var out bytes.Buffer
	if err := WriteCSV(&out, rows[1:2]); err != nil {
		t.Fatal(err)
	}
	data, err := csv.NewReader(&out).ReadAll()
	if err != nil || data[1][3] != UnknownProvider || data[1][len(CSVHeader)-3] != "relay" || data[1][len(CSVHeader)-2] != "" || data[1][len(CSVHeader)-1] != "false" {
		t.Fatalf("unconfirmed CSV route misattributed %+v: %v", data, err)
	}
	gateway := []Record{
		{Time: at, Agent: "opencode", Provider: "relay", Host: "relay.example", Model: "gpt-6-astra", Input: 1, Status: 200},
		{Time: at, Agent: "claude", Provider: "third-api", Host: "third.example", Model: "claude-opus-5", Input: 1, Status: 200},
	}
	for _, provider := range []string{"relay", "third-api"} {
		actual, _, _, _ := ledgerWith(time.Time{}, Filter{Provider: provider}, gateway, logs)
		if len(actual) != 1 || actual[0].Provider != provider || actual[0].Host == "" || actual[0].Source != "" {
			t.Fatalf("third-party gateway route changed: %+v", actual)
		}
	}
}

func TestLocalSessionAccountsDoNotEstablishProviders(t *testing.T) {
	account, org := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	r := &sessionResolver{
		identities: []provider.SessionIdentity{
			{Agent: "codex", AccountID: "a-mine", UserID: "u-mine", User: "mine@example.com"},
			{Agent: "claude", AccountID: account, OrganizationID: org, User: "claude@example.com"},
		},
		desktop: map[string]desktopSessionIdentity{},
		bySession: map[string][]desktopSessionIdentity{
			"mine":     {{account: account, org: org, email: "claude@example.com", linkable: true}},
			"other":    {{account: account, org: "33333333-3333-4333-8333-333333333333", email: "team@example.com", linkable: true}},
			"third-3p": {{account: account, org: org, email: "claude@example.com"}},
		},
	}
	for _, tc := range []struct {
		name string
		call sessions.Call
		want string
	}{
		{"Codex on magpie's account", sessions.Call{Agent: "codex", Upstream: "openai", AccountID: "a-mine", UserID: "u-mine"}, "mine@example.com"},
		{"Codex on another account", sessions.Call{Agent: "codex", Upstream: "openai", AccountID: "a-other"}, ""},
		{"Codex with no account recorded", sessions.Call{Agent: "codex", Upstream: "openai"}, ""},
		{"magpie's account through a relay", sessions.Call{Agent: "codex", Upstream: "relay", AccountID: "a-mine", UserID: "u-mine"}, "mine@example.com"},
		{"Claude Desktop on magpie's account", sessions.Call{Agent: "claude-desktop", Session: "mine"}, "claude@example.com"},
		{"Claude Desktop in another organization", sessions.Call{Agent: "claude-desktop", Session: "other"}, "team@example.com"},
		{"Claude-3p on magpie's account's ids", sessions.Call{Agent: "claude-desktop", Session: "third-3p"}, "claude@example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := r.resolve(tc.call); got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestLedgerKeepsLocalAccountsSeparateFromGatewayProviders(t *testing.T) {
	home := t.TempDir()
	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	if err := os.MkdirAll(sessions.CodexDir(), 0700); err != nil {
		t.Fatal(err)
	}
	claims := `{"email":"mine@example.com","https://api.openai.com/auth":{"chatgpt_account_id":"a","chatgpt_user_id":"u","chatgpt_plan_type":"plus"}}`
	jwt := "h." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".sig"
	auth, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]string{"account_id": "a", "id_token": jwt, "access_token": jwt, "refresh_token": "r"}})
	os.WriteFile(filepath.Join(sessions.CodexDir(), "auth.json"), auth, 0600)
	os.WriteFile(filepath.Join(sessions.CodexDir(), "config.toml"), []byte("model_provider = \"custom\"\n[model_providers.custom]\nname = \"OpenAI\"\nrequires_openai_auth = true\n"), 0600)
	at := time.Now()
	gateway := []Record{{Time: at.Add(-time.Minute), Agent: "opencode", Provider: "codex", Host: "mine@example.com", Model: "gpt-6-sol", Input: 5, Output: 1, Status: 200}}
	logs := []sessions.Call{
		{Time: at, Agent: "codex", Model: "gpt-6-sol", Upstream: "openai", AccountID: "a", UserID: "u", Tokens: sessions.Tokens{Input: 10, Output: 2}},
		{Time: at, Agent: "codex", Model: "gpt-6-sol", Upstream: "openai", AccountID: "someone-else", Tokens: sessions.Tokens{Input: 7, Output: 1}},
	}
	rows, _, _, providers := ledgerWith(time.Time{}, Filter{}, gateway, logs)
	if len(rows) != 3 || !slices.Equal(providers, []string{"codex", UnknownProvider}) {
		t.Fatalf("providers %v", providers)
	}
	for _, b := range Breakdown(rows, "provider") {
		want := 2
		if b.ID == "codex" {
			want = 1
		}
		if b.Calls != want {
			t.Fatalf("local calls merged into a supplier: %+v", b)
		}
	}
	for _, row := range rows {
		if row.Source == "log" {
			if row.Provider != UnknownProvider || row.Host != "" {
				t.Fatalf("local call inferred a provider: %+v", row)
			}
		} else if row.Provider != "codex" || row.Host != "mine@example.com" {
			t.Fatalf("gateway metadata changed: %+v", row)
		}
	}
	if rows[0].SessionAccount != "mine@example.com" || rows[1].SessionAccount != "" {
		t.Fatalf("local identity was lost or guessed: %+v", rows)
	}
}
