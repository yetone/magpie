package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/gwmw"
	"github.com/yetone/magpie/internal/masking"
	"github.com/yetone/magpie/internal/maskrec"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// redacted masks a request's secrets, as the settings say, before it goes
// to a vendor, and wraps w so what the vendor answers has them back; done
// writes what the wrapper still holds. Nothing masked, nothing wrapped. It
// fails closed: masking wanted with no masker serving, or a masker that
// fails, is an error, and the request goes nowhere (gwmw.Masking).
func redacted(ctx context.Context, w http.ResponseWriter, body []byte) (http.ResponseWriter, []byte, func(), error) {
	w, body, done, _, err := redactedHits(ctx, w, body)
	return w, body, done, err
}

// redactedHits is redacted with what it swapped, for the record of it
// (noteMasked). A masker that waits (for the local model's turn) stops
// waiting when ctx ends, and the request goes nowhere.
func redactedHits(ctx context.Context, w http.ResponseWriter, body []byte) (http.ResponseWriter, []byte, func(), maskHits, error) {
	m, err := gwmw.Masking(redactionWanted())
	if m == nil {
		return w, body, func() {}, maskHits{}, err
	}
	type out struct {
		b []byte
		h maskHits
	}
	began := time.Now()
	defer noteRedacting(ctx)()
	o, err := gwmw.Safely(func() out {
		if t, ok := m.(gwmw.ContextTeller); ok {
			b, hits := t.JSONHitsContext(ctx, body)
			return out{b, hitsOf(hits)}
		}
		if t, ok := m.(gwmw.Teller); ok {
			b, hits := t.JSONHits(body)
			return out{b, hitsOf(hits)}
		}
		b, n := m.JSON(body)
		return out{b, maskHits{n: n}}
	})
	o.h.took = time.Since(began)
	if err == nil {
		err = blocked(o.h)
	}
	if err != nil {
		return w, body, func() {}, maskHits{}, err
	}
	if o.h.n == 0 { // only values sent as written: told, nothing masked
		o.b = body
	}
	// put back even when this request masked nothing: a conversation
	// the vendor holds part of (Codex's compaction, sealed in
	// encrypted_content, a session kept on the vendor's side) has
	// placeholders a turn of only new, unmasked words doesn't carry, and
	// the model writes them back in its answer
	rw, done := m.Writer(w)
	done = toldBack(rw, done, &o.h)
	return rw, o.b, done, o.h, nil
}

// toldBack is done, which then tells the request's record what rw put
// back in the answer, where rw counts it and the request was recorded.
func toldBack(rw http.ResponseWriter, done func(), h *maskHits) func() {
	c, ok := rw.(interface{ Put() map[string]int })
	if !ok {
		return done
	}
	key := new(int64)
	h.key = key
	return func() {
		done()
		maskrec.Restored(*key, c.Put())
	}
}

// redactedPrompt masks a parsed image or video prompt before any vendor
// body is built, including multipart forms, and restores the reply's text.
// It fails closed as redacted does.
func redactedPrompt(ctx context.Context, w http.ResponseWriter, prompt string) (http.ResponseWriter, string, func(), error) {
	w, masked, done, _, err := redactedPromptHits(ctx, w, prompt)
	return w, masked, done, err
}

// redactedPromptHits is redactedPrompt with what it swapped.
func redactedPromptHits(ctx context.Context, w http.ResponseWriter, prompt string) (http.ResponseWriter, string, func(), maskHits, error) {
	m, masked, h, err := maskTextHits(ctx, prompt)
	if err != nil || m == nil { // masking off: nothing to put back
		return w, prompt, func() {}, h, err
	}
	if h.n == 0 {
		masked = prompt
	}
	// put back even with nothing masked, as redactedHits does
	rw, done := m.Writer(w)
	done = toldBack(rw, done, &h)
	return rw, masked, done, h, nil
}

// maskedText is s masked as the settings say, and how many values it
// swapped; an error when it can't be (fail closed).
func maskedText(ctx context.Context, s string) (string, int, error) {
	_, masked, h, err := maskTextHits(ctx, s)
	return masked, h.n, err
}

