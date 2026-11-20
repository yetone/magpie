// Package egress finds where this computer's way out to the internet is,
// through each proxy a Claude subscription's requests go through, and
// says when it is in a region Anthropic doesn't list as supported
// (Supported): a proxy that dropped, a VPN turned off, a node in the
// wrong country (@alterxyz4 on X asked for this "canary").
//
// The lookup asks Cloudflare's trace page (TraceURL) through the same
// proxy, and reads its loc= line. It never touches Anthropic or the
// user's account. It runs when magpie starts, when the network or a
// proxy setting changes, and every Every, never per request (Keep). A
// lookup that fails says nothing: its state is Unknown, which neither
// warns nor holds.
package egress

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/settings"
)

// The states a way out can be in.
const (
	OK          = "ok"          // in a supported region
	Unsupported = "unsupported" // in a region the policy doesn't list
	Unknown     = "unknown"     // not looked up, or the lookup failed
)

// Result is what the last lookup through one proxy choice found.
type Result struct {
	State string    `json:"state"`
	Loc   string    `json:"loc,omitempty"` // ISO 3166-1 code, as Cloudflare names it
	At    time.Time `json:"at,omitempty"`
	Error string    `json:"error,omitempty"`
	// Via is the proxy it went through, as magpie may show it
	// (netproxy.Redact): "" for the global one, "direct" for none.
	Via string `json:"via,omitempty"`
}

// TraceURL is the page asked; a test points it at its own server.
var TraceURL = "https://www.cloudflare.com/cdn-cgi/trace"

// Client sends the lookup. http.DefaultClient goes through netproxy
// (netproxy.Install), so the choice With names is the proxy it takes.
var Client = http.DefaultClient

// Timeout is how long one lookup may take.
var Timeout = 10 * time.Second

var (
	// Every is how often each choice is looked up again with nothing
	// changed; Poll how often Keep looks for a change of network or
	// proxy; Fresh how old an Unsupported may be before Hold looks again.
	Every = 15 * time.Minute
	Poll  = 20 * time.Second
	Fresh = 30 * time.Second
)

var (
	mu      sync.Mutex
	results = map[string]Result{}
	kick    = make(chan struct{}, 1)
	// changed is called after a lookup changes a state (the tray's
	// tooltip draws again)
	changed []func()
)

// OnChange has f called after a lookup changes what a choice's state is.
func OnChange(f func()) {
	mu.Lock()
	changed = append(changed, f)
	mu.Unlock()
}

// Of is the last lookup through choice; Unknown when there was none.
func Of(choice string) Result {
	mu.Lock()
	defer mu.Unlock()
	if r, ok := results[strings.TrimSpace(choice)]; ok {
		return r
	}
	return Result{State: Unknown, Via: shown(choice)}
}

// Worst is the result of choices to show: the first Unsupported, else
// the first Unknown, else an OK (one of each choice's being fine).
func Worst(choices []string) Result {
	var unknown, ok *Result
	for _, c := range choices {
		r := Of(c)
		switch r.State {
		case Unsupported:
			return r
		case Unknown:
			if unknown == nil {
				unknown = &r
			}
		default:
			if ok == nil {
				ok = &r
			}
		}
	}
	if unknown != nil {
		return *unknown
	}
	if ok != nil {
		return *ok
	}
	return Result{State: Unknown}
}

// Check looks up the way out through choice now, and keeps what it found.
func Check(ctx context.Context, choice string) Result {
	choice = strings.TrimSpace(choice)
	r := lookup(ctx, choice)
	r.Via = shown(choice)
	mu.Lock()
	was, had := results[choice]
	results[choice] = r
	fs := slices.Clone(changed)
	mu.Unlock()
	if !had || was.State != r.State || was.Loc != r.Loc {
		for _, f := range fs {
			f()
		}
	}
	return r
}

// Forget drops every lookup kept (a test's cleanup).
func Forget() {
	mu.Lock()
	results = map[string]Result{}
	mu.Unlock()
}

// Again has Keep look every choice up again now (the GUI's Check again).
func Again() {
	select {
	case kick <- struct{}{}:
	default:
	}
}

func shown(choice string) string {
	switch choice = strings.TrimSpace(choice); choice {
	case "", "direct":
		return choice
	}
	if settings.IsProxyRef(choice) {
		return settings.ProxyLabel(settings.ProxyID(choice))
	}
	return netproxy.Redact(choice)
}

