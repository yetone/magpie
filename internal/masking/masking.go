// Package masking is Settings → Privacy's masking as the gateway's masker
// (gwmw.Masker): secrets, personal data and the user's own words go to a
// vendor as placeholders (internal/redact), and come back as they were.
// It reads the settings on each call. plugins/redact serves it.
package masking

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gwmw"
	"github.com/yetone/magpie/internal/pii"
	"github.com/yetone/magpie/internal/redact"
	"github.com/yetone/magpie/internal/settings"
)

// Masker masks as the settings say, read on each call.
type Masker struct{}

// Wanted says settings st ask for anything to be masked.
func Wanted(st settings.Settings) bool {
	return st.Redact || st.RedactPersonal || len(st.RedactWords) > 0
}

func options() redact.Options { return optionsCtx(context.Background()) }

// routeKey holds where a request masked on ctx may go (WithRoute).
type routeKey struct{}

// WithRoute is ctx for masking a request that may go only to route (the
// providers its model, the model's fallbacks or a routing group's members
// are served by): a text sent on it before goes as it went there
// (redact.Options.Route).
func WithRoute(ctx context.Context, route string) context.Context {
	return context.WithValue(ctx, routeKey{}, route)
}

type forcedKey struct{}

// WithForced is ctx for masking a request a gateway key holds to masking
// (access.RedactOn) whatever the settings say: where they mask nothing it
// is masked for secrets and personal data by the defaults.
func WithForced(ctx context.Context) context.Context {
	return context.WithValue(ctx, forcedKey{}, true)
}

// optionsCtx is options with the model's wait ending with ctx, for the
// route ctx has (WithRoute).
func optionsCtx(ctx context.Context) redact.Options {
	st := settings.Shared()
	o := redact.Options{Secrets: st.Redact, Personal: st.RedactPersonal, Kinds: st.RedactKinds, Words: st.RedactWords, WordsAnyCase: st.RedactWordsAnyCase, WordsWhole: st.RedactWordsWhole, Rules: st.RedactRules,
		Actions: st.RedactActions, Allow: st.RedactAllow, Shape: st.RedactShape}
	o.Route, _ = ctx.Value(routeKey{}).(string)
	if forced, _ := ctx.Value(forcedKey{}).(bool); forced && !Wanted(st) {
		o.Secrets, o.Personal = true, true
		st.Redact, st.RedactPersonal = true, true
	}
	if (st.Redact || st.RedactPersonal) && ModelWanted(st) {
		// until the model is on disk the rules mask alone, and nothing is
		// held back for a download (it is started here)
		if m := picked(st); m.Installed() {
			o.MinScore = pii.Threshold(st.RedactModelScore, m)
			o.Model = model(ctx, m, o.MinScore, st.RedactModel)
			o.NamesByModel = m.ID() == pii.AstrLinkGuard
		} else {
			fetch(m, st.RedactModel)
		}
	}
	return o
}

// ModelWanted says st has masking ask the local model: the user turned it
// on, or left it as it is, on by default. One who had a model downloaded,
// or picked, with it off before it was on by default had turned it off.
func ModelWanted(st settings.Settings) bool {
	switch {
	case st.RedactModel:
		return true
	case st.RedactModelOff:
		return false
	case st.RedactModelAuto:
		return true
	}
	return st.RedactModelName == "" && !onDisk()
}

// onDisk says a local model is downloaded; a var for tests.
var onDisk = func() bool {
	for _, m := range pii.Models() {
		if m.Installed() {
			return true
		}
	}
	return false
}

// fetched is when fetch last started each model's download, by id, so a
// failed one is tried again an hour later rather than on every request.
var fetched = struct {
	sync.Mutex
	at map[string]time.Time
}{at: map[string]time.Time{}}

// fetch downloads m, the model masking would ask but isn't on disk, and
// warms it once it is; the model on by default (on is false: the user
// didn't turn it on) is saved as the default's (UseDefaultRedactModel),
// unless the user picked or turned it off in between. Never in a test,
// which stays off the network (a test swaps it). A var for tests.
var fetch = func(m pii.Model, on bool) {
	if !m.Supported() || testing.Testing() {
		return
	}
	fetched.Lock()
	t, ok := fetched.at[m.ID()]
	if ok && time.Since(t) < time.Hour {
		fetched.Unlock()
		return
	}
	fetched.at[m.ID()] = time.Now()
	fetched.Unlock()
	m.Download(func() {
		if on {
			m.Warm()
		} else if ok, err := settings.UseDefaultRedactModel(m.ID()); ok && err == nil {
			m.Warm()
		}
	})
}

// picked is the local model st has masking use; a var for tests. One
// that is Privacy Filter only because no model was picked (pii.Choose)
// starts AstrLink Guard's download, the default, and is moved onto it
// once that is done.
var picked = func(st settings.Settings) pii.Model {
	id := pii.Choose(st.RedactModelName, true)
	if st.RedactModelName == "" && id == pii.PrivacyFilter {
		toGuard()
	}
	return pii.Of(id)
}

