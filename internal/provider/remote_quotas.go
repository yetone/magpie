package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// A remote magpie's quotas (莫 on Discord): a computer whose providers are
// another's magpie (remote-magpie) has the accounts signed in there, not
// here, so its Usage page, menu bar and magpie quota had nothing to show.
// They now show the other magpie's cards as it last read them, each named
// with that magpie ("Codex · office") and its provider id put after the
// remote's ("office/codex"), so they never meet this computer's own.
//
// Only the magpie holding the sign-ins asks the vendors. It answers
// RemoteCardsPath from what it has kept (CachedCards: its page's cards and
// what was read behind the requests since, cards_kept.go) and asks nobody;
// a card's refresh here asks it to read that card again
// (RemoteRefreshPath), which is the one time a vendor is asked for us.

// RemoteCardsPath is the gateway's answer of its kept cards.
const RemoteCardsPath = "/v1/magpie/quotas/cards"

// RemoteRefreshPath has the gateway read a card (?provider=, &user=)
// again, or every card when no provider is named, then answer as
// RemoteCardsPath.
const RemoteRefreshPath = "/v1/magpie/quotas/refresh"

// RemoteCodexResetPath has the gateway spend one of its Codex account's
// rate-limit resets (?user=, the account always named; &credit=, one of
// its resets.each, else the one that runs out first), as its Usage page's
// "Use a reset" does (#3850, prgrmrwy): a reset shown on another magpie's
// card, or in an app reading the cards, can be used from there too. It
// can't be undone, so the gateway takes it only with a gateway key of full
// access, never from a page or app it lets in without one.
const RemoteCodexResetPath = "/v1/magpie/quotas/codex-reset"

// RemoteResetWait is how long a remote magpie is waited for to spend a
// reset: its use (20s at most) and its wait for routing to know (10s).
const RemoteResetWait = 35 * time.Second

// remoteCardsFor is how long another magpie's cards are kept here before
// they are asked for again: reading its cache costs it nothing from the
// vendors, but the menu bar and alerts ask often.
const remoteCardsFor = 30 * time.Second

// errRemoteNothingRead is a remote magpie that has read no card since it
// started: it is asked to only when a card is refreshed here.
const errRemoteNothingRead = "nothing read on that magpie yet; refresh to have it read"

// errRemoteNoShare is a remote magpie too old to answer RemoteCardsPath.
const errRemoteNoShare = "remote magpie doesn't share its quotas; update magpie on that computer"

// errRemoteNotShared is a remote magpie that answers models but not its
// quotas (403): it isn't shared on its local network, as an older magpie's
// gateway that MAGPIE_ADDR opened (Docker's) wasn't until it was (Fim980 on
// X: the card said only "Allowance unavailable"). A newer one asks a key of
// every other machine and, given one, answers the quotas too.
const errRemoteNotShared = "remote magpie isn't shared; turn on Share on local network on that computer"

// errRemoteHeld is the remote's own card while it holds this provider's
// key to a budget, models or accounts (errKeyHeld): its accounts' cards
// aren't told to the key, and the card says so and where that is turned
// on, rather than leaving the remote's Claude or Codex out with no word
// (yi_time on Discord: a Claude shared from another computer showed no
// usage on any page here, its key held to Claude's models there).
const errRemoteHeld = "remote magpie tells this key none of its quotas, as it holds the key to a budget, models or accounts; on that computer, turn on Quotas for the key under Gateway keys, and let it use every model of a provider whose usage it is to see"

// errRemoteKey is a remote magpie that didn't take this provider's key (401).
const errRemoteKey = "remote magpie didn't take this key; use one of its enabled gateway keys"

// KeyReadOnlyCode is the error code a gateway answers a read-only key
// with on a route that uses or changes anything.
const KeyReadOnlyCode = "gateway_key_read_only"

