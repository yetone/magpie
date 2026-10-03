package provider

import (
	"context"
	"fmt"
	"slices"
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