// guardTried is when toGuard last started a download, so a failed one is
// tried again an hour later rather than on every request.
var guardTried atomic.Int64

// toGuard downloads AstrLink Guard, then saves it as the pick, unless the
// user picked a model in between, and lets Privacy Filter go.
func toGuard() {
	now := time.Now().Unix()
	if t := guardTried.Load(); t != 0 && now-t < 3600 || !guardTried.CompareAndSwap(t, now) {
		return
	}
	g := pii.Of(pii.AstrLinkGuard)
	g.Download(func() {
		if settings.Shared().RedactModelName == "" && settings.UseRedactModel(pii.AstrLinkGuard) == nil {
			pii.Of(pii.PrivacyFilter).Stop()
			g.Warm()
		}
	})
}

// defaultWait is how long a request waits for the local model on by
// default (not turned on by the user): its turn, its loading, its reading.
// Past it the rules mask alone. AstrLink Guard reads on the CPU, a long
// history it hasn't seen takes minutes there, and a few requests at a
// time are read, on 4 cores at most between them (pii's guardTurn):
// unbounded, every request queued behind the first (shensmart on
// Discord, 10-28: Codex's requests sat masking once the default's
// download finished). What the model read meanwhile is kept, so the next
// request finds it. A var for tests.
var defaultWait = 2 * time.Second

// strictWait is how long a request waits for the local model the user
// turned on. Past it, what the model has read of the request is masked
// with its finds and the rest by the rules alone, and the request goes:
// nothing is sent as written that the rules would mask. Unbounded, a
// burst of requests with text the model hadn't read (new tool results, a
// resumed conversation, subagents at once) queued behind each other a
// 12 KB chunk at a time, AstrLink Guard reading about 6 KB a second on 8
// cores of a Linux CPU, and each request that stopped waiting was held
// back whole: xRayySummers on X saw waits of 1.2s to 290s in one minute
// and "masking failed, so nothing was sent" for each request it ended.
// What the model reads meanwhile is kept, so the next turn, which sends
// the same history, finds it. A var for tests.
var strictWait = 15 * time.Second

// model is the local model m (internal/pii) as redact's Model, keeping
// what it is at least min sure of, waited for until ctx ends or the wait
// runs out: defaultWait for the model on by default, strictWait for one
// the user turned on (strict), from now. A wait that runs out, or a ctx
// that ends, keeps what the model had read of the texts by then (a
// pii.Partial), and the rules mask the rest alone. A model that fails
// (didn't load, isn't on disk, held back after a failure) panics where
// the user turned it on, and the request is held back (gwmw.Safely)
// rather than sent with only the rules' masking: the user asked for the
// model's, and it is broken, not busy. On by default, it finds nothing
// then: the rules mask alone.
func model(ctx context.Context, m pii.Model, min float64, strict bool) redact.Model {
	wait := defaultWait
	if strict {
		wait = strictWait
	}
	by := time.Now().Add(wait)
	return func(texts []string) [][]redact.Found {
		if !strict {
			// what is new in it first, a backlog a little a request
			ctx = pii.Bounded(ctx)
		}
		ctx, cancel := context.WithDeadline(ctx, by)
		defer cancel()
		got, err := find(ctx, m, texts)
		var p *pii.Partial
		switch {
		case errors.As(err, &p):
			notePartial(p, wait)
			if len(got) != len(texts) {
				got = make([][]redact.Found, len(texts))
			}
		case err != nil && strict:
			panic(err)
		case err != nil:
			return make([][]redact.Found, len(texts))
		}
		out := make([][]redact.Found, len(got))
		for i, fs := range got {
			for _, f := range fs {
				if f.Score >= min {
					out[i] = append(out[i], f)
				}
			}
		}
		return out
	}
}

// partialLogged is when notePartial last wrote to the log, so a burst of
// requests past their wait writes one line.
var partialLogged atomic.Int64

// notePartial logs, at most once a minute, that a request went with the
// model's finds for only part of it.
func notePartial(p *pii.Partial, wait time.Duration) {
	if p.Missing == 0 {
		return
	}
	now := time.Now().Unix()
	if t := partialLogged.Load(); t != 0 && now-t < 60 || !partialLogged.CompareAndSwap(t, now) {
		return
	}
	log.Printf("masking: the local model read %d of a request's %d texts in %s (%v); the rules masked the rest alone, and the model keeps reading for the next request", p.Of-p.Missing, p.Of, wait, p.Err)
}

// handBack is how long find waits, once ctx ends, for m to answer with
// what it knew by then (internal/pii's backend does at once, from its
// cache) before giving up on it.
const handBack = 100 * time.Millisecond

