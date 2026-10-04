package provider

// The standalone Copilot CLI keeps its sign-in apart from the editors: who
// is signed in in ~/.copilot/config.json, the token in the system keychain
// (service copilot-cli), or in that file when the user chose plaintext. Its
// token is not traded for a session token the way an editor's is (that
// exchange answers 403): the CLI sends it as is, under its own integration.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/jsonc"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/proc"
)

// copilotCLIHeaders are what the CLI sends with its token.
var copilotCLIHeaders = map[string]string{
	"Editor-Version":         "copilot/1.0.88",
	"Copilot-Integration-Id": "copilot-developer-cli",
	"X-GitHub-Api-Version":   "2026-07-01",
	"User-Agent":             "copilot/1.0.88",
}

// CopilotUserURL says where an account's Copilot API is; a var so tests can
// point it elsewhere.
var CopilotUserURL = "https://api.github.com/copilot_internal/user"

type copilotCLIUser struct {
	Host  string `json:"host"`
	Login string `json:"login"`
}

// copilotCLIHome is where the CLI keeps its settings.
func copilotCLIHome() string {
	if h := appdir.Getenv("COPILOT_HOME"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".copilot")
}

// copilotCLILogin is the account the Copilot CLI is signed in to, with its
// token.
func copilotCLILogin() (copilotApp, bool) {
	raw, err := os.ReadFile(filepath.Join(copilotCLIHome(), "config.json"))
	if err != nil {
		return copilotApp{}, false
	}
	var cfg struct {
		Last   *copilotCLIUser   `json:"lastLoggedInUser"`
		Users  []copilotCLIUser  `json:"loggedInUsers"`
		Tokens map[string]string `json:"copilotTokens"`
	}
	if json.Unmarshal(jsonc.ToJSON(raw), &cfg) != nil {
		return copilotApp{}, false
	}
	users := cfg.Users
	if cfg.Last != nil {
		users = append([]copilotCLIUser{*cfg.Last}, users...)
	}
	// a github.com account first, as before; then one an enterprise's
	// `copilot login --host <name>.ghe.com` signed in (#723). A GitHub
	// Enterprise Server's own host is left out: its API isn't GHE.com's.
	for _, ghe := range []bool{false, true} {
		for _, u := range users {
			host, ok := copilotHostOf(u.Host)
			if u.Login == "" || !ok || (host != "") != ghe {
				continue
			}
			key := "https://github.com:" + u.Login
			if ghe {
				key = strings.TrimSuffix(u.Host, "/") + ":" + u.Login
			}
			tok := cfg.Tokens[key]
			if tok == "" {
				tok = copilotCLISecret(key)
			}
			if tok != "" {
				return copilotApp{User: u.Login, Token: tok, Host: host, cli: true}, true
			}
		}
	}
	return copilotApp{}, false
}

var copilotSecrets struct {
	sync.Mutex
	at  map[string]time.Time
	tok map[string]string
}

// copilotCLISecret reads the CLI's token from the keychain, once in a
// while: each read may ask the user to allow it.
var copilotCLISecret = func(account string) string {
	c := &copilotSecrets
	c.Lock()
	defer c.Unlock()
	if c.at == nil {
		c.at, c.tok = map[string]time.Time{}, map[string]string{}
	}
	ttl := time.Hour
	if c.tok[account] == "" {
		ttl = 10 * time.Minute // denied or absent; don't ask again at once
	}
	if at, ok := c.at[account]; ok && time.Since(at) < ttl {
		return c.tok[account]
	}
	var out []byte
	switch runtime.GOOS {
	case "darwin":
		out, _ = proc.Command("security", "find-generic-password", "-s", "copilot-cli", "-a", account, "-w").Output()
	case "linux":
		if p, err := exec.LookPath("secret-tool"); err == nil {
			out, _ = proc.Command(p, "lookup", "service", "copilot-cli", "account", account).Output()
		}
	}
	c.at[account], c.tok[account] = time.Now(), string(bytes.TrimSpace(out))
	return c.tok[account]
}

// copilotDirect is the session a CLI token makes: the token itself, at the
// API its account is served from.
func copilotDirect(ctx context.Context, app copilotApp) (copilotSession, error) {
	github := app.Token
	copilotMu.Lock()
	defer copilotMu.Unlock()
	if s, ok := copilotSessions[github]; ok && time.Until(time.Unix(s.ExpiresAt, 0)) > 2*time.Minute {
		return s, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, copilotUserURL(app.Host), nil)
	if err != nil {
		return copilotSession{}, err
	}
	req.Header.Set("Authorization", "token "+github)
	req.Header.Set("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return copilotSession{}, errors.New("Copilot sign-in: " + err.Error())
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var u struct {
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if res.StatusCode != 200 || json.Unmarshal(b, &u) != nil {
		return copilotSession{}, errors.New("the Copilot CLI's sign-in was refused (" + APIError(b, res.Status) + "); run `copilot login` again")
	}
	s := copilotSession{Token: github, ExpiresAt: time.Now().Add(30 * time.Minute).Unix(), direct: true}
	s.Endpoints.API = u.Endpoints.API
	copilotSessions[github] = s
	return s, nil
}
