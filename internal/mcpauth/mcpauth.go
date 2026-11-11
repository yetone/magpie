// Package mcpauth signs magpie in to a remote MCP server that asks for an
// OAuth sign-in (Neon, Notion, Supabase…), once for every agent (#615).
// Each agent would otherwise run the sign-in itself and keep its own token,
// in its own place: magpie signs in, keeps the tokens in mcp-signins.json
// beside its other sign-ins, renews them, and the gateway's /mcp/<name>
// relays the agents' requests to the server with them (gateway/mcp.go).
// The library then gives the agents that address in place of the server's.
//
// The sign-in is the one the MCP spec lays out (2025-06-18, Authorization):
// the server's 401 names its protected resource metadata (RFC 9728), that
// names the authorization server, whose metadata (RFC 8414) says where to
// register a client (RFC 7591), authorize with PKCE (S256) and get tokens.
// A server of the older spec (2025-03-26) with no resource metadata has its
// authorization server at its own origin.
package mcpauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/filelock"
	"github.com/yetone/magpie/internal/lastgood"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/steady"
)

// Client makes the sign-in's requests, through the proxy magpie's others
// take; tests point it elsewhere.
var Client = &http.Client{Timeout: 30 * time.Second, Transport: proxied()}

func proxied() http.RoundTripper {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = netproxy.Func
	return netproxy.Dispatch(t)
}

// Changed is called when a server is signed in or out, for the library to
// give the agents its new address.
var Changed func(name string)

// renewLead is how long before it runs out a token is renewed.
var renewLead = 2 * time.Minute

// signInTimeout is how long a sign-in waits for the browser.
var signInTimeout = 10 * time.Minute

// ErrNotSignedIn is a server magpie holds no sign-in for.
var ErrNotSignedIn = errors.New("magpie isn't signed in to this server")

// ErrExpired is a sign-in the server no longer takes: it has to be done again.
var ErrExpired = errors.New("magpie's sign-in to this server has run out: sign in again in the Library")

// ErrNoSignIn is a server that answered an agent's first request without
// asking for a sign-in: it works as it is. One that takes a key as a header
// takes it in Headers; one that signs in only at another address (Exa's
// mcp.exa.ai/mcp?login) is signed in to there. The GUI has it in Chinese.
var ErrNoSignIn = errors.New("this server already works without signing in, so there is nothing to sign in to: the agents given it can use it as it is. A key it takes goes in Headers; a server that signs in at another address (as Exa's ?login) needs that URL")

// ErrClientRefused is a server whose authorization server turned magpie's
// registration away with 401 or 403: it admits only the clients it lists,
// as Figma's (mcp.figma.com, a 403 "Forbidden" to any client_name it doesn't
// list, #1619). magpie doesn't pass itself off as a listed client; a server
// magpie isn't signed in to is given to the agents at its own address, so
// each agent signs in there itself. The GUI has it in every language.
var ErrClientRefused = errors.New("this server's sign-in admits only the apps it lists and turned magpie away, so magpie can't sign in for the agents. The agents given it still have the server's own address: sign in from each agent itself (in Claude Code, run /mcp and pick the server)")

// ErrTurnedAway is a server whose address answered 403 with a web page
// rather than the server's own reply: a firewall in front of it (Cloudflare's,
// as agents.stackoverflow.com has) judged the network the request came from
// and stopped it before the server could offer a sign-in (fremontdango on
// Discord, where magpie said only "answered 403 and says nothing of a
// sign-in"). The GUI has it in every language.
var ErrTurnedAway = errors.New("a firewall in front of this server turned magpie's request away (403) before the server could offer a sign-in. It judges the network the request came from: try again on another network, or give magpie a proxy in Settings")

// ErrNoRegistration is a server whose sign-in doesn't let an app register
// itself (GitHub's, 新v学员 on Discord): magpie signs in to it only as an
// OAuth app of the user's own, made with the server's vendor, as Claude
// Code does with --client-id. The page opens the server's fields for one
// on it.
var ErrNoRegistration = errors.New("the server's sign-in doesn't let an app register itself (no dynamic client registration), so magpie signs in to it only as an OAuth app of your own: make one with the server's vendor (GitHub's under Settings › Developer settings › OAuth Apps), with http://localhost/callback as its callback URL, then give its client ID and secret under \"OAuth app\" in the server's editor")

