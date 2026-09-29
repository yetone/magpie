package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/qoder"
)

type qoderTransport func(*http.Request) (*http.Response, error)

func (f qoderTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func qoderTestClient(t *testing.T, h http.Handler) *http.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	client := &http.Client{Transport: qoderTransport(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme, r.URL.Host = u.Scheme, u.Host
		return srv.Client().Transport.RoundTrip(r)
	})}
	old := qoderClient
	qoderClient = client
	t.Cleanup(func() { qoderClient = old })
	return client
}

func qoderTestCredential(user string) qoder.Credential {
	return qoder.Credential{UID: user, Email: user + "@x", Token: "jt-" + user, RefreshToken: "rt-" + user,
		DeviceToken: "dt-" + user, MachineID: "machine-" + user, ExpiresAt: time.Now().Add(time.Hour).UnixMilli(),
		Models: json.RawMessage(`{"chat":[{"key":"other-model","enable":true,"display_name":"Other","source":"system","format":"openai","is_reasoning":false,"max_input_tokens":64000,"custom_field":17}]}`)}
}

func TestQoderConcurrentRefresh(t *testing.T) {
	signIn(t)
	var hits atomic.Int32
	client := qoderTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != qoder.JobTokenRefreshPath {
			t.Errorf("path %q", r.URL.Path)
		}
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)
		if b["refresh_token"] != "rt-one" || hits.Add(1) != 1 {
			w.WriteHeader(401)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "jt-new", "refresh_token": "rt-new", "expires_in": 3600000})
	}))
	c := qoderTestCredential("one")
	c.ExpiresAt = time.Now().Add(time.Minute).UnixMilli()
	if err := qoderSave(c); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan *qoder.Credential, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			fresh, err := QoderCredential(context.Background(), "one@x")
			if err != nil {
				t.Error(err)
				return
			}
			results <- fresh
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	if hits.Load() != 1 || len(results) != 8 {
		t.Fatalf("refresh hits %d results %d", hits.Load(), len(results))
	}
	for got := range results {
		if got.Token != "jt-new" || got.RefreshToken != "rt-new" || got.MachineID != c.MachineID {
			t.Fatalf("fresh %+v", got)
		}
		got.Token = "caller-mutated"
		got.Models[0] = 'x'
	}
	fresh, err := QoderCredential(context.Background(), "one@x")
	if err != nil || fresh.Token != "jt-new" || !json.Valid(fresh.Models) {
		t.Fatalf("shared mutable credential: %+v %v", fresh, err)
	}
	// Refresh is also a value operation when called directly.
	client.Transport = qoderTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") })
	_, _ = c.Refresh(context.Background(), client)
	if c.Token != "jt-one" || c.RefreshToken != "rt-one" {
		t.Fatal("original credential changed")
	}
}

