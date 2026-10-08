package provider

// Codex rate-limit resets: credits a ChatGPT account is granted that each
// start its current usage windows again at once. The usage reading says
// how many the account holds; the credits' own endpoint says when they run
// out, and spends one. They are shown on the account's card so they aren't
// forgotten, and spent only when the user says so.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

// ResetCredits is how many rate-limit resets an account holds and the
// soonest one of them runs out; Until is nil when none does, or when that
// couldn't be read.
type ResetCredits struct {
	Count int        `json:"count"`
	Until *time.Time `json:"until,omitempty"`
	// ByWindow: each reset is for one window, FiveHour of the five hours'
	// and Weekly of the week's, and is spent on the vendor's own page (a
	// GLM Coding team plan's, zcode_team.go), not from magpie
	ByWindow bool `json:"byWindow,omitempty"`
	FiveHour int  `json:"fiveHour,omitempty"`
	Weekly   int  `json:"weekly,omitempty"`
	// Team: the resets are a GLM Coding team plan's, not the person's own
	Team bool `json:"team,omitempty"`
	// Each is every reset still to be used and when it runs out, the
	// soonest first, one that never does last (#960: the card's tooltip
	// lists them, where it only said the first). Empty when the vendor
	// didn't say.
	Each []ResetCard `json:"each,omitempty"`
}

// ResetCard is one reset: when it runs out (nil: it never does) and, of
// resets counted by window, which one's ("fiveHour", "weekly").
type ResetCard struct {
	Until  *time.Time `json:"until,omitempty"`
	Window string     `json:"window,omitempty"`
}

// sortResetCards puts the resets in the order they run out, one that
// never does after all that do.
func sortResetCards(cs []ResetCard) {
	slices.SortStableFunc(cs, func(a, b ResetCard) int {
		switch {
		case a.Until == nil && b.Until == nil:
			return 0
		case a.Until == nil:
			return 1
		case b.Until == nil:
			return -1
		}
		return a.Until.Compare(*b.Until)
	})
}

// Words are the resets counted: "2 resets", or by window, "2 five-hour
// resets · 1 weekly reset".
func (r ResetCredits) Words() string {
	n := func(c int, unit string) string {
		if c == 1 {
			return "1 " + unit
		}
		return fmt.Sprintf("%d %ss", c, unit)
	}
	if !r.ByWindow {
		return n(r.Count, "reset")
	}
	var out []string
	if r.FiveHour > 0 {
		out = append(out, n(r.FiveHour, "five-hour reset"))
	}
	if r.Weekly > 0 {
		out = append(out, n(r.Weekly, "weekly reset"))
	}
	return strings.Join(out, " · ")
}

// codexResetDetailsTimeout bounds the second look, at when the resets run
// out: without it the count alone is shown.
var codexResetDetailsTimeout = 4 * time.Second

type codexResetCreditsWire struct {
	AvailableCount int `json:"available_count"`
	Credits        []struct {
		ID        string  `json:"id"`
		ResetType string  `json:"reset_type"`
		Status    string  `json:"status"`
		GrantedAt string  `json:"granted_at"`
		ExpiresAt *string `json:"expires_at"`
	} `json:"credits"`
}

// first is the credit still to be used that runs out first, and when:
// one that never runs out comes after all that do. id is "" when none is
// left.
func (w codexResetCreditsWire) first() (id string, at *time.Time) {
	for _, c := range w.Credits {
		if c.Status != "available" || c.ID == "" {
			continue
		}
		var t *time.Time
		if c.ExpiresAt != nil {
			if p, err := time.Parse(time.RFC3339, *c.ExpiresAt); err == nil {
				t = &p
			}
		}
		if id == "" || t != nil && (at == nil || t.Before(*at)) {
			id, at = c.ID, t
		}
	}
	return id, at
}

// soonest is when the first of the credits still to be used runs out, nil
// when none of them does.
func (w codexResetCreditsWire) soonest() *time.Time {
	_, at := w.first()
	return at
}

// each is every credit still to be used, as ResetCredits.Each lists them.
func (w codexResetCreditsWire) each() []ResetCard {
	var out []ResetCard
	for _, c := range w.Credits {
		if c.Status != "available" || c.ID == "" {
			continue
		}
		var r ResetCard
		if c.ExpiresAt != nil {
			if p, err := time.Parse(time.RFC3339, *c.ExpiresAt); err == nil {
				r.Until = &p
			}
		}
		out = append(out, r)
	}
	sortResetCards(out)
	return out
}