// Record is magpie's sign-in to one server.
type Record struct {
	URL      string `json:"url"`                // the server's, as the library has it
	Resource string `json:"resource,omitempty"` // what the tokens are for (RFC 8707)
	Issuer   string `json:"issuer,omitempty"`
	TokenURL string `json:"tokenURL"`
	ClientID string `json:"clientID"`
	Secret   string `json:"clientSecret,omitempty"`
	// AuthMethod is how the client authenticates at the token endpoint:
	// none, client_secret_post or client_secret_basic
	AuthMethod string `json:"authMethod,omitempty"`
	Scope      string `json:"scope,omitempty"`
	Access     string `json:"access"`
	Refresh    string `json:"refresh,omitempty"`
	Expires    int64  `json:"expires,omitempty"` // unix ms; 0 when the server didn't say
	At         int64  `json:"at"`                // when it was signed in, unix ms
	// Dead is a sign-in the server refused to renew: the page asks for it
	// again
	Dead bool `json:"dead,omitempty"`

	// More than one sign-in to a server (Elorynth on X: several accounts,
	// one in use). This record is always the one in use, so every reader of
	// a server's sign-in (the gateway's relay, the check, a renewal) reads
	// the account in use without knowing there are others; the others wait
	// in Spare, and a switch swaps them. Seq keeps each account's place in
	// the list, which a swap doesn't move. A sign-in made before accounts
	// has none of these: it is account 1, with no label.
	ID    string    `json:"id,omitempty"`
	Label string    `json:"label,omitempty"`
	Seq   int       `json:"seq,omitempty"`
	Spare []*Record `json:"spare,omitempty"`
}

// fileMu is held for a change to mcp-signins.json read afresh under it,
// across magpies too (#1674): the gateway renews a sign-in while a magpie
// run from a terminal checks or renames a server.
var fileMu = filelock.Mutex{Path: func() string { return path() + ".lock" }}

func path() string { return filepath.Join(settings.Dir(), "mcp-signins.json") }

func load() map[string]*Record {
	m, _ := read()
	return m
}

// read is the sign-ins, or their last good generation when a crash left
// the file all zero (#1505); an error when neither reads, for a change
// not to write them over as none.
func read() (map[string]*Record, error) {
	m := map[string]*Record{}
	b, err := lastgood.Read(path(), validRecords)
	if errors.Is(err, fs.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	_ = json.Unmarshal(b, &m)
	return m, nil
}

func validRecords(b []byte) bool {
	var m map[string]*Record
	return json.Unmarshal(b, &m) == nil
}

func save(m map[string]*Record) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := lastgood.Keep(path(), validRecords, 0o600); err != nil {
		return err
	}
	return writePrivate(path(), append(b, '\n'))
}

// writePrivate replaces a file only the user can read, atomically.
func writePrivate(p string, b []byte) error {
	p, err := edit.Target(p)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return steady.WriteFile(p, b, 0o600) // on the disk before it is renamed in
}

func update(f func(m map[string]*Record) bool) error {
	fileMu.Lock()
	defer fileMu.Unlock()
	// sign-ins that don't read are kept aside before a new one is saved
	// (save), and never written over with none
	m, err := read()
	changed := f(m)
	if err != nil && len(m) == 0 {
		return err // what to change may be in what doesn't read
	}
	if !changed {
		return nil
	}
	return save(m)
}

// Get is the sign-in to the server by that name.
func Get(name string) (Record, bool) {
	fileMu.Lock()
	defer fileMu.Unlock()
	r := load()[name]
	if r == nil {
		return Record{}, false
	}
	c := *r
	c.Spare = nil
	return c, true
}

// SignedIn says whether magpie holds a sign-in to the server by that name
// at that URL: one to give the agents magpie's address for. One the server
// refused to renew still is, so the agents get magpie's word to sign in
// again rather than a sign-in of their own.
func SignedIn(name, serverURL string) bool {
	r, ok := Get(name)
	return ok && r.URL == serverURL
}

// Status is a server's sign-in as the page shows it.
type Status struct {
	SignedIn bool  `json:"signedIn"`
	Dead     bool  `json:"dead,omitempty"`
	At       int64 `json:"at,omitempty"`
	// Accounts are the sign-ins, when there is more than one
	Accounts []Account `json:"accounts,omitempty"`
}

// StatusOf is the sign-in to the server by that name at that URL.
func StatusOf(name, serverURL string) Status {
	r, ok := Get(name)
	if !ok || r.URL != serverURL {
		return Status{}
	}
	st := Status{SignedIn: true, Dead: r.Dead, At: r.At}
	if l := Accounts(name); len(l) > 1 {
		st.Accounts = l
	}
	return st
}

// SignOut forgets magpie's sign-in to the server.
func SignOut(name string) error {
	had := false
	err := update(func(m map[string]*Record) bool {
		_, had = m[name]
		delete(m, name)
		return had
	})
	if err == nil && had && Changed != nil {
		Changed(name)
	}
	return err
}

// Forget forgets the sign-in to a server the library no longer has, which
// gives it to no agent: there is nothing to tell.
func Forget(name string) error {
	return update(func(m map[string]*Record) bool {
		_, had := m[name]
		delete(m, name)
		return had
	})
}

// Account is one of a server's sign-ins, as the page lists it.
type Account struct {
	ID     string `json:"id"`
	Label  string `json:"label,omitempty"` // "" for one made before labels
	Active bool   `json:"active,omitempty"`
	Dead   bool   `json:"dead,omitempty"`
	At     int64  `json:"at,omitempty"`
}

