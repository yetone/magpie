package access

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/settings"
)

// Short-lived keys (xazaj on X, moving off an enterprise LLM gateway: an
// orchestrator gives each sandboxed agent a key of its own that stops
// working by itself). A key may expire (Key.Expires), and an issued one
// may be issued under another key (Key.Issuer), which bounds it: it is
// refused once its issuer is removed, turned off or expired, it uses only
// what its issuer may use (foldIssuer, at every authentication, so a
// change to the issuer reaches its keys at once), and what it spends
// counts against its issuer's limits too (budget, Identity.Issuer).

// MinTTL and MaxTTL bound how long an issued key lives.
const (
	MinTTL = time.Minute
	MaxTTL = 30 * 24 * time.Hour
)

// expiredKept is how long an expired key stays in the store after it
// expires, so a client still sending it is told it expired rather than
// that magpie doesn't know it; Prune removes it after that.
const expiredKept = time.Hour

// Refused is why Check turned away a key it knows: expired, or bounded by
// an issuer that no longer lets it in. The gateway says it with a 401 on
// every route, from this computer too.
type Refused struct{ Why string }

func (r *Refused) Error() string { return r.Why }

// errUnknown: the secret is no enabled key, or the store can't be read.
var errUnknown = errors.New("not an enabled gateway key")

// expired: k has an expiry and it has come.
func (k Key) expired(now time.Time) bool { return !k.Expires.IsZero() && !now.Before(k.Expires) }

// Issue is what IssueKey makes.
type Issue struct {
	Name string
	TTL  time.Duration
	// Models and Accounts hold the key as Key's do; with an issuer, each
	// must be one the issuer may use, and none takes the issuer's.
	Models   []string
	Accounts []string
	// Tokens and Cost cap what the key may spend over its whole life.
	Tokens   int64
	Cost     float64
	ReadOnly bool
	// Issuer is the id of the key it is issued under; "" for one the
	// user issues themselves (the command line, or loopback with no key).
	Issuer string
}

// Issued is a key IssueKey made, its secret told once.
type Issued struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Key      string    `json:"key"`
	Expires  time.Time `json:"expires"`
	Issuer   string    `json:"issuer,omitempty"`
	Models   []string  `json:"models,omitempty"`
	Accounts []string  `json:"accounts,omitempty"`
	Limit    *Limit    `json:"limit,omitempty"`
	ReadOnly bool      `json:"readOnly,omitempty"`
}

