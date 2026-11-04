package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

const codexAuthList = CodexPath + "/models?client_version=0.166.0&auth_test=kept"

// Codex sends its ChatGPT OAuth token beside the gateway key. Both guards
// must keep their identities apart, including when the key came in a URL.
func TestCodexModelsKeepOAuthBesideGatewayKey(t *testing.T) {
	for _, tc := range []struct{ name, from, keyAt string }{
		{"local x-api-key", fromHere, "x-api-key"},
		{"local x-goog-api-key", fromHere, "x-goog-api-key"},
		{"local query key", fromHere, "query"},
		{"LAN x-api-key", fromThere, "x-api-key"},
		{"local OAuth alone", fromHere, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			var asks atomic.Int32
			chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("auth_test") == "kept" {
					asks.Add(1)
					if r.URL.Path != CodexPath+"/models" || r.URL.Query().Get("client_version") != "0.166.0" {
						t.Errorf("model-list route changed: %s", r.URL)
					}
					if r.Header.Get("Authorization") != "Bearer client-oauth" || r.Header.Get("chatgpt-account-id") != "client-account" {
						t.Error("the client's OAuth token or account id did not reach ChatGPT")
						w.WriteHeader(http.StatusUnauthorized)
						io.WriteString(w, `{"detail":"invalid client sign-in"}`)
						return
					}
					for _, key := range []string{"x-api-key", "x-goog-api-key", "x-openai-actor-authorization"} {
						if r.Header.Get(key) != "" {
							t.Errorf("gateway credential header %s reached ChatGPT", key)
						}
					}
					if r.URL.Query().Has("key") {
						t.Error("gateway key reached ChatGPT in the query")
					}
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"models":[{"slug":"gpt-6-astra","priority":1,"context_window":272000,"max_context_window":872000,"effective_context_window_percent":95}]}`)
			})
			codexChatGPTAuth(t, "local-account@example.com")
			if err := provider.Save(provider.Provider{ID: "codex", Contexts: map[string]int{"gpt-6-astra": 872000}}); err != nil {
				t.Fatal(err)
			}
			_, secrets := newCaller(t, "Codex")
			path := codexAuthList
			headers := []string{"chatgpt-account-id", "client-account"}
			if tc.keyAt == "query" {
				path += "&key=" + url.QueryEscape(secrets[0])
			} else if tc.keyAt != "" {
				headers = append(headers, tc.keyAt, secrets[0], "x-openai-actor-authorization", "Bearer "+secrets[0])
			}
			w := keysCall(lanGuard(New().Handler()), "GET", path, tc.from, "client-oauth", "", headers...)
			if w.Code != http.StatusOK {
				t.Fatalf("model list: %d %s", w.Code, w.Body.String())
			}
			var got struct {
				Models []struct {
					Slug    string `json:"slug"`
					Window  int    `json:"context_window"`
					Maximum int    `json:"max_context_window"`
					Percent int    `json:"effective_context_window_percent"`
				} `json:"models"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, m := range got.Models {
				if m.Slug == "gpt-6-astra" {
					found = true
					if m.Window != 872000 || m.Maximum != 872000 || m.Percent != 95 {
						t.Errorf("Astra lost the configured window or backend reserve: %+v", m)
					}
				}
			}
			if !found || asks.Load() != 1 {
				t.Fatalf("Astra listed=%v, upstream requests=%d; want the authenticated backend list", found, asks.Load())
			}
		})
	}
}

// A gateway key authorizes access to magpie, never to the ChatGPT account
// stored on this computer. Missing OAuth stays a refusal, without a fetch.
func TestCodexModelsGatewayKeyIsNotOAuth(t *testing.T) {
	for _, bearer := range []string{"", "managed", Token, TokenFor("codex")} {
		t.Run(bearer, func(t *testing.T) {
			fresh(t)
			var asks atomic.Int32
			chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("auth_test") == "kept" {
					asks.Add(1)
				}
				io.WriteString(w, `{"models":[{"slug":"gpt-local-account","priority":1}]}`)
			})
			codexChatGPTAuth(t, "local-account@example.com")
			_, secrets := newCaller(t, "Codex")
			if bearer == "managed" {
				bearer = secrets[0]
			}
			w := keysCall(lanGuard(New().Handler()), "GET", codexAuthList, fromHere, bearer, "", "x-api-key", secrets[0])
			if w.Code != http.StatusUnauthorized || asks.Load() != 0 {
				t.Fatalf("without client OAuth: status %d, upstream requests %d; want a local 401", w.Code, asks.Load())
			}
		})
	}
}

