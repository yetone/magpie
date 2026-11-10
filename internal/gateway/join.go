package gateway

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// JoinPagePath is the join page (EV_John_Liu on X): on a magpie shared on
// the network with a join password set (Settings → Network and sharing →
// Join with a password), another computer opens <address>/join, types the
// password and a name for itself, and gets a gateway key of its own, made
// then and listed in Gateway keys under that name, where it is limited or
// removed as any other key. The page then shows that key's setup for
// Claude Code, Codex, another magpie or any OpenAI-compatible app, as the
// key page (/me) does: it is the same page.
const JoinPagePath = "/join"

// joinData is the join's own route: GET says whether joining is open,
// POST {password, name} makes the key.
const joinData = "/v1/magpie/join"

// joinPageOpen: r asks for the join page itself, the key page's bytes,
// which lanGuard serves to another machine without a key while magpie is
// shared.
func joinPageOpen(r *http.Request) bool {
	return r.URL.Path == JoinPagePath && r.URL.RawPath == "" && (r.Method == http.MethodGet || r.Method == http.MethodHead)
}

// joinAsked: r is the join's route, which carries a password, not a key:
// the handler judges it (joinKey).
func joinAsked(r *http.Request) bool {
	return r.URL.Path == joinData && r.URL.RawPath == "" && (r.Method == http.MethodGet || r.Method == http.MethodPost)
}

// The join's own bucket, apart from tooManyAuthFails': each try from
// another machine takes a token, a right password too (each makes a key),
// so a guesser gets joinBurst tries and then one each joinRefill — 360 an
// hour against a password of 8 characters or more — and a flood of tries
// makes no flood of keys. A right key elsewhere is never held by it.
const (
	joinBurst  = 10
	joinRefill = 10 * time.Second
)

var joinThrottle = struct {
	sync.Mutex
	tokens float64
	last   time.Time
	now    func() time.Time
}{tokens: joinBurst, now: time.Now}

// tooManyJoins takes one try's token; over when there is none, with the
// wait until one refills.
func tooManyJoins() (over bool, retry time.Duration) {
	t := &joinThrottle
	t.Lock()
	defer t.Unlock()
	now := t.now()
	if t.last.IsZero() {
		t.last = now
	}
	t.tokens = min(joinBurst, t.tokens+now.Sub(t.last).Seconds()/joinRefill.Seconds())
	t.last = now
	if t.tokens >= 1 {
		t.tokens--
		return false, 0
	}
	return true, time.Duration((1 - t.tokens) * float64(joinRefill))
}

func resetJoinThrottle() {
	t := &joinThrottle
	t.Lock()
	t.tokens, t.last, t.now = joinBurst, time.Time{}, time.Now
	t.Unlock()
}

// joinPage serves the key page, which reads from its address that it is
// the join page.
func (s *Server) joinPage(w http.ResponseWriter, r *http.Request) { s.keyPage(w, r) }

// joinOpen tells the page whether a password is set to join with.
func (s *Server) joinOpen(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]bool{"open": access.JoinOn()})
}

// joinRefusal: why a browser's join isn't one its own page sent, or "".
// A browser's POST carries an Origin, which must be the page's own address
// (the Host it called), at a name of this computer's (ownHost): an IP,
// localhost, MAGPIE_PUBLIC_URL's or its own host name. So a page elsewhere
// can't make keys through a visitor's browser, neither by its own Origin,
// by Origin "null" from a sandboxed frame, at a localhost subdomain WebKit
// asks the DNS for, nor by a name of its own rebound to this computer. A
// request no browser sent (curl, a script) carries none of a browser's
// marks and is judged on the password alone.
func joinRefusal(r *http.Request) string {
	if !fromBrowser(r) {
		return ""
	}
	if !ownHost(r.Host) {
		return "open the join page at this computer's address (an IP, or MAGPIE_PUBLIC_URL's name), not " + r.Host
	}
	origin := r.Header.Get("Origin")
	if origin == "" || origin == "null" {
		return "a browser joins from the join page itself"
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Path != "" ||
		hostPort(u.Host, u.Scheme) != hostPort(r.Host, u.Scheme) {
		return "a browser joins from the join page itself, not " + origin
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		return "a browser joins from the join page itself"
	}
	return ""
}

// joinKey makes a gateway key for a computer that knows the join password.
// Neither the password nor the key is logged; the key goes back in the
// body alone, never cached.
func (s *Server) joinKey(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if why := joinRefusal(r); why != "" {
		log.Printf("refused %s %s from %s: %s", r.Method, r.URL.Path, r.RemoteAddr, why)
		writeError(w, provider.Chat, http.StatusForbidden, why)
		return
	}
	if !access.JoinOn() {
		writeError(w, provider.Chat, http.StatusNotFound, "joining with a password is off on this magpie")
		return
	}
	if !local(r) {
		if over, retry := tooManyJoins(); over {
			w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
			writeError(w, provider.Chat, http.StatusTooManyRequests, "too many tries to join; slow down")
			return
		}
	}
	var in struct {
		Password string `json:"password"`
		Name     string `json:"name"`
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 8<<10))
	if json.Unmarshal(b, &in) != nil {
		writeError(w, provider.Chat, http.StatusBadRequest, `send {"password": "…", "name": "this computer's name"}`)
		return
	}
	if !access.JoinPasswordOK(in.Password) {
		log.Printf("refused %s %s from %s: wrong join password", r.Method, r.URL.Path, r.RemoteAddr)
		writeError(w, provider.Chat, http.StatusUnauthorized, "wrong password")
		return
	}
	name := joinKeyName(in.Name, clientIP(r))
	secret, err := access.Update("add-key", access.Change{Name: name})
	if err != nil {
		writeError(w, provider.Chat, http.StatusInternalServerError, err.Error())
		return
	}
	who, _ := access.Authenticate(secret)
	log.Printf("made gateway key %s (%s) for a computer that joined from %s", who.KeyID, name, r.RemoteAddr)
	writeJSON(w, http.StatusCreated, map[string]string{"key": secret, "id": who.KeyID, "name": name})
}

// joinKeyName is the name typed, one line of at most 120 characters, or
// "Joined from <address>".
func joinKeyName(typed, ip string) string {
	name := strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, typed)), " ")
	if utf8.RuneCountInString(name) > 120 {
		name = string([]rune(name)[:119]) + "…"
	}
	if name == "" {
		name = "Joined from " + ip
		if ip == "" {
			name = "Joined"
		}
	}
	return name
}
