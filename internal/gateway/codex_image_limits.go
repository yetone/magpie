package gateway

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/provider"
)

// ChatGPT's images API tells the image allowance of the account it drew on
// in every reply, a 200 as well as a 429 (#6412, hqqqqqqqh): it names the
// limit counted, x-codex-active-limit: imagegen_premium, and the window in
// the x-codex-primary-* headers (window-minutes 1440, reset-at), or, as
// Codex 0.160.1 reads it (codex-api rate_limits.rs, parse_all_rate_limits),
// in a family named for the limit: x-imagegen-premium-primary-*, or
// x-image-gen-primary-* for image_gen. That window is the image row's
// (provider.ImageLane), never the account's 5-hour or weekly one: it is
// kept apart from them (provider.NoteCodexLimits is never told it), and a
// chat reply that names an image limit tells no chat window either.

// imageLimitName is a limit id of the image allowance: imagegen_premium,
// image_gen, as x-codex-active-limit or a header family names it.
func imageLimitName(name string) bool {
	n := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(name)), "-", "_")
	return strings.HasPrefix(n, "imagegen") || strings.HasPrefix(n, "image_gen")
}

// imageFamily is a header of a limit's own family's window
// (x-imagegen-premium-primary-window-minutes), with the limit's name.
var imageFamily = lazyre.New(`^x-(.+)-(?:primary|secondary)-window-minutes$`)

// codexImageHeaderLimit is the image window h tells: of the family named
// for the image limit first, else, when x-codex-active-limit names an
// image limit, of the x-codex-* family; false when it tells none.
func codexImageHeaderLimit(h http.Header, now time.Time) (provider.CodexLimit, bool) {
	var fams []string
	for k := range h {
		if m := imageFamily.FindStringSubmatch(strings.ToLower(k)); m != nil && m[1] != "codex" && imageLimitName(m[1]) {
			fams = append(fams, m[1])
		}
	}
	if imageLimitName(h.Get("x-codex-active-limit")) {
		fams = append(fams, "codex")
	}
	for _, fam := range fams {
		if ls := headerLimitsOf(h, "x-"+fam+"-", now); len(ls) > 0 {
			l := ls[0]
			for _, o := range ls[1:] {
				// of two windows, the fuller is the one that holds
				if o.Used > l.Used {
					l = o
				}
			}
			return l, true
		}
	}
	return provider.CodexLimit{}, false
}

// codexImageHeard is the image window each ChatGPT account's last reply
// told, by agent (as its usage is read)/lower-cased user.
var codexImageHeard = struct {
	sync.Mutex
	m map[string]provider.CodexLimit
}{m: map[string]provider.CodexLimit{}}

func init() { provider.ImageHeard = imageHeardOf }

// heardCodexImage keeps the image window h tells of p's ChatGPT account.
func heardCodexImage(p provider.Provider, h http.Header) {
	if p.Account == nil || p.Account.Agent != "codex" || p.Account.User == "" || h == nil {
		return
	}
	l, ok := codexImageHeaderLimit(h, time.Now())
	if !ok {
		return
	}
	codexImageHeard.Lock()
	codexImageHeard.m[p.Account.UsageAgent()+"/"+strings.ToLower(p.Account.User)] = l
	codexImageHeard.Unlock()
}

// imageHeardOf is the image window a reply last told of agent's account
// user, while it has not renewed: one with no reset said, or one past
// it, tells nothing of now.
func imageHeardOf(agent, user string, now time.Time) (provider.CodexLimit, bool) {
	codexImageHeard.Lock()
	l, ok := codexImageHeard.m[agent+"/"+strings.ToLower(user)]
	codexImageHeard.Unlock()
	return l, ok && l.Resets.After(now)
}
