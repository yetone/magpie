package provider

// A provider can hold several accounts, as a subscription can: several API
// keys — a personal one and the team's, a paid plan and a free one. Any
// number of them can be on at once: requests go to the first, and when it
// is out of quota or rate limited the gateway moves on to the next one
// that's on (see gateway/fallback.go). Key stays the first, so everything
// else that talks to the vendor reads it as before.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// KeyAccount is a saved key after the first. Off keeps it without using it.
type KeyAccount struct {
	Name string `json:"name,omitempty"`
	Key  string `json:"key"`
	Off  bool   `json:"off,omitempty"`
	// Protocol, when set, is the only one the key works with: some relays
	// give out one key for Anthropic and another for OpenAI. Empty is any.
	Protocol Protocol `json:"protocol,omitempty"`
	// Weight is the key's share of the requests when the provider's
	// routing is Weighted (#841): one with 3 takes three for every one a
	// key with 1 takes. None, or 0, counts as 1.
	Weight int `json:"weight,omitempty"`
	// Plan, on a preset whose plans are sold on keys of their own
	// (PresetDef.KeyPlans), is the id of the Region the key is for: its
	// requests go to that plan's endpoints, and it is asked only for that
	// plan's models. Empty is the provider's own plan, as before.
	Plan string `json:"plan,omitempty"`
}

// KeyInfo describes one of a provider's keys without giving it away.
type KeyInfo struct {
	ID     string `json:"id"` // a fingerprint, to name it in a switch
	Name   string `json:"name,omitempty"`
	Masked string `json:"masked"`
	Active bool   `json:"active"` // the first, where requests go
	On     bool   `json:"on"`     // in use: the first, or next in line

	Protocol Protocol `json:"protocol,omitempty"` // the only one it works with
	Weight   int      `json:"weight,omitempty"`   // its share when routing is Weighted
	Plan     string   `json:"plan,omitempty"`     // its plan's region id, "" for the provider's

	// Rest is why the gateway passes it over now, after a failure, and
	// until when; nil while it takes requests.
	Rest *KeyRest `json:"rest,omitempty"`
	// Spend is the spend cap set on it and what it has spent in its
	// period; nil with none.
	Spend *KeySpend `json:"spend,omitempty"`
	// UsageCap: its own usage windows are read, so it can be capped at a
	// share of them; CapHeld is how the cap holds it now (key_allowance.go,
	// account_caps.go), nil while it takes requests
	UsageCap bool         `json:"usageCap,omitempty"`
	CapHeld  *KeyCapState `json:"capHeld,omitempty"`
	// Balance is the balance the user typed for it and what is left of
	// it by magpie's count (key_balance_own.go); nil with none.
	Balance *BalanceEstimate `json:"balance,omitempty"`
}

// KeySpend is a key's spend cap as its row tells it: the cap, what magpie
// counted spent in its period, when that renews (none for a cap in all),
// and whether routing holds the key at it now.
type KeySpend struct {
	SpendCap
	Spent float64    `json:"spent"`
	Reset *time.Time `json:"reset,omitempty"`
	Held  bool       `json:"held,omitempty"`
}

// KeyCapState is a key held at its usage cap: the share used of the fullest
// window at its cap, that cap and when the last such window renews (zero
// when one doesn't say).
type KeyCapState struct {
	Used float64   `json:"used"`
	Cap  int       `json:"cap"`
	Back time.Time `json:"back,omitzero"`
}

// KeyRest is a key's rest as the gateway keeps it: failQuota, failRate…,
// the status it answered, until when, and the key to lift it by.
type KeyRest struct {
	Why    string    `json:"why"`
	Status int       `json:"status"`
	Until  time.Time `json:"until"`
	Key    string    `json:"key"`
	// Probed: the gateway asks it again in the background, and lifts the
	// rest as soon as it answers (gateway/rest_probe.go)
	Probed bool `json:"probed,omitempty"`
	// By: what set how long (gateway.Rest.By): "resets" or "retry-after"
	// when the vendor named the time
	By string `json:"by,omitempty"`
	// Model: the one model resting, the account still answering its
	// others; "" for the whole account or key
	Model string `json:"model,omitempty"`
	// Error: what the vendor said
	Error string `json:"error,omitempty"`
	// Rate: the vendor called it a rate limit and named when it lifts
	// (gateway.Rest.Rate): the page says rate limited, not quota used up
	Rate bool `json:"rate,omitempty"`
}