// maskTextHits is maskedText with the masker that masked it and what it
// swapped. Its wait for the local model ends with ctx, as redactedHits'
// does.
func maskTextHits(ctx context.Context, s string) (gwmw.Masker, string, maskHits, error) {
	m, err := gwmw.Masking(redactionWanted())
	if m == nil {
		return nil, s, maskHits{}, err
	}
	type out struct {
		s string
		h maskHits
	}
	began := time.Now()
	defer noteRedacting(ctx)()
	o, err := gwmw.Safely(func() out {
		if t, ok := m.(gwmw.ContextTeller); ok {
			got, hits := t.TextHitsContext(ctx, s)
			return out{got, hitsOf(hits)}
		}
		if t, ok := m.(gwmw.Teller); ok {
			got, hits := t.TextHits(s)
			return out{got, hitsOf(hits)}
		}
		got, n := m.Text(s)
		return out{got, maskHits{n: n}}
	})
	o.h.took = time.Since(began)
	if err == nil {
		err = blocked(o.h)
	}
	if err != nil {
		return nil, s, maskHits{}, err
	}
	return m, o.s, o.h, nil
}

// hitsOf are a Teller's hits, counting the places masked: a value the
// user set to warn about goes as written, so it is told, not counted.
func hitsOf(hits []gwmw.Hit) maskHits {
	h := maskHits{hits: hits}
	for _, x := range hits {
		if x.Action != "warn" {
			h.n++
		}
	}
	return h
}

// maskBlocked is a request held back: it has a value of a kind the user
// set to block (Settings › Privacy). It names the kind and the value's
// hint, never the value.
type maskBlocked struct{ hit gwmw.Hit }

func (e maskBlocked) Error() string {
	one := fmt.Sprintf("%d characters", e.hit.Len)
	if e.hit.Head != "" {
		one = e.hit.Head + "…, " + one
	}
	return fmt.Sprintf("magpie held this request back: Settings › Privacy blocks %s, and it has one (%s). Nothing was sent.", e.hit.Kind, one)
}

// blocked is the error for hits with a value to hold the request back
// for, recorded as a request held back; nil without one.
func blocked(h maskHits) error {
	for _, x := range h.hits {
		if x.Action == "block" {
			noteBlocked(h)
			return maskBlocked{x}
		}
	}
	return nil
}

// maskStatus is the status a request masking failed for gets: 400 for
// one held back, which sent again is held back again, 503 for a masker
// that couldn't mask it.
func maskStatus(err error) int {
	if errors.As(err, new(maskBlocked)) {
		return http.StatusBadRequest
	}
	return http.StatusServiceUnavailable
}

// restored is s with what a masker masked before put back; s when none
// serves.
func restored(s string, escaped bool) string {
	if m, ok := gwmw.Served(); ok {
		return m.Restore(s, escaped)
	}
	return s
}

// restoring wraps w so values masked before come back, when a masker
// serves.
func restoring(w http.ResponseWriter) (http.ResponseWriter, func()) {
	if m, ok := gwmw.Served(); ok {
		return m.Writer(w)
	}
	return w, func() {}
}

// maskedAsKey holds, on a request masked here or by the magpie that
// passed it on, what it was masked by (ownMaskedAs).
type maskedAsKey struct{}

// withMaskedAs is r noted as masked by the settings fp names, which a
// remote magpie it is passed on to is told (passOnCaller); r as it is
// for "".
func withMaskedAs(r *http.Request, fp string) *http.Request {
	if fp == "" {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), maskedAsKey{}, fp))
}

// maskedAsOf is the fingerprint ctx's request was masked by, "" for one
// not masked.
func maskedAsOf(ctx context.Context) string {
	fp, _ := ctx.Value(maskedAsKey{}).(string)
	return fp
}

