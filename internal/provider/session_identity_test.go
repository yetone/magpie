package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionIdentitiesReadOnly(t *testing.T) {
	home := signIn(t)
	path := filepath.Join(home, ".codex", "auth.json")
	before, _ := os.ReadFile(path)
	oldAuth, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token":     fakeJWT(map[string]any{"email": "old@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "old-account", "chatgpt_user_id": "old-user"}}),
		"access_token": "secret-access", "refresh_token": "secret-refresh", "account_id": "old-account"}})
	profile := json.RawMessage(`{"accountUuid":"claude-old","organizationUuid":"org-old","emailAddress":"claude-old@example.com"}`)
	if err := writeLogins([]savedLogin{{Agent: "codex", User: "old@example.com", Auth: oldAuth}, {Agent: "claude", User: "claude-old@example.com", Profile: profile}}); err != nil {
		t.Fatal(err)
	}
	ids := SessionIdentities(filepath.Dir(path))
	byID := map[string]SessionIdentity{}
	for _, id := range ids {
		byID[id.AccountID] = id
	}
	if byID["acct-1"].User != "me@example.com" || byID["old-account"].UserID != "old-user" || byID["claude-old"].OrganizationID != "org-old" {
		t.Fatalf("identities %+v", ids)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("identity lookup changed the agent's auth file")
	}
	b, _ := json.Marshal(ids)
	if strings.Contains(string(b), "secret-") {
		t.Fatal("credentials leaked into identity metadata")
	}
}

func TestOfficialLoginRequiresExplicitAuthenticationMetadata(t *testing.T) {
	for _, mode := range []string{"chatgpt", "", "custom", "apikey"} {
		b, _ := json.Marshal(map[string]any{"auth_mode": mode, "tokens": map[string]string{
			"account_id": "a", "id_token": fakeJWT(map[string]any{"email": "me@example.com", "https://api.openai.com/auth": map[string]string{"chatgpt_user_id": "u"}}),
		}})
		id, ok := codexSessionIdentity(b)
		if mode == "apikey" {
			if ok {
				t.Fatal("API key mode accepted as a login identity")
			}
			continue
		}
		if !ok || id.OfficialLogin != (mode == "chatgpt") {
			t.Fatalf("mode %q: %+v, %v", mode, id, ok)
		}
	}
	id, ok := claudeSessionIdentity([]byte(`{"accountUuid":"a","organizationUuid":"org","emailAddress":"me@example.com"}`))
	if !ok || !id.OfficialLogin {
		t.Fatal("Claude oauthAccount lost its explicit login method")
	}
}