// RestKey is what the gateway rests the key k of a provider by: the
// provider's id while it has one key on, else the id and the key's.
func (p Provider) RestKey(k KeyInfo) string { return p.RestKeys()(k) }

// RestKeys is RestKey for each of a provider's keys in turn, whether it
// has more than one on found once: found again for every key, a list of
// a thousand keys was read a thousand times over, and the Providers page
// took seconds to draw it, minutes for five thousand (@NetRozee on X).
func (p Provider) RestKeys() func(KeyInfo) string {
	several := p.severalOn()
	return func(k KeyInfo) string {
		if several {
			return p.ID + "#" + k.ID
		}
		return p.ID
	}
}

// severalOn is len(p.KeysOn()) > 1 without listing them: a key on after
// the first that isn't the first again.
func (p Provider) severalOn() bool {
	if p.Key == "" {
		return false
	}
	for _, k := range p.Keys {
		if !k.Off && k.Key != "" && k.Key != p.Key {
			return true
		}
	}
	return false
}

func keyID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:5])
}

// KeyID is the fingerprint a key is named by, in lists and in the gateway.
func KeyID(key string) string { return keyID(key) }

// KeyList is a provider's keys, in the order requests try them.
func (p Provider) KeyList() []KeyInfo {
	var out []KeyInfo
	if p.Key != "" {
		out = append(out, KeyInfo{ID: keyID(p.Key), Name: p.KeyName, Masked: Mask(p.Key), Active: true, On: true, Protocol: p.KeyProtocol, Weight: p.KeyWeight, Plan: p.KeyPlan})
	}
	for _, k := range p.Keys {
		out = append(out, KeyInfo{ID: keyID(k.Key), Name: k.Name, Masked: Mask(k.Key), On: !k.Off, Protocol: k.Protocol, Weight: k.Weight, Plan: k.Plan})
	}
	return out
}

// KeysOn is the keys in use, first to last: the first key, then every
// saved one that's on. A key listed twice — providers.json's "keys"
// naming the first key again, as #1772's did — is one key, asked once.
func (p Provider) KeysOn() []KeyAccount {
	keysListed.Add(1)
	if p.Key == "" {
		return nil
	}
	out := []KeyAccount{p.first()}
	// a set, not a look along the list for each key: the gateway asks
	// this for every request, and a thousand keys made it half a million
	// looks (@NetRozee on X)
	seen := make(map[string]bool, len(p.Keys)+1)
	seen[p.Key] = true
	for _, k := range p.Keys {
		if !k.Off && k.Key != "" && !seen[k.Key] {
			seen[k.Key] = true
			out = append(out, k)
		}
	}
	return out
}

// keysListed counts KeysOn's lists (KeysListed).
var keysListed atomic.Int64

// KeysListed is how many times a provider's keys on have been listed in
// this process: a test counts them, not the time a page or request took,
// to say they are listed once and not once a key (@NetRozee on X).
func KeysListed() int64 { return keysListed.Load() }

// AddKey saves one more key for a provider, on: it takes requests after
// the ones before it. It becomes the first when the provider has none.
func AddKey(id, name, key string, proto Protocol) error {
	name, key = strings.TrimSpace(name), strings.TrimSpace(key)
	if err := keyProtocolOK(proto); err != nil {
		return err
	}
	p, err := Find(id)
	if err != nil {
		return err
	}
	if p.Account != nil {
		return errors.New("a signed-in account has no keys")
	}
	// nothing pasted: the key the provider is asked with when the user has
	// none (OpenCode Zen's public, #1795); for any other, nothing to add
	anon := false
	if key == "" {
		if key = p.AnonymousKey(); key == "" {
			return errors.New("paste the key to add")
		}
		anon = true
	}
	if p.HasKey(key) {
		if anon {
			return fmt.Errorf("%s already has %s, the key its free models answer with none of yours", p.Name, key)
		}
		return fmt.Errorf("%s already has this key", p.Name)
	}
	if p.Key == "" {
		p.Key, p.KeyName, p.KeyProtocol = key, name, proto
	} else {
		p.Keys = append(p.Keys, KeyAccount{Name: name, Key: key, Protocol: proto})
	}
	return Save(*p)
}

// AnonymousKey is the key the provider is asked with when the user has
// none of their own, "" when it has no such key: OpenCode Zen's public,
// which its free models answer (normalize gives it to a Zen provider
// saved with no key).
func (p Provider) AnonymousKey() string {
	if p.Preset == "opencode-zen" {
		return OpenCodeAnonymousKey
	}
	return ""
}