func lookup(ctx context.Context, choice string) Result {
	ctx, cancel := context.WithTimeout(netproxy.With(ctx, choice), Timeout)
	defer cancel()
	now := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, TraceURL, nil)
	if err != nil {
		return Result{State: Unknown, At: now, Error: err.Error()}
	}
	resp, err := Client.Do(req)
	if err != nil {
		return Result{State: Unknown, At: now, Error: redactErr(err.Error(), choice)}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Result{State: Unknown, At: now, Error: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}
	loc := ""
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 16<<10))
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), "="); ok && k == "loc" {
			loc = strings.ToUpper(strings.TrimSpace(v))
		}
	}
	return judge(loc, now)
}

// judge is the state a lookup's loc says. Cloudflare's XX (not known)
// and T1 (Tor), and anything that isn't two letters, are Unknown.
func judge(loc string, at time.Time) Result {
	if len(loc) != 2 || loc[0] < 'A' || loc[0] > 'Z' || loc[1] < 'A' || loc[1] > 'Z' || loc == "XX" {
		return Result{State: Unknown, Loc: loc, At: at, Error: "no region in the answer"}
	}
	if supportedLoc(loc) {
		return Result{State: OK, Loc: loc, At: at}
	}
	return Result{State: Unsupported, Loc: loc, At: at}
}

// redactErr takes a proxy's address, with its password, out of an error.
func redactErr(s, choice string) string {
	if a := netproxy.Resolve(choice); a != "" && a != "direct" {
		s = strings.ReplaceAll(s, a, netproxy.Redact(a))
	}
	if i := strings.Index(s, "://"); i >= 0 {
		if j := strings.Index(s[i+3:], "@"); j >= 0 && !strings.ContainsAny(s[i+3:i+3+j], " /") {
			s = s[:i+3] + s[i+3+j+1:]
		}
	}
	return s
}

// Keep looks up the way out through each of choices() when it starts,
// when the network or a proxy changes, every Every, and when Again asks,
// until ctx ends. choices is asked each Poll: the distinct proxy choices
// the Claude accounts' requests take; none, nothing is looked up.
func Keep(ctx context.Context, choices func() []string) {
	var last string
	var at time.Time
	t := time.NewTicker(Poll)
	defer t.Stop()
	for {
		cs := choices()
		fp := fingerprint(cs)
		if len(cs) > 0 && (fp != last || time.Since(at) >= Every) {
			last, at = fp, time.Now()
			var wg sync.WaitGroup
			for _, c := range cs {
				wg.Add(1)
				go func() { defer wg.Done(); Check(ctx, c) }()
			}
			wg.Wait()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-kick:
			at = time.Time{}
		}
	}
}

// fingerprint is what a change of network or proxy changes: this
// computer's addresses, the global proxy in force and each choice's.
var fingerprint = func(choices []string) string {
	var b strings.Builder
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			b.WriteString(a.String())
			b.WriteByte(',')
		}
	}
	p, src := netproxy.Describe()
	fmt.Fprintf(&b, "|%s %s|", p, src)
	for _, c := range choices {
		fmt.Fprintf(&b, "%s=%s;", c, netproxy.Resolve(c))
	}
	return b.String()
}

// Hold is the error a Claude subscription's request through choice is
// held with: when the user turned the hold on (settings
// ClaudeRegionHold) and the way out through choice is in an unsupported
// region. An Unsupported older than Fresh is looked up again first, so a
// proxy turned back on lets the next request go. Unknown holds nothing.
func Hold(ctx context.Context, choice string) error {
	if !settings.Shared().ClaudeRegionHold {
		return nil
	}
	r := Of(choice)
	if r.State == Unknown && r.At.IsZero() {
		// a choice not looked up yet (an account given its own proxy a
		// moment ago): looked up for the next request, this one goes
		Again()
		return nil
	}
	if r.State == Unsupported && time.Since(r.At) > Fresh {
		r = Check(ctx, choice)
	}
	if r.State != Unsupported {
		return nil
	}
	return &HeldError{Result: r}
}

// HeldError is a request Hold kept from going out.
type HeldError struct{ Result Result }

// HeldWords begin every HeldError's text; the gateway reads them as a
// request that never left (nobody rests for it).
const HeldWords = "magpie held this Claude request"

func (e *HeldError) Error() string {
	via := "as magpie's proxy setting sends it"
	switch e.Result.Via {
	case "":
	case "direct":
		via = "going direct"
	default:
		via = "through " + e.Result.Via
	}
	return fmt.Sprintf("%s: this computer's way out to the internet, %s, is in region %s, which isn't on Anthropic's list of supported regions. Turn the proxy or VPN back on, or turn off \"Hold requests outside supported regions\" in Providers › Claude › Advanced", HeldWords, via, e.Result.Loc)
}