// errRemoteReadOnly is a remote magpie whose key here is read-only: it
// shows the cards, and spends nothing.
const errRemoteReadOnly = "this key is read-only on the remote magpie; give the provider a key with full access to use a reset from here"

// KeyHeldCode is the error code a gateway answers a key held to a budget,
// models or accounts with, where its own accounts would be told (#1981):
// the key is someone else's to use, and is told its own limit alone.
const KeyHeldCode = "gateway_key_held"

// errKeyHeld is a remote magpie that holds this provider's key to a
// budget, models or accounts: it tells the key's own card (remoteKeyCards)
// and none of its accounts', so none is shown, not even one kept from
// before the key was held; the remote's own card says why (errRemoteHeld).
var errKeyHeld = errors.New("remote magpie holds this key to a budget, models or accounts")

// errRemoteUnreachable starts the error of a remote magpie that couldn't
// be reached at all.
const errRemoteUnreachable = "couldn't reach the remote magpie"

// CachedCards is the Usage page's cards as this magpie last read them,
// each with its kind, for another magpie to show: no vendor is asked,
// whatever their age. The cards a Remote magpie gave this one, as last
// read from it (passedOnCards), are passed on too, so a magpie that
// uses this one sees the accounts behind it as well (yuyong_id on
// Discord: a third magpie in a chain had none of the first's Codex
// cards). Each card carries the way it came by, this magpie last (Via).
//
// The cards are the page's with what was read of the same accounts and
// keys behind it since (cards_kept.go): a magpie serving only other
// magpies has nobody on its page (#1313).
func CachedCards(now time.Time) []SubscriptionQuota {
	c := &subscriptionUsageCache
	c.Lock()
	data := c.data
	c.Unlock()
	subs := withDailyCredits(visibleQuotas(withAccountReadings(data)), now)
	p := &planQuotaCache
	p.Lock()
	plans := slices.Clone(p.data)
	p.Unlock()
	b := &keyBalanceCache
	b.Lock()
	balances := slices.Clone(b.data)
	b.Unlock()
	ps := keyCardProviders()
	if plans == nil || balances == nil {
		keptPlans, keptBalances := keptKeyCards(ps)
		if plans == nil {
			plans = keptPlans
		}
		if balances == nil {
			balances = keptBalances
		}
	}
	plans, balances = withKeyReadings(plans, ps, "plan"), withKeyReadings(balances, ps, "balance")
	plans, subs = seatCards(plans, subs)
	subs = WithImageLane(withAccountNames(subs), now)
	out := []SubscriptionQuota{}
	for _, g := range []struct {
		kind string
		qs   []SubscriptionQuota
	}{{"subscription", subs}, {"plan", plans}, {"balance", balances}} {
		for _, q := range g.qs {
			if q.From == "" {
				q.Kind = g.kind
				q.Via = catalog.ViaHere(nil)
				out = append(out, q)
			}
		}
	}
	return oneCardPerAccount(append(out, passedOnCards()...))
}

// passedOnCards are the cards of each Remote magpie this one uses, as
// last read from it (remoteCards, at most 30 seconds old while anything
// here shows them): no remote is asked, so a chain of magpies, or two
// that are each the other's Remote, never asks along itself. A remote
// read nothing from, or holding this magpie's key (errKeyHeld), passes
// on nothing; the card standing for the remote itself and its keys'
// limits (remoteKeyCards) are this magpie's own business with it.
func passedOnCards() []SubscriptionQuota {
	var out []SubscriptionQuota
	for _, p := range remoteMagpies() {
		c := &remoteCardCache
		c.Lock()
		r := c.m[remoteCardsID(p)]
		c.Unlock()
		if r.held {
			continue
		}
		for _, q := range namedRemoteCards(p, r.cards) {
			q.Via = catalog.ViaHere(q.Via)
			out = append(out, q)
		}
	}
	return out
}

