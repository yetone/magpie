package provider

// Copilot on GitHub Enterprise Cloud with data residency (#723): an
// enterprise's accounts live at <name>.ghe.com, not github.com. They sign in
// with the same device flow and OAuth app at https://<name>.ghe.com, trade
// their token at https://api.<name>.ghe.com/copilot_internal/v2/token, are
// looked up at api.<name>.ghe.com, and are served at the API their session
// names (endpoints.api: https://copilot-api.<name>.ghe.com), as VS Code's
// Copilot Chat, copilot.lua and `copilot login --host` do. An account
// remembers its host; one on github.com has none, and every URL it uses is
// the one it always was.

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// gheName is an enterprise's subdomain of ghe.com: one DNS label.
var gheName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// CopilotHost is where a Copilot account signs in, from what a user typed
// ("acme.ghe.com", "https://acme.ghe.com/", "github.com"): "" for
// github.com, "<name>.ghe.com" for an enterprise on GHE.com. Nothing else
// is taken, so a GitHub token is never sent to a host of the user's choosing.
func CopilotHost(s string) (string, error) {
	h := strings.ToLower(strings.TrimSpace(s))
	if strings.Contains(h, "://") {
		u, err := url.Parse(h)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Port() != "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return "", errors.New("not a GitHub host: " + strings.TrimSpace(s))
		}
		h = u.Hostname()
	}
	h = strings.TrimSuffix(h, "/")
	if h == "" || h == "github.com" || h == "www.github.com" {
		return "", nil
	}
	if name, ok := strings.CutSuffix(h, ".ghe.com"); ok && gheName.MatchString(name) {
		return h, nil
	}
	return "", errors.New("Copilot signs in on github.com or an enterprise's <name>.ghe.com, not " + strings.TrimSpace(s))
}

// copilotGHEOrigin is https://[sub.]host; a var so tests can point an
// enterprise's hosts elsewhere.
var copilotGHEOrigin = func(host, sub string) string {
	if sub == "" {
		return "https://" + host
	}
	return "https://" + sub + "." + host
}

// copilotHostOf is an account's host from the Copilot CLI's
// ("https://acme.ghe.com"); ok is false for one magpie can't serve.
func copilotHostOf(cli string) (string, bool) {
	if cli == "" {
		return "", true
	}
	h, err := CopilotHost(cli)
	return h, err == nil
}

// The URLs an account uses: github.com's own (vars tests point elsewhere)
// with no host, its enterprise's otherwise.

func copilotDeviceURL(host string) string {
	if host == "" {
		return gitHubDeviceURL
	}
	return copilotGHEOrigin(host, "") + "/login/device/code"
}

func copilotOAuthURL(host string) string {
	if host == "" {
		return gitHubTokenURL
	}
	return copilotGHEOrigin(host, "") + "/login/oauth/access_token"
}

func copilotDevicePage(host string) string {
	if host == "" {
		return "https://github.com/login/device"
	}
	return copilotGHEOrigin(host, "") + "/login/device"
}

func copilotTokenURL(host string) string {
	if host == "" {
		return CopilotTokenURL
	}
	return copilotGHEOrigin(host, "api") + "/copilot_internal/v2/token"
}

func copilotUserURL(host string) string {
	if host == "" {
		return CopilotUserURL
	}
	return copilotGHEOrigin(host, "api") + "/copilot_internal/user"
}

func gitHubUserURL(host string) string {
	if host == "" {
		return GitHubUserURL
	}
	return copilotGHEOrigin(host, "api") + "/user"
}

// copilotBaseOf is the Copilot API an account is served at when its
// session names none.
func copilotBaseOf(host string) string {
	if host == "" {
		return copilotBase
	}
	return copilotGHEOrigin(host, "copilot-api")
}

// apiBase is where this session's requests go.
func (s copilotSession) apiBase(host string) string {
	if s.Endpoints.API != "" {
		return s.Endpoints.API
	}
	return copilotBaseOf(host)
}