// IssueKey makes a short-lived key. It never outlives its issuer, nor
// is let use a model or account its issuer may not.
func IssueKey(in Issue) (Issued, error) {
	mu.Lock()
	defer mu.Unlock()
	keys, err := load()
	if err != nil {
		return Issued{}, err
	}
	now := Now().Round(0)
	keys, _ = prune(keys, now)
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = "Issued " + now.Format("2006-01-02 15:04")
	}
	if utf8.RuneCountInString(name) > 120 {
		return Issued{}, errors.New("Use a name between 1 and 120 characters")
	}
	if in.TTL < MinTTL || in.TTL > MaxTTL {
		return Issued{}, fmt.Errorf("An issued key lives from %s to %d days: give a ttl such as 1h", MinTTL, int(MaxTTL.Hours()/24))
	}
	expires := now.Add(in.TTL)
	models, err := CleanModels(in.Models)
	if err != nil {
		return Issued{}, err
	}
	var accounts []string
	if len(in.Accounts) > 0 {
		if accounts, err = CleanAccounts(in.Accounts); err != nil {
			return Issued{}, err
		}
	}
	readOnly := in.ReadOnly
	if in.Issuer != "" {
		i := slices.IndexFunc(keys, func(k Key) bool { return k.ID == in.Issuer })
		if i < 0 || keys[i].Off || keys[i].Secret == "" || keys[i].expired(now) {
			return Issued{}, errors.New("The key issuing is no longer an enabled gateway key")
		}
		p := keys[i]
		if !p.Issues || p.Issuer != "" {
			return Issued{}, errors.New("This gateway key can't issue keys: turn it on with magpie gateway-key issuer <id> on")
		}
		if !p.Expires.IsZero() && expires.After(p.Expires) {
			expires = p.Expires // never outlives its issuer
		}
		held := HeldModels(p.Models, p.Accounts)
		for _, m := range models {
			if !covered(held, m) {
				return Issued{}, fmt.Errorf("The key issuing may not use %s, so the key it issues can't either", m)
			}
		}
		for _, a := range accounts {
			if !(Identity{Accounts: p.Accounts}).allowsEntry(a) {
				return Issued{}, fmt.Errorf("The key issuing may not use the account %s, so the key it issues can't either", a)
			}
		}
		readOnly = readOnly || p.ReadOnly
	}
	if in.Tokens < 0 || in.Cost < 0 || math.IsNaN(in.Cost) || math.IsInf(in.Cost, 0) {
		return Issued{}, errors.New("A limit can't be negative")
	}
	var lim *Limit
	if in.Tokens > 0 || in.Cost > 0 {
		// one cycle as long as the key's life, from now: a cap on all it spends
		hours := int(math.Ceil(expires.Sub(now).Hours()))
		if lim, err = (&Limit{Period: "hours", Hours: max(hours, 1), Tokens: in.Tokens, Cost: in.Cost}).Valid(); err != nil {
			return Issued{}, err
		}
		lim.Since = now
	}
	id, err := random(12)
	if err != nil {
		return Issued{}, err
	}
	token, err := random(24)
	if err != nil {
		return Issued{}, err
	}
	k := Key{ID: id, Name: name, Secret: Prefix + token, Limit: lim, Models: models, Accounts: accounts,
		ReadOnly: readOnly, Expires: expires, Issuer: in.Issuer}
	keys = append(keys, k)
	if err := save(keys); err != nil {
		return Issued{}, err
	}
	return Issued{ID: k.ID, Name: k.Name, Key: k.Secret, Expires: k.Expires, Issuer: k.Issuer, Models: models, Accounts: accounts, Limit: lim, ReadOnly: readOnly}, nil
}

// RevokeIssued removes the issued key id. by is the key asking: it may
// remove only a key issued under it; "" is the user, who may remove any
// issued key (a key of their own they remove with remove-key).
func RevokeIssued(id, by string) error {
	mu.Lock()
	defer mu.Unlock()
	keys, err := load()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(keys, func(k Key) bool { return k.ID == id })
	if i < 0 || keys[i].Expires.IsZero() || (by != "" && keys[i].Issuer != by) {
		return errors.New("Key not found")
	}
	return save(slices.Delete(keys, i, i+1))
}

// IssuedBy lists the keys issued under the key by ("" for those the user
// issued themselves), without their secrets.
func IssuedBy(by string) ([]Key, error) {
	keys, err := List()
	if err != nil {
		return nil, err
	}
	var out []Key
	for _, k := range keys {
		if !k.Expires.IsZero() && k.Issuer == by {
			out = append(out, k)
		}
	}
	return out, nil
}

// Prune removes the keys that expired over expiredKept ago and those
// issued under a key no longer in the store, and says how many.
func Prune() (int, error) {
	mu.Lock()
	defer mu.Unlock()
	keys, err := load()
	if err != nil {
		return 0, err
	}
	kept, n := prune(keys, Now())
	if n == 0 {
		return 0, nil
	}
	return n, save(kept)
}

func prune(keys []Key, now time.Time) ([]Key, int) {
	ids := map[string]bool{}
	for _, k := range keys {
		ids[k.ID] = true
	}
	kept := slices.DeleteFunc(slices.Clone(keys), func(k Key) bool {
		return k.expired(now.Add(-expiredKept)) || (k.Issuer != "" && !ids[k.Issuer])
	})
	return kept, len(keys) - len(kept)
}