// ReadAllCards has every card read again, the page's Refresh asked by
// another magpie whose card of this one's said nothing was read yet, or
// by an app holding a gateway key (POST RemoteRefreshPath with no card
// named). It is the user's refresh of every allowance, as the Usage
// page's Refresh is (AskEveryClaudeUsage): each saved Claude account's
// /usage is run too, under the same 30-second floor and holds (#5443,
// prgrmrwy: it asked AskUsage alone, and a saved account kept its old
// reading). The accounts are read side by side, each bounded by
// subscriptionTimeout, so many of them answer within the gateway's wait.
func ReadAllCards(ctx context.Context) {
	AskEveryClaudeUsage()
	ForgetBalances()
	forgetPlanQuotas()
	Quotas(Asked(ctx))
}

var remoteCardCache struct {
	sync.Mutex
	m map[string]remoteCardsRead
}

type remoteCardsRead struct {
	at    time.Time
	cards []SubscriptionQuota // as the remote has them, not yet named
	err   string
	held  bool // the remote holds the key: no card of its accounts is shown
}

// remoteCardsID is what a remote's cards are kept here by: the provider
// and its key, so cards read with one key are never shown under another
// (a key replaced by one the remote holds shows none of them).
func remoteCardsID(p Provider) string {
	return p.ID + "\x00" + keyID(p.Key)
}

// ForgetRemoteCardsForTest drops the remote magpies' cards kept here, for
// a test run again (-count) to ask its own fake remote afresh.
func ForgetRemoteCardsForTest() {
	c := &remoteCardCache
	c.Lock()
	c.m = nil
	c.Unlock()
	k := &remoteKeyCache
	k.Lock()
	k.m = nil
	k.Unlock()
}

// ForgetKeptCardsForTest has the cards kept on disk (quotas.json) read
// again from the home a test has set: they are read once per process.
func ForgetKeptCardsForTest() {
	c := &lastQuotas
	c.Lock()
	c.m, c.loaded = nil, false
	c.Unlock()
}

// remoteMagpies are the remote magpies this magpie uses.
func remoteMagpies() []Provider {
	var out []Provider
	for _, p := range All() {
		if p.IsRemoteMagpie() && !p.Hidden && !p.Off && !p.SelfLeftOut() && p.Key != "" && p.Anthropic != "" {
			out = append(out, p)
		}
	}
	return out
}

// RemoteCards is every remote magpie's cards, named for this computer.
func RemoteCards(ctx context.Context) []SubscriptionQuota {
	ps := remoteMagpies()
	got := make([][]SubscriptionQuota, len(ps))
	var wg sync.WaitGroup
	for i, p := range ps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// its keys' own limits (#1547), read beside its cards
			keys := make(chan []SubscriptionQuota, 1)
			go func() { keys <- remoteKeyCards(ctx, p, false, "") }()
			got[i] = append(remoteCards(ctx, p, false), <-keys...)
		}()
	}
	wg.Wait()
	return oneCardPerAccount(slices.Concat(got...))
}

// remoteCards is p's cards, from what was kept here when it is recent and
// fresh isn't asked. A remote that can't be asked keeps showing its last
// cards, as old as they are; with none, one card says why.
func remoteCards(ctx context.Context, p Provider, fresh bool) []SubscriptionQuota {
	id := remoteCardsID(p)
	c := &remoteCardCache
	c.Lock()
	r, ok := c.m[id]
	c.Unlock()
	if !ok || fresh || time.Since(r.at) >= remoteCardsFor {
		asked := time.Now()
		cards, err := askRemote(ctx, p, http.MethodGet, RemoteCardsPath, remoteWait)
		r = keepRemoteCards(id, asked, cards, err)
	}
	name := remoteName(p)
	if len(r.cards) == 0 || r.held {
		// the latest read's failure first: one since the key was held
		// says the remote can't be read now
		msg := r.err
		if msg == "" && r.held {
			msg = errRemoteHeld
		} else if msg == "" {
			msg = errRemoteNothingRead
		}
		return []SubscriptionQuota{{Provider: p.ID, Name: name, Icon: "magpie", Kind: "subscription", From: name, Windows: []QuotaWindow{}, Error: msg}}
	}
	return namedRemoteCards(p, r.cards)
}