// maskedBefore says r came from another magpie that masked it already by
// the settings this one masks by (MaskedAsHeader): masking it again here
// finds nothing the first pass left, and costs the request another run of
// the rules and the local model (YeYaka88 on X). It goes on masked as it
// came, and that magpie puts its values back in the answer. A magpie
// whose settings differ masks it again by its own, and one that masks
// nothing never says so. A client can send the header only with a key
// this magpie takes, and only for its own request: what it leaves
// unmasked is its own.
//
// A magpie that sends a request masked by its own model as written
// (Settings › Privacy's route, maskRouteFor) needs its own pass to know
// there was something to mask, so it masks again.
func maskedBefore(r *http.Request) (string, bool) {
	c := callerOf(r)
	if c.maskedAs == "" || MaskRoute(strings.TrimSpace(settings.Shared().RedactRouteTo)) {
		return "", false
	}
	fp := ownMaskedAs()
	return fp, fp != "" && fp == c.maskedAs
}

// ownMaskedAs names what this magpie masks a request by: its masking
// settings (masking.Fingerprint) and the masker's code, this build's
// version, since a newer one can find what an older one let by. "" when
// it masks nothing.
func ownMaskedAs() string {
	fp := masking.Fingerprint(settings.Shared())
	if fp == "" {
		return ""
	}
	return Version + "/" + fp
}

// redactionWanted: the settings ask for something to be masked. The
// masking itself is served by the redact plugin (plugins/redact); this is
// the core's own reading, so a request isn't sent unmasked when the plugin
// isn't there to mask it.
func redactionWanted() bool {
	return masking.Wanted(settings.Shared())
}

// maskedQuery masks a web search's query, as the settings say, before it
// goes to a search service that isn't a model's endpoint: a search API, or
// a Kimi Code plan's or WorkBuddy's search (to p, nil for an API; one the
// user set to go unmasked keeps it as it is). A model's web_search writes
// its query from a request already masked, but an agent's call to the
// search MCP carries it as the agent's model wrote it, values put back
// (lc on Discord). It fails closed as redacted does.
func maskedQuery(ctx context.Context, query string, p *provider.Provider) (string, error) {
	if p != nil && p.SkipsRedaction() {
		return query, nil
	}
	_, masked, h, err := maskTextHits(ctx, query)
	if err == nil {
		id := ""
		if p != nil {
			id = p.ID
		}
		noteMasked(nil, "search", h, masked, 0, id, "")
	}
	return masked, err
}

// maskedCopy is body masked for a record of a request sent nowhere, nil
// when masking fails: nothing unmasked is kept in its place.
func maskedCopy(ctx context.Context, body []byte) []byte {
	m, err := gwmw.Masking(redactionWanted())
	if err != nil {
		return nil
	}
	if m == nil {
		return body
	}
	b, err := gwmw.Safely(func() []byte {
		if t, ok := m.(gwmw.ContextTeller); ok {
			b, _ := t.JSONHitsContext(ctx, body)
			return b
		}
		b, _ := m.JSON(body)
		return b
	})
	if err != nil {
		return nil
	}
	return b
}

// laterMasked is the body of a request that went as written, masked the
// first time an observer is handed it (withBodies): after its try, never
// on its way (#3973). nil when masking fails: nothing unmasked is handed
// on in its place.
type laterMasked struct {
	once  sync.Once
	ctx   context.Context
	plain []byte
	b     []byte
}

func (l *laterMasked) body() []byte {
	l.once.Do(func() {
		l.b, l.plain = maskedCopy(l.ctx, l.plain), nil
	})
	return l.b
}

// unredactedRoute says a request resolved to p, or to the group whose
// models are ms, goes only where the user set requests to go unmasked
// (provider.SkipsRedaction): p and each of its fallbacks, or every model of
// the group (a member's own fallbacks are not the group's, see planGroup).
// One that may go on to anyone else stays masked for all of them.
func unredactedRoute(p provider.Provider, isGroup bool, ms []provider.Member) bool {
	if isGroup {
		if len(ms) == 0 {
			return false
		}
		for _, m := range ms {
			if !m.Provider.SkipsRedaction() {
				return false
			}
		}
		return true
	}
	if !p.SkipsRedaction() {
		return false
	}
	// one that resolves to nothing isn't tried (plan); a routing group
	// among them is each of its members
	for _, m := range provider.FallbackModels(p) {
		if !m.Provider.SkipsRedaction() {
			return false
		}
	}
	return true
}

