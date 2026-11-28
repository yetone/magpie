package provider

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// A ChatGPT API account's tokens name neither the ChatGPT account nor its
// user: OpenAI's Sign in with ChatGPT access token carries only a salt and
// encrypted metadata under https://api.openai.com/auth, and no plan (the
// row had none), so SamePlanAccounts' match on those ids never found the
// Codex sign-in of the same account (tmr11235 on X, after v0.1.4998: "It
// still doesn't show here. I'm sure this account is signed in to Codex
// too"). What does tie them is the app: chatgpt.com's Settings › Usage
// lists each app the account let in, by its client id, through
// /wham/usage/chatpass/apps asked with the account's Codex sign-in. A
// Codex sign-in whose list holds the ChatGPT API account's client id once
// is the same ChatGPT account (workspace); none, or two, and it is none.
// No name or email is matched.

// siwcAppID is the client id OpenAI gave magpie for the account's Sign in
// with ChatGPT: the access token's client_id, else the one kept.
func siwcAppID(c siwcCreds) string {
	return cmp.Or(claimString(jwtClaims(c.Access), "client_id"), c.ClientID)
}

// codexTwinCand is a Codex sign-in magpie knows that could be a ChatGPT API
// account's twin: a saved Codex account, or the one Codex itself is
// signed in to (live), listed in magpie or not.
type codexTwinCand struct {
	user string
	live bool
	sub  string // the ID token's subject, to ask the likeliest first
	auth json.RawMessage
}

func (s codexTwinCand) key() string { return "codex\x00" + strings.ToLower(s.user) }

// siwcAppList is what one Codex sign-in's app list held when read: each
// app's client id, by how many items carried it.
type siwcAppList struct {
	at  time.Time
	ids map[string]int
}

// siwcTwinAgain is how long an app list stands: a sign-in made since, or
// let in since, is looked for again after it.
const siwcTwinAgain = 6 * time.Hour

var siwcTwins = struct {
	sync.Mutex
	lists map[string]siwcAppList // by codexTwinCand.key
	seen  map[string]time.Time   // when each client id was first looked for
	learn chan struct{}          // one look at a time
}{lists: map[string]siwcAppList{}, seen: map[string]time.Time{}, learn: make(chan struct{}, 1)}

// forgetSIWCTwins drops what was learned (tests).
func forgetSIWCTwins() {
	siwcTwins.Lock()
	siwcTwins.lists, siwcTwins.seen = map[string]siwcAppList{}, map[string]time.Time{}
	siwcTwins.Unlock()
}

// siwcTwinKnown is the Codex sign-in (its codexTwinCand.key) whose app list
// held id once, by what was read already, asking no one: false when none
// did, a list held it twice, or two sign-ins' lists held it.
func siwcTwinKnown(id string) (string, bool) {
	if id == "" {
		return "", false
	}
	siwcTwins.Lock()
	defer siwcTwins.Unlock()
	return siwcTwinIn(id)
}

func siwcTwinIn(id string) (string, bool) {
	var twin string
	for k, l := range siwcTwins.lists {
		switch n := l.ids[id]; {
		case n == 0:
		case n > 1 || twin != "":
			return "", false
		default:
			twin = k
		}
	}
	return twin, twin != ""
}

// codexTwinCands are the Codex sign-ins magpie knows: Logins("codex"), and
// the one Codex is signed in to when it isn't among them (the Codex
// provider removed, so magpie keeps no copy). None with account discovery
// off.
func codexTwinCands() []codexTwinCand {
	if settings.AccountDiscoveryOff() {
		return nil
	}
	saved := map[string]json.RawMessage{}
	for _, l := range sharedLogins() {
		if l.Agent == "codex" {
			saved[strings.ToLower(l.User)] = l.Auth
		}
	}
	live, liveOK := codexLoginIn(codexHome())
	var out []codexTwinCand
	listed := map[string]bool{}
	for _, l := range Logins("codex") {
		s := codexTwinCand{user: l.User, live: l.Active, auth: saved[strings.ToLower(l.User)]}
		if s.live && liveOK {
			s.auth = live.Auth
		}
		listed[strings.ToLower(l.User)] = true
		out = append(out, s)
	}
	if liveOK && live.User != "" && !listed[strings.ToLower(live.User)] {
		out = append(out, codexTwinCand{user: live.User, live: true, auth: live.Auth})
	}
	for i := range out {
		var a codexAuth
		if json.Unmarshal(out[i].auth, &a) == nil {
			out[i].sub = claimString(jwtClaims(a.Tokens.IDToken), "sub")
		}
	}
	return out
}

// token is s's access token and ChatGPT account id. The one Codex is
// signed in to is taken as Codex keeps it, never renewed here; a saved
// one is renewed as its own row's read would, unless magpie leaves Codex
// alone (switched off or removed), when it too is taken as kept.
func (s codexTwinCand) token(ctx context.Context) (tok, accountID string, err error) {
	if s.live {
		return codexTokenAsKept(codexHome())
	}
	if !leftAlone("codex") {
		return savedLoginToken(ctx, "codex", s.user)
	}
	var a codexAuth
	if json.Unmarshal(s.auth, &a) != nil || a.Tokens.AccessToken == "" {
		return "", "", errors.New("no sign-in kept")
	}
	if exp, _ := jwtClaims(a.Tokens.AccessToken)["exp"].(float64); exp != 0 && !time.Now().Before(time.Unix(int64(exp), 0)) {
		return "", "", errors.New("the sign-in has expired")
	}
	return a.Tokens.AccessToken, codexAccountID(a), nil
}