// namedRemoteCards is cards, as the remote magpie p told them, named for
// this computer. A card p has from a magpie behind it (its own Remote
// magpie, yuyong_id on Discord: magpie3 → magpie2 → magpie1, and
// magpie3 had magpie1's Codex card from nobody) comes with the way it
// came by (Via) and is named after it ("Codex · magpie1 · magpie2",
// "magpie2/magpie1/codex"); one whose way leaves this magpie and comes
// back to it (two magpies each the other's Remote) is left out, as a
// model listed that way is (catalog.Loops).
func namedRemoteCards(p Provider, cards []SubscriptionQuota) []SubscriptionQuota {
	name := remoteName(p)
	wanted := remoteCardWanted(p)
	out := make([]SubscriptionQuota, 0, len(cards))
	for _, q := range cards {
		if q.Provider == "" || catalog.Loops(q.Via) || !wanted(q.Provider) {
			continue
		}
		q.Provider = p.ID + "/" + q.Provider
		q.Name += " · " + name
		q.From = name
		// what is pressed on a card acts on this computer's accounts:
		// a check-in is the other magpie's to do. A Codex reset is
		// spent there when pressed here (UseRemoteCodexReset, #3850);
		// a GLM team's (byWindow) is spent on the vendor's page
		if q.Provider != p.ID+"/codex" || q.Resets != nil && q.Resets.ByWindow {
			q.Resets = nil
		}
		q.Checkins, q.CheckinBy, q.Checkin, q.CheckinOff = false, "", nil, false
		if q.Windows == nil {
			q.Windows = []QuotaWindow{}
		}
		out = append(out, q)
	}
	return out
}

func remoteName(p Provider) string {
	if p.Name != "" {
		return p.Name
	}
	return p.ID
}

// remoteOf is the remote magpie a card's provider id names, and the id of
// the card there ("" for the card that stands for the remote itself).
func remoteOf(id string) (Provider, string, bool) {
	rid, card, _ := strings.Cut(id, "/")
	for _, p := range remoteMagpies() {
		if p.ID == rid {
			return p, card, true
		}
	}
	return Provider{}, "", false
}

// IsRemoteCard is whether id is a card this magpie has from a remote
// magpie, or the card standing for the remote itself.
func IsRemoteCard(id string) bool {
	_, _, ok := remoteOf(id)
	return ok
}

// refreshRemote has the remote magpie read its card again (every card,
// for the remote's own), then keeps what it now has.
func refreshRemote(ctx context.Context, p Provider, card, user string) {
	// a key's limit card is read again with that key (or every key's)
	if card == remoteKeyCard {
		remoteKeyCards(ctx, p, true, user)
		return
	}
	q := url.Values{}
	if card != "" {
		q.Set("provider", card)
		if user != "" {
			q.Set("user", user)
		}
	}
	path := RemoteRefreshPath
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	asked := time.Now()
	if _, err := askRemote(ctx, p, http.MethodPost, path, remoteRefreshWait); err != nil {
		keepRemoteCards(remoteCardsID(p), asked, nil, err)
	}
	remoteCards(ctx, p, true)
}

// keepRemoteCards keeps what a read of a remote's cards asked at asked
// got, and gives back what is now kept. A failed read keeps the cards
// read before, with its error; a key the remote holds drops them. A read
// that comes back after one asked later is not kept over it, so a slow
// answer from before the key was held can't bring its cards back.
func keepRemoteCards(id string, asked time.Time, cards []SubscriptionQuota, err error) remoteCardsRead {
	c := &remoteCardCache
	c.Lock()
	defer c.Unlock()
	r := c.m[id]
	if asked.Before(r.at) {
		return r
	}
	r.at = asked
	switch {
	case errors.Is(err, errKeyHeld):
		r.cards, r.err, r.held = nil, "", true
	case err != nil:
		r.err = err.Error()
	default:
		// a card that came back round isn't kept, nor told again
		r.cards, r.err, r.held = withoutLoops(cards), "", false
	}
	if c.m == nil {
		c.m = map[string]remoteCardsRead{}
	}
	c.m[id] = r
	return r
}

