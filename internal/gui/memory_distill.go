package gui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/memdistill"
	"github.com/yetone/magpie/internal/provider"
)

// distillView is the Memory tab's "learn from conversations" row: the
// switch, the model picked and the automatic one, the models to pick
// from, the last run, and whether one is going.
type distillView struct {
	memdistill.State
	Auto    string     `json:"auto"`
	Models  []modelRef `json:"models"`
	Running bool       `json:"running"`
	// Dreaming is a tidy going (Running is conversations being read)
	Dreaming bool `json:"dreaming"`
	// Efforts are the reasoning levels of the model in use (picked or
	// automatic), weakest first, none when they aren't known; EffortNow
	// the picked Effort fitted to them
	Efforts   []string `json:"efforts,omitempty"`
	EffortNow string   `json:"effortNow,omitempty"`
	// Ranges are the ranges, in days, reading can look back (All -1)
	Ranges []int `json:"ranges"`
	// Doing of Of are the run going's progress, 0 of 0 with none
	Doing int `json:"doing,omitempty"`
	Of    int `json:"of,omitempty"`
}

func readDistill() (*distillView, error) {
	st, err := memdistill.Load()
	if err != nil {
		return nil, err
	}
	st.Done = nil // the marks are the run's, not the page's
	v := &distillView{State: st, Auto: distillAuto(), Models: []modelRef{}, Running: memdistill.Running() && !memdistill.Dreaming(), Dreaming: memdistill.Dreaming(), Ranges: memdistill.Ranges}
	if v.Running {
		v.Doing, v.Of = memdistill.Progress()
	}
	inUse := st.Model
	if inUse == "" {
		inUse = v.Auto
	}
	if inUse != "" {
		v.Efforts, v.EffortNow = gateway.ModelEfforts(inUse), gateway.SideEffort(inUse, st.Effort)
	}
	v.Models = servedModels()
	return v, nil
}

// servedModels are the models a side job can be sent to through the
// gateway: a ready subscription's (not one that only decides) and each
// routing group's, the group named in place of a provider
func servedModels() []modelRef {
	ms := []modelRef{}
	for _, e := range provider.Served() {
		if e.Group != "" || e.Provider.Ready() && !e.Provider.DecideOnly() {
			m := modelRef{ID: e.ID, Name: e.Name, Provider: e.Provider.ID, PName: e.Provider.Name, Icon: e.Provider.Icon}
			if e.Group != "" {
				m.Provider, m.PName = "", e.Group
			}
			ms = append(ms, m)
		}
	}
	return ms
}

// distillAuto is the model conversations are read with when none is
// picked: the small model magpie picks to describe images, one of the
// user's own subscriptions first (gateway.AutoVision).
func distillAuto() string { return gateway.AutoVision() }

// distillComplete sends one request through the gateway this process
// serves, as any agent's goes: routed, masked and counted, under the
// memory agent's name, asked to reason at effort when one is picked
// (gateway.WithSideEffort; RealZeroP27 on X).
func distillComplete(ctx context.Context, model, effort, system, user string) (string, error) {
	gw := served.Load()
	if gw == nil {
		return "", errors.New("the gateway isn't served by this magpie")
	}
	body, err := json.Marshal(map[string]any{
		"model": model, "max_tokens": 4096, "system": system,
		"messages": []map[string]any{{"role": "user", "content": user}},
	})
	if err != nil {
		return "", err
	}
	body = gateway.WithSideEffort(body, model, effort)
	req, err := http.NewRequestWithContext(ctx, "POST", gateway.URL()+"/v1/messages", strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.RemoteAddr = "127.0.0.1:0"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("x-api-key", gateway.TokenFor(memdistill.Agent))
	rec := httptest.NewRecorder()
	gw.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		msg := strings.TrimSpace(rec.Body.String())
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(rec.Body.Bytes(), &e) == nil && e.Error.Message != "" {
			msg = e.Error.Message
		}
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		return "", fmt.Errorf("%d: %s", rec.Code, msg)
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		return "", fmt.Errorf("a reply that isn't a message: %w", err)
	}
	var b strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	return b.String(), nil
}

