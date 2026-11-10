package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// copilotTwoPlans signs Copilot's editors in to octocat (Pro+) and saves
// hubot (Pro) on beside it, and points Copilot at a fake whose /models
// answers each account with its own plan's list: both have gpt-4.1,
// only octocat claude-opus-9, only hubot gemini-9-pro. A model an
// account's list lacks is refused it 400 model_not_supported, as Copilot
// does. tried takes the account:model of each chat since it was last
// called.
func copilotTwoPlans(t *testing.T) (tried func() string) {
	return copilotTwoPlansListing(t, nil)
}

// copilotTwoPlansListing is copilotTwoPlans with octocat's /models also
// listing extra, which Copilot still refuses octocat, as it does a Student
// plan (copilot_refused.go).
func copilotTwoPlansListing(t *testing.T, extra []string) (tried func() string) {
	t.Helper()
	fresh(t)
	cfg := os.Getenv("XDG_CONFIG_HOME")
	os.MkdirAll(filepath.Join(cfg, "github-copilot"), 0o755)
	os.WriteFile(filepath.Join(cfg, "github-copilot", "apps.json"), mustJSON(map[string]any{
		"github.com:Iv1.x": map[string]any{"user": "octocat", "oauth_token": "gho_octo_pool"},
	}), 0o600)
	os.MkdirAll(filepath.Dir(provider.Path()), 0o755)
	os.WriteFile(filepath.Join(filepath.Dir(provider.Path()), "logins.json"), mustJSON([]map[string]any{
		{"agent": "copilot", "user": "hubot", "plan": "Pro", "on": true, "seen": time.Now(), "auth": map[string]string{"oauth_token": "gho_hubot_pool"}},
	}), 0o600)

	plans := map[string][]string{
		"sess-gho_octo_pool":  {"gpt-4.1", "claude-opus-9"},
		"sess-gho_hubot_pool": {"gpt-4.1", "gemini-9-pro"},
	}
	who := map[string]string{"sess-gho_octo_pool": "octocat", "sess-gho_hubot_pool": "hubot"}
	var mu sync.Mutex
	var sent []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		has, ok := plans[tok]
		if !ok {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/models" {
			listed := has
			if who[tok] == "octocat" {
				listed = append(slices.Clone(has), extra...)
			}
			var data []map[string]any
			for _, id := range listed {
				data = append(data, map[string]any{"id": id, "name": id, "model_picker_enabled": true, "policy": map[string]string{"state": "enabled"},
					"supported_endpoints": []string{"/chat/completions"}, "capabilities": map[string]string{"type": "chat"}})
			}
			json.NewEncoder(w).Encode(map[string]any{"data": data})
			return
		}
		var q struct {
			Model string `json:"model"`
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &q)
		mu.Lock()
		sent = append(sent, who[tok]+":"+q.Model)
		mu.Unlock()
		if !slices.Contains(has, q.Model) {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"The requested model is not supported.","code":"model_not_supported","param":"model","type":"invalid_request_error"}}`)
			return
		}
		io.WriteString(w, `{"id":"c1","model":"`+q.Model+`","choices":[{"index":0,"message":{"role":"assistant","content":"pong from `+who[tok]+`"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
	}))
	t.Cleanup(api.Close)
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			tok := strings.TrimPrefix(r.Header.Get("Authorization"), "token ")
			json.NewEncoder(w).Encode(map[string]any{"token": "sess-" + tok, "expires_at": time.Now().Add(time.Hour).Unix(), "endpoints": map[string]string{"api": api.URL}})
			return
		}
		io.WriteString(w, `{"copilot_plan":"individual"}`)
	}))
	t.Cleanup(gh.Close)
	oldTok, oldUser := provider.CopilotTokenURL, provider.CopilotUserURL
	provider.CopilotTokenURL, provider.CopilotUserURL = gh.URL+"/token", gh.URL+"/user"
	t.Cleanup(func() { provider.CopilotTokenURL, provider.CopilotUserURL = oldTok, oldUser })
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		got := strings.Join(sent, ",")
		sent = nil
		return got
	}
}

