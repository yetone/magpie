package provider

// PLUGIN-SERVED (see AGENTS.md): Antigravity ("antigravity") is a
// deprecated built-in subscription served by its plugin,
// @magpie-community/opencode-antigravity-auth, once moved onto it
// (provider.Moved). A moved one's sign-ins, models, requests and usage are
// all the plugin's, never this code's (only the move, in migrate*.go, still
// reads its accounts). A fix here alone doesn't reach those users; fix the
// plugin (github.com/magpie-community/plugins, packages/antigravity) and
// raise the mover's min in internal/provider/migrate_antigravity.go.
// Gemini CLI ("gemini") and the agy agent stay built in.

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// Antigravity rolls models out per account. The first account's catalog
// alone can hide models another enabled account already has. Keep each
// account's list for routing, and offer the union in account order.
func antigravityPoolModels(ctx context.Context) ([]catalog.Model, error) {
	var accounts []googleLogin
	for _, l := range googleLogins("antigravity") {
		if l.On {
			accounts = append(accounts, l)
		}
	}
	type result struct {
		models []catalog.Model
		err    error
	}
	results := make([]result, len(accounts))
	var wg sync.WaitGroup
	slots := make(chan struct{}, 4)
	for i, l := range accounts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
				results[i].err = ctx.Err()
				return
			}
			own, cancel := context.WithTimeout(ViaLogin(ctx, "antigravity", l.User), 8*time.Second)
			defer cancel()
			ms, err := l.acct.models(own)
			results[i] = result{ms, err}
		}()
	}
	wg.Wait()

	var out []catalog.Model
	var lastErr error
	for i, l := range accounts {
		ms, err := results[i].models, results[i].err
		key := accountModels("antigravity", l.User)
		if err != nil {
			lastErr = err
			// A failed refresh does not erase a working account's catalog.
			ms, _, _ = catalog.Live(key)
			log.Printf("antigravity %s: models not refreshed: %v; keeping the %d it listed before", l.User, err, len(ms))
		} else if err := catalog.SaveLive(key, l.acct.app.base, ms); err != nil {
			return nil, err
		}
		out = mergeAntigravityModels(out, ms)
	}
	if len(out) == 0 {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, fmt.Errorf("Antigravity has no enabled account models")
	}
	return out, catalog.SaveLive("antigravity", codeAssistDaily, out)
}

// mergeAntigravityModels adds an account's list to the pooled one. Each
// list is in its account's picker order, so a model only this account has
// goes right after the model before it in this list: a 5.5 rolled out to
// it lands where its picker shows it, ahead of the 4.6 an account without
// it still lists. A model both have keeps its place and what the least of
// them supports.
func mergeAntigravityModels(out, ms []catalog.Model) []catalog.Model {
	next := 0
	for _, m := range ms {
		if i := slices.IndexFunc(out, func(o catalog.Model) bool { return o.ID == m.ID }); i >= 0 {
			out[i].ImageInput = sharedImageInput(out[i].ImageInput, m.ImageInput)
			out[i].Images = out[i].Images && m.Images
			if m.Context > 0 && (out[i].Context == 0 || m.Context < out[i].Context) {
				out[i].Context = m.Context
			}
			if m.Output > 0 && (out[i].Output == 0 || m.Output < out[i].Output) {
				out[i].Output = m.Output
			}
			next = max(next, i+1)
			continue
		}
		out = slices.Insert(out, next, m)
		next++
	}
	return out
}

// antigravityNoted serialises noteAntigravityList's read, merge and save of
// the pooled list, for two accounts' readings at once not to save over
// each other's.
var antigravityNoted sync.Mutex

// noteAntigravityList keeps what an On account's quota reading listed as
// its own list, and the pooled list merged again from every On account's
// own, when the account's models changed: the reading runs for every
// account on its own schedule, and a model the account was given since its
// list was fetched (the first listing, a probe, a sync) was otherwise left
// out of the picker, of routing to it and of its bar until one. Only the
// models' ids are compared, as the reading's list is the one models() makes
// of the same reply.
func noteAntigravityList(user, base string, ms []catalog.Model) {
	if len(ms) == 0 {
		return
	}
	on := false
	var accounts []googleLogin
	for _, l := range googleLogins("antigravity") {
		if l.On {
			accounts = append(accounts, l)
			on = on || strings.EqualFold(l.User, user)
		}
	}
	if !on {
		return
	}
	antigravityNoted.Lock()
	defer antigravityNoted.Unlock()
	key := accountModels("antigravity", user)
	if had, _, ok := catalog.Live(key); ok && sameModelIDs(had, ms) {
		return
	}
	if err := catalog.SaveLive(key, base, ms); err != nil {
		log.Printf("antigravity %s: models listed by its reading not kept: %v", user, err)
		return
	}
	pooled, _, listed := catalog.Live("antigravity")
	if !listed {
		return // not listed yet: the first listing pools every account
	}
	var out []catalog.Model
	for _, l := range accounts {
		own, _, _ := catalog.Live(accountModels("antigravity", l.User))
		out = mergeAntigravityModels(out, own)
	}
	if len(out) == 0 || sameModelIDs(out, pooled) {
		return
	}
	if err := catalog.SaveLive("antigravity", codeAssistDaily, out); err != nil {
		log.Printf("antigravity: pooled models not kept: %v", err)
	}
}

// sameModelIDs says two lists name the same models in the same order.
func sameModelIDs(a, b []catalog.Model) bool {
	return slices.EqualFunc(a, b, func(x, y catalog.Model) bool { return x.ID == y.ID })
}
