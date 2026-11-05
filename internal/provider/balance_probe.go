package provider

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// A relay added by hand, with no Balance URL, is asked where relays tell a
// key its balance: a sub2api relay's /v1/usage, then a new-api (one-api)
// relay's /api/usage/token, both with the key alone. A relay imported from
// its own site names its query; one typed in had none, so its balance
// showed nowhere until the user found the field at the foot of the form
// (小鱼 of yylx.io, 11-05: deleted and imported again, it worked).
//
// What is found is kept for the origin and key, for balanceProbeHit, and
// then looked for again, so a relay that moved to other software is not
// read at a query that now means something else; finding nothing is kept
// for balanceProbeMiss.
var balanceProbes = []string{sub2APIKeyUsage, newAPIKeyUsage}

// balanceProbeDown is how long a relay that couldn't be reached at all
// is left before it is looked at again.
const (
	balanceProbeHit  = 24 * time.Hour
	balanceProbeMiss = 6 * time.Hour
	balanceProbeDown = 5 * time.Minute
)

type probeFound struct {
	url   string    // "" when neither query told a balance
	until time.Time // when it is looked for again
}

// probeLocal lets a test's relay on loopback be looked at.
var probeLocal bool

var probed struct {
	sync.Mutex
	m map[string]probeFound
}

// balanceProbeKey is what a probe is kept by: the origins and the key
// (hashed, never kept as it is).
func balanceProbeKey(origins []string, key string) string {
	return strings.Join(origins, " ") + " " + keyTag("probe", key)
}

// probesBalance is a provider whose balance is looked for: added by hand
// (no preset, no account, a key), with no Balance URL and no host magpie
// knows the query of, and at a relay's address, never a model vendor's own
// or this computer's.
func probesBalance(p Provider) bool {
	if p.Account != nil || p.Key == "" || strings.TrimSpace(p.BalanceURL) != "" || !relayPreset(p.Preset) {
		return false
	}
	if _, ok := balanceSourceOf(p); ok || clineKeyCard(p) || openCodeZen(p) {
		return false
	}
	if _, ok := planQuotaSourceOf(p); ok {
		return false // a plan's windows are its card
	}
	return len(probeOrigins(p)) > 0
}

// relayPreset is a provider added by hand, or from a relay's preset,
// which names no balance query of its own (鱼鱼连线's, the same relay
// as 小鱼's).
func relayPreset(id string) bool {
	if id == "" {
		return true
	}
	for _, d := range presets {
		if d.ID == id {
			return d.Kind == KindRelay || d.Kind == KindPartner
		}
	}
	return false
}

// noProbeHosts are relays whose keys are told their balance some other
// way, or not at all: OpenCode's Zen and Go (openCodeZen, its plan) and
// the Cline API (clineKeyCard).
var noProbeHosts = []string{"opencode.ai", "api.cline.bot"}

// probeOrigins are the scheme and host of each base the provider names,
// each once, leaving out a vendor's own host and a local one.
func probeOrigins(p Provider) []string {
	var out []string
	for _, raw := range []string{p.Chat, p.Responses, p.Anthropic} {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			continue
		}
		if notProbed(u.Hostname()) {
			continue
		}
		o := u.Scheme + "://" + strings.ToLower(u.Host)
		if !contains(out, o) {
			out = append(out, o)
		}
	}
	return out
}

// notProbed is a host never asked a relay's query: this computer, or a
// host a vendor's or a local server's preset names.
func notProbed(host string) bool {
	host = strings.ToLower(host)
	if !probeLocal && (isLoopback(host) || strings.HasSuffix(host, ".localhost")) {
		return true
	}
	if contains(noProbeHosts, host) {
		return true
	}
	for _, d := range presets {
		if d.Kind != KindVendor && d.Kind != KindLocal {
			continue
		}
		bases := []string{d.Chat, d.Responses, d.Anthropic}
		for _, r := range d.Regions {
			bases = append(bases, r.Chat, r.Responses, r.Anthropic)
		}
		for _, b := range bases {
			if b != "" && strings.EqualFold(hostnameOf(b), host) {
				return true
			}
		}
	}
	return false
}

func hostnameOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// probedBalanceURL is the query found for p, without asking: "" when none
// was found or none was looked for yet.
func probedBalanceURL(p Provider) string {
	if !probesBalance(p) {
		return ""
	}
	probed.Lock()
	defer probed.Unlock()
	return probed.m[balanceProbeKey(probeOrigins(p), p.Key)].url
}

// BalanceFound is the balance query magpie found for p by itself, with
// its field ("" when none was found yet), and whether p is one it looks
// for at all: a relay added by hand with no Balance URL. The editor shows
// it, so a wrong amount can be put right with a Balance URL of the user's.
func BalanceFound(p Provider) (u, field string, looked bool) {
	p = keyQueryOf(p)
	if !probesBalance(p) {
		return "", "", false
	}
	if u = probedBalanceURL(p); u != "" {
		p.BalanceURL = u
		field = balancePathOf(p)
	}
	return u, field, true
}

// probeBalance looks for p's balance query, asking each origin's relay
// queries in turn, and is what was found (false: none told a balance).
// A query that answers 200 with its field read is the one; a failure to
// reach the relay is not kept, so the next read looks again.
func probeBalance(ctx context.Context, p Provider) (string, bool) {
	origins := probeOrigins(p)
	k := balanceProbeKey(origins, p.Key)
	probed.Lock()
	f, seen := probed.m[k]
	probed.Unlock()
	if seen && time.Now().Before(f.until) {
		return f.url, f.url != ""
	}
	found, reached, refused := "", false, false
	for _, o := range origins {
		for _, path := range balanceProbes {
			ok, code := askProbe(ctx, p, o+path)
			reached = reached || code != 0
			if o+path == f.url && (code == http.StatusUnauthorized || code == http.StatusForbidden) {
				refused = true
			}
			if ok {
				found = o + path
				break
			}
		}
		if found != "" {
			break
		}
	}
	if ctx.Err() != nil {
		return "", false // cut short: nothing learnt
	}
	wait := balanceProbeMiss
	switch {
	case found != "":
		wait = balanceProbeHit
	case f.url != "" && (refused || !reached):
		// the query found before turned the key away, or the relay is
		// down: still its query, where the card says what went wrong
		found, wait = f.url, balanceProbeDown
		if refused {
			wait = balanceProbeHit
		}
	case !reached:
		wait = balanceProbeDown
	}
	probed.Lock()
	if probed.m == nil {
		probed.m = map[string]probeFound{}
	}
	probed.m[k] = probeFound{found, time.Now().Add(wait)}
	probed.Unlock()
	return found, found != ""
}

// askProbe asks one relay query as balanceRead asks it, and is whether it
// told a balance, and the status the relay answered with (0: none).
func askProbe(ctx context.Context, p Provider, raw string) (ok bool, code int) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return false, 0
	}
	for k, v := range AuthHeaders(p, Chat) {
		req.Header.Set(k, v)
	}
	for k, v := range p.SentHeaders(ctx) {
		req.Header.Set(k, withBalanceKey(v, p.Key, false))
	}
	req.Header.Set("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, 0
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return false, res.StatusCode
	}
	q := p
	q.BalanceURL, q.BalancePath = raw, ""
	amount, err := readBalancePath(b, balancePathOf(q))
	return err == nil && amount != "", res.StatusCode
}
