package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeKiroManagement answers Kiro's management API as it answers a Builder
// ID sign-in: no profiles to list (a 403), and the account's models and
// usage for the Builder ID service profile.
func fakeKiroManagement(t *testing.T) *[]string {
	t.Helper()
	var mu sync.Mutex
	asked := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/List-Available-Profiles":
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"message":"AWS Builder ID is not supported for this operation."}`))
		case "/List-Available-Models":
			if r.URL.Query().Get("profileArn") != kiroBuilderIDProfile {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"message":"profileArn is required"}`))
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"defaultModel": map[string]any{"modelId": "auto"}, "models": []any{
				map[string]any{"modelId": "auto", "modelName": "auto", "tokenLimits": map[string]any{"maxInputTokens": 1000000}},
				map[string]any{"modelId": "claude-opus-5.5", "modelName": "Claude Opus 5.5", "supportedInputTypes": []string{"TEXT", "IMAGE"},
					"tokenLimits": map[string]any{"maxInputTokens": 1000000, "maxOutputTokens": 128000}},
				map[string]any{"modelId": "claude-sonnet-5", "modelName": "Claude Sonnet 5", "tokenLimits": map[string]any{"maxInputTokens": 1000000}},
				map[string]any{"modelId": "claude-haiku-4.5", "modelName": "Claude Haiku 4.5", "tokenLimits": map[string]any{"maxInputTokens": 200000}},
			}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	was := kiroManagementURL
	kiroManagementURL = func(string) string { return srv.URL + "/" }
	t.Cleanup(func() { kiroManagementURL = was })
	return &asked
}

func kiroFetchedIDs(t *testing.T) []string {
	t.Helper()
	p, ok := kiroAccount()
	if !ok {
		t.Fatal("no Kiro account")
	}
	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	var ids []string
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	return ids
}

// A Builder ID sign-in (kiro-cli's here) names no profile and Kiro lists
// none for it, so magpie couldn't ask for its models at all and offered
// Auto alone (#422); it is asked with Builder ID's service profile, as
// kiro-cli and the IDE do, and Kiro's whole list is offered.
func TestKiroBuilderIDListsItsModels(t *testing.T) {
	kiroSandbox(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	asked := fakeKiroManagement(t)
	writeKiroCLI(t, map[string]any{
		"kirocli:odic:token": map[string]any{"access_token": "at", "refresh_token": "rt", "expires_at": "2099-01-01T00:00:00Z",
			"region": "us-east-1", "start_url": kiroBuilderIDStart, "oauth_flow": "DeviceCode"},
		"kirocli:odic:device-registration": map[string]any{"client_id": "cid", "client_secret": "sec"},
	})
	got := strings.Join(kiroFetchedIDs(t), ",")
	if got != "auto,claude-opus-5.5,claude-sonnet-5,claude-haiku-4.5" {
		t.Fatalf("models = %s (asked %v)", got, *asked)
	}
	for _, a := range *asked {
		if a == "/List-Available-Profiles" {
			t.Fatalf("asked for a Builder ID sign-in's profiles: %v", *asked)
		}
	}
	a, err := KiroAuthOf(context.Background(), "", "", false)
	if err != nil || a.Profile != kiroBuilderIDProfile || a.Region != "us-east-1" {
		t.Fatalf("auth = %+v %v", a, err)
	}
	// once listed, the account offers them, not Auto alone
	p, _ := kiroAccount()
	if ms := p.Available(); len(ms) != 4 {
		t.Fatalf("available = %+v", ms)
	}
}

// An Identity Center sign-in still has its profile listed; one that turns
// out to be Builder ID's (the IDE's file not saying) takes Builder ID's
// profile when Kiro says it has none to list.
func TestKiroBuilderIDFoundByKirosAnswer(t *testing.T) {
	kiroSandbox(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	asked := fakeKiroManagement(t)
	dir := kiroIDEDir()
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "kiro-auth-token.json"), []byte(`{"accessToken":"at","refreshToken":"rt","expiresAt":"2099-01-01T00:00:00Z",
		"authMethod":"IdC","region":"us-east-1","clientIdHash":"h"}`), 0o600)
	os.WriteFile(filepath.Join(dir, "h.json"), []byte(`{"clientId":"cid","clientSecret":"sec"}`), 0o600)
	if got := strings.Join(kiroFetchedIDs(t), ","); got != "auto,claude-opus-5.5,claude-sonnet-5,claude-haiku-4.5" {
		t.Fatalf("models = %s (asked %v)", got, *asked)
	}
	if (*asked)[0] != "/List-Available-Profiles" {
		t.Fatalf("asked %v", *asked)
	}
}
