package provider

// A provider with a decision API (Provider.Decide) — TypeSafe's System One,
// which Jev answers — serves no conversation. Given a message and typed
// questions it answers with a choice and how likely each option was, so a
// routing group can ask it, as a user's turn begins, which of its rules'
// intents the message is and how hard the turn is to think about (see
// gateway/decide.go). Its models are never in the list agents pick from,
// nor members of a group: they are only a group's classifier.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// JevLatest is the Jev a decision provider is asked with when the user
// picked none: TypeSafe's latest stable one.
const JevLatest = "jev-latest"

// Decides reports whether the provider is a decision API.
func (p Provider) Decides() bool { return p.Decide != "" }

// decideModels are the models a decision provider offers: the vendor's
// list when fetched, else Jev's aliases.
func (p Provider) decideModels() []catalog.Model {
	if live, _, ok := catalog.Live(p.ID); ok && len(live) > 0 {
		return live
	}
	return []catalog.Model{{ID: JevLatest, Name: "Jev"}, {ID: "jev-preview", Name: "Jev (preview)"}}
}

// Deciders are the models of the decision providers ready now, as a
// group's classifier names them.
func Deciders() []Entry {
	var out []Entry
	for _, p := range All() {
		if !p.Decides() || !p.Ready() {
			continue
		}
		for _, m := range p.Exposed() {
			out = append(out, Entry{ID: p.ID + "/" + m.ID, Model: m.ID, Name: m.Name, Provider: p})
		}
	}
	return out
}

// IsDecider reports whether a classifier ("provider/model") is a decision
// provider's model.
func IsDecider(id string) bool {
	p, _, ok := Resolve(id)
	return ok && p.Decides()
}

// fetchDecide lists the models a decision provider's key can use:
// {"models":[{"name":…,"description":…}]}.
func (p Provider) fetchDecide(ctx context.Context) ([]catalog.Model, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.Decide+"/models", nil)
	if err != nil {
		return nil, err
	}
	if err := p.Sign(ctx, req, Chat, nil); err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", p.Name, APIError(b, res.Status))
	}
	var out struct {
		Models []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"models"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("%s: not a model list", p.Name)
	}
	var ms []catalog.Model
	for _, m := range out.Models {
		if m.Name != "" {
			ms = append(ms, catalog.Model{ID: m.Name, Name: m.Name})
		}
	}
	if len(ms) == 0 {
		return nil, fmt.Errorf("%s lists no models", p.Name)
	}
	return ms, catalog.SaveLive(p.ID, p.Decide, ms)
}

// testDecide asks the decision API for its models, the one call it
// answers that costs nothing.
func (p Provider) testDecide(ctx context.Context) []Result {
	t0 := time.Now()
	r := Result{Protocol: "decide", Model: JevLatest}
	ms, err := p.fetchDecide(ctx)
	r.Millis = time.Since(t0).Milliseconds()
	if err != nil {
		r.Error = err.Error()
		return []Result{r}
	}
	r.OK, r.Status = true, http.StatusOK
	if len(ms) > 0 {
		r.Model = ms[0].ID
	}
	return []Result{r}
}