// find is m.Find, given up when ctx ends even where m doesn't stop for
// it (a read of its cache from disk, a load): m is given handBack to
// answer with what it had read, then it is a pii.Partial with nothing
// read, and m finishes on its own.
func find(ctx context.Context, m pii.Model, texts []string) ([][]redact.Found, error) {
	type res struct {
		got [][]redact.Found
		err error
	}
	ch := make(chan res, 1)
	go func() {
		// a panic in the model is this request's error: on a goroutine
		// of its own it would end magpie (#4553)
		defer func() {
			if r := recover(); r != nil {
				log.Printf("masking: the local model panicked: %v\n%s", r, debug.Stack())
				ch <- res{nil, fmt.Errorf("the local masking model failed reading this request (%v)", r)}
			}
		}()
		got, err := m.Find(ctx, texts)
		ch <- res{got, err}
	}()
	select {
	case r := <-ch:
		return r.got, r.err
	case <-ctx.Done():
	}
	t := time.NewTimer(handBack)
	defer t.Stop()
	select {
	case r := <-ch:
		return r.got, r.err
	case <-t.C:
		return nil, &pii.Partial{Missing: len(texts), Of: len(texts), Err: ctx.Err()}
	}
}

// ModelOn says Settings → Privacy's local model masks requests, as the
// user turned it on: only then is a request held back for a model that
// fails, and waited for it strictWait (model's strict).
func ModelOn() bool { return settings.Shared().RedactModel }

// ModelBusy says what the local model is doing that a request may wait
// on, for the error of one that waited too long: "" when nothing.
func ModelBusy() string {
	st := settings.Shared()
	return busy(picked(st).Now())
}

func busy(st pii.Status) string {
	var what string
	switch {
	case st.Loading:
		what = "it is still loading"
	case st.Running:
		what = "it is masking another request"
	default:
		return ""
	}
	if st.Waiting > 0 {
		what += fmt.Sprintf(", with %d more waiting for it", st.Waiting)
	}
	return what
}

func (Masker) JSON(body []byte) ([]byte, int) {
	o := options()
	if !o.Secrets && !o.Personal && len(o.Words) == 0 {
		return body, 0
	}
	return redact.MaskJSON(body, o)
}

func (Masker) Text(s string) (string, int) { return redact.Mask(s, options()) }

// JSONHits is JSON, with what it masked (gwmw.Teller).
func (m Masker) JSONHits(body []byte) ([]byte, []gwmw.Hit) {
	return m.JSONHitsContext(context.Background(), body)
}

// JSONHitsContext is JSONHits, its wait for the model ending with ctx
// (gwmw.ContextTeller).
func (Masker) JSONHitsContext(ctx context.Context, body []byte) ([]byte, []gwmw.Hit) {
	o := optionsCtx(ctx)
	if !o.Secrets && !o.Personal && len(o.Words) == 0 {
		return body, nil
	}
	var hits []gwmw.Hit
	o.Seen = told(&hits)
	out, _ := redact.MaskJSON(body, o)
	return out, hits
}

// TextHits is Text, with what it masked.
func (m Masker) TextHits(s string) (string, []gwmw.Hit) {
	return m.TextHitsContext(context.Background(), s)
}

// TextHitsContext is TextHits, its wait for the model ending with ctx.
func (Masker) TextHitsContext(ctx context.Context, s string) (string, []gwmw.Hit) {
	o := optionsCtx(ctx)
	var hits []gwmw.Hit
	o.Seen = told(&hits)
	out, _ := redact.Mask(s, o)
	return out, hits
}

func (Masker) Value(p string) (string, bool) { return redact.Value(p) }

// told adds each value found to hits, with its hint: the value itself
// stays in redact's memory, or, sent as written, nowhere.
func told(hits *[]gwmw.Hit) func(redact.Hit) {
	return func(h redact.Hit) {
		*hits = append(*hits, gwmw.Hit{Kind: h.Kind, By: h.By, Placeholder: h.Placeholder, Action: h.Action, Head: h.Head, Len: h.Len})
	}
}

// Preview is s as the settings would send it, and what was found in it,
// with nothing remembered: no placeholder is kept for the replies, and
// none of its values is masked again later as one seen before. With
// masking off it is as masking would be turned on (Off says so), so the
// user can try it first.
func Preview(s string) (out string, hits []gwmw.Hit, off bool) {
	o := options()
	if !o.Secrets && !o.Personal && len(o.Words) == 0 {
		o.Secrets, o.Personal, off = true, true, true
	}
	o.Seen = told(&hits)
	out = redact.Preview(s, o)
	return out, hits, off
}

func (Masker) Restore(s string, escaped bool) string { return redact.Restore(s, escaped) }

func (Masker) Writer(w http.ResponseWriter) (http.ResponseWriter, func()) {
	rw := redact.NewWriter(w)
	return rw, rw.Finish
}

var (
	_ gwmw.Teller        = Masker{}
	_ gwmw.ContextTeller = Masker{}
)