// applemacssma on X: several GitHub Copilot accounts, rotated and spread
// evenly, "每个ghcp账号有的模型不一样" — each with other models. The
// copilot provider listed the first account's models alone, so one only
// another account's plan has was nowhere to pick, and every account was
// sent every model. Its list is now what the accounts have together, a
// model goes only to the accounts whose list has it, and one both have is
// still spread over both.
func TestCopilotAccountsWithOtherModels(t *testing.T) {
	tried := copilotTwoPlans(t)
	p, err := provider.Find("copilot")
	if err != nil {
		t.Fatal(err)
	}
	if p.Account == nil || p.Account.User != "octocat" || len(p.AlsoOn()) != 1 {
		t.Fatalf("accounts: %+v, also %d", p.Account, len(p.AlsoOn()))
	}
	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	for _, want := range []string{"gpt-4.1", "claude-opus-9", "gemini-9-pro"} {
		if !slices.Contains(ids, want) {
			t.Fatalf("the copilot provider lists %v, without %s", ids, want)
		}
	}
	octo := &provider.Account{Agent: "copilot", User: "octocat"}
	hubot := &provider.Account{Agent: "copilot", User: "hubot"}
	if !octo.Lists("claude-opus-9") || octo.Lists("gemini-9-pro") || hubot.Lists("claude-opus-9") || !hubot.Lists("gemini-9-pro") {
		t.Fatal("an account's own list took in the other's models")
	}

	if err := provider.SetRouting("copilot", provider.Rotate); err != nil {
		t.Fatal(err)
	}
	s := New()
	ask := func(model string) string {
		t.Helper()
		code, body := postAs(t, s, "", `{"model":"copilot/`+model+`","messages":[{"role":"user","content":"ping"}]}`)
		if code != 200 || !strings.Contains(body, "pong") {
			t.Fatalf("%s: %d %s (tried %s)", model, code, body, tried())
		}
		return tried()
	}
	for range 3 {
		if got := ask("gemini-9-pro"); got != "hubot:gemini-9-pro" {
			t.Fatalf("gemini-9-pro, which only hubot has, went %s", got)
		}
		if got := ask("claude-opus-9"); got != "octocat:claude-opus-9" {
			t.Fatalf("claude-opus-9, which only octocat has, went %s", got)
		}
	}
	// one both have goes round both, In turn
	var went []string
	for range 4 {
		went = append(went, ask("gpt-4.1"))
	}
	if !slices.Contains(went, "octocat:gpt-4.1") || !slices.Contains(went, "hubot:gpt-4.1") {
		t.Fatalf("gpt-4.1 in turn went %v", went)
	}
}

// A model one account's /models lists but Copilot refuses it
// (model_not_supported, as a Student plan is) is that account's no more:
// once refused it goes only to the account that is served it, and stays
// in the provider's list, which the other account still has it in.
func TestCopilotRefusedModelGoesToTheAccountServedIt(t *testing.T) {
	tried := copilotTwoPlansListing(t, []string{"gemini-9-pro"})
	p, err := provider.Find("copilot")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetRouting("copilot", provider.Rotate); err != nil {
		t.Fatal(err)
	}
	s := New()
	ask := func() string {
		t.Helper()
		postAs(t, s, "", `{"model":"copilot/gemini-9-pro","messages":[{"role":"user","content":"ping"}]}`)
		return tried()
	}
	// until octocat is refused it once, rotation may send it there
	for i := 0; !strings.Contains(ask(), "octocat:gemini-9-pro"); i++ {
		if i == 6 {
			t.Fatal("octocat, whose list has gemini-9-pro, was never sent it in turn")
		}
	}
	for range 4 {
		code, body := postAs(t, s, "", `{"model":"copilot/gemini-9-pro","messages":[{"role":"user","content":"ping"}]}`)
		if got := tried(); code != 200 || got != "hubot:gemini-9-pro" {
			t.Fatalf("gemini-9-pro, refused octocat, went %s: %d %s", got, code, body)
		}
	}
	p, err = provider.Find("copilot")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range p.Available() {
		ids = append(ids, m.ID)
	}
	if !slices.Contains(ids, "gemini-9-pro") {
		t.Fatalf("gemini-9-pro, which hubot is served, left the list %v after octocat was refused it", ids)
	}
}
