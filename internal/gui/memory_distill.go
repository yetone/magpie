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
}

func readDistill() (*distillView, error) {
	st, err := memdistill.Load()
	if err != nil {
		return nil, err
	}
	st.Done = nil // the marks are the run's, not the page's
	v := &distillView{State: st, Auto: distillAuto(), Models: []modelRef{}, Running: memdistill.Running() && !memdistill.Dreaming(), Dreaming: memdistill.Dreaming()}
	for _, e := range provider.Served() {
		if e.Group != "" || e.Provider.Ready() && !e.Provider.DecideOnly() {
			m := modelRef{ID: e.ID, Name: e.Name, Provider: e.Provider.ID, PName: e.Provider.Name, Icon: e.Provider.Icon}
			if e.Group != "" {
				m.Provider, m.PName = "", e.Group
			}
			v.Models = append(v.Models, m)
		}
	}
	return v, nil
}

// distillAuto is the model conversations are read with when none is
// picked: the small model magpie picks to describe images, one of the
// user's own subscriptions first (gateway.AutoVision).
func distillAuto() string { return gateway.AutoVision() }

// distillComplete sends one request through the gateway this process
// serves, as any agent's goes: routed, masked and counted, under the
// memory agent's name.
func distillComplete(ctx context.Context, model, system, user string) (string, error) {
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

// distillNow starts a run in the background, for the switch turned on
// and the Read now button; the tab polls while it runs.
var distillNow = func() {
	go memdistill.Run(backendCtx, distillAuto(), distillComplete)
}

func memoryDistillRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/memory/distill", func(rw http.ResponseWriter, r *http.Request) {
		v, err := readDistill()
		if err != nil {
			fail(rw, err)
			return
		}
		writeJSON(rw, v)
	})
	// {on, model}: switched or a model picked; switched on, it reads at once
	mux.HandleFunc("POST /api/memory/distill", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			On    bool   `json:"on"`
			Model string `json:"model"`
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
		if _, err := memdistill.Set(in.On, in.Model); err != nil {
			fail(rw, err)
			return
		}
		if in.On && (!was.On || was.Model != in.Model) {
			distillNow()
		}
		v, err := readDistill()
		if err != nil {
			fail(rw, err)
			return
		}
		v.Running = v.Running || in.On && (!was.On || was.Model != in.Model)
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