// Check is Authenticate with why a key it knows is turned away: a
// *Refused for one expired or whose issuer no longer lets it in.
func Check(secret string) (Identity, error) {
	mu.Lock()
	defer mu.Unlock()
	keys, err := load()
	if err != nil || secret == "" {
		return Identity{}, errUnknown
	}
	i := slices.IndexFunc(keys, func(k Key) bool { return subtle.ConstantTimeCompare([]byte(secret), []byte(k.Secret)) == 1 })
	if i < 0 {
		s := settings.Load()
		// A read-only config directory may prevent the first store write. Never
		// fall back after either durable marker exists, or to a revoked mirror.
		if s.LAN && s.LANKeyID == "" && s.LANKey != "" && !strings.HasPrefix(s.LANKey, revokedLANPrefix) && !slices.ContainsFunc(keys, func(k Key) bool { return k.LAN }) && subtle.ConstantTimeCompare([]byte(secret), []byte(s.LANKey)) == 1 {
			k := legacyLANKey(s.LANKey)
			// sum as Check gives the same key once migrated, so the
			// caller's identity is one before and after
			return Identity{KeyID: k.ID, KeyName: k.Name, Default: true, sum: sha256.Sum256([]byte(k.Secret))}, nil
		}
		return Identity{}, errUnknown
	}
	k := keys[i]
	if k.Off {
		return Identity{}, errUnknown
	}
	now := Now()
	if k.expired(now) {
		return Identity{}, &Refused{Why: fmt.Sprintf("the gateway key %q expired at %s: ask for a new one", k.Name, k.Expires.UTC().Format(time.RFC3339))}
	}
	who, err := identityOf(k, keys, now)
	who.sum = sha256.Sum256([]byte(k.Secret))
	return who, err
}

// Current: who, a key Check took earlier, is still one it takes now, by
// the same secret: not turned off, removed, expired, rotated, nor
// bounded by an issuer that no longer lets it in. A connection a key
// opened (Magpie Browser's WebSocket) is closed once it isn't. false
// for no key, and when the store can't be read.
func Current(who Identity) bool {
	if who.KeyID == "" {
		return false
	}
	mu.Lock()
	defer mu.Unlock()
	keys, err := load()
	if err != nil {
		return false
	}
	i := slices.IndexFunc(keys, func(k Key) bool { return k.ID == who.KeyID })
	if i < 0 {
		return false // the legacy sharing key not yet in the store is never a browser's
	}
	k := keys[i]
	if k.Off || k.Secret == "" || k.expired(Now()) || sha256.Sum256([]byte(k.Secret)) != who.sum {
		return false
	}
	_, err = identityOf(k, keys, Now())
	return err == nil
}

// ErrNoKey: there is no gateway key with that id.
var ErrNoKey = errors.New("Key not found")

// ScopeBrowser holds the key id to connecting the Magpie Browser
// extension alone (Key.Browser), read-only for a magpie from before
// that, and unable to issue keys. It is for the key a magpie from before
// the scope made for the extension (gui's browserKey), which could use
// every model and was written into a zip in Downloads: the extension
// goes on connecting with it, and the copy in the zip loses the rest.
// The sharing key, which magpie gives its own agents, is never scoped.
func ScopeBrowser(id string) error {
	mu.Lock()
	defer mu.Unlock()
	keys, err := load()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(keys, func(k Key) bool { return k.ID == id })
	if i < 0 {
		return ErrNoKey
	}
	k := &keys[i]
	if k.LAN || k.ID == settings.Load().LANKeyID {
		return errors.New("The sharing key is given to magpie's own agents, so it can't be held to Magpie Browser")
	}
	if k.Browser && k.ReadOnly && !k.Issues {
		return nil
	}
	k.Browser, k.ReadOnly, k.Issues = true, true, false
	return save(keys)
}