// HasKey reports whether key is one of the provider's, on or off.
func (p Provider) HasKey(key string) bool {
	id := keyID(key)
	for _, k := range p.KeyList() {
		if k.ID == id {
			return true
		}
	}
	return false
}

// SplitKeys is the keys in text pasted at once: one a line, or separated
// by commas, semicolons or spaces, trimmed of quotes, each once.
func SplitKeys(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range strings.FieldsFunc(text, func(r rune) bool {
		return r == ',' || r == ';' || r == '，' || r == '；' || r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\u3000'
	}) {
		f = strings.Trim(f, "\"'`")
		if f != "" && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

// AddKeys saves several keys at once, on, after the ones the provider has,
// with the protocol given: those it has already are passed over. added is
// how many were new, had how many it had.
func AddKeys(id string, keys []string, proto Protocol) (added, had int, err error) {
	if err := keyProtocolOK(proto); err != nil {
		return 0, 0, err
	}
	p, err := Find(id)
	if err != nil {
		return 0, 0, err
	}
	if p.Account != nil {
		return 0, 0, errors.New("a signed-in account has no keys")
	}
	seen := map[string]bool{}
	for _, k := range p.KeyList() {
		seen[k.ID] = true
	}
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if seen[keyID(key)] {
			had++
			continue
		}
		seen[keyID(key)] = true
		added++
		if p.Key == "" {
			p.Key, p.KeyName, p.KeyProtocol = key, "", proto
		} else {
			p.Keys = append(p.Keys, KeyAccount{Key: key, Protocol: proto})
		}
	}
	if added == 0 {
		if had > 0 {
			return 0, had, fmt.Errorf("%s already has these keys", p.Name)
		}
		return 0, 0, errors.New("paste the keys to add")
	}
	return added, had, Save(*p)
}

// keyReadsAtOnce is how many of one provider's keys have their balance,
// plan or limits read from the vendor at once. Every key at once was a
// thousand requests to one relay together for a provider of a thousand
// keys (@NetRozee on X); other providers' keys are read beside them.
const keyReadsAtOnce = 8

// keySlots is one read's slots, keyReadsAtOnce for each provider.
type keySlots struct {
	sync.Mutex
	m map[string]chan struct{}
}

// take waits for a slot of the provider id's, and gives what frees it. A
// read given up on (ctx done) goes on without one, to fail at once.
func (s *keySlots) take(ctx context.Context, id string) (release func()) {
	s.Lock()
	if s.m == nil {
		s.m = map[string]chan struct{}{}
	}
	c := s.m[id]
	if c == nil {
		c = make(chan struct{}, keyReadsAtOnce)
		s.m[id] = c
	}
	s.Unlock()
	select {
	case c <- struct{}{}:
		return func() { <-c }
	case <-ctx.Done():
		return func() {}
	}
}

// first is the first key as a KeyAccount, and setFirst makes k the first.
func (p *Provider) first() KeyAccount {
	return KeyAccount{Name: p.KeyName, Key: p.Key, Protocol: p.KeyProtocol, Weight: p.KeyWeight, Plan: p.KeyPlan}
}

func (p *Provider) setFirst(k KeyAccount) {
	p.Key, p.KeyName, p.KeyProtocol, p.KeyWeight, p.KeyPlan = k.Key, k.Name, k.Protocol, k.Weight, k.Plan
}

func keyProtocolOK(proto Protocol) error {
	switch proto {
	case "", Chat, Responses, Anthropic, Gemini:
		return nil
	}
	return fmt.Errorf("unknown protocol %q", proto)
}

// SetKeyProtocol says which one protocol a key works with; empty is any.
func SetKeyProtocol(id, keyRef string, proto Protocol) error {
	if err := keyProtocolOK(proto); err != nil {
		return err
	}
	p, err := Find(id)
	if err != nil {
		return err
	}
	i, ok := findKey(p, keyRef)
	if !ok {
		return fmt.Errorf("%s has no such key", p.Name)
	}
	if i < 0 {
		p.KeyProtocol = proto
	} else {
		p.Keys[i].Protocol = proto
	}
	return Save(*p)
}

// SetKeyPlan says which of the preset's plans (a Region's id) a key is
// for; empty is the provider's own. Its requests then go to that plan's
// endpoints, with that plan's models (WithKey).
func SetKeyPlan(id, keyRef, plan string) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	plan = strings.TrimSpace(plan)
	if plan != "" {
		pr := Preset(p.Preset)
		if pr == nil || !pr.KeyPlans {
			return fmt.Errorf("%s's keys are all on one plan", p.Name)
		}
		if !slices.ContainsFunc(pr.Regions, func(r Region) bool { return r.ID == plan }) {
			return fmt.Errorf("%s has no plan %q", pr.Name, plan)
		}
		// the provider's own plan is kept as none: the key follows the
		// provider when its plan is changed in the editor
		if r := p.regionOf(pr); r != nil && r.ID == plan {
			plan = ""
		}
	}
	i, ok := findKey(p, keyRef)
	if !ok {
		return fmt.Errorf("%s has no such key", p.Name)
	}
	if i < 0 {
		p.KeyPlan = plan
	} else {
		p.Keys[i].Plan = plan
	}
	return Save(*p)
}