// keepDistilling reads conversations into memory while the backend runs:
// a few minutes after start, then every hour.
func keepDistilling(ctx context.Context) {
	memdistill.Keep(ctx, 5*time.Minute, time.Hour, distillAuto, distillComplete)
}

// distillNow reads every conversation due in the background, for the
// switch turned on, a model or a longer range picked, and the Read now
// button; the tab polls while it runs, and Stop ends it (RealZeroP27 on
// X: the hourly 10 read 400 in 40 hours).
var distillNow = func() {
	go memdistill.ReadAll(backendCtx, distillAuto(), distillComplete)
}

// distillStop ends the run going.
var distillStop = memdistill.Stop

func memoryDistillRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/memory/distill", func(rw http.ResponseWriter, r *http.Request) {
		v, err := readDistill()
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, v)
	})
	// {on, model, effort?, days?}: switched or a model picked; switched
	// on, it reads at once. effort and days, when sent, pick the
	// reasoning level and how far back it reads; a longer range reads at
	// once too
	mux.HandleFunc("POST /api/memory/distill", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			On     bool    `json:"on"`
			Model  string  `json:"model"`
			Effort *string `json:"effort"`
			Days   *int    `json:"days"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if in.Model != "" && !strings.Contains(in.Model, "/") {
			fail(rw, fmt.Errorf("a model's id such as codex/gpt-5.5, not %q", in.Model))
			return
		}
		was, _ := memdistill.Load()
		if in.Effort != nil {
			if _, err := memdistill.SetEffort(*in.Effort); err != nil {
				fail(rw, err)
				return
			}
		}
		longer := false
		if in.Days != nil {
			st, err := memdistill.SetRange(*in.Days)
			if err != nil {
				fail(rw, err)
				return
			}
			longer = st.Since.Before(was.Since)
		}
		if _, err := memdistill.Set(in.On, in.Model); err != nil {
			fail(rw, err)
			return
		}
		read := in.On && (!was.On || was.Model != in.Model || longer)
		if read {
			distillNow()
		}
		v, err := readDistill()
		if err != nil {
			fail(rw, err)
			return
		}
		v.Running = v.Running || read
		writeJSON(rw, v)
	})
	mux.HandleFunc("POST /api/memory/distill/run", func(rw http.ResponseWriter, r *http.Request) {
		distillNow()
		v, err := readDistill()
		if err != nil {
			fail(rw, err)
			return
		}
		v.Running = true
		writeJSON(rw, v)
	})
	mux.HandleFunc("POST /api/memory/distill/stop", func(rw http.ResponseWriter, r *http.Request) {
		distillStop()
		v, err := readDistill()
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, v)
	})
	// {on}: the nightly tidy switched; the first is due at the next night
	mux.HandleFunc("POST /api/memory/dream", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			On bool `json:"on"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(rw, err)
			return
		}
		if _, err := memdistill.SetDream(in.On); err != nil {
			fail(rw, err)
			return
		}
		v, err := readDistill()
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, v)
	})
	mux.HandleFunc("POST /api/memory/dream/run", func(rw http.ResponseWriter, r *http.Request) {
		dreamNow()
		v, err := readDistill()
		if err != nil {
			fail(rw, err)
			return
		}
		v.Dreaming, v.Running = true, false
		writeJSON(rw, v)
	})
	mux.HandleFunc("POST /api/memory/dream/undo", func(rw http.ResponseWriter, r *http.Request) {
		if _, err := memdistill.Undo(); err != nil {
			fail(rw, err)
			return
		}
		v, err := readDistill()
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, v)
	})
}

// dreamNow tidies memory in the background, for the Tidy now button.
var dreamNow = func() {
	go memdistill.Dream(backendCtx, distillAuto(), distillComplete)
}