// Keeping OAuth must not bypass the gateway's LAN, disabled-key or expiry
// checks. All these requests carry an otherwise valid client sign-in.
func TestCodexModelsOAuthStillNeedsAnEnabledGatewayKey(t *testing.T) {
	fresh(t)
	var asks atomic.Int32
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("auth_test") == "kept" {
			asks.Add(1)
		}
		io.WriteString(w, `{"models":[{"slug":"gpt-6-astra","priority":1}]}`)
	})
	keys, secrets := newCaller(t, "Disabled")
	if _, err := access.Update("off-key", access.Change{Key: keys[0].ID}); err != nil {
		t.Fatal(err)
	}
	issued, err := access.IssueKey(access.Issue{Name: "Expired", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	was := access.Now
	t.Cleanup(func() { access.Now = was })
	access.Now = func() time.Time { return was().Add(61 * time.Minute) }
	h := lanGuard(New().Handler())
	for _, tc := range []struct{ name, from, key string }{
		{"LAN without key", fromThere, ""},
		{"local disabled key", fromHere, secrets[0]},
		{"LAN disabled key", fromThere, secrets[0]},
		{"local expired key", fromHere, issued.Key},
		{"LAN expired key", fromThere, issued.Key},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := keysCall(h, "GET", codexAuthList, tc.from, "client-oauth", "", "x-api-key", tc.key)
			if w.Code != http.StatusUnauthorized || asks.Load() != 0 {
				t.Fatalf("status %d, upstream requests %d; want a local 401", w.Code, asks.Load())
			}
		})
	}
}

// Without account-id, the OAuth token distinguishes backend catalogs.
// Normalizing both to the same gateway token must not merge those caches.
func TestCodexModelsCacheUsesClientOAuth(t *testing.T) {
	fresh(t)
	var asks atomic.Int32
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("auth_test") != "kept" {
			io.WriteString(w, `{"models":[]}`)
			return
		}
		asks.Add(1)
		slug := ""
		switch r.Header.Get("Authorization") {
		case "Bearer oauth-a":
			slug = "gpt-client-a"
		case "Bearer oauth-b":
			slug = "gpt-client-b"
		default:
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"detail":"invalid client sign-in"}`)
			return
		}
		if r.Header.Get("chatgpt-account-id") != "" {
			t.Error("a local account id was added to the client's request")
		}
		io.WriteString(w, `{"models":[{"slug":"`+slug+`","priority":1}]}`)
	})
	_, secrets := newCaller(t, "Shared gateway key")
	h := lanGuard(New().Handler())
	for _, tc := range []struct{ token, slug string }{
		{"oauth-a", "gpt-client-a"},
		{"oauth-a", "gpt-client-a"},
		{"oauth-b", "gpt-client-b"},
		{"oauth-a", "gpt-client-a"},
	} {
		w := keysCall(h, "GET", codexAuthList, fromHere, tc.token, "", "x-api-key", secrets[0])
		if w.Code != http.StatusOK {
			t.Fatalf("%s list: %d %s", tc.token, w.Code, w.Body.String())
		}
		var list struct {
			Models []struct{ Slug string } `json:"models"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, m := range list.Models {
			if m.Slug == tc.slug {
				found = true
			} else if m.Slug == "gpt-client-a" || m.Slug == "gpt-client-b" {
				t.Errorf("%s received another sign-in's model %s", tc.token, m.Slug)
			}
		}
		if !found {
			t.Fatalf("%s did not receive its own model %s", tc.token, tc.slug)
		}
	}
	if asks.Load() != 2 {
		t.Fatalf("%d upstream requests, want one per OAuth token with reuse", asks.Load())
	}
}