// MaxKeyWeight is the most a key's weight can be.
const MaxKeyWeight = 1000

// SetKeyWeight sets a key's share of the requests under Weighted routing;
// 0 takes it back to the default, 1.
func SetKeyWeight(id, keyRef string, weight int) error {
	if weight < 0 || weight > MaxKeyWeight {
		return fmt.Errorf("a key's weight is 0 to %d", MaxKeyWeight)
	}
	p, err := Find(id)
	if err != nil {
		return err
	}
	i, ok := findKey(p, keyRef)
	if !ok {
		return fmt.Errorf("%s has no such key", p.Name)
	}
	if weight == 1 {
		weight = 0 // the default: kept out of the file
	}
	if i < 0 {
		p.KeyWeight = weight
	} else {
		p.Keys[i].Weight = weight
	}
	return Save(*p)
}

// WeightOf is the key's weight as Weighted routing counts it: 1 when none.
func (k KeyAccount) WeightOf() int { return max(k.Weight, 1) }

// WithKey is p using key k: at k's plan's endpoints when k is for a plan
// of its own (KeyAccount.Plan), and its endpoints narrowed to k's
// protocol when k has one. It has none left when p doesn't serve that
// protocol.
func (p Provider) WithKey(k KeyAccount) Provider {
	p.Key, p.KeyName, p.KeyProtocol, p.KeyWeight, p.KeyPlan = k.Key, k.Name, k.Protocol, k.Weight, k.Plan
	p = p.atKeyPlan(k.Plan)
	p.keyView = true
	if k.Protocol != "" {
		for _, pr := range Protocols {
			if pr != k.Protocol {
				switch pr {
				case Chat:
					p.Chat = ""
				case Responses:
					p.Responses = ""
				case Anthropic:
					p.Anthropic = ""
				}
			}
		}
		// a custom provider's Gemini API, kept for a key made for it only
		// (a relay's key per protocol, corddis_114 on Discord)
		if k.Protocol != Gemini {
			p.Gemini = ""
		}
	}
	return p
}

// findKey is where a key is among p.Keys: -1 for the first key, and ok
// false when p has no such key.
func findKey(p *Provider, ref string) (int, bool) {
	if p.Key != "" && keyID(p.Key) == ref {
		return -1, true
	}
	for i, k := range p.Keys {
		if keyID(k.Key) == ref {
			return i, true
		}
	}
	return 0, false
}

// KeyOf is one of a provider's keys, by its id (keyID), for the editor
// to show or copy: a provider of several keys has a row for each, and
// each row copies its own (#1480).
func KeyOf(id, keyRef string) (string, error) {
	p, err := Find(id)
	if err != nil {
		return "", err
	}
	i, ok := findKey(p, keyRef)
	if !ok {
		return "", fmt.Errorf("%s has no such key", p.ID)
	}
	if i < 0 {
		return p.Key, nil
	}
	return p.Keys[i].Key, nil
}

// UseKey makes one of a provider's keys the first, and turns it on; the
// one it replaces stays on, next in line.
func UseKey(id, keyRef string) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	i, ok := findKey(p, keyRef)
	if !ok {
		return fmt.Errorf("%s has no such key", p.Name)
	}
	if i < 0 {
		return nil
	}
	k := p.Keys[i]
	rest := append([]KeyAccount{p.first()}, append(p.Keys[:i:i], p.Keys[i+1:]...)...)
	p.setFirst(k)
	p.Keys = rest
	return Save(*p)
}

// promote takes the first key out of use: the next key that's on takes
// its place. ok is false when none is.
func promote(p *Provider) (old KeyAccount, ok bool) {
	for i, k := range p.Keys {
		if !k.Off {
			old = p.first()
			p.setFirst(k)
			p.Keys = append(p.Keys[:i:i], p.Keys[i+1:]...)
			return old, true
		}
	}
	return KeyAccount{}, false
}