// RemoteQuotaHistories is every remote magpie's quota history since days
// ago, as its own GET /v1/magpie/quotas/history has it (read from its
// disk, no vendor asked), the providers named as RemoteCards names them.
func RemoteQuotaHistories(ctx context.Context, days string) []QuotaHistory {
	ps := remoteMagpies()
	got := make([][]QuotaHistory, len(ps))
	var wg sync.WaitGroup
	for i, p := range ps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var hs []QuotaHistory
			if err := remoteJSON(ctx, p, http.MethodGet, "/v1/magpie/quotas/history?days="+url.QueryEscape(days), remoteWait, &hs); err != nil {
				return
			}
			for j := range hs {
				hs[j].Provider = p.ID + "/" + hs[j].Provider
			}
			got[i] = hs
		}()
	}
	wg.Wait()
	return slices.Concat(got...)
}

// remoteWait is how long a remote magpie is waited for to answer what it
// keeps; remoteRefreshWait, to read a card or every card again first,
// which asks the vendors there: one saved Claude account's /usage alone
// takes about 5 seconds, and a refresh of the remote's every card runs
// each saved account's (#5443), within its gateway's 30 seconds. At 8
// seconds the refresh was given up on while it still read, and the cards
// asked for after it were the ones from before.
var (
	remoteWait        = 8 * time.Second
	remoteRefreshWait = 25 * time.Second
)

func askRemote(ctx context.Context, p Provider, method, path string, wait time.Duration) ([]SubscriptionQuota, error) {
	var cards []SubscriptionQuota
	if err := remoteJSON(ctx, p, method, path, wait, &cards); err != nil {
		return nil, err
	}
	return cards, nil
}

// signRemote puts on req, a request of magpie's own to the remote magpie
// p (its cards, a refresh, its quota history, a key's limit), key and p's
// Headers, as its model list and its API requests carry them (Yirasumi on
// Discord: the Usage page's read of the remote went without the headers a
// proxy in front of it asks for). It is no turn of a conversation:
// ${sessionId} and ${threadId} are an id of its own (conversation.go).
// The key goes as Authorization: Bearer, which a user's Authorization
// replaces (a proxy's own scheme), and always as x-api-key (remoteKey).
func (p Provider) signRemote(req *http.Request, key string) {
	req.Header.Set("Authorization", "Bearer "+key)
	for k, v := range p.SentHeaders(context.Background()) {
		catalog.PutUserHeader(req.Header, k, v)
	}
	remoteKey(req.Header, key)
}

// remoteKey writes key as x-api-key, in place of the header in any case:
// to a remote magpie that header is magpie's key, which a gateway reads
// beside Authorization, so a user's header (a proxy's Authorization) never
// leaves the request without it, and a key's limit is read with that key.
func remoteKey(h http.Header, key string) {
	for k := range h {
		if strings.EqualFold(k, "x-api-key") {
			delete(h, k)
		}
	}
	h.Set("x-api-key", key)
}

// remoteJSON asks p's gateway, with its key, for a {"data": …} list.
func remoteJSON(ctx context.Context, p Provider, method, path string, wait time.Duration, dst any) error {
	b, err := remoteBody(ctx, p, method, path, wait)
	if err != nil {
		return err
	}
	var list struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		return err
	}
	if len(list.Data) == 0 {
		return nil
	}
	return json.Unmarshal(list.Data, dst)
}

