package gateway

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/settings"
)

const joinPw = "correct horse battery"

// joinGateway is a shared gateway with a join password set, its join
// bucket full and on a clock the test moves.
func joinGateway(t *testing.T) (http.Handler, *time.Time) {
	t.Helper()
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	newCaller(t)
	if err := access.SetJoinPassword(joinPw); err != nil {
		t.Fatal(err)
	}
	resetJoinThrottle()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	joinThrottle.Lock()
	joinThrottle.now = func() time.Time { return now }
	joinThrottle.Unlock()
	t.Cleanup(resetJoinThrottle)
	return lanGuard(New().Handler()), &now
}

type joinReq struct {
	from, host, body string
	header           map[string]string
}

func (q joinReq) do(h http.Handler) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", joinData, strings.NewReader(q.body))
	r.RemoteAddr = q.from
	if q.from == "" {
		r.RemoteAddr = "192.168.1.9:5000"
	}
	r.Host = q.host
	if q.host == "" {
		r.Host = "192.168.1.5:3425"
	}
	r.Header.Set("Content-Type", "application/json")
	for k, v := range q.header {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func joinBody(pw, name string) string {
	b, _ := json.Marshal(map[string]string{"password": pw, "name": name})
	return string(b)
}

// the join page's own POST, as a browser sends it
var joinPageHeaders = map[string]string{
	"Origin":         "http://192.168.1.5:3425",
	"Sec-Fetch-Site": "same-origin",
	"Sec-Fetch-Mode": "cors",
	"User-Agent":     "Mozilla/5.0",
}

// EV_John_Liu on X: another computer on the network opens /join without a
// key, types the password, and gets a gateway key of its own, named for
// it, which works at once and stops when removed in Gateway keys.
func TestJoinMakesARevocableKey(t *testing.T) {
	h, _ := joinGateway(t)
	var logs bytes.Buffer
	was := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(was) })

	page := keyPageReq{method: "GET", path: JoinPagePath, from: "192.168.1.9:5000", host: "192.168.1.5:3425"}.do(h)
	if page.Code != 200 || !strings.Contains(page.Body.String(), `id="join"`) {
		t.Fatal("join page", page.Code)
	}
	open := keyPageReq{method: "GET", path: joinData, from: "192.168.1.9:5000", host: "192.168.1.5:3425"}.do(h)
	if open.Code != 200 || strings.TrimSpace(open.Body.String()) != `{"open":true}` {
		t.Fatal("open", open.Code, open.Body.String())
	}

	w := joinReq{body: joinBody(joinPw, "Studio\nMac  mini"), header: joinPageHeaders}.do(h)
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Error("Cache-Control", cc)
	}
	var got struct{ Key, ID, Name string }
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Key == "" {
		t.Fatal(err, w.Body.String())
	}
	if got.Name != "Studio Mac mini" {
		t.Errorf("name %q", got.Name)
	}
	keys, _ := access.List()
	found := false
	for _, k := range keys {
		found = found || (k.ID == got.ID && k.Name == "Studio Mac mini")
	}
	if !found {
		t.Fatalf("no key %s in %+v", got.ID, keys)
	}

	me := keyPageReq{method: "GET", path: keyPageData, from: "192.168.1.9:5000", host: "192.168.1.5:3425", header: map[string]string{"Authorization": "Bearer " + got.Key}}.do(h)
	if me.Code != 200 {
		t.Fatal("the new key", me.Code, me.Body.String())
	}
	if _, err := access.Update("remove-key", access.Change{Key: got.ID}); err != nil {
		t.Fatal(err)
	}
	me = keyPageReq{method: "GET", path: keyPageData, from: "192.168.1.9:5000", host: "192.168.1.5:3425", header: map[string]string{"Authorization": "Bearer " + got.Key}}.do(h)
	if me.Code != 401 {
		t.Fatal("a removed key", me.Code)
	}

	// a name left empty is the address it joined from; curl (no browser)
	// joins on the password alone
	w = joinReq{body: joinBody(joinPw, "")}.do(h)
	if w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), `"name":"Joined from 192.168.1.9"`) {
		t.Fatal(w.Code, w.Body.String())
	}

	if strings.Contains(logs.String(), got.Key) || strings.Contains(logs.String(), joinPw) {
		t.Errorf("a secret in the log: %s", logs.String())
	}
}

