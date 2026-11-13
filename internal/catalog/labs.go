package catalog

// models.dev keeps two lists. api.json is each provider's models, with
// what that host charges; models.json is each lab's models (labs/<maker>),
// with only what is the model's on every host: name, window, reply limit,
// thinking, inputs, release. A model can be in the second alone: models.dev
// took the gated Claude Mythos 5.1 out of anthropic's provider list
// (models.dev #11327) and kept its lab file (#11328), since not every
// account may call it. An account that has it still lists it — Anthropic's
// /v1/models with its key, the models Claude Code was told a Claude account
// has — and magpie describes it from its lab file (iebb on Discord).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
)

// LabsPath is where Sync keeps models.dev's lab catalog (models.json),
// beside its provider catalog.
func LabsPath() string { return filepath.Join(filepath.Dir(CachePath()), "labs.json") }

// labsTried is set once this process asked models.dev for its lab
// catalog, answered or not: one models.dev stops serving doesn't have the
// provider catalog fetched again every hour (Stale).
var labsTried atomic.Bool

// labsURL is the lab catalog beside the provider catalog modelsDevURL
// names, or "" when it names none (a test's own server root).
func labsURL() string {
	if base, ok := strings.CutSuffix(modelsDevURL, "/api.json"); ok {
		return base + "/models.json"
	}
	return ""
}

// readLabs is the lab catalog Sync kept, by "lab/model", or none when it
// has none or can't be read: the provider catalog answers alone, as it did
// before there was one.
func readLabs() map[string]mdModel {
	b, err := os.ReadFile(LabsPath())
	if err != nil {
		return nil
	}
	var m map[string]mdModel
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

// syncLabs fetches the lab catalog into LabsPath, written whole through a
// rename. A failed fetch keeps the one kept before.
func syncLabs(ctx context.Context) (bool, error) {
	u := labsURL()
	if u == "" {
		return false, nil
	}
	labsTried.Store(true)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("User-Agent", "magpie")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("models.dev labs: %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return false, err
	}
	var probe map[string]mdModel
	if err := json.Unmarshal(b, &probe); err != nil || len(probe) == 0 {
		return false, fmt.Errorf("models.dev labs: unexpected payload")
	}
	return keepFetched(LabsPath(), b)
}

// labsMissing says the provider catalog is magpie's own but no lab catalog
// was kept beside it, and none was asked for yet: one kept before magpie
// read labs is fetched again at once, not a day later.
func labsMissing() bool {
	if labsTried.Load() || labsURL() == "" || Source() != CachePath() {
		return false
	}
	_, err := os.Stat(LabsPath())
	return err != nil
}

// labKinEfforts gives a lab-only model that thinks and lists no levels
// (lab files have none) the levels of the newest thinking model of its
// family released by then that any provider gives levels: Claude Mythos
// 5.1 takes Mythos 5's. Called under loadMu, as the tables are built.
func labKinEfforts(all map[string]mdProvider) {
	type kin struct{ id, date string }
	byFamily := map[string][]kin{}
	for _, p := range all {
		for id, x := range p.Models {
			if x.Family != "" {
				byFamily[x.Family] = append(byFamily[x.Family], kin{bareID(id), x.ReleaseDate})
			}
		}
	}
	for b, x := range labs {
		if len(efforts[b]) > 0 || !thinks[b] || x.Family == "" {
			continue
		}
		var best kin
		for _, k := range byFamily[x.Family] {
			if len(efforts[k.id]) == 0 || !thinks[k.id] || x.ReleaseDate != "" && k.date > x.ReleaseDate {
				continue
			}
			if best.id == "" || k.date > best.date || k.date == best.date && k.id > best.id {
				best = k
			}
		}
		if best.id != "" {
			efforts[b] = slices.Clone(efforts[best.id])
		}
	}
}

// Describe is a model a vendor or an account lists by id, described as
// models.dev knows it on any host — most providers' word, else its lab's —
// for a list that has it where the provider's own catalog doesn't: its
// window, reply limit, thinking, levels, images and release. name is the
// list's own, kept when it gives one. It has no price: a lab gives none,
// and another host's isn't this one's.
func Describe(id, name string) Model {
	load()
	m := Model{ID: id, Name: strings.TrimSpace(name)}
	if m.Name == "" || m.Name == id {
		m.Name = id
		if n := NameOf(id); n != "" {
			m.Name = n
		}
	}
	m.Context, m.Output = ContextOf(id), OutputOf(id)
	m.Efforts = EffortsOf(id)
	m.Reasoning = Thinks(id) || len(m.Efforts) > 0
	m.Images = SeesImages(id)
	if x, ok := lookup(id, labs); ok {
		m.ImageInput = imageInput(x.Modalities.Input)
		m.Released, m.Temperature = x.ReleaseDate, x.Temperature
		m.InputModalities, m.OutputModalities = Kinds(x.Modalities.Input), Kinds(x.Modalities.Output)
	}
	return m
}
