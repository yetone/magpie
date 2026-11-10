package library

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/settings"
)

// What billion-context saved, as bili counts it itself. bili has no
// command that prints it (bili 0.1.213); its running proxy serves it at
// GET /__bili/overview, the numbers its own dashboard shows, summed over
// every session in its store (~/.local/share/billion-context/sessions,
// one store for every agent and lane on this computer). Each bili that
// runs (bili start, a lane's proxy) registers itself in
// <state>/instances/<id>.json, <state> being $XDG_STATE_HOME or
// ~/.local/state, then billion-context, on every system; a crashed one
// can leave its file behind, so a file is only where to ask. magpie asks
// the newest that answers, on loopback only, and keeps the last answer
// (bili-saved.json) for when none runs: it never starts a bili to ask.

// BiliSaved is bili's own count of what it saved.
type BiliSaved struct {
	// Net is bili's net savings: what its folds kept out of every later
	// request, less the rebuilds after a fold and the summaries' cost
	// (netSavedTotal). It can be below zero. Only when HasFolds.
	Net int64 `json:"net"`
	// Gross is the same before those costs (grossSavedTotal), which bili
	// calls a theoretical upper bound.
	Gross    int64 `json:"gross"`
	HasFolds bool  `json:"hasFolds"`
	Folds    int64 `json:"folds"`
	Sessions int64 `json:"sessions"`
	Requests int64 `json:"requests"`
	Input    int64 `json:"input"`
	// USD is bili's net savings in dollars, when it priced the models
	// (hasMoneyData); 0 when it didn't.
	USD float64 `json:"usd,omitempty"`
	// At is when bili said so (RFC 3339).
	At string `json:"at"`
	// Live is whether a running bili said so just now; false is the
	// last answer magpie kept.
	Live    bool   `json:"live"`
	Version string `json:"version,omitempty"`
}

// biliOverview is the part of /__bili/overview magpie reads.
type biliOverview struct {
	Overview *struct {
		Sessions        float64 `json:"sessions"`
		Requests        float64 `json:"requests"`
		InputTokens     float64 `json:"inputTokens"`
		TokensSaved     float64 `json:"tokensSaved"`
		GrossSavedTotal float64 `json:"grossSavedTotal"`
		NetSavedTotal   float64 `json:"netSavedTotal"`
		HasFoldData     bool    `json:"hasFoldData"`
		FoldsTotal      float64 `json:"foldsTotal"`
		SavedUsdTotal   float64 `json:"savedUsdTotal"`
		HasMoneyData    bool    `json:"hasMoneyData"`
	} `json:"overview"`
	Version string `json:"version"`
}

// biliInstance is a file of bili's instance registry.
type biliInstance struct {
	Origin    string  `json:"origin"`
	StartedAt float64 `json:"startedAt"`
}

func biliStateDir() string {
	base := strings.TrimSpace(appdir.Getenv("XDG_STATE_HOME"))
	if base == "" {
		base = filepath.Join(home(), ".local", "state")
	}
	return filepath.Join(base, "billion-context")
}

func biliSavedFile() string { return filepath.Join(settings.Dir(), "bili-saved.json") }

// biliLoopback is whether origin is http on this computer: the only
// place magpie asks, as bili answers /__bili/ on loopback alone.
func biliLoopback(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" || u.Port() == "" || u.Path != "" && u.Path != "/" {
		return false
	}
	h := u.Hostname()
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// biliOrigins are the registered bilis to ask, newest first.
func biliOrigins() []string {
	dir := filepath.Join(biliStateDir(), "instances")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var found []biliInstance
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var in biliInstance
		if json.Unmarshal(b, &in) != nil || !biliLoopback(in.Origin) {
			continue
		}
		found = append(found, in)
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].StartedAt > found[j].StartedAt })
	var out []string
	for _, in := range found {
		o := strings.TrimSuffix(in.Origin, "/")
		if !slices.Contains(out, o) {
			out = append(out, o)
		}
	}
	return out
}

// biliFetch reads a bili's overview. A var for tests.
var biliFetch = func(ctx context.Context, origin string) (*biliOverview, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/__bili/overview", nil)
	if err != nil {
		return nil, err
	}
	// never through a proxy: it is this computer's
	c := &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	res, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", origin, res.Status)
	}
	var o biliOverview
	if err := json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&o); err != nil {
		return nil, err
	}
	if o.Overview == nil {
		return nil, fmt.Errorf("%s: no overview", origin)
	}
	return &o, nil
}

// biliAsk is how long magpie waits for the bilis it asks, all told.
var biliAsk = 3 * time.Second

// biliNow is the clock; a var for tests.
var biliNow = time.Now

// readBiliSaved is what bili says it saved: a running bili's answer,
// else the last one kept, else nil (no bili has answered here yet).
func readBiliSaved() *BiliSaved {
	if origins := biliOrigins(); len(origins) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), biliAsk)
		defer cancel()
		for _, o := range origins {
			ov, err := biliFetch(ctx, o)
			if err != nil {
				if ctx.Err() != nil {
					break
				}
				continue
			}
			s := savedOf(ov)
			if b, err := json.Marshal(s); err == nil {
				_ = os.MkdirAll(settings.Dir(), 0o700)
				_ = edit.WriteAtomic(biliSavedFile(), b)
			}
			s.Live = true
			return s
		}
	}
	b, err := os.ReadFile(biliSavedFile())
	if err != nil {
		return nil
	}
	var s BiliSaved
	if json.Unmarshal(b, &s) != nil || s.At == "" {
		return nil
	}
	s.Live = false
	return &s
}

func savedOf(ov *biliOverview) *BiliSaved {
	o := ov.Overview
	n := func(f float64) int64 { return int64(math.Round(f)) }
	s := &BiliSaved{
		Gross:    n(o.GrossSavedTotal),
		HasFolds: o.HasFoldData,
		Folds:    n(o.FoldsTotal),
		Sessions: n(o.Sessions),
		Requests: n(o.Requests),
		Input:    n(o.InputTokens),
		At:       biliNow().UTC().Format(time.RFC3339),
		Version:  ov.Version,
	}
	if o.HasFoldData {
		s.Net = n(o.NetSavedTotal)
		if o.HasMoneyData {
			s.USD = math.Round(o.SavedUsdTotal*100) / 100
		}
	}
	return s
}
