package provider

// Local identities for associating historical session metadata. No credential
// refresh, sign-in discovery command or login-store write is needed here.
import (
	"cmp"
	"encoding/json"
	"path/filepath"

	"github.com/yetone/magpie/internal/filememo"
)

// SessionIdentity is an account's public identity, without its credentials.
type SessionIdentity struct {
	Agent, AccountID, UserID, OrganizationID, User string
	// OfficialLogin describes this account's recorded login method, not the
	// route or credentials used for any particular historical request.
	OfficialLogin bool
}

// SessionIdentities reads the agent's current identity and remembered identities.
// Callers must match the IDs recorded in a session; presence alone is not evidence
// that the account served that session's requests.
func SessionIdentities(codexDir string) []SessionIdentity {
	out := currentSessionIdentities(codexDir)
	for _, l := range readLogins() {
		if id, ok := savedSessionIdentity(l); ok {
			out = append(out, id)
		}
	}
	return out
}

func savedSessionIdentity(l savedLogin) (SessionIdentity, bool) {
	switch l.Agent {
	case "codex":
		return codexSessionIdentity(l.Auth)
	case "claude":
		return claudeSessionIdentity(l.Profile)
	}
	return SessionIdentity{}, false
}

func codexSessionIdentity(b []byte) (SessionIdentity, bool) {
	var a codexAuth
	if json.Unmarshal(b, &a) != nil || a.AuthMode == "apikey" {
		return SessionIdentity{}, false
	}
	claims := jwtClaims(a.Tokens.IDToken)
	id := cmp.Or(a.Tokens.AccountID, claimString(claims, "https://api.openai.com/auth", "chatgpt_account_id"))
	user := codexUser(claims)
	return SessionIdentity{Agent: "codex", AccountID: id, OfficialLogin: a.AuthMode == "chatgpt",
		UserID: cmp.Or(claimString(claims, "https://api.openai.com/auth", "chatgpt_user_id"), claimString(claims, "https://api.openai.com/auth", "user_id")), User: user}, id != "" && user != ""
}

func claudeSessionIdentity(b []byte) (SessionIdentity, bool) {
	var a struct {
		Account string `json:"accountUuid"`
		Org     string `json:"organizationUuid"`
		Email   string `json:"emailAddress"`
	}
	if json.Unmarshal(b, &a) != nil {
		return SessionIdentity{}, false
	}
	// This profile is read only from Claude's oauthAccount or its saved copy.
	return SessionIdentity{Agent: "claude", AccountID: a.Account, OrganizationID: a.Org, User: a.Email, OfficialLogin: true}, a.Account != "" && a.Email != ""
}

func currentSessionIdentities(codexDir string) []SessionIdentity {
	var out []SessionIdentity
	// Cache only the identity parse, not another copy of the credential blob.
	current, _ := filememo.Read("session codex identity", filepath.Join(codexDir, "auth.json"), func(b []byte) ([]SessionIdentity, error) {
		if id, ok := codexSessionIdentity(b); ok {
			return []SessionIdentity{id}, nil
		}
		return nil, nil
	})
	out = append(out, current...)
	if acct, ok := claudeProfileAccount(); ok {
		b, _ := json.Marshal(acct)
		if id, ok := claudeSessionIdentity(b); ok {
			out = append(out, id)
		}
	}
	return out
}