var (
	// ErrNoAccount is an account the server has no sign-in for.
	ErrNoAccount = errors.New("magpie has no such sign-in to this server")
	// ErrLabelUsed is a label another sign-in to the server has.
	ErrLabelUsed = errors.New("another sign-in to this server has that name")
)

// the record's id and place, which a sign-in made before accounts lacks
func idOf(r *Record) string {
	if r.ID == "" {
		return "1"
	}
	return r.ID
}

func seqOf(r *Record) int {
	if r.Seq == 0 {
		return 1
	}
	return r.Seq
}

// all is the record with its spares, in the order they were signed in.
func all(r *Record) []*Record {
	a := *r
	a.Spare = nil
	out := append([]*Record{&a}, r.Spare...)
	slices.SortStableFunc(out, func(x, y *Record) int { return seqOf(x) - seqOf(y) })
	return out
}

// Accounts are the sign-ins to the server by that name, in the order they
// were made; none when magpie isn't signed in to it.
func Accounts(name string) []Account {
	fileMu.Lock()
	defer fileMu.Unlock()
	r := load()[name]
	if r == nil {
		return nil
	}
	var out []Account
	for _, x := range all(r) {
		out = append(out, Account{ID: idOf(x), Label: x.Label, Active: x == nil || idOf(x) == idOf(r), Dead: x.Dead, At: x.At})
	}
	return out
}

// InUse is the id of the sign-in in use, "" when there is none.
func InUse(name string) string {
	for _, a := range Accounts(name) {
		if a.Active {
			return a.ID
		}
	}
	return ""
}

// Use puts the sign-in by that id in use: what the gateway sends the
// server, and the check, from the next request on. The agents are told
// through Changed as for a new sign-in.
func Use(name, id string) error {
	found := false
	err := update(func(m map[string]*Record) bool {
		cur := m[name]
		if cur == nil {
			return false
		}
		if idOf(cur) == id {
			found = true
			return false
		}
		i := slices.IndexFunc(cur.Spare, func(x *Record) bool { return idOf(x) == id })
		if i < 0 {
			return false
		}
		found = true
		was := *cur
		was.ID, was.Seq, was.Spare = idOf(cur), seqOf(cur), nil
		next := *cur.Spare[i]
		next.ID, next.Seq = idOf(&next), seqOf(&next)
		rest := slices.Clone(cur.Spare)
		rest[i] = &was
		next.Spare = rest
		m[name] = &next
		return true
	})
	if err != nil {
		return err
	}
	if !found {
		return ErrNoAccount
	}
	if Changed != nil {
		Changed(name)
	}
	return nil
}

// RemoveAccount forgets one sign-in to the server. The one in use going
// puts the first of the others in use; the last going is a sign-out.
func RemoveAccount(name, id string) error {
	found := false
	err := update(func(m map[string]*Record) bool {
		cur := m[name]
		if cur == nil {
			return false
		}
		l := all(cur)
		i := slices.IndexFunc(l, func(x *Record) bool { return idOf(x) == id })
		if i < 0 {
			return false
		}
		found = true
		active := idOf(cur)
		l = slices.Delete(l, i, i+1)
		if len(l) == 0 {
			delete(m, name)
			return true
		}
		// the one in use stays, unless it is the one removed
		next := 0
		if id != active {
			next = slices.IndexFunc(l, func(x *Record) bool { return idOf(x) == active })
		}
		n := *l[next]
		n.ID, n.Seq = idOf(&n), seqOf(&n)
		n.Spare = slices.Delete(slices.Clone(l), next, next+1)
		m[name] = &n
		return true
	})
	if err != nil {
		return err
	}
	if !found {
		return ErrNoAccount
	}
	if Changed != nil {
		Changed(name)
	}
	return nil
}

// Rename moves a sign-in to the server's new name.
func Rename(old, name string) error {
	return update(func(m map[string]*Record) bool {
		r := m[old]
		if r == nil || old == name {
			return false
		}
		delete(m, old)
		m[name] = r
		return true
	})
}

// ---- tokens ----------------------------------------------------------------

// renewing is one renewal per server at a time: a vendor that hands out a
// new refresh token with each renewal takes the old one only once.
var renewing sync.Map // name → *sync.Mutex