// codexResets is the resets an account holds, from the count its usage
// reading gave: nil when it holds none. When it does, the credits are
// asked when they run out, best effort — a failure leaves the count.
func codexResets(ctx context.Context, base, token, accountID string, count int) *ResetCredits {
	if count <= 0 {
		return nil
	}
	out := &ResetCredits{Count: count}
	ctx, cancel := context.WithTimeout(ctx, codexResetDetailsTimeout)
	defer cancel()
	var w codexResetCreditsWire
	if accountJSON(ctx, base+"/wham/rate-limit-reset-credits", token, map[string]string{"chatgpt-account-id": accountID}, &w) == nil {
		out.Until = w.soonest()
		// listed only when they are the ones counted: a list that says
		// otherwise isn't put on the card as if it were
		if each := w.each(); len(each) == count {
			out.Each = each
		}
	}
	return out
}

// ResetOutcome is what spending a reset did: Code is the vendor's word for
// it (reset, nothing_to_reset, no_credit, already_redeemed; for Claude
// already_used, not_limited, cooldown, ineligible, unavailable too) and
// Windows how many windows started again.
type ResetOutcome struct {
	Code    string `json:"code"`
	Windows int    `json:"windows"`
}

// Text says an outcome in words.
func (o ResetOutcome) Text() string {
	switch o.Code {
	case "reset":
		if o.Windows == 1 {
			return "1 window started again"
		}
		return fmt.Sprintf("%d windows started again", o.Windows)
	case "nothing_to_reset":
		return "nothing to reset — no window has been used, and the reset is kept"
	case "no_credit":
		return "no reset left on the account"
	case "already_redeemed", "already_used":
		return "that reset was already used"
	}
	return o.Code
}

// UseCodexReset spends one of the rate-limit resets of the Codex account
// user (the one Codex is signed in to when ""), starting its current
// usage windows again: the one that runs out first, so none that would
// last longer goes before it. It can't be undone: callers ask first. The
// account's usage is read afresh after.
func UseCodexReset(ctx context.Context, user string) (ResetOutcome, error) {
	user, tok, accountID, err := codexUserToken(ViaLogin(ctx, "codex", user), user)
	if err != nil {
		return ResetOutcome{}, err
	}
	ctx = ViaLogin(ctx, "codex", user) // through the account's own proxy
	base := strings.TrimSuffix(CodexBase, "/codex")
	// which of them to spend is named, as Codex itself does: left to the
	// vendor, it may be one that lasts longer
	var w codexResetCreditsWire
	if err := accountJSON(ctx, base+"/wham/rate-limit-reset-credits", tok, map[string]string{"chatgpt-account-id": accountID}, &w); err != nil {
		return ResetOutcome{}, fmt.Errorf("couldn't read which reset runs out first, so none was used: %w", err)
	}
	credit, _ := w.first()
	if credit == "" {
		return ResetOutcome{Code: "no_credit"}, nil
	}
	out, err := consumeCodexReset(ctx, base, tok, accountID, credit, newRedeemID())
	if err != nil {
		return out, err
	}
	StaleAllowance("codex", user)
	if out.Code == "reset" {
		renewedNow("codex", user)
	}
	subscriptionUsageCache.Lock()
	subscriptionUsageCache.at = time.Time{}
	subscriptionUsageCache.data = nil
	subscriptionUsageCache.Unlock()
	return out, nil
}

// codexUserToken signs in as the Codex account user: the one Codex itself
// is signed in to, or one magpie keeps — and says which it was.
func codexUserToken(ctx context.Context, user string) (who, tok, accountID string, err error) {
	// the one signed in now, read from Codex's own file: the list of those
	// kept may not have caught up with it yet
	if live, ok := liveLogin("codex"); user == "" || ok && strings.EqualFold(live.User, user) {
		tok, accountID, err = codexToken(ctx, codexAuthPath())
		return live.User, tok, accountID, err
	}
	for _, l := range Logins("codex") {
		if strings.EqualFold(l.User, user) {
			tok, accountID, err = savedLoginToken(ctx, "codex", l.User)
			return l.User, tok, accountID, err
		}
	}
	return "", "", "", fmt.Errorf("no Codex account %q", user)
}

// consumeCodexReset spends the reset credit; id makes a retry of the same
// request spend it once.
func consumeCodexReset(ctx context.Context, base, token, accountID, credit, id string) (ResetOutcome, error) {
	var out ResetOutcome
	body, _ := json.Marshal(map[string]string{"credit_id": credit, "redeem_request_id": id})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/wham/rate-limit-reset-credits/consume", bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if accountID != "" {
		req.Header.Set("chatgpt-account-id", accountID)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return out, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return out, fmt.Errorf("Codex reset: %s", http.StatusText(res.StatusCode))
	}
	var wire struct {
		Code         string `json:"code"`
		WindowsReset int    `json:"windows_reset"`
	}
	if err := json.Unmarshal(b, &wire); err != nil || wire.Code == "" {
		return out, errors.New("Codex reset: an answer that says nothing")
	}
	return ResetOutcome{Code: wire.Code, Windows: wire.WindowsReset}, nil
}

// newRedeemID is a random UUID (v4), the consume call's idempotency key.
func newRedeemID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
