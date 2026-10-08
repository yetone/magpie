package provider

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// zcodeEncrypt stores a value as ZCode's credential store does.
func zcodeEncrypt(t *testing.T, v string) string {
	t.Helper()
	block, _ := aes.NewCipher(zcodeSecret())
	gcm, _ := cipher.NewGCM(block)
	iv := []byte("0123456789ab")
	sealed := gcm.Seal(nil, iv, []byte(v), nil)
	ct, tag := sealed[:len(sealed)-16], sealed[len(sealed)-16:]
	b := base64.RawURLEncoding.EncodeToString
	return "enc:v1:" + b(iv) + "." + b(tag) + "." + b(ct)
}

// ZCode's own account is read from its credential store; a second, signed
// in from magpie with ZCode's sign-in, is kept beside it with its own key.
func TestZCodeAccounts(t *testing.T) {
	home := signIn(t)
	t.Setenv("ZCODE_CREDENTIAL_SECRET", "test-secret")
	writeFile(t, filepath.Join(home, ".zcode", "v2", "credentials.json"), map[string]any{
		"oauth:zai:user_info": zcodeEncrypt(t, `{"user_id":"u1","email":"13800000000@phone.local","name":"旅行者0000"}`),
		"account-provider:coding-plan:account:zai-individual-coding-plan:account:abc:api-key": zcodeEncrypt(t, "own.secret"),
		"oauth:zai:access_token": zcodeEncrypt(t, "jwt"),
	})

	var polls atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		ok := func(data any) { json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data}) }
		switch {
		case r.URL.Path == "/api/v1/oauth/cli/init":
			if !strings.HasPrefix(auth, "Bearer ") {
				w.WriteHeader(401)
				return
			}
			ok(map[string]any{"flow_id": "f1", "authorize_url": "https://chat.z.ai/oauth?client_id=c&redirect_uri=x",
				"expires_at": time.Now().Add(time.Minute).Unix(), "poll_interval_sec": 0})
		case r.URL.Path == "/api/v1/oauth/cli/poll/f1":
			if polls.Add(1) < 2 {
				ok(map[string]any{"status": "pending"})
				return
			}
			ok(map[string]any{"status": "ready", "token": "zc", "zai": map[string]any{"access_token": "zai-tok"},
				"user": map[string]any{"user_id": "u2", "email": "two@example.com", "name": "Two"}})
		case r.URL.Path == "/api/auth/z/login":
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["token"] != "zai-tok" {
				w.WriteHeader(401)
				return
			}
			ok(map[string]any{"access_token": "biz"})
		case auth == "Bearer biz" && r.URL.Path == "/api/biz/customer/getCustomerInfo":
			ok(map[string]any{"organizations": []any{
				map[string]any{"organizationId": "o0", "organizationName": "other", "projects": []any{map[string]any{"projectId": "p0", "projectName": "x", "projectType": "1"}}},
				map[string]any{"organizationId": "o1", "organizationName": "默认机构", "projects": []any{
					map[string]any{"projectId": "p9", "projectName": "api", "projectType": "2"},
					map[string]any{"projectId": "p1", "projectName": "默认项目", "projectType": "1"},
				}},
			}})
		case auth == "Bearer biz" && r.URL.Path == "/api/biz/v1/organization/o1/projects/p1/api_keys":
			if r.Method == http.MethodPost {
				ok(map[string]any{"name": "zcode-api-key", "apiKey": "two"})
				return
			}
			ok([]any{map[string]any{"name": "other", "apiKey": "nope"}})
		case auth == "Bearer biz" && r.URL.Path == "/api/biz/v1/organization/o1/projects/p1/api_keys/copy/two":
			ok(map[string]any{"secretKey": "secret2"})
		case r.URL.Path == "/api/biz/subscription/list":
			if auth != "two.secret2" && auth != "own.secret" {
				w.WriteHeader(401)
				return
			}
			ok([]any{map[string]any{"productName": "GLM Coding Pro", "status": "VALID"}})
		case r.URL.Path == "/api/biz/customer-package-reset/list":
			// the account's own resets, asked with its key (#1191)
			if auth != "two.secret2" || r.URL.Query().Get("targetType") != "PERSONAL" {
				w.WriteHeader(401)
				return
			}
			w.Write([]byte(zhipuResetList))
		case r.URL.Path == "/api/monitor/usage/quota/limit":
			if auth != "two.secret2" && auth != "own.secret" {
				w.WriteHeader(401)
				return
			}
			reset := time.Now().Add(time.Hour).UnixMilli()
			ok(map[string]any{"level": "pro", "limits": []any{
				map[string]any{"type": "CREDIT_LIMIT", "unit": 3, "number": 5, "usage": 2000, "remaining": 1500, "percentage": 25, "nextResetTime": reset},
				map[string]any{"type": "CREDIT_LIMIT", "unit": 6, "number": 1, "usage": 10000, "remaining": 9000, "percentage": 10, "nextResetTime": reset},
			}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	oldAPI, oldZai, oldBase := zcodeAPI, zcodeZaiAPI, ZCodeZaiBase
	zcodeAPI, zcodeZaiAPI, ZCodeZaiBase = srv.URL, srv.URL, srv.URL+"/api/anthropic"
	defer func() { zcodeAPI, zcodeZaiAPI, ZCodeZaiBase = oldAPI, oldZai, oldBase }()

	// ZCode's own, named by its phone number
	who, k, ok := zcodeOwn()
	if !ok || who != "13800000000" || k.Key != "own.secret" {
		t.Fatalf("own: %v %q %+v", ok, who, k)
	}

	st, err := StartSignIn("zcode")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(st.URL)
	if u.Host != "chat.z.ai" || !strings.Contains(u.Query().Get("redirect_uri"), "zcode%3A%2F%2Foauth%2Fcallback") && !strings.Contains(u.Query().Get("redirect_uri"), "zcode://oauth/callback") {
		t.Fatalf("sign-in page: %s", st.URL)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, _ = WaitSignIn(ctx, st.ID)
	if st.State != "done" || st.User != "two@example.com" || st.Plan != "GLM Coding Pro" || st.Using {
		t.Fatalf("done: %+v", st)
	}

	var users []string
	for _, l := range Logins("zcode") {
		users = append(users, l.User+map[bool]string{true: "*", false: ""}[l.Active]+map[bool]string{true: "+", false: ""}[l.On])
	}
	if got := strings.Join(users, " "); got != "13800000000*+ two@example.com+" {
		t.Fatalf("logins: %s", got)
	}
	p, ok := find(All(), "zcode")
	if !ok || p.Account.User != "13800000000" || p.Anthropic != srv.URL+"/api/anthropic" {
		t.Fatalf("first: %+v", p)
	}
	also := p.AlsoOn()
	if len(also) != 1 || also[0].Account.User != "two@example.com" {
		t.Fatalf("also on: %+v", also)
	}
	req, _ := http.NewRequest("POST", also[0].Anthropic+"/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer magpie")
	if err := also[0].Sign(context.Background(), req, Anthropic, []byte(`{}`)); err != nil ||
		req.Header.Get("x-api-key") != "two.secret2" || req.Header.Get("Authorization") != "Bearer two.secret2" {
		t.Fatalf("signs with its own key: %v %v", err, req.Header)
	}
	if ms := also[0].Account.models(); len(ms) != len(zcodeModels) || ms[0].Context != 1_000_000 {
		t.Fatalf("models: %+v", ms)
	}

	use := LoginUsage(context.Background(), "zcode")
	q := use["two@example.com"]
	if q.Error != "" || q.Plan != "GLM Coding Pro" || len(q.Windows) != 2 || q.Windows[0].Name != "5 hours" || q.Windows[0].Used != 25 ||
		q.Windows[1].Name != "Weekly" || q.Windows[1].Used != 10 || q.Windows[0].ResetsAt == nil {
		t.Fatalf("usage: %+v", q)
	}
	if r := q.Resets; r == nil || r.Count != 2 || r.FiveHour != 1 || r.Weekly != 1 || r.Team {
		t.Fatalf("resets: %+v", r)
	}

	if err := SwitchLogin("zcode", "two@example.com"); err != nil {
		t.Fatal(err)
	}
	if p, _ := find(All(), "zcode"); p.Account.User != "two@example.com" {
		t.Fatalf("after switch: %+v", p.Account)
	}
	if err := SwitchLogin("zcode", "13800000000"); err != nil {
		t.Fatal(err)
	}
	if err := ForgetLogin("zcode", "two@example.com"); err != nil {
		t.Fatal(err)
	}
	if ls := Logins("zcode"); len(ls) != 1 {
		t.Fatalf("after forget: %+v", ls)
	}

	// signed out of ZCode, with none of magpie's: no ZCode subscription
	os.Remove(filepath.Join(home, ".zcode", "v2", "credentials.json"))
	if _, ok := find(All(), "zcode"); ok {
		t.Fatal("zcode without an account")
	}
}
