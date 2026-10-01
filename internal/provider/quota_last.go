package provider

// The last allowance each account reported, kept on disk, so a vendor
// turning the usage endpoint away for a while (Anthropic's 429s) or not
// answering shows what was last read, not an error — across restarts too.
// A reading that comes back replaces it at once.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var lastQuotas struct {
	sync.Mutex
	m      map[string]lastQuota // provider/user
	loaded bool
}

type lastQuota struct {
	At time.Time         `json:"at"`
	Q  SubscriptionQuota `json:"quota"`
	// what the windows' json leaves out, for routing
	Spans []lastWindow `json:"spans,omitempty"`
}

type lastWindow struct {
	Span  time.Duration `json:"span,omitempty"`
	Model string        `json:"model,omitempty"`
	Aside bool          `json:"aside,omitempty"`
}

func lastQuotasPath() string { return filepath.Join(filepath.Dir(Path()), "quotas.json") }

// passing is an error that says nothing about the account: the vendor
// rate limiting, failing or out of reach, or a Claude account's usage not
// read this time (errClaudeNotAsked, errClaudeSaved). A sign-in gone bad
// is not one.
var passing = regexp.MustCompile(`(?i)not read yet|doesn't read a saved account|rate.?limit|too many requests|429|internal server error|bad gateway|service unavailable|gateway timeout|deadline exceeded|timeout|timed out|connection (refused|reset)|no such host|network is unreachable|EOF|asks again|fetch failed|unable to connect|ECONN[A-Z]+|ENOTFOUND|EAI_AGAIN|socket hang up|HTTP 5\d\d`)

// keyTag names a key's card among the last readings, by what it is (a
// plan's windows, a balance) and a digest of the key, never the key: a
// key's card has no user to go by.
func keyTag(kind, key string) string {
	sum := sha256.Sum256([]byte(key))
	return kind + " " + hex.EncodeToString(sum[:8])
}

// keepLast is q, or the last reading of its account when q failed in
// passing; a good reading is kept for next time.
func keepLast(q SubscriptionQuota, user string) SubscriptionQuota {
	if user == "" {
		user = q.User
	}
	if user == "" || q.Provider == "" {
		return q
	}
	key := q.Provider + "/" + strings.ToLower(user)
	c := &lastQuotas
	c.Lock()
	defer c.Unlock()
	if !c.loaded {
		c.loaded = true
		if b, err := os.ReadFile(lastQuotasPath()); err == nil {
			json.Unmarshal(b, &c.m)
		}
	}
	if q.Error == "" {
		if len(q.Windows) == 0 && q.Balance == "" {
			return q
		}
		e := lastQuota{At: time.Now(), Q: q}
		for _, w := range q.Windows {
			if w.matches != nil {
				return q // a pool that can't be written down isn't kept
			}
			e.Spans = append(e.Spans, lastWindow{w.Span, w.Model, w.Aside})
		}
		e.Q.Error = ""
		e.Q.AsOf = nil
		if c.m == nil {
			c.m = map[string]lastQuota{}
		}
		c.m[key] = e
		if b, err := json.Marshal(c.m); err == nil {
			os.MkdirAll(filepath.Dir(lastQuotasPath()), 0o700)
			writeFileAtomic(lastQuotasPath(), b)
		}
		return q
	}
	e, ok := c.m[key]
	if !ok || !passing.MatchString(q.Error) {
		return q
	}
	out := e.Q
	out.Windows = make([]QuotaWindow, len(e.Q.Windows))
	copy(out.Windows, e.Q.Windows)
	for i := range out.Windows {
		if i < len(e.Spans) {
			out.Windows[i].Span, out.Windows[i].Model, out.Windows[i].Aside = e.Spans[i].Span, e.Spans[i].Model, e.Spans[i].Aside
		}
	}
	out.Windows = elapsed(out.Windows, time.Now())
	out.Name, out.Icon, out.User = q.Name, q.Icon, q.User
	if out.Name == "" {
		out.Name, out.Icon = e.Q.Name, e.Q.Icon
	}
	at := e.At
	out.AsOf = &at
	return out
}