// chatpassApps is /wham/usage/chatpass/apps: the apps the ChatGPT
// account let in, each by its client id.
type chatpassApps struct {
	Items []struct {
		ID string `json:"id"`
	} `json:"items"`
}

// siwcAppsOf reads s's app list.
func siwcAppsOf(ctx context.Context, s codexTwinCand) (map[string]int, error) {
	ctx = ViaLogin(ctx, "codex", s.user)
	tok, acct, err := s.token(ctx)
	if err != nil {
		return nil, err
	}
	var apps chatpassApps
	base := strings.TrimSuffix(CodexBase, "/codex")
	if err := accountJSON(ctx, base+"/wham/usage/chatpass/apps", tok, codexAccountHeaders(acct), &apps); err != nil {
		return nil, err
	}
	ids := map[string]int{}
	for _, it := range apps.Items {
		if it.ID != "" {
			ids[it.ID]++
		}
	}
	return ids, nil
}

// siwcTwin is the Codex sign-in that is the same ChatGPT account as the
// ChatGPT API account c, looked for if it isn't known: each Codex sign-in
// whose app list is older than siwcTwinAgain, or than c's first look, is
// read once, the ones whose subject is c's first, until one holds c's
// client id. A read that fails is tried again on the next look.
func siwcTwin(ctx context.Context, c siwcCreds) (codexTwinCand, bool) {
	id := siwcAppID(c)
	if id == "" || settings.AccountDiscoveryOff() {
		return codexTwinCand{}, false
	}
	select {
	case siwcTwins.learn <- struct{}{}:
		defer func() { <-siwcTwins.learn }()
	case <-ctx.Done():
		return codexTwinCand{}, false
	}
	cands := codexTwinCands()
	sort.SliceStable(cands, func(i, j int) bool {
		return c.Subject != "" && cands[i].sub == c.Subject && cands[j].sub != c.Subject
	})
	byKey := map[string]codexTwinCand{}
	for _, s := range cands {
		byKey[s.key()] = s
	}
	now := time.Now()
	siwcTwins.Lock()
	first, ok := siwcTwins.seen[id]
	if !ok {
		first = now
		siwcTwins.seen[id] = now
	}
	siwcTwins.Unlock()
	for _, s := range cands {
		siwcTwins.Lock()
		twin, found := siwcTwinIn(id)
		l, read := siwcTwins.lists[s.key()]
		siwcTwins.Unlock()
		if found {
			if t, ok := byKey[twin]; ok {
				return t, true
			}
		}
		if read && now.Sub(l.at) < siwcTwinAgain && (l.ids[id] > 0 || !l.at.Before(first)) {
			continue
		}
		ids, err := siwcAppsOf(ctx, s)
		if err != nil {
			continue
		}
		siwcTwins.Lock()
		siwcTwins.lists[s.key()] = siwcAppList{at: time.Now(), ids: ids}
		siwcTwins.Unlock()
	}
	siwcTwins.Lock()
	twin, found := siwcTwinIn(id)
	siwcTwins.Unlock()
	t, ok := byKey[twin]
	return t, found && ok
}

// siwcQuota is a ChatGPT API account's reading: its plan's windows, read
// with the Codex sign-in of the same ChatGPT account (siwcTwin), since
// OpenAI's API tells them nowhere for the account itself. A twin magpie
// lists as a Codex account, and reads as one, is left to that read:
// samePlanReadings shows it on this row, so the one window is asked once.
// The rest, Codex's own sign-in with the Codex provider removed or
// switched off, are read here as kept. With no twin, the row says where
// the usage is (siwcNoUsage).
func siwcQuota(ctx context.Context, l Login) SubscriptionQuota {
	none := SubscriptionQuota{Provider: l.Agent, Plan: l.Plan, Windows: []QuotaWindow{}, Error: siwcNoUsage, SeeAt: SIWCUsageURL}
	var c siwcCreds
	found := false
	for _, s := range sharedLogins() {
		if s.Agent == ChatGPTAPIID && strings.EqualFold(s.User, l.User) {
			c, found = siwcSaved(s)
		}
	}
	if !found {
		return none
	}
	// a Codex row magpie reads: samePlanReadings lays its reading here
	codexRow := func(key string) bool {
		if leftAlone("codex") {
			return false
		}
		for _, cl := range Logins("codex") {
			if "codex\x00"+strings.ToLower(cl.User) == key {
				return true
			}
		}
		return false
	}
	if to, ok := SamePlanAccounts()[ChatGPTAPIID+"\x00"+strings.ToLower(l.User)]; ok && codexRow(to) {
		return none
	}
	twin, ok := siwcTwin(ctx, c)
	if !ok {
		return none
	}
	if codexRow(twin.key()) {
		return none
	}
	ctx = ViaLogin(ctx, "codex", twin.user)
	tok, acct, err := twin.token(ctx)
	if err != nil {
		return SubscriptionQuota{Provider: l.Agent, Plan: l.Plan, Windows: []QuotaWindow{}, Error: err.Error()}
	}
	// the plan's resets, credits and end too, as the Codex row reads them
	// (tmr11235 on X); its resets come in the same read, nothing more is
	// asked, and the Codex app's hold is the Codex app's
	plan, ws, resets, credits, _, _, err := codexWindowsLeft(ctx, tok, acct)
	q := SubscriptionQuota{Provider: l.Agent, Plan: cmp.Or(plan, l.Plan), Windows: ws, Resets: resets, Balance: credits, Until: codexUntil(twin.auth, time.Now())}
	if err != nil {
		q.Error = err.Error()
	}
	return q
}
