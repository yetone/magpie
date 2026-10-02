package provider

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// zcodeTestJWT is a token shaped as ZCode's, running out at exp.
func zcodeTestJWT(exp time.Time) string {
	b := base64.RawURLEncoding.EncodeToString
	claims, _ := json.Marshal(map[string]any{"sub": "u1", "exp": exp.Unix()})
	return b([]byte(`{"alg":"HS256","typ":"JWT"}`)) + "." + b(claims) + ".sig"
}

// zcodeStartUpstream is Z.ai and zcode.z.ai for these tests: plans is what
// /api/biz/subscription/list says of every key, and balance what
// /api/v1/zcode-plan/billing/balance says to jwt.
type zcodeStartUpstream struct {
	srv     *httptest.Server
	plans   []any
	balance map[string]any
	jwt     string
	model   *http.Request // the last request to the Start Plan's endpoint
	body    []byte        // and its body
}

func newZCodeStartUpstream(t *testing.T, jwt string) *zcodeStartUpstream {
	t.Helper()
	u := &zcodeStartUpstream{jwt: jwt, plans: []any{}}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		ok := func(data any) { json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data}) }
		switch {
		case r.URL.Path == "/api/auth/z/login":
			ok(map[string]any{"access_token": "biz"})
		case auth == "Bearer biz" && r.URL.Path == "/api/biz/customer/getCustomerInfo":
			ok(map[string]any{"organizations": []any{map[string]any{"organizationId": "o1", "organizationName": "默认机构",
				"projects": []any{map[string]any{"projectId": "p1", "projectName": "默认项目", "projectType": "1"}}}}})
		case auth == "Bearer biz" && r.URL.Path == "/api/biz/v1/organization/o1/projects/p1/api_keys":
			ok([]any{map[string]any{"name": "zcode-api-key", "apiKey": "key"}})
		case auth == "Bearer biz" && r.URL.Path == "/api/biz/v1/organization/o1/projects/p1/api_keys/copy/key":
			ok(map[string]any{"secretKey": "secret"})
		case r.URL.Path == "/api/biz/subscription/list":
			if auth != "key.secret" {
				w.WriteHeader(401)
				return
			}
			ok(u.plans)
		case r.URL.Path == "/api/v1/zcode-plan/billing/balance":
			if r.Header.Get("X-Device-Mid") == "" { // as zcode.z.ai does (#282)
				w.WriteHeader(400)
				w.Write([]byte(`{"code":3001,"msg":"parameter error"}`))
				return
			}
			if auth != "Bearer "+u.jwt || r.URL.Query().Get("app_version") == "" {
				w.WriteHeader(401)
				return
			}
			ok(u.balance)
		case strings.HasPrefix(r.URL.Path, "/api/v1/zcode-plan/anthropic/"):
			u.model = r
			u.body, _ = io.ReadAll(r.Body)
			w.Write([]byte(`{}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(u.srv.Close)
	oldAPI, oldZai, oldBase := zcodeAPI, zcodeZaiAPI, ZCodeZaiBase
	zcodeAPI, zcodeZaiAPI, ZCodeZaiBase = u.srv.URL, u.srv.URL, u.srv.URL+"/api/anthropic"
	t.Cleanup(func() { zcodeAPI, zcodeZaiAPI, ZCodeZaiBase = oldAPI, oldZai, oldBase })
	zcodeRoutes.Lock()
	zcodeRoutes.m = map[string]zcodeRoute{}
	zcodeRoutes.Unlock()
	return u
}

// a Start Plan with one bucket, GLM-5.1's tokens for the day, a quarter used
func zcodeActiveStart(now time.Time, status string) map[string]any {
	return map[string]any{
		"server_time": now.Unix(),
		"plans": []any{map[string]any{"plan_id": "zai-start-plan", "user_plan_id": "up1", "name": "Start Plan", "status": status,
			"ends_at":      now.Add(7 * 24 * time.Hour).Unix(),
			"entitlements": []any{map[string]any{"entitlement_id": "e1", "period": "daily"}}}},
		"balances": []any{map[string]any{"plan_id": "zai-start-plan", "user_plan_id": "up1", "entitlement_id": "e1",
			"show_name": "GLM-5.1", "capabilities": []any{"model:GLM-5.1"},
			"total_units": "1000000", "used_units": 250000, "remaining_units": 750000,
			"expires_at": now.Add(time.Hour).Unix()}},
	}
}

// #236: ZCode signed in to an account with only its Start Plan (体验套餐)
// keeps no coding plan key, just its own session token. magpie reads that
// account, sends its requests where ZCode does, with that token, and shows
// the Start Plan's allowance.
func TestZCodeStartPlanOwnAccount(t *testing.T) {
	home := signIn(t)
	t.Setenv("ZCODE_CREDENTIAL_SECRET", "test-secret")
	jwt := zcodeTestJWT(time.Now().Add(24 * time.Hour))
	u := newZCodeStartUpstream(t, jwt)
	now := time.Now()
	u.balance = zcodeActiveStart(now, "active")
	writeFile(t, filepath.Join(home, ".zcode", "v2", "credentials.json"), map[string]any{
		"oauth:bigmodel:user_info": zcodeEncrypt(t, `{"user_id":"u1","email":"trial@example.com"}`),
		"zcodejwttoken":            zcodeEncrypt(t, "Bearer "+jwt),
		"oauth:active_provider":    zcodeEncrypt(t, "bigmodel"),
	})

	who, k, ok := zcodeOwn()
	if !ok || who != "trial@example.com" || k.Key != "" || k.JWT != jwt {
		t.Fatalf("own: %v %q %+v", ok, who, k)
	}
	p, ok := find(All(), "zcode")
	if !ok || p.Account.User != "trial@example.com" || p.Anthropic != u.srv.URL+"/api/v1/zcode-plan/anthropic" {
		t.Fatalf("provider: %v %+v", ok, p)
	}
	req, _ := http.NewRequest("POST", p.Anthropic+"/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer magpie")
	if err := p.Sign(context.Background(), req, Anthropic, []byte(`{}`)); err != nil ||
		req.Header.Get("x-api-key") != "" || req.Header.Get("Authorization") != "Bearer "+jwt ||
		req.Header.Get("X-ZCode-App-Version") == "" || req.URL.Path != "/api/v1/zcode-plan/anthropic/v1/messages" {
		t.Fatalf("signs with ZCode's token: %v %s %v", err, req.URL, req.Header)
	}
	if ms := p.Account.models(); len(ms) != len(zcodeStartModels) {
		t.Fatalf("models: %+v", ms)
	}
	if id := zcodePlanID(p.Anthropic); id != "account:zai-start-plan" {
		t.Fatalf("plan id: %s", id)
	}

	q := LoginUsage(context.Background(), "zcode")["trial@example.com"]
	if q.Error != "" || q.Plan != "Start Plan" || q.Until == nil || q.Renew != "off" || len(q.Windows) != 1 {
		t.Fatalf("usage: %+v", q)
	}
	w := q.Windows[0]
	if w.Name != "GLM-5.1" || w.Used != 25 || w.Display != compactNumber(250000)+" / "+compactNumber(1000000) ||
		w.ResetsAt == nil || w.Span != 24*time.Hour || w.matches == nil || !w.matches("glm-5.1") || w.matches("GLM-5-Turbo") {
		t.Fatalf("window: %+v", w)
	}

	// the Start Plan over: none of its buckets, and it says so
	u.balance = zcodeActiveStart(now, "expired")
	if q := zcodeQuota(context.Background(), Login{User: "trial@example.com"}, k); q.Error == "" || len(q.Windows) != 0 {
		t.Fatalf("usage over: %+v", q)
	}
	// still "active" past its end is over too, as ZCode reads it
	u.balance = zcodeActiveStart(now, "active")
	u.balance["server_time"] = now.Add(8 * 24 * time.Hour).Unix()
	if b, err := zcodeStartBalance(context.Background(), jwt); err != nil || len(b.Balances) != 0 {
		t.Fatalf("past its end: %v %+v", err, b)
	} else if _, _, ok := b.active(); ok {
		t.Fatal("past its end is active")
	}
}

// An account with a coding plan key and ZCode's token goes to the Coding
// Plan while it has one, and to the Start Plan when it has none: the 1113
// "Insufficient balance or no resource package" of #236 was the key's
// requests going to the Coding Plan's endpoint with no Coding Plan.
func TestZCodeStartPlanRouting(t *testing.T) {
	signIn(t)
	jwt := zcodeTestJWT(time.Now().Add(24 * time.Hour))
	u := newZCodeStartUpstream(t, jwt)
	u.balance = zcodeActiveStart(time.Now(), "active")
	k := zcodeKey{Key: "key.secret", Base: ZCodeZaiBase, JWT: jwt}

	sign := func() *http.Request {
		t.Helper()
		p := zcodeProvider("a@example.com", "", k)
		req, _ := http.NewRequest("POST", p.Anthropic+"/v1/messages?beta=true", nil)
		if err := p.Sign(context.Background(), req, Anthropic, []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
		return req
	}
	// no Coding Plan: the Start Plan, with ZCode's token
	req := sign()
	if req.URL.String() != u.srv.URL+"/api/v1/zcode-plan/anthropic/v1/messages?beta=true" || req.Header.Get("Authorization") != "Bearer "+jwt {
		t.Fatalf("no coding plan: %s %v", req.URL, req.Header)
	}
	if q := zcodeQuota(context.Background(), Login{User: "a@example.com"}, k); q.Plan != "Start Plan" || len(q.Windows) != 1 {
		t.Fatalf("quota: %+v", q)
	}
	// and the request reaches it
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 || u.model == nil || u.model.Header.Get("Authorization") != "Bearer "+jwt {
		t.Fatalf("sent: %v %v", err, res)
	}
	res.Body.Close()

	// with a Coding Plan: api.z.ai with the key, as before
	u.plans = []any{map[string]any{"productName": "GLM Coding Lite", "status": "VALID"}}
	zcodeRoutes.Lock()
	zcodeRoutes.m = map[string]zcodeRoute{}
	zcodeRoutes.Unlock()
	req = sign()
	if req.URL.String() != u.srv.URL+"/api/anthropic/v1/messages?beta=true" || req.Header.Get("x-api-key") != "key.secret" || req.Header.Get("X-ZCode-App-Version") != "" {
		t.Fatalf("coding plan: %s %v", req.URL, req.Header)
	}
	// with no ZCode token at all: the Coding Plan, asking nothing
	if zcodeOnStart(context.Background(), zcodeKey{Key: "x.y", Base: ZCodeZaiBase}) {
		t.Fatal("no token on the Start Plan")
	}

	// ZCode's token run out: sign in again
	old := zcodeKey{Base: ZCodeZaiBase, JWT: zcodeTestJWT(time.Now().Add(-time.Hour))}
	p := zcodeProvider("b@example.com", "", old)
	req, _ = http.NewRequest("POST", p.Anthropic+"/v1/messages", nil)
	if err := p.Sign(context.Background(), req, Anthropic, nil); !errors.Is(err, errZCodeExpired) {
		t.Fatalf("expired: %v", err)
	}
}

// Signing in to an account with no Coding Plan was refused ("no GLM Coding
// Plan"); with ZCode's Start Plan it is added on that, and refused, saying
// both, when that is over too.
func TestZCodeSignInStartPlan(t *testing.T) {
	signIn(t)
	jwt := zcodeTestJWT(time.Now().Add(24 * time.Hour))
	u := newZCodeStartUpstream(t, jwt)
	u.balance = zcodeActiveStart(time.Now(), "active")
	ctx := context.Background()

	k, plan, err := zcodeSignedIn(ctx, "zai", "zai-tok", jwt)
	if err != nil || plan != "Start Plan" || k.Key != "key.secret" || k.JWT != jwt || !zcodeOnStart(ctx, k) {
		t.Fatalf("start plan: %v %q %+v", err, plan, k)
	}
	u.plans = []any{map[string]any{"productName": "GLM Coding Pro", "status": "VALID"}}
	if k, plan, err := zcodeSignedIn(ctx, "zai", "zai-tok", jwt); err != nil || plan != "GLM Coding Pro" || k.JWT != jwt {
		t.Fatalf("coding plan: %v %q %+v", err, plan, k)
	}
	u.plans = []any{}
	u.balance = zcodeActiveStart(time.Now(), "expired")
	if _, _, err := zcodeSignedIn(ctx, "zai", "zai-tok", jwt); err == nil || !strings.Contains(err.Error(), "Start Plan") {
		t.Fatalf("neither: %v", err)
	}
	if _, _, err := zcodeSignedIn(ctx, "zai", "zai-tok", ""); err == nil {
		t.Fatal("no token, no plan: signed in")
	}
}

// ARNO on Discord (v0.1.639): the Start Plan turned magpie's request away
// with 405 / code 3012, while an OpenCode plugin run in magpie's plugin host
// got through on the same machine and account, its headers and body the
// same. What differed was the wire: Bun's fetch, ZCode's own Node fetch and
// zcode2api-plus all speak HTTP/1.1 to zcode.z.ai, and the gateway's client
// HTTP/2 wherever it is offered. A Start Plan request goes over HTTP/1.1
// whatever client it is given; a Coding Plan account's still goes through
// that client.
func TestZCodeStartHTTP1(t *testing.T) {
	var mu sync.Mutex
	var got []string
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.URL.Path+" "+r.Proto)
		mu.Unlock()
		w.Write([]byte(`{}`))
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	oldAPI, oldBase, oldTLS := zcodeAPI, ZCodeZaiBase, zcodeStartTransport.TLSClientConfig
	zcodeAPI, ZCodeZaiBase = srv.URL, srv.URL+"/api/anthropic"
	zcodeStartTransport.TLSClientConfig = &tls.Config{RootCAs: pool}
	t.Cleanup(func() {
		zcodeStartTransport.CloseIdleConnections()
		zcodeAPI, ZCodeZaiBase, zcodeStartTransport.TLSClientConfig = oldAPI, oldBase, oldTLS
	})
	gateway := srv.Client() // speaks HTTP/2, as the gateway's client does
	send := func(p Provider) {
		t.Helper()
		body := []byte(`{"model":"GLM-5.3-Flash","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
		req, _ := http.NewRequest(http.MethodPost, p.Base(Anthropic)+"/v1/messages", bytes.NewReader(body))
		if err := p.Sign(context.Background(), req, Anthropic, body); err != nil {
			t.Fatal(err)
		}
		res, err := p.Do(gateway, req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
	}
	send(zcodeProvider("trial@example.com", "Start Plan", zcodeKey{Base: ZCodeZaiBase, JWT: zcodeTestJWT(time.Now().Add(time.Hour))}))
	send(zcodeProvider("pro@example.com", "GLM Coding Pro", zcodeKey{Key: "id.secret", Base: ZCodeZaiBase}))
	want := []string{"/api/v1/zcode-plan/anthropic/v1/messages HTTP/1.1", "/api/anthropic/v1/messages HTTP/2.0"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("sent:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