// remoteBody asks p's gateway, with its key, waiting wait at most, and
// gives back the body of a 2xx answer; any other is an error that says
// what to do about it.
func remoteBody(ctx context.Context, p Provider, method, path string, wait time.Duration) ([]byte, error) {
	return remoteBodyAs(ctx, p, method, path, wait, 8<<20, errRemoteNoShare)
}

// remoteBodyAs is remoteBody for an answer of up to max bytes, and with
// noShare as the word for a remote that doesn't have the path.
func remoteBodyAs(ctx context.Context, p Provider, method, path string, wait time.Duration, max int64, noShare string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, p.Anthropic+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	p.signRemote(req, p.Key)
	res, err := p.Do(http.DefaultClient, req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", errRemoteUnreachable, remoteName(p), err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, max))
	if err != nil && ctx.Err() != nil {
		return nil, fmt.Errorf("%s %s: %w", errRemoteUnreachable, remoteName(p), ctx.Err())
	}
	var e struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	switch {
	case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusMethodNotAllowed:
		return nil, fmt.Errorf("%s", noShare)
	case res.StatusCode == http.StatusForbidden:
		json.Unmarshal(b, &e)
		switch e.Error.Code {
		case KeyHeldCode:
			return nil, errKeyHeld
		case KeyReadOnlyCode:
			return nil, fmt.Errorf("%s", errRemoteReadOnly)
		}
		return nil, fmt.Errorf("%s", errRemoteNotShared)
	case res.StatusCode == http.StatusUnauthorized:
		return nil, fmt.Errorf("%s", errRemoteKey)
	case res.StatusCode < 200 || res.StatusCode >= 300:
		if json.Unmarshal(b, &e) == nil && e.Error.Message != "" {
			return nil, fmt.Errorf("%s: %s", remoteName(p), e.Error.Message)
		}
		return nil, fmt.Errorf("%s answered %s", remoteName(p), res.Status)
	}
	return b, nil
}

// errRemoteResetUnknown is a reset asked of a remote magpie that didn't
// answer in time: it may have been spent there, so it isn't asked again.
const errRemoteResetUnknown = "%s didn't say in time whether the reset was used; it may have been. Refresh the card to see before asking again"

// UseRemoteCodexReset has the remote magpie whose card id is (its Codex
// card, "office/codex") spend one of user's resets there, as its own
// Usage page would (#3850, prgrmrwy: the card here said "1 reset", and
// using it took a walk to the other computer). credit is one of the
// card's resets.each, "" for the one that runs out first. The account is
// always named: which one the remote is signed in to isn't known here,
// and a reset can't be undone. The remote's cards are read afresh after,
// so the card shows its windows started again.
func UseRemoteCodexReset(ctx context.Context, id, user, credit string) (ResetOutcome, error) {
	p, card, ok := remoteOf(id)
	if !ok || card != "codex" {
		return ResetOutcome{}, fmt.Errorf("%s is no remote magpie's Codex card", id)
	}
	if user == "" {
		return ResetOutcome{}, errors.New("name the Codex account whose reset is used")
	}
	q := url.Values{"user": {user}}
	if credit != "" {
		q.Set("credit", credit)
	}
	b, err := remoteBody(ctx, p, http.MethodPost, RemoteCodexResetPath+"?"+q.Encode(), RemoteResetWait)
	if err != nil {
		// no answer: the remote may have spent it all the same
		if errors.Is(err, context.DeadlineExceeded) {
			return ResetOutcome{}, fmt.Errorf(errRemoteResetUnknown, remoteName(p))
		}
		return ResetOutcome{}, err
	}
	var out ResetOutcome
	if err := json.Unmarshal(b, &out); err != nil || out.Code == "" {
		return ResetOutcome{}, fmt.Errorf(errRemoteResetUnknown, remoteName(p))
	}
	// the remote read the card again after the reset; what it has now
	read, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	remoteCards(read, p, true)
	cancel()
	toldUsage()
	return out, nil
}
