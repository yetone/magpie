package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// gaubeebangeel on X: `magpie update`, run by an agent magpie serves,
// asks the magpie serving the gateway to restart into the version it put
// in. Only a program on this computer may (the heap profile's callers),
// never another machine with a key, a page, or another Host; the
// restarter isn't called for them. Without a restarter (magpie serve)
// the answer says so.
func TestRestartAskedOnlyFromThisComputer(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	_, secrets := newCaller(t, "Page")
	s := settings.Load()
	s.CORSOrigins = []string{"https://listed.example"}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	var asked []string
	SetRestarter(func(v string) RestartReply {
		asked = append(asked, v)
		return RestartReply{State: "restarting", Handover: true}
	})
	t.Cleanup(func() { SetRestarter(nil) })
	srv := New()
	call := func(guarded bool, remote, host string, hdr ...string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("POST", RestartPath, strings.NewReader(`{"version":"v0.1.9999"}`))
		r.RemoteAddr = remote
		r.Host = host
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		h := srv.Handler()
		if guarded {
			h = lanGuard(h)
		}
		h.ServeHTTP(w, r)
		return w
	}
	for _, c := range profileCallersRefused(secrets[0]) {
		for _, guarded := range []bool{true, false} {
			if w := call(guarded, c.remote, c.host, c.hdr...); w.Code != http.StatusForbidden && w.Code != http.StatusUnauthorized {
				t.Errorf("%s (lanGuard %v): %d %q", c.name, guarded, w.Code, w.Body.String())
			}
		}
	}
	if len(asked) != 0 {
		t.Fatalf("restarted for a refused caller: %v", asked)
	}
	w := call(true, "127.0.0.1:50123", "127.0.0.1:3425", "User-Agent", "Go-http-client/1.1")
	var got RestartReply
	if err := json.Unmarshal(w.Body.Bytes(), &got); w.Code != http.StatusOK || err != nil {
		t.Fatalf("magpie update's ask: %d %q", w.Code, w.Body.String())
	}
	if got.State != "restarting" || !got.Handover || got.Version != Version || len(asked) != 1 || asked[0] != "v0.1.9999" {
		t.Fatalf("answered %+v, restarter asked %v", got, asked)
	}

	SetRestarter(nil)
	w = call(true, "127.0.0.1:50123", "localhost:3425", "User-Agent", "Go-http-client/1.1")
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.State != "unsupported" {
		t.Fatalf("without a restarter: %d %q", w.Code, w.Body.String())
	}
}

// A forced ask reaches the forcing restarter; an ordinary one doesn't.
func TestRestartForceReachesTheForcingRestarter(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	var which []string
	SetRestarter(func(string) RestartReply { which = append(which, "wait"); return RestartReply{State: "waiting"} })
	SetForceRestarter(func(string) RestartReply {
		which = append(which, "force")
		return RestartReply{State: "restarting", Forced: true}
	})
	t.Cleanup(func() { SetRestarter(nil); SetForceRestarter(nil) })
	srv := New()
	for _, body := range []string{`{"version":"v1"}`, `{"version":"v1","force":true}`} {
		r := httptest.NewRequest("POST", RestartPath, strings.NewReader(body))
		r.RemoteAddr, r.Host = "127.0.0.1:5555", "127.0.0.1:3425"
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body)
		}
	}
	if len(which) != 2 || which[0] != "wait" || which[1] != "force" {
		t.Errorf("asked %v", which)
	}
}