// unmaskedMembers says every model in ms, the ones a picture, a video or
// a vendor's own route may go to, is a provider the user set to go
// unmasked (provider.SkipsRedaction), so the request goes as written, as
// a chat's does on such a route (unredactedRoute). None is masked.
func unmaskedMembers(ms []provider.Member) bool {
	return len(ms) > 0 && !slices.ContainsFunc(ms, func(m provider.Member) bool { return !m.Provider.SkipsRedaction() })
}

// maskedFor is ctx for masking a request resolved to p, or to the group
// whose models are ms, that may go to the providers unredactedRoute walks:
// p and each of its fallbacks, or every model of the group. A text sent
// on that route before goes on it again as it went (masking.WithRoute),
// so a prefix a vendor cached isn't rewritten by what was found in it
// since; another route masks it afresh.
func maskedFor(ctx context.Context, p provider.Provider, isGroup bool, ms []provider.Member) context.Context {
	var ids []string
	if !isGroup {
		ids = append(ids, p.ID)
		ms = provider.FallbackModels(p)
	}
	for _, m := range ms {
		ids = append(ids, m.Provider.ID)
	}
	slices.Sort(ids)
	return masking.WithRoute(ctx, strings.Join(slices.Compact(ids), "\x00"))
}

// agentTurnKey marks a request an agent sent (serveAgent, count_tokens,
// a Codex turn on its own sign-in), the ones the user's route for masked
// requests is for (maskRouteFor); magpie's own asks made on an agent's
// context take it off (ownAsk).
type agentTurnKey struct{}

func agentTurn(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), agentTurnKey{}, true))
}

func isAgentTurn(r *http.Request) bool {
	on, _ := r.Context().Value(agentTurnKey{}).(bool)
	return on
}

// MaskRoute says id may be the model an agent's request goes to as
// written when masking would change it (settings.RedactRouteTo): it
// resolves, is on, holds a conversation, and every place it can go is
// one the user set to go unmasked (unredactedRoute), its fallbacks and a
// group's members too, so nothing of it reaches a vendor.
func MaskRoute(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	if _, off := idOff(id); off {
		return false
	}
	p, model, ok := provider.Resolve(id)
	if !ok || p.DecidesModel(model) {
		return false
	}
	_, ms, isGroup := provider.FindGroup(id)
	return unredactedRoute(p, isGroup, ms)
}

// maskRouteFor is the model the user set for an agent's request that
// masking changed (h counts a value masked), the request to go there as
// written instead of masked (jessyoungyu on X); "" leaves it masked: none
// set, nothing masked (a value only warned of goes as written anyway),
// magpie's own ask, or a model that can't take it whole now (MaskRoute)
// or that the calling key may not use. Its failures are its own: what it
// can go on to is only where the user set requests to go unmasked, and
// the request is never handed back to be masked and sent to a vendor.
func maskRouteFor(r *http.Request, h maskHits) string {
	if h.n == 0 || !isAgentTurn(r) {
		return ""
	}
	to := strings.TrimSpace(settings.Shared().RedactRouteTo)
	if !MaskRoute(to) {
		return ""
	}
	if who, held := keyHolds(r); held || withGatewayKey(r) {
		p, model, _ := provider.Resolve(to)
		g, ms, isGroup := provider.FindGroup(to)
		if isGroup && !groupGiven(r, who, held, g, ms) || held && !isGroup && !modelAllowed(who, p, model) {
			return ""
		}
	}
	return to
}

// codexMaskRoute is maskRouteFor for a turn of Codex's on its own ChatGPT
// sign-in (codexUpstreamOn masks it there): masked here only when a
// model is set that can take it, so nobody else's turn is masked twice.
// A turn held back (redact.Block) is left to be held back there.
func codexMaskRoute(r *http.Request, rest string, body []byte) string {
	if rest != "/responses" || !MaskRoute(settings.Shared().RedactRouteTo) {
		return ""
	}
	// one the user set to go unmasked goes there as written anyway
	// (codexUpstreamOn), and stays on it
	if !apiKey(r.Header) && codexRouteUnmasked(nil) {
		return ""
	}
	_, _, _, hits, err := redactedHits(r.Context(), httptest.NewRecorder(), body)
	if err != nil {
		return ""
	}
	return maskRouteFor(agentTurn(r), hits)
}