// identityOf is who k is, folded into its issuer when it has one.
func identityOf(k Key, keys []Key, now time.Time) (Identity, error) {
	who := Identity{KeyID: k.ID, KeyName: k.Name, Limit: k.Limit, More: k.More, Models: HeldModels(k.Models, k.Accounts), AccountModels: len(k.Models) == 0 && len(k.Accounts) > 0, Accounts: k.Accounts, MCP: k.MCP, Quotas: k.Quotas, ReadOnly: k.ReadOnly, Browser: k.Browser, Expires: k.Expires, Issues: k.Issues && k.Issuer == "" && !k.Browser, MaxConcurrency: k.MaxConcurrency}
	// the default key as Default tells it: marked LAN, or the id the
	// settings name (settings read only for an unmarked key)
	who.Default = k.LAN || (k.ID != "" && settings.Load().LANKeyID == k.ID)
	if k.Issuer == "" {
		return who, nil
	}
	j := slices.IndexFunc(keys, func(p Key) bool { return p.ID == k.Issuer })
	if j < 0 || keys[j].Off || keys[j].Secret == "" || keys[j].expired(now) {
		return Identity{}, &Refused{Why: fmt.Sprintf("the gateway key %q was issued under a key that is no longer enabled, so it no longer works", k.Name)}
	}
	p := keys[j]
	pModels := HeldModels(p.Models, p.Accounts)
	who.AccountModels = len(k.Models) == 0 && len(p.Models) == 0 && (len(k.Accounts) > 0 || len(p.Accounts) > 0)
	if len(pModels) > 0 {
		if who.Models = foldModels(who.Models, pModels); len(who.Models) == 0 {
			return Identity{}, &Refused{Why: fmt.Sprintf("the gateway key %q may use none of its models now: the key it was issued under no longer lets it", k.Name)}
		}
	}
	who.Accounts = foldAccounts(who.Accounts, p.Accounts)
	who.ReadOnly = who.ReadOnly || p.ReadOnly
	who.Browser = who.Browser || p.Browser
	who.Quotas = who.Quotas && p.Quotas
	if !p.Expires.IsZero() && (who.Expires.IsZero() || p.Expires.Before(who.Expires)) {
		who.Expires = p.Expires
	}
	who.Issuer = &Identity{KeyID: p.ID, KeyName: p.Name, Limit: p.Limit, More: p.More, MCP: p.MCP, MaxConcurrency: p.MaxConcurrency}
	return who, nil
}

// covered: every model the pattern m names is one of patterns' (none:
// every model). A '*' in m is taken as written: a pattern that matches
// it so matches whatever it stands for, as a pattern's '*' takes any run.
func covered(patterns []string, m string) bool {
	return ModelAllowed(patterns, m)
}

// foldModels is what both lists allow, as patterns: a child's pattern the
// issuer's cover, and an issuer's pattern a child's cover. Empty when they
// share none.
func foldModels(child, issuer []string) []string {
	if len(child) == 0 {
		return issuer
	}
	var out []string
	add := func(m string) {
		if !slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, m) }) {
			out = append(out, m)
		}
	}
	for _, c := range child {
		if covered(issuer, c) {
			add(c)
		}
	}
	for _, p := range issuer {
		if covered(child, p) {
			add(p)
		}
	}
	return out
}

// foldAccounts holds a child to the issuer's accounts too: of each
// provider the issuer names accounts of, the child's own of them that the
// issuer has, else the issuer's; the child's of other providers as given.
func foldAccounts(child, issuer []string) []string {
	if len(issuer) == 0 {
		return child
	}
	byProvider := func(as []string) map[string][]string {
		m := map[string][]string{}
		for _, a := range as {
			if p, _, ok := strings.Cut(strings.ToLower(strings.TrimSpace(a)), "/"); ok {
				m[p] = append(m[p], a)
			}
		}
		return m
	}
	cs, ps := byProvider(child), byProvider(issuer)
	var out []string
	for _, a := range child {
		p, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(a)), "/")
		if _, named := ps[p]; !named || slices.ContainsFunc(ps[p], func(x string) bool { return strings.EqualFold(x, a) }) {
			out = append(out, a)
		}
	}
	for _, a := range issuer {
		p, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(a)), "/")
		mine := slices.ContainsFunc(cs[p], func(x string) bool {
			return slices.ContainsFunc(ps[p], func(y string) bool { return strings.EqualFold(x, y) })
		})
		if !mine {
			out = append(out, a)
		}
	}
	return out
}

// allowsEntry: the account entry "<provider>/<ref>" is one who may use.
func (who Identity) allowsEntry(a string) bool {
	p, ref, _ := strings.Cut(a, "/")
	return who.AllowsAccount(p, ref)
}

// IssuerID is the id of the key who was issued under, "" for none.
func (who Identity) IssuerID() string {
	if who.Issuer == nil {
		return ""
	}
	return who.Issuer.KeyID
}

// Limited: a request of who's is held to a limit, its own key's or the
// key it was issued under's.
func (who Identity) Limited() bool {
	return who.KeyID != "" && (len(who.Limits()) > 0 || (who.Issuer != nil && len(who.Issuer.Limits()) > 0))
}