func TestQoderRefreshSaveFailureKeepsRotatedPair(t *testing.T) {
	signIn(t)
	c := qoderTestCredential("one")
	c.ExpiresAt = time.Now().Add(time.Minute).UnixMilli()
	if err := qoderSave(c); err != nil {
		t.Fatal(err)
	}
	path := loginsPath()
	var hits atomic.Int32
	qoderTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) != 1 {
			w.WriteHeader(401)
			return
		}
		// Make the atomic replacement fail after the upstream rotates the pair.
		if err := os.Rename(path, path+".saved"); err != nil {
			t.Error(err)
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{"token":"jt-new","refresh_token":"rt-new","expires_in":3600000}`))
	}))
	if _, err := QoderCredential(context.Background(), "one@x"); err == nil {
		t.Fatal("failed write reported success")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".saved", path); err != nil {
		t.Fatal(err)
	}
	fresh, err := QoderCredential(context.Background(), "one@x")
	if err != nil || fresh.Token != "jt-new" || fresh.RefreshToken != "rt-new" || hits.Load() != 1 {
		t.Fatalf("spent token reused: %+v %v hits %d", fresh, err, hits.Load())
	}
	if len(qoderPending) != 0 {
		t.Fatal("pending write was not cleared")
	}
}

func TestQoderAccountsAndMigration(t *testing.T) {
	signIn(t)
	legacy := filepath.Join(filepath.Dir(Path()), "qoder.json")
	one := qoderTestCredential("one")
	one.MachineID = ""
	writeFile(t, legacy, one)
	ls := Logins("qoder")
	if len(ls) != 1 || ls[0].User != "one@x" || !ls[0].Active {
		t.Fatalf("migration %+v", ls)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy file remains: %v", err)
	}
	oneRead, err := QoderCredential(context.Background(), "one@x")
	if err != nil || oneRead.MachineID == "" {
		t.Fatalf("migrated identity %+v %v", oneRead, err)
	}
	if err := qoderSave(qoderTestCredential("two")); err != nil {
		t.Fatal(err)
	}
	p, ok := qoderAccount()
	if !ok || p.Account.User != "one@x" || len(p.AlsoOn()) != 1 || p.AlsoOn()[0].Account.User != "two@x" {
		t.Fatalf("accounts %+v", p)
	}
	if err := SetLoginOn("qoder", "two@x", false); err != nil {
		t.Fatal(err)
	}
	if len(p.AlsoOn()) != 0 {
		t.Fatal("disabled account is routed")
	}
	if err := SwitchLogin("qoder", "two@x"); err != nil {
		t.Fatal(err)
	}
	p, _ = qoderAccount()
	if p.Account.User != "two@x" {
		t.Fatalf("switch %+v", p.Account)
	}
	cred, err := QoderCredential(context.Background(), p.Account.User)
	if err != nil || cred.UID != "two" {
		t.Fatalf("selected credential %+v %v", cred, err)
	}
	mi, err := QoderModel(context.Background(), "two@x", "other-model")
	if err != nil || mi.MaxInputTokens != 64000 || mi.IsReasoning {
		t.Fatalf("model %+v %v", mi, err)
	}
	if _, err := QoderModel(context.Background(), "two@x", "unknown"); err == nil {
		t.Fatal("unknown model accepted")
	}
	if err := ForgetLogin("qoder", "one@x"); err != nil {
		t.Fatal(err)
	}
	if err := ForgetLogin("qoder", "two@x"); err != nil {
		t.Fatal(err)
	}
	if len(Logins("qoder")) != 0 || QoderSignedIn() {
		t.Fatal("removed accounts remain")
	}
	if _, err := QoderCredential(context.Background(), "two@x"); err == nil {
		t.Fatal("removed token is usable")
	}
}

func TestQoderSignInDeadline(t *testing.T) {
	signIn(t)
	_, flow, err := QoderAuthURL()
	if err != nil {
		t.Fatal(err)
	}
	if left := time.Until(flow.deadline); left <= 14*time.Minute || left > 15*time.Minute {
		t.Fatalf("deadline %v", left)
	}
	flow.deadline = time.Now().Add(-time.Second)
	_, err = QoderCompleteSignIn(context.Background(), flow)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired flow: %v", err)
	}
}

func TestQoderCompleteSignIn(t *testing.T) {
	signIn(t)
	var machine string
	var named bool
	qoderTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case qoder.DeviceTokenPollPath:
			_, _ = w.Write([]byte(`{"token":"dt-one","refresh_token":"drt","user_id":"u1"}`))
		case qoder.JobTokenPath:
			if r.Header.Get("Authorization") != "Bearer dt-one" {
				t.Error("wrong device token")
			}
			_, _ = w.Write([]byte(`{"token":"jt-one","refresh_token":"rt-one","expires_in":3600000}`))
		case qoder.UserInfoPath:
			if named {
				_, _ = w.Write([]byte(`{"id":"u1","email":"one@x","name":"One"}`))
			} else {
				_, _ = w.Write([]byte(`{"id":"u1"}`))
			}
		case strings.Split(qoder.ListModelsPath, "?")[0]:
			if r.Header.Get("Cosy-MachineId") != machine || r.Header.Get("Cosy-MachineOS") != qoder.MachineOS() {
				t.Error("model fetch uses wrong fingerprint")
			}
			_, _ = w.Write(qoderTestCredential("one").Models)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	for i := 0; i < 2; i++ {
		authURL, flow, err := QoderAuthURL()
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(authURL)
		machine = u.Query().Get("machine_id")
		who, err := QoderCompleteSignIn(context.Background(), flow)
		if err != nil {
			t.Fatal(err)
		}
		want := "u1"
		if named {
			want = "one@x"
		}
		if who != want || len(Logins("qoder")) != 1 {
			t.Fatalf("sign-in %s %+v", who, Logins("qoder"))
		}
		cred, err := QoderCredential(context.Background(), who)
		if err != nil || cred.MachineID != machine || len(cred.Models) == 0 {
			t.Fatalf("saved flow: %+v %v", cred, err)
		}
		named = true
	}
}

func TestQoderLoginUsage(t *testing.T) {
	signIn(t)
	c := qoderTestCredential("one")
	c.DeviceRefresh = "drt-old"
	if err := qoderSave(c); err != nil {
		t.Fatal(err)
	}
	var refreshes atomic.Int32
	qoderTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case qoder.DeviceTokenRefreshPath:
			refreshes.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]string{"device_token": "dt-new", "refresh_token": "drt-new"})
		case qoder.AccountUsagePath:
			if r.Header.Get("Authorization") != "Bearer dt-new" {
				w.WriteHeader(401)
				return
			}
			_, _ = w.Write([]byte(`{"displayMode":"qoder","qoderUsage":{"userType":"pro","userQuota":{"total":100,"used":25},"addOnQuota":{"cap":50,"remaining":40},"orgResourcePackage":{"total":20,"used":10},"dedicatedResourcePackages":[{"name":"Team","total":10,"used":2}]}}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	q := qoderLoginQuota(context.Background(), Login{Agent: "qoder", User: "one@x"})
	if q.Error != "" || q.Plan != "pro" || len(q.Windows) != 4 || q.Windows[0].Used != 25 || q.Windows[1].Used != 20 {
		t.Fatalf("quota %+v", q)
	}
	fresh, err := QoderCredential(context.Background(), "one@x")
	if err != nil || fresh.DeviceToken != "dt-new" || fresh.DeviceRefresh != "drt-new" || fresh.Token != c.Token || fresh.MachineID != c.MachineID || refreshes.Load() != 1 {
		t.Fatalf("device refresh %+v %v", fresh, err)
	}
}
