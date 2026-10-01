package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// zcodeFakeAccount is an account on Z.ai or BigModel as ZCode's sign-in
// hands it back.
type zcodeFakeAccount struct {
	site, uid, email, name string
	secret                 string // its zcode-api-key's secret
}

// newZCodeAccountsUpstream is zcode.z.ai, Z.ai and BigModel for these
// tests: each sign-in's poll answers with next's account, whose own
// project holds its zcode-api-key, k-<uid>.<secret>, on a GLM Coding Plan.
func newZCodeAccountsUpstream(t *testing.T) (next func(zcodeFakeAccount)) {
	t.Helper()
	var (
		mu    sync.Mutex
		flows = map[string]zcodeFakeAccount{}
		queue []zcodeFakeAccount
		byTok = map[string]zcodeFakeAccount{} // business Authorization → account
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		auth := r.Header.Get("Authorization")
		ok := func(data any) { json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data}) }
		p := r.URL.Path
		switch {
		case p == "/api/v1/oauth/cli/init":
			if len(queue) == 0 {
				w.WriteHeader(400)
				return
			}
			id := fmt.Sprintf("f%d", len(flows)+1)
			flows[id], queue = queue[0], queue[1:]
			ok(map[string]any{"flow_id": id, "authorize_url": "https://chat.z.ai/oauth?client_id=c",
				"expires_at": time.Now().Add(time.Minute).Unix(), "poll_interval_sec": 0})
		case strings.HasPrefix(p, "/api/v1/oauth/cli/poll/"):
			a := flows[strings.TrimPrefix(p, "/api/v1/oauth/cli/poll/")]
			tok := "tok-" + a.uid
			got := map[string]any{"status": "ready", "token": "zc-" + a.uid,
				"user": map[string]any{"user_id": a.uid, "email": a.email, "name": a.name}}
			if a.site == "bigmodel" {
				got["bigmodel"] = map[string]any{"accessToken": tok}
				byTok[tok] = a
			} else {
				got["zai"] = map[string]any{"access_token": tok}
				byTok["Bearer biz-"+a.uid] = a
			}
			ok(got)
		case p == "/api/auth/z/login":
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			uid := strings.TrimPrefix(body["token"], "tok-")
			ok(map[string]any{"access_token": "biz-" + uid})
		case p == "/api/biz/customer/getCustomerInfo":
			a, found := byTok[auth]
			if !found {
				w.WriteHeader(401)
				return
			}
			ok(map[string]any{"organizations": []any{map[string]any{"organizationId": "o-" + a.uid, "organizationName": "默认机构",
				"projects": []any{map[string]any{"projectId": "p-" + a.uid, "projectName": "默认项目", "projectType": "1"}}}}})
		case strings.HasSuffix(p, "/api_keys"):
			a, found := byTok[auth]
			if !found || p != "/api/biz/v1/organization/o-"+a.uid+"/projects/p-"+a.uid+"/api_keys" {
				w.WriteHeader(401)
				return
			}
			ok([]any{map[string]any{"name": "zcode-api-key", "apiKey": "k-" + a.uid}})
		case strings.Contains(p, "/api_keys/copy/"):
			a, found := byTok[auth]
			if !found || !strings.HasSuffix(p, "/copy/k-"+a.uid) {
				w.WriteHeader(401)
				return
			}
			ok(map[string]any{"secretKey": a.secret})
		case p == "/api/biz/subscription/list":
			ok([]any{map[string]any{"productName": "GLM Coding Pro", "status": "VALID"}})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	oldAPI, oldZai, oldBM, oldZaiBase, oldBMBase := zcodeAPI, zcodeZaiAPI, zcodeBigModelAPI, ZCodeZaiBase, ZCodeBigModelBase
	zcodeAPI, zcodeZaiAPI, zcodeBigModelAPI = srv.URL, srv.URL, srv.URL
	ZCodeZaiBase, ZCodeBigModelBase = srv.URL+"/zai/api/anthropic", srv.URL+"/bigmodel/api/anthropic"
	t.Cleanup(func() {
		zcodeAPI, zcodeZaiAPI, zcodeBigModelAPI, ZCodeZaiBase, ZCodeBigModelBase = oldAPI, oldZai, oldBM, oldZaiBase, oldBMBase
	})
	return func(a zcodeFakeAccount) {
		mu.Lock()
		defer mu.Unlock()
		queue = append(queue, a)
	}
}

func zcodeSignInAs(t *testing.T, next func(zcodeFakeAccount), a zcodeFakeAccount) SignInState {
	t.Helper()
	next(a)
	agent := "zcode"
	if a.site == "bigmodel" {
		agent = "zcode:bigmodel"
	}
	st, err := StartSignIn(agent)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, _ = WaitSignIn(ctx, st.ID)
	if st.State != "done" {
		t.Fatalf("sign-in %s: %+v", a.uid, st)
	}
	return st
}

// zcodeKeysByUser is every ZCode account magpie keeps, by name: its key.
func zcodeKeysByUser() map[string]string {
	out := map[string]string{}
	for _, l := range zcodeLogins() {
		out[l.User] = l.key.Key
	}
	return out
}

