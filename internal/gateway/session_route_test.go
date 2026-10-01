package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// GET /v1/magpie/route tells an agent's UI where its session's turn went
// while it is under way (#405): the member routing put first, the effort it
// was sent at, and each fallback as it happens — before the reply's first
// token, not after. Only the session asked for is told, and only to this
// machine or one with the shared gateway's key.
func TestSessionRoute(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":{"message":"rate limited"}}`)
	}))
	t.Cleanup(limited.Close)
	asked, release := make(chan struct{}, 1), make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked <- struct{}{}
		<-release
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"ok","model":"m","choices":[{"message":{"role":"assistant","content":"hi"}}]}`)
	}))
	t.Cleanup(slow.Close)
	var once sync.Once
	let := func() { once.Do(func() { close(release) }) }
	t.Cleanup(let) // before slow.Close, cleanups running last first
	for _, p := range []provider.Provider{
		{ID: "ra", Name: "RA", Key: "k", Models: []string{"m"}, Chat: limited.URL + "/v1"},
		{ID: "rb", Name: "RB", Key: "k", Models: []string{"m"}, Chat: slow.URL + "/v1"},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{Name: "R", Members: []string{"ra/m", "rb/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	h := lanGuard(s.Handler())
	get := func(from, target string, hdr ...string) (int, map[string]any) {
		t.Helper()
		r := httptest.NewRequest("GET", target, nil)
		r.RemoteAddr = from
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	const here = "127.0.0.1:5000"

	done := make(chan int, 1)
	go func() {
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"group/r","reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`))
		r.RemoteAddr = here
		r.Header.Set(SessionHeader, "pi-1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		done <- w.Code
	}()
	select {
	case <-asked:
	case <-time.After(5 * time.Second):
		t.Fatal("the second member was never asked")
	}

	// under way: the first member failed over to the second, which has
	// not answered yet
	code, out := get(here, "/v1/magpie/route", SessionHeader, "pi-1")
	if code != 200 || out["session"] != "pi-1" {
		t.Fatalf("in flight: %d %v", code, out)
	}
	rt, _ := out["route"].(map[string]any)
	if rt == nil || rt["done"] != false || rt["asked"] != "group/r" || rt["group"] != "r" || rt["model"] != "rb/m" || rt["effort"] != "high" {
		t.Fatalf("in flight route: %v", out)
	}
	tries, _ := rt["tries"].([]any)
	if len(tries) != 2 {
		t.Fatalf("tries: %v", rt["tries"])
	}
	first, second := tries[0].(map[string]any), tries[1].(map[string]any)
	if first["model"] != "ra/m" || first["status"] != float64(429) || first["fail"] == nil || second["model"] != "rb/m" || second["done"] != false {
		t.Fatalf("tries: %v", tries)
	}
	seq := int64(out["seq"].(float64))

	// a long poll hears updates, including the turn end
	polled := make(chan map[string]any, 1)
	go func() {
		_, o := get(here, "/v1/magpie/route?session=pi-1&wait=5&after="+strconv.FormatInt(seq, 10))
		polled <- o
	}()
	time.Sleep(50 * time.Millisecond)
	let()
	if c := <-done; c != 200 {
		t.Fatal("the turn got", c)
	}
	o := <-polled
	// A try ending can wake the poll before the whole route is done.
	// The handler has returned now; ask past that update for its end.
	if rt, _ := o["route"].(map[string]any); rt != nil && rt["done"] == false {
		after := int64(o["seq"].(float64))
		_, o = get(here, "/v1/magpie/route?session=pi-1&wait=5&after="+strconv.FormatInt(after, 10))
	}
	if rt, _ := o["route"].(map[string]any); rt == nil || rt["done"] != true || rt["status"] != float64(200) || rt["model"] != "rb/m" {
		t.Fatalf("polled: %v", o)
	}

	// another session hears nothing of this one
	if code, out := get(here, "/v1/magpie/route?session=other"); code != 200 || out["route"] != nil {
		t.Fatalf("other session: %d %v", code, out)
	}
	if code, _ := get(here, "/v1/magpie/route"); code != http.StatusBadRequest {
		t.Fatal("no session got", code)
	}
	// another machine needs the shared gateway's key, as the quotas do
	callerKeys, secrets := newCaller(t, "Session status")
	key := secrets[0]
	if code, _ := get("192.168.1.9:5000", "/v1/magpie/route?session=pi-1"); code != http.StatusUnauthorized {
		t.Fatal("no key got", code)
	}
	if code, out := get("192.168.1.9:5000", "/v1/magpie/route?session=pi-1", "Authorization", "Bearer "+key); code != 200 || out["route"] == nil {
		t.Fatalf("with the key: %d %v", code, out)
	}
	if code, out := get("192.168.1.9:5000", "/v1/magpie/route?session=pi-1", "x-api-key", key); code != 200 || out["route"] == nil {
		t.Fatalf("with x-api-key: %d %v", code, out)
	}
	if code, out := get("192.168.1.9:5000", "/v1/magpie/route?session=pi-1&key="+key); code != 200 || out["route"] == nil {
		t.Fatalf("with query key: %d %v", code, out)
	}
	if code, _ := get("192.168.1.9:5000", "/v1/magpie/route?session=pi-1", "Authorization", "Bearer wrong"); code != http.StatusUnauthorized {
		t.Fatal("wrong key got", code)
	}
	if _, err := access.Update("off-key", access.Change{Key: callerKeys[0].ID}); err != nil {
		t.Fatal(err)
	}
	if code, _ := get("192.168.1.9:5000", "/v1/magpie/route?session=pi-1", "Authorization", "Bearer "+key); code != http.StatusUnauthorized {
		t.Fatal("disabled key got", code)
	}
	shared := settings.Load()
	shared.LAN = false
	if err := settings.Save(shared); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAGPIE_ADDR", "0.0.0.0:3425")
	if code, _ := get("192.168.1.9:5000", "/v1/magpie/route?session=pi-1"); code != http.StatusForbidden {
		t.Fatal("MAGPIE_ADDR, not shared, got", code)
	}
}
