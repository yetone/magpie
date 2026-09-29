package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// WorkBuddy AI, the international build, is a subscription of its own: its
// own account file, its own endpoint and sign-in platform, its own models,
// and its accounts kept apart from the Chinese build's.
func TestWorkBuddyAI(t *testing.T) {
	home := signIn(t)
	wbTokens.Lock()
	wbTokens.m = map[string]wbCreds{}
	wbTokens.Unlock()

	future := time.Now().Add(24 * time.Hour).UnixMilli()
	own := func(path, uid, name, access, domain string) {
		writeFile(t, path, map[string]any{
			"account": map[string]any{"uid": uid, "nickname": name},
			"auth": map[string]any{"accessToken": access, "refreshToken": "r",
				"expiresAt": future, "refreshExpiresAt": future, "domain": domain},
		})
	}
	aiFile := filepath.Join(filepath.Dir(wbAuthFile(home)), "workbuddy-desktop-ai.info")
	own(wbAuthFile(home), "cn1", "国内", "cn-access", "www.codebuddy.cn")
	own(aiFile, "ai1", "Abroad", "ai-access", "www.codebuddy.ai")

	cn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("WorkBuddy AI asked the Chinese endpoint: %s", r.URL)
		w.WriteHeader(500)
	}))
	defer cn.Close()
	var platform string
	ai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok := func(data any) { json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data}) }
		switch r.URL.Path {
		case "/v2/plugin/auth/state":
			platform = r.URL.Query().Get("platform")
			ok(map[string]any{"state": "s1", "authUrl": "https://www.workbuddy.ai/login?platform=workbuddy-ai&state=s1"})
		case "/v2/plugin/auth/token":
			ok(map[string]any{"accessToken": "ai2-access", "refreshToken": "ai2-refresh",
				"expiresIn": 3600, "refreshExpiresIn": 7200, "domain": "www.codebuddy.ai"})
		case "/v2/plugin/login/account":
			ok(map[string]any{"uid": "ai2", "nickname": "Second"})
		case "/billing/meter/get-user-resource-summary":
			if r.Header.Get("Authorization") != "Bearer ai-access" || r.Header.Get("X-Domain") != "www.codebuddy.ai" {
				w.WriteHeader(401)
				return
			}
			ok(map[string]any{"IsPaidUser": false, "Packages": []any{
				map[string]any{"CycleTotalCapacity": "1000", "CycleUsedCapacity": "100"},
			}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer ai.Close()
	oldCN, oldAI, oldInt := wbEndpoint, wbAIEndpoint, wbPollInterval
	wbEndpoint, wbAIEndpoint, wbPollInterval = cn.URL, ai.URL, 5*time.Millisecond
	defer func() { wbEndpoint, wbAIEndpoint, wbPollInterval = oldCN, oldAI, oldInt }()

	st, err := StartSignIn(WorkBuddyAIID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if st, _ = WaitSignIn(ctx, st.ID); st.State != "done" || st.User != "Second" {
		t.Fatalf("sign-in: %+v", st)
	}
	if platform != "workbuddy-ai" {
		t.Fatalf("platform %q", platform)
	}

	names := func(agent string) (out []string) {
		for _, l := range Logins(agent) {
			out = append(out, l.User)
		}
		return out
	}
	if got := names(WorkBuddyAIID); len(got) != 2 || got[0] != "Abroad" || got[1] != "Second" {
		t.Fatalf("WorkBuddy AI logins: %v", got)
	}
	if got := names("workbuddy"); len(got) != 1 || got[0] != "国内" {
		t.Fatalf("WorkBuddy logins: %v", got)
	}

	p, ok := find(All(), WorkBuddyAIID)
	if !ok || p.Name != "WorkBuddy AI" || p.Account.User != "Abroad" || p.Chat != ai.URL+"/v2" || p.Account.Agent != WorkBuddyAIID {
		t.Fatalf("provider: %+v", p)
	}
	if ms := p.Account.models(); len(ms) != len(wbAIModels) || ms[0].ID != "default-model" {
		t.Fatalf("models: %+v", ms)
	}
	req, _ := http.NewRequest("POST", p.Chat+"/chat/completions", nil)
	if err := p.Sign(context.Background(), req, Chat, []byte(`{}`)); err != nil ||
		req.Header.Get("Authorization") != "Bearer ai-access" || req.Header.Get("X-Domain") != "www.codebuddy.ai" {
		t.Fatalf("sign: %v %v", err, req.Header)
	}
	// a chat with no system prompt is given one (#124)
	if got := string(p.Prepare([]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))); !strings.HasPrefix(got, `{"messages":[{"content":"`+wbSystem+`","role":"system"},{"content":"hi","role":"user"}]`) {
		t.Errorf("prepare: %s", got)
	}
	// what WorkBuddy's own chats carry besides the account's
	for h, want := range map[string]string{"X-Requested-With": "XMLHttpRequest", "X-Agent-Intent": "craft",
		"X-IDE-Type": "WorkBuddy", "X-IDE-Name": "WorkBuddy", "X-IDE-Version": wbUAVersion, "X-Product": "SaaS"} {
		if got := req.Header.Get(h); got != want {
			t.Errorf("%s: %q, want %q", h, got, want)
		}
	}
	for _, h := range []string{"X-Conversation-ID", "X-Conversation-Request-ID", "X-Conversation-Message-ID", "X-Request-ID"} {
		if len(req.Header.Get(h)) != 32 {
			t.Errorf("%s: %q", h, req.Header.Get(h))
		}
	}
	if req.Header.Get("X-Request-ID") == req.Header.Get("X-Conversation-ID") {
		t.Error("the request id is the conversation's")
	}
	if also := p.AlsoOn(); len(also) != 1 || also[0].Account.User != "Second" || also[0].ID != WorkBuddyAIID {
		t.Fatalf("also on: %+v", also)
	}
	if cnp, ok := find(All(), "workbuddy"); !ok || cnp.Account.User != "国内" || cnp.Chat != cn.URL+"/v2" {
		t.Fatalf("WorkBuddy: %+v", cnp)
	}

	q := LoginUsage(context.Background(), WorkBuddyAIID)["Abroad"]
	if q.Error != "" || q.Provider != WorkBuddyAIID || q.Plan != "Free" || len(q.Windows) != 1 || q.Windows[0].Used != 10 {
		t.Fatalf("usage: %+v", q)
	}

	if err := ForgetLogin(WorkBuddyAIID, "Second"); err != nil {
		t.Fatal(err)
	}
	os.Remove(aiFile)
	if _, ok := find(All(), WorkBuddyAIID); ok {
		t.Fatal("WorkBuddy AI without an account")
	}
	if _, ok := find(All(), "workbuddy"); !ok {
		t.Fatal("WorkBuddy went with it")
	}
}