func lockOf(name string) *sync.Mutex {
	m, _ := renewing.LoadOrStore(name, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// Token is the access token for the server by that name, renewed first
// when it runs out within renewLead.
func Token(ctx context.Context, name string) (string, error) {
	r, ok := Get(name)
	if !ok {
		return "", ErrNotSignedIn
	}
	if r.Dead {
		return "", ErrExpired
	}
	if !due(r) {
		return r.Access, nil
	}
	return renew(ctx, name, func(cur Record) bool { return due(cur) })
}

// Renew renews the sign-in the server just refused stale, unless another
// request renewed it meanwhile: then the new token is returned.
func Renew(ctx context.Context, name, stale string) (string, error) {
	return renew(ctx, name, func(cur Record) bool { return cur.Access == stale })
}

func due(r Record) bool {
	return r.Expires != 0 && time.Now().Add(renewLead).UnixMilli() >= r.Expires
}

func renew(ctx context.Context, name string, still func(Record) bool) (string, error) {
	mu := lockOf(name)
	mu.Lock()
	defer mu.Unlock()
	// one renewal at a time across magpies as well: a server that rotates
	// its refresh tokens takes each once
	unlock, _ := filelock.Lock(path()+".renew.lock", filelock.Wait)
	defer unlock()
	r, ok := Get(name)
	if !ok {
		return "", ErrNotSignedIn
	}
	if r.Dead {
		return "", ErrExpired
	}
	if !still(r) {
		return r.Access, nil
	}
	if r.Refresh == "" {
		markDead(name, r.Access)
		return "", ErrExpired
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {r.Refresh}}
	if r.Resource != "" {
		form.Set("resource", r.Resource)
	}
	t, err := tokenRequest(ctx, r, form)
	if err != nil {
		var te *tokenError
		if errors.As(err, &te) && (dead(te.Code) || te.Status == http.StatusUnauthorized) {
			markDead(name, r.Access)
			return "", ErrExpired
		}
		return "", err
	}
	err = update(func(m map[string]*Record) bool {
		cur := m[name]
		if cur == nil || cur.Access != r.Access {
			return false // signed in again, or out, meanwhile
		}
		cur.Access = t.Access
		if t.Refresh != "" {
			cur.Refresh = t.Refresh
		}
		cur.Expires = t.expires()
		return true
	})
	return t.Access, err
}

// dead says a token endpoint's error means the refresh token, or the app
// it was given to, won't be taken again; GitHub's has its own names.
func dead(code string) bool {
	switch code {
	case "invalid_grant", "invalid_client", "bad_refresh_token", "incorrect_client_credentials":
		return true
	}
	return false
}

// Rejected says the server refused access, a token magpie had just
// renewed: renewing it again won't help, so the sign-in has run out, and
// the page says so instead of calling it signed in beside the server's
// refusal (新v学员 on Discord). A sign-in renewed or redone meanwhile is
// left as it is.
func Rejected(name, access string) { markDead(name, access) }

func markDead(name, access string) {
	_ = update(func(m map[string]*Record) bool {
		cur := m[name]
		if cur == nil || cur.Access != access {
			return false
		}
		cur.Dead = true
		return true
	})
}

type tokens struct {
	Access    string `json:"access_token"`
	Refresh   string `json:"refresh_token"`
	ExpiresIn int64  `json:"expires_in"`
	Scope     string `json:"scope"`
}

func (t tokens) expires() int64 {
	if t.ExpiresIn <= 0 {
		return 0
	}
	return time.Now().Add(time.Duration(t.ExpiresIn) * time.Second).UnixMilli()
}

type tokenError struct {
	Status      int
	Code        string `json:"error"`
	Description string `json:"error_description"`
}

func (e *tokenError) Error() string {
	s := fmt.Sprintf("the token endpoint answered %d", e.Status)
	if e.Code != "" {
		s += ": " + e.Code
	}
	if e.Description != "" {
		s += " (" + e.Description + ")"
	}
	return s
}

// tokenRequest posts form to the record's token endpoint, as its client.
func tokenRequest(ctx context.Context, r Record, form url.Values) (tokens, error) {
	if r.AuthMethod != "client_secret_basic" {
		form.Set("client_id", r.ClientID)
	}
	if r.AuthMethod == "client_secret_post" && r.Secret != "" {
		form.Set("client_secret", r.Secret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return tokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if r.AuthMethod == "client_secret_basic" {
		req.SetBasicAuth(url.QueryEscape(r.ClientID), url.QueryEscape(r.Secret))
	}
	resp, err := Client.Do(req)
	if err != nil {
		return tokens{}, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		te := &tokenError{Status: resp.StatusCode}
		_ = json.Unmarshal(b, te)
		return tokens{}, te
	}
	var t tokens
	// GitHub's answers its errors with a 200 too
	if te := (&tokenError{Status: resp.StatusCode}); json.Unmarshal(b, te) == nil && te.Code != "" {
		return tokens{}, te
	}
	if err := json.Unmarshal(b, &t); err != nil {
		// a few answer as a form, as GitHub's does
		if v, perr := url.ParseQuery(string(b)); perr == nil && v.Get("access_token") != "" {
			t.Access, t.Refresh, t.Scope = v.Get("access_token"), v.Get("refresh_token"), v.Get("scope")
			fmt.Sscan(v.Get("expires_in"), &t.ExpiresIn)
			return t, nil
		}
		return tokens{}, fmt.Errorf("the token endpoint's answer isn't JSON: %w", err)
	}
	if t.Access == "" {
		return tokens{}, errors.New("the token endpoint gave no access token")
	}
	return t, nil
}

// ---- discovery -------------------------------------------------------------

// meta is what discovery found: where the sign-in goes.
type meta struct {
	Resource     string
	Issuer       string
	AuthorizeURL string
	TokenURL     string
	RegisterURL  string
	Scope        string
	AuthMethods  []string // token_endpoint_auth_methods_supported
}

func origin(u *url.URL) string { return u.Scheme + "://" + u.Host }

// challenge is the server's 401: its resource metadata and the scope it
// asks for, from WWW-Authenticate.
func challenge(h http.Header) (metadata, scope string) {
	for _, v := range h.Values("WWW-Authenticate") {
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(v)), "bearer") {
			continue
		}
		for k, val := range authParams(strings.TrimSpace(v)[len("bearer"):]) {
			switch strings.ToLower(k) {
			case "resource_metadata":
				metadata = val
			case "scope":
				scope = val
			}
		}
	}
	return
}

// authParams parses a challenge's k=v and k="v" parameters.
func authParams(s string) map[string]string {
	out := map[string]string{}
	for s = strings.TrimSpace(s); s != ""; {
		eq := strings.IndexByte(s, '=')
		if eq < 0 {
			break
		}
		k := strings.TrimSpace(strings.TrimLeft(s[:eq], ", "))
		s = strings.TrimSpace(s[eq+1:])
		var v string
		if strings.HasPrefix(s, `"`) {
			var b strings.Builder
			i := 1
			for ; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				b.WriteByte(s[i])
			}
			v, s = b.String(), s[min(i+1, len(s)):]
		} else {
			end := strings.IndexByte(s, ',')
			if end < 0 {
				end = len(s)
			}
			v, s = strings.TrimSpace(s[:end]), s[end:]
		}
		out[k] = v
		s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), ","))
	}
	return out
}