// Bandit on Discord: a Z.ai and a BigModel account on the same phone
// number, and then another Z.ai account, each took the place of the one
// added before, as magpie kept ZCode's accounts by name alone. Each is kept
// on its own now; signing in to one again updates it in place.
func TestZCodeAccountsKeptApart(t *testing.T) {
	signIn(t)
	t.Setenv("ZCODE_CREDENTIAL_SECRET", "test-secret")
	next := newZCodeAccountsUpstream(t)

	const phone = "13800000000"
	zai := zcodeFakeAccount{site: "zai", uid: "za1", email: phone + "@phone.local", secret: "s1"}
	bm := zcodeFakeAccount{site: "bigmodel", uid: "bm1", email: phone + "@phone.local", secret: "s2"}
	zai2 := zcodeFakeAccount{site: "zai", uid: "za2", email: phone + "@phone.local", secret: "s3"} // named alike by ZCode

	if st := zcodeSignInAs(t, next, zai); st.User != phone {
		t.Fatalf("z.ai: %+v", st)
	}
	if st := zcodeSignInAs(t, next, bm); st.User != phone+" (BigModel)" {
		t.Fatalf("bigmodel: %+v", st)
	}
	if st := zcodeSignInAs(t, next, zai2); st.User != phone+" (Z.ai)" {
		t.Fatalf("second z.ai: %+v", st)
	}
	want := map[string]string{phone: "k-za1.s1", phone + " (BigModel)": "k-bm1.s2", phone + " (Z.ai)": "k-za2.s3"}
	if got := zcodeKeysByUser(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("accounts: %v, want %v", got, want)
	}
	for _, l := range zcodeLogins() {
		if (l.User == phone+" (BigModel)") != (l.key.Base == ZCodeBigModelBase) {
			t.Fatalf("%s served at %s", l.User, l.key.Base)
		}
	}

	// each signed in again: updated in place under its own name
	zai.secret, bm.secret, zai2.secret = "s1b", "s2b", "s3b"
	for _, a := range []zcodeFakeAccount{bm, zai2, zai} {
		zcodeSignInAs(t, next, a)
	}
	want = map[string]string{phone: "k-za1.s1b", phone + " (BigModel)": "k-bm1.s2b", phone + " (Z.ai)": "k-za2.s3b"}
	if got := zcodeKeysByUser(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("after signing in again: %v, want %v", got, want)
	}
}

// An account kept before magpie kept ids is told by its key: another
// account named alike is kept beside it, and it, signed in again, is
// updated in place and given its id.
func TestZCodeOldAccountKeptByKey(t *testing.T) {
	signIn(t)
	t.Setenv("ZCODE_CREDENTIAL_SECRET", "test-secret")
	next := newZCodeAccountsUpstream(t)
	auth, _ := json.Marshal(zcodeKey{Key: "k-za1.old", Base: ZCodeZaiBase})
	if err := addSideLogin(savedLogin{Agent: "zcode", User: "bandit@example.com", Plan: "GLM Coding Pro", Auth: auth}, "", func(savedLogin) {}); err != nil {
		t.Fatal(err)
	}

	if st := zcodeSignInAs(t, next, zcodeFakeAccount{site: "zai", uid: "za9", email: "bandit@example.com", secret: "s9"}); st.User != "bandit@example.com (Z.ai)" {
		t.Fatalf("another account: %+v", st)
	}
	if st := zcodeSignInAs(t, next, zcodeFakeAccount{site: "zai", uid: "za1", email: "bandit@example.com", secret: "new"}); st.User != "bandit@example.com" {
		t.Fatalf("the old one again: %+v", st)
	}
	want := map[string]string{"bandit@example.com": "k-za1.new", "bandit@example.com (Z.ai)": "k-za9.s9"}
	if got := zcodeKeysByUser(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("accounts: %v, want %v", got, want)
	}
	for _, l := range zcodeLogins() {
		if l.User == "bandit@example.com" && l.key.UID != "za1" {
			t.Fatalf("old account's id: %+v", l.key)
		}
	}
}

// ZCode's own account, on Z.ai by a phone number: the BigModel account on
// that number, signed in from magpie, was taken for it and never kept.
func TestZCodeOwnAccountKeptApart(t *testing.T) {
	home := signIn(t)
	t.Setenv("ZCODE_CREDENTIAL_SECRET", "test-secret")
	next := newZCodeAccountsUpstream(t)
	writeFile(t, filepath.Join(home, ".zcode", "v2", "credentials.json"), map[string]any{
		"oauth:zai:user_info": zcodeEncrypt(t, `{"user_id":"za1","email":"13800000000@phone.local"}`),
		"account-provider:coding-plan:account:zai-individual-coding-plan:account:abc:api-key": zcodeEncrypt(t, "k-za1.own"),
	})
	if st := zcodeSignInAs(t, next, zcodeFakeAccount{site: "bigmodel", uid: "bm1", email: "13800000000@phone.local", secret: "s2"}); st.User != "13800000000 (BigModel)" || st.Using {
		t.Fatalf("bigmodel: %+v", st)
	}
	want := map[string]string{"13800000000": "k-za1.own", "13800000000 (BigModel)": "k-bm1.s2"}
	if got := zcodeKeysByUser(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("accounts: %v, want %v", got, want)
	}
	// ZCode's own, signed in from magpie too: it, not another
	if st := zcodeSignInAs(t, next, zcodeFakeAccount{site: "zai", uid: "za1", email: "13800000000@phone.local", secret: "own"}); st.User != "13800000000" || !st.Using {
		t.Fatalf("own again: %+v", st)
	}
	if got := zcodeKeysByUser(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("accounts after: %v, want %v", got, want)
	}
}