// SetKeyOn turns a key on or off. The last key that's on stays on.
func SetKeyOn(id, keyRef string, on bool) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	i, ok := findKey(p, keyRef)
	if !ok {
		return fmt.Errorf("%s has no such key", p.Name)
	}
	switch {
	case i >= 0:
		p.Keys[i].Off = !on
	case !on:
		old, ok := promote(p)
		if !ok {
			return errors.New("that's the only key in use; turn another on first")
		}
		old.Off = true
		p.Keys = append([]KeyAccount{old}, p.Keys...)
	default:
		return nil
	}
	return Save(*p)
}

// RemoveKey forgets a key. The last key in use stays: turn another on first.
func RemoveKey(id, keyRef string) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	i, ok := findKey(p, keyRef)
	if !ok {
		return fmt.Errorf("%s has no such key", p.Name)
	}
	if i >= 0 {
		p.Keys = append(p.Keys[:i], p.Keys[i+1:]...)
	} else if _, ok := promote(p); !ok {
		return errors.New("that's the only key in use; turn another on before removing it")
	}
	return Save(*p)
}

// RemoveKeys forgets several keys at once (361 on Discord: hundreds of
// keys, some dead): those named that it has. The first key, when it is
// among them, gives its place to the next one on that stays. One key in
// use always stays: removing every key in use is refused.
func RemoveKeys(id string, refs []string) (removed int, err error) {
	p, err := Find(id)
	if err != nil {
		return 0, err
	}
	gone := map[string]bool{}
	for _, r := range refs {
		gone[r] = true
	}
	var kept []KeyAccount
	for _, k := range p.Keys {
		if gone[keyID(k.Key)] {
			removed++
			continue
		}
		kept = append(kept, k)
	}
	firstGone := p.Key != "" && gone[keyID(p.Key)]
	if firstGone {
		removed++
	}
	if removed == 0 {
		return 0, fmt.Errorf("%s has none of these keys", p.Name)
	}
	p.Keys = kept
	// the first key is always on: only its removal can leave none in use
	if firstGone {
		if _, ok := promote(p); !ok {
			return 0, errors.New("those are all the keys in use; keep one on")
		}
	}
	return removed, Save(*p)
}

// RenameKey names one of a provider's keys, "Personal", "Team".
func RenameKey(id, keyRef, name string) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	i, ok := findKey(p, keyRef)
	if !ok {
		return fmt.Errorf("%s has no such key", p.Name)
	}
	name = strings.TrimSpace(name)
	if i < 0 {
		p.KeyName = name
	} else {
		p.Keys[i].Name = name
	}
	return Save(*p)
}

// ReplaceKey puts a new value in place of one of a provider's keys (#1869:
// a key rotated at the vendor or the relay). The key keeps its name, its
// place, its protocol and weight, whether it is on, and what was set for
// it by its fingerprint: the models it serves, its proxy, its concurrency.
// It returns the key's fingerprint before and after; the same value pasted
// again changes nothing and returns the same one twice.
func ReplaceKey(id, keyRef, key string) (was, now string, err error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", "", errors.New("paste the key's new value")
	}
	p, err := Find(id)
	if err != nil {
		return "", "", err
	}
	i, ok := findKey(p, keyRef)
	if !ok {
		return "", "", fmt.Errorf("%s has no such key", p.Name)
	}
	now = keyID(key)
	if now == keyRef {
		return keyRef, now, nil
	}
	if p.HasKey(key) {
		return "", "", fmt.Errorf("%s already has this key", p.Name)
	}
	if i < 0 {
		p.Key = key
	} else {
		p.Keys[i].Key = key
	}
	moveKeyed(p.AccountModels, keyRef, now)
	moveKeyed(p.AccountModelsAll, keyRef, now)
	moveKeyed(p.AccountProxies, keyRef, now)
	moveKeyed(p.AccountConcurrency, keyRef, now)
	moveKeyed(p.KeySpendCaps, keyRef, now)
	moveKeyed(p.AccountCaps, keyRef, now)
	moveKeyed(p.AccountWindowCaps, keyRef, now)
	moveKeyed(p.AccountWindowAmounts, keyRef, now)
	moveKeyed(p.KeyOwnBalances, keyRef, now)
	moveKeyed(p.KeyPriceRates, keyRef, now)
	return keyRef, now, Save(*p)
}

// moveKeyed moves m's entry for the key was, if it has one, to now.
func moveKeyed[V any](m map[string]V, was, now string) {
	if v, ok := m[was]; ok {
		delete(m, was)
		m[now] = v
	}
}