// A wrong password is 401, and every try from another machine takes from
// the join's bucket: past the burst it is 429 with Retry-After, the right
// password too, until the bucket refills.
func TestJoinHoldsAGuesser(t *testing.T) {
	h, now := joinGateway(t)
	var logs bytes.Buffer
	was := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(was) })
	for i := range joinBurst {
		if w := (joinReq{body: joinBody("guess-"+string(rune('a'+i))+"-wrong", "")}).do(h); w.Code != 401 {
			t.Fatal(i, w.Code, w.Body.String())
		}
	}
	w := joinReq{body: joinBody(joinPw, "")}.do(h)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Fatal("past the burst", w.Code, w.Header())
	}
	if strings.Contains(w.Body.String(), `"key"`) {
		t.Fatal("a key past the burst", w.Body.String())
	}
	if strings.Contains(logs.String(), "guess-") {
		t.Errorf("a password in the log: %s", logs.String())
	}
	*now = now.Add(joinRefill)
	if w := (joinReq{body: joinBody(joinPw, "")}).do(h); w.Code != http.StatusCreated {
		t.Fatal("after a refill", w.Code, w.Body.String())
	}
	if w := (joinReq{body: joinBody(joinPw, "")}).do(h); w.Code != http.StatusTooManyRequests {
		t.Fatal("one token a refill", w.Code)
	}
}

// A page elsewhere can't make keys through a visitor's browser: Origin
// null, another site, a *.localhost name, a name of its own rebound to
// this computer, a cross-site fetch and a browser's POST with no Origin
// are refused, before the password is judged; none makes a key.
func TestJoinRefusesOtherPages(t *testing.T) {
	h, _ := joinGateway(t)
	browser := func(extra map[string]string) map[string]string {
		m := map[string]string{"User-Agent": "Mozilla/5.0", "Sec-Fetch-Mode": "cors"}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	for name, q := range map[string]joinReq{
		"null":          {header: browser(map[string]string{"Origin": "null"})},
		"another site":  {header: browser(map[string]string{"Origin": "https://evil.example"})},
		"localhost sub": {header: browser(map[string]string{"Origin": "http://evil.localhost:3425"})},
		"localhost host": {host: "evil.localhost:3425", from: "127.0.0.1:5000",
			header: browser(map[string]string{"Origin": "http://evil.localhost:3425"})},
		"rebound": {host: "evil.example:3425", from: "127.0.0.1:5000",
			header: browser(map[string]string{"Origin": "http://evil.example:3425"})},
		"rebound remote": {host: "evil.example:3425",
			header: browser(map[string]string{"Origin": "http://evil.example:3425"})},
		"cross-site": {header: browser(map[string]string{"Origin": "http://192.168.1.5:3425", "Sec-Fetch-Site": "cross-site"})},
		"no Origin":  {header: browser(nil)},
		"other port": {header: browser(map[string]string{"Origin": "http://192.168.1.5:8080"})},
	} {
		q.body = joinBody(joinPw, "x")
		w := q.do(h)
		if w.Code < 400 || strings.Contains(w.Body.String(), `"key"`) {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	if keys, _ := access.List(); len(keys) != 0 {
		t.Errorf("keys made: %+v", keys)
	}
	// and the page's own POST from this computer goes through
	q := joinReq{from: "127.0.0.1:5000", host: "127.0.0.1:3425", body: joinBody(joinPw, "here"),
		header: browser(map[string]string{"Origin": "http://127.0.0.1:3425", "Sec-Fetch-Site": "same-origin"})}
	if w := q.do(h); w.Code != http.StatusCreated {
		t.Fatal("own page", w.Code, w.Body.String())
	}
}

// With no password set, or magpie not shared, nothing joins.
func TestJoinClosed(t *testing.T) {
	h, _ := joinGateway(t)
	if err := access.SetJoinPassword(""); err != nil {
		t.Fatal(err)
	}
	if w := (joinReq{body: joinBody(joinPw, "")}).do(h); w.Code != 404 {
		t.Error("no password", w.Code, w.Body.String())
	}
	open := keyPageReq{method: "GET", path: joinData, from: "192.168.1.9:5000", host: "192.168.1.5:3425"}.do(h)
	if strings.TrimSpace(open.Body.String()) != `{"open":false}` {
		t.Error("open", open.Body.String())
	}
	if err := access.SetJoinPassword(joinPw); err != nil {
		t.Fatal(err)
	}
	s := settings.Load()
	s.LAN = false
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if w := (joinReq{body: joinBody(joinPw, "")}).do(h); w.Code == http.StatusCreated {
		t.Error("not shared", w.Code, w.Body.String())
	}
	if keys, _ := access.List(); len(keys) != 0 {
		t.Errorf("keys made: %+v", keys)
	}
}

func TestJoinKeyName(t *testing.T) {
	long := strings.Repeat("é", 200)
	for in, want := range map[string]string{
		"  Studio\tMac\x00 ": "Studio Mac",
		"":                   "Joined from 10.0.0.2",
		long:                 strings.Repeat("é", 119) + "…",
	} {
		if got := joinKeyName(in, "10.0.0.2"); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
	if got := joinKeyName("", ""); got != "Joined" {
		t.Error(got)
	}
}