func getJSON(ctx context.Context, u string, v any) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("MCP-Protocol-Version", protocolVersion)
	resp, err := Client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return false
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(v) == nil
}

const protocolVersion = "2025-06-18"

// discover finds how to sign in to the server at serverURL.
func discover(ctx context.Context, serverURL string) (meta, error) {
	su, err := url.Parse(serverURL)
	if err != nil || su.Host == "" {
		return meta{}, fmt.Errorf("%q isn't a URL", serverURL)
	}
	// ask it something, as an agent would: its 401 says where to sign in
	body := `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"` + protocolVersion + `","capabilities":{},"clientInfo":{"name":"magpie","version":"1"}}}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL, strings.NewReader(body))
	if err != nil {
		return meta{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", protocolVersion)
	resp, err := Client.Do(req)
	if err != nil {
		return meta{}, fmt.Errorf("couldn't reach %s: %w", su.Host, err)
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode/100 == 2 {
		return meta{}, ErrNoSignIn
	}
	metadataURL, scope := challenge(resp.Header)

	var prm struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
		Scopes               []string `json:"scopes_supported"`
	}
	var tries []string
	if metadataURL != "" {
		tries = append(tries, metadataURL)
	}
	if p := strings.TrimRight(su.Path, "/"); p != "" {
		tries = append(tries, origin(su)+"/.well-known/oauth-protected-resource"+p)
	}
	tries = append(tries, origin(su)+"/.well-known/oauth-protected-resource")
	found := false
	for _, u := range tries {
		if getJSON(ctx, u, &prm) && len(prm.AuthorizationServers) > 0 {
			found = true
			break
		}
	}
	m := meta{Resource: serverURL, Scope: scope}
	issuer := origin(su)
	if found {
		issuer = strings.TrimRight(prm.AuthorizationServers[0], "/")
		if prm.Resource != "" {
			m.Resource = prm.Resource
		}
		if m.Scope == "" && len(prm.Scopes) > 0 {
			m.Scope = strings.Join(prm.Scopes, " ")
		}
	}
	var as struct {
		Issuer        string   `json:"issuer"`
		Authorize     string   `json:"authorization_endpoint"`
		Token         string   `json:"token_endpoint"`
		Register      string   `json:"registration_endpoint"`
		Challenges    []string `json:"code_challenge_methods_supported"`
		AuthMethods   []string `json:"token_endpoint_auth_methods_supported"`
		ScopesSupport []string `json:"scopes_supported"`
	}
	iu, err := url.Parse(issuer)
	if err != nil || iu.Host == "" {
		return meta{}, fmt.Errorf("the server names an authorization server that isn't a URL (%q)", issuer)
	}
	var asTries []string
	if p := strings.TrimRight(iu.Path, "/"); p != "" {
		asTries = []string{origin(iu) + "/.well-known/oauth-authorization-server" + p,
			origin(iu) + "/.well-known/openid-configuration" + p, issuer + "/.well-known/openid-configuration"}
	} else {
		asTries = []string{issuer + "/.well-known/oauth-authorization-server", issuer + "/.well-known/openid-configuration"}
	}
	got := false
	for _, u := range asTries {
		if getJSON(ctx, u, &as) && as.Authorize != "" && as.Token != "" {
			got = true
			break
		}
	}
	switch {
	case got:
		if len(as.Challenges) > 0 && !contains(as.Challenges, "S256") {
			return meta{}, errors.New("the server's sign-in doesn't offer PKCE (S256), which magpie needs")
		}
		m.Issuer, m.AuthorizeURL, m.TokenURL, m.RegisterURL, m.AuthMethods = as.Issuer, as.Authorize, as.Token, as.Register, as.AuthMethods
		m.Scope = withOffline(m.Scope, as.ScopesSupport)
	case !found:
		// the older spec: the server's origin is its authorization server,
		// at these paths when it has no metadata
		o := origin(su)
		m.Issuer, m.AuthorizeURL, m.TokenURL, m.RegisterURL = o, o+"/authorize", o+"/token", o+"/register"
	default:
		return meta{}, fmt.Errorf("couldn't read the metadata of %s, the server's authorization server", issuer)
	}
	if resp.StatusCode != http.StatusUnauthorized && !found && !got {
		if turnedAway(resp) {
			return meta{}, ErrTurnedAway
		}
		return meta{}, fmt.Errorf("the server answered %d and says nothing of a sign-in", resp.StatusCode)
	}
	return m, nil
}

// turnedAway is a 403 from a firewall rather than the MCP server: a web page
// to a JSON-RPC request, or Cloudflare saying it stopped it.
func turnedAway(resp *http.Response) bool {
	if resp.StatusCode != http.StatusForbidden {
		return false
	}
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	return ct == "text/html" || resp.Header.Get("Cf-Mitigated") != ""
}

// withOffline asks for offline_access too when the authorization server
// offers it. An OpenID provider gives a refresh token only to a sign-in that
// asks for it (Vercel's: the server names only "openid", and without
// offline_access its access token runs out in an hour with nothing to renew
// it by, #615); the MCP spec lets a client add it. A sign-in that names no
// scope is left as the server's default.
func withOffline(scope string, supported []string) string {
	if scope == "" || !contains(supported, "offline_access") || contains(strings.Fields(scope), "offline_access") {
		return scope
	}
	return scope + " offline_access"
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// register makes magpie a client of the authorization server, sent back to
// redirect.
func register(ctx context.Context, m meta, redirect string) (id, secret, method string, err error) {
	if m.RegisterURL == "" {
		return "", "", "", ErrNoRegistration
	}
	method = "none"
	if len(m.AuthMethods) > 0 && !contains(m.AuthMethods, "none") {
		// the metadata's order is no preference: one listing private_key_jwt
		// first, which magpie can't do, may take a secret too
		switch {
		case contains(m.AuthMethods, "client_secret_post"):
			method = "client_secret_post"
		case contains(m.AuthMethods, "client_secret_basic"):
			method = "client_secret_basic"
		default:
			return "", "", "", fmt.Errorf("the server's token endpoint takes only %s, which magpie can't do", strings.Join(m.AuthMethods, ", "))
		}
	}
	reg := map[string]any{
		"client_name":                "Magpie",
		"client_uri":                 "https://usemagpie.ai",
		"redirect_uris":              []string{redirect},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": method,
	}
	if m.Scope != "" {
		reg["scope"] = m.Scope
	}
	b, _ := json.Marshal(reg)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.RegisterURL, bytes.NewReader(b))
	if err != nil {
		return "", "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := Client.Do(req)
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", "", "", ErrClientRefused
	}
	if resp.StatusCode/100 != 2 {
		return "", "", "", fmt.Errorf("registering magpie with the server's sign-in failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(rb)))
	}
	var out struct {
		ID     string `json:"client_id"`
		Secret string `json:"client_secret"`
		Method string `json:"token_endpoint_auth_method"`
	}
	if err := json.Unmarshal(rb, &out); err != nil || out.ID == "" {
		return "", "", "", errors.New("the server's sign-in registered magpie but gave it no client id")
	}
	if out.Method != "" {
		// the server may register magpie for another of the methods it
		// supports than the one asked for (RFC 7591's registration
		// response): one magpie can't send would fail the code exchange
		// after the browser round trip, so it is said here instead
		if !contains([]string{"none", "client_secret_post", "client_secret_basic"}, out.Method) {
			return "", "", "", fmt.Errorf("the server's sign-in registered magpie for %s, which magpie can't do", out.Method)
		}
		method = out.Method
	}
	if out.Secret == "" {
		method = "none"
	} else if method == "none" {
		method = "client_secret_post"
	}
	return out.ID, out.Secret, method, nil
}

// appMethod is how an app of the user's own gives its secret to the token
// endpoint: in the form unless the endpoint takes only basic auth.
func appMethod(m meta, secret string) string {
	switch {
	case secret == "":
		return "none"
	case contains(m.AuthMethods, "client_secret_basic") && !contains(m.AuthMethods, "client_secret_post"):
		return "client_secret_basic"
	}
	return "client_secret_post"
}

// ---- the sign-in -------------------------------------------------------------

// State is where a sign-in stands, for the page to show.
type State struct {
	ID     string `json:"id"`
	Server string `json:"server"`
	URL    string `json:"url,omitempty"` // the sign-in page, to open
	State  string `json:"state"`         // waiting, done, failed or canceled
	Error  string `json:"error,omitempty"`
}

type flow struct {
	mu       sync.Mutex
	st       State
	srv      *http.Server
	done     chan struct{}
	claimed  bool
	finished bool
}

var flows = struct {
	sync.Mutex
	m map[string]*flow
}{m: map[string]*flow{}}

func randomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// App is an OAuth app of the user's own, for a server whose sign-in
// doesn't let magpie register itself: Claude Code's --client-id,
// --client-secret and --callback-port. Its callback is Claude Code's,
// http://localhost:<port>/callback, so an app made for Claude Code signs
// magpie in as it is; the callback is still listened for on 127.0.0.1
// alone, as Claude Code does. Without a CallbackPort the port is any free
// one, which a loopback callback URL registered without one takes
// (GitHub's, RFC 8252).
type App struct {
	ClientID     string
	ClientSecret string
	CallbackPort int
}

// Start begins signing magpie in to the server called name at serverURL:
// open the State's URL in a browser and the rest happens on its own;
// StatusOf follows it.
func Start(ctx context.Context, name, serverURL string) (State, error) {
	return StartWith(ctx, name, serverURL, nil)
}

// StartWith is Start as app when there is one, which magpie then doesn't
// register in its place.
func StartWith(ctx context.Context, name, serverURL string, app *App) (State, error) {
	return start(ctx, name, serverURL, app, nil)
}

// StartAccount is StartWith as another account of the server: the sign-in
// it makes is added to the ones there are and put in use, with the label,
// and the others are kept. With no sign-in yet it is the first.
func StartAccount(ctx context.Context, name, serverURL string, app *App, label string) (State, error) {
	label = strings.TrimSpace(label)
	if len([]rune(label)) > 60 {
		return State{}, errors.New("the name of a sign-in is at most 60 characters")
	}
	if label != "" {
		for _, a := range Accounts(name) {
			if strings.EqualFold(a.Label, label) {
				return State{}, ErrLabelUsed
			}
		}
	}
	return start(ctx, name, serverURL, app, &label)
}

func start(ctx context.Context, name, serverURL string, app *App, add *string) (State, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	m, err := discover(ctx, serverURL)
	if err != nil {
		return State{}, err
	}
	port := 0
	if app != nil {
		port = app.CallbackPort
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		if port != 0 {
			return State{}, fmt.Errorf("the OAuth app's callback port %d is in use by another program: %w", port, err)
		}
		return State{}, err
	}
	// magpie's own registration names 127.0.0.1, as RFC 8252 asks; an
	// app of the user's own is called back as Claude Code calls it back,
	// at localhost (its v2.1.231 went back to localhost because servers
	// that match the registered URL exactly refused 127.0.0.1).
	host := "127.0.0.1"
	if app != nil {
		host = "localhost"
	}
	redirect := fmt.Sprintf("http://%s:%d/callback", host, ln.Addr().(*net.TCPAddr).Port)
	var id, secret, method string
	if app != nil {
		id, secret, method = app.ClientID, app.ClientSecret, appMethod(m, app.ClientSecret)
	} else if id, secret, method, err = register(ctx, m, redirect); err != nil {
		ln.Close()
		return State{}, err
	}
	verifier, state := randomToken(48), randomToken(24)
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {id},
		"redirect_uri":          {redirect},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
		"state":                 {state},
		"resource":              {m.Resource},
	}
	if m.Scope != "" {
		q.Set("scope", m.Scope)
	}
	au, err := url.Parse(m.AuthorizeURL)
	if err != nil {
		ln.Close()
		return State{}, fmt.Errorf("the authorization endpoint isn't a URL: %w", err)
	}
	aq := au.Query()
	for k, v := range q {
		aq[k] = v
	}
	au.RawQuery = aq.Encode()

	f := &flow{done: make(chan struct{}), st: State{ID: randomToken(9), Server: name, URL: au.String(), State: "waiting"}}
	rec := Record{URL: serverURL, Resource: m.Resource, Issuer: m.Issuer, TokenURL: m.TokenURL, ClientID: id, Secret: secret, AuthMethod: method, Scope: m.Scope}
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		v := r.URL.Query()
		if v.Get("state") != state {
			page(w, http.StatusBadRequest, "This sign-in link is out of date. Start the sign-in again from Magpie.")
			return
		}
		f.mu.Lock()
		if f.claimed || f.finished {
			f.mu.Unlock()
			page(w, http.StatusOK, "Magpie has this sign-in already. You can close this tab.")
			return
		}
		f.claimed = true
		f.mu.Unlock()
		if e := v.Get("error"); e != "" {
			msg := e
			if d := v.Get("error_description"); d != "" {
				msg += ": " + d
			}
			f.finish(State{State: "failed", Error: "the sign-in was refused (" + msg + ")"})
			page(w, http.StatusOK, "The sign-in was refused: "+msg)
			return
		}
		cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer ccancel()
		form := url.Values{"grant_type": {"authorization_code"}, "code": {v.Get("code")}, "redirect_uri": {redirect}, "code_verifier": {verifier}}
		if rec.Resource != "" {
			form.Set("resource", rec.Resource)
		}
		t, err := tokenRequest(cctx, rec, form)
		if err != nil {
			f.finish(State{State: "failed", Error: err.Error()})
			page(w, http.StatusOK, "Magpie couldn't finish the sign-in: "+err.Error())
			return
		}
		r2 := rec
		r2.Access, r2.Refresh, r2.Expires, r2.At = t.Access, t.Refresh, t.expires(), time.Now().UnixMilli()
		if t.Scope != "" {
			r2.Scope = t.Scope
		}
		if err := f.keep(name, r2, add); err != nil {
			page(w, http.StatusOK, "Magpie couldn't keep the sign-in: "+err.Error())
			return
		}
		page(w, http.StatusOK, "Magpie is signed in to "+name+". Every agent given it in the Library uses this sign-in. You can close this tab.")
	})
	f.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go f.srv.Serve(ln)
	flows.Lock()
	flows.m[f.st.ID] = f
	flows.Unlock()
	go func() {
		select {
		case <-f.done:
		case <-time.After(signInTimeout):
			f.finish(State{State: "failed", Error: "the sign-in timed out; start it again"})
		}
	}()
	return f.status(), nil
}

func (f *flow) status() State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st
}

// keep commits the callback only while the flow is still live. Cancel or
// timeout and the write take the same lock, so a late token cannot save a
// sign-in the user already ended, or replace their previous sign-in.
func (f *flow) keep(name string, r Record, add *string) error {
	f.mu.Lock()
	if f.finished {
		f.mu.Unlock()
		return errors.New("the sign-in has ended; start it again")
	}
	err := update(func(m map[string]*Record) bool { m[name] = signedAs(m[name], r, add); return true })
	f.finished = true
	f.mu.Unlock()
	if err != nil {
		f.end(State{State: "failed", Error: err.Error()})
		return err
	}
	// The commit won over cancellation; give the agents the new address
	// outside the flow's lock, before saying the sign-in is done.
	if Changed != nil {
		Changed(name)
	}
	f.end(State{State: "done"})
	return nil
}

// signedAs is the record a new sign-in leaves for the server: an added
// account joins the ones there are, in use; any other replaces the one in
// use (signing in again), and the other accounts stay.
func signedAs(old *Record, r Record, add *string) *Record {
	if old == nil || old.URL != r.URL {
		if add != nil {
			r.Label = *add
		}
		return &r
	}
	if add == nil {
		r.ID, r.Label, r.Seq, r.Spare = old.ID, old.Label, old.Seq, old.Spare
		return &r
	}
	l := all(old)
	top := 0
	for _, x := range l {
		top = max(top, seqOf(x))
		x.ID, x.Seq = idOf(x), seqOf(x)
	}
	r.Seq, r.ID, r.Label = top+1, strconv.Itoa(top+1), *add
	r.Spare = l
	return &r
}

func (f *flow) finish(st State) {
	f.mu.Lock()
	if f.finished {
		f.mu.Unlock()
		return
	}
	f.finished = true
	f.mu.Unlock()
	f.end(st)
}

// end is called once by the operation that claimed finished.
func (f *flow) end(st State) {
	f.mu.Lock()
	f.st.State, f.st.Error = st.State, st.Error
	f.mu.Unlock()
	close(f.done)
	// the browser still has the page to load
	go func() {
		time.Sleep(2 * time.Second)
		f.srv.Close()
	}()
	go func() {
		time.Sleep(10 * time.Minute)
		flows.Lock()
		delete(flows.m, f.st.ID)
		flows.Unlock()
	}()
}

// Progress is a sign-in Start began.
func Progress(id string) (State, bool) {
	flows.Lock()
	f := flows.m[id]
	flows.Unlock()
	if f == nil {
		return State{}, false
	}
	return f.status(), true
}

// Cancel ends a sign-in Start began.
func Cancel(id string) {
	flows.Lock()
	f := flows.m[id]
	flows.Unlock()
	if f != nil {
		f.finish(State{State: "canceled"})
	}
}

func page(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>Magpie</title><body style="font:15px -apple-system,system-ui,sans-serif;max-width:32em;margin:4em auto;padding:0 1em;color:#222">%s</body>`, html.EscapeString(msg))
}
