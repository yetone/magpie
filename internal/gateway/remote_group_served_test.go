package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// A remote magpie asked for one of its routing groups answers with the
// member the group routed to: that is the group picking, not the vendor
// serving another model, so neither the route nor the usage row is marked
// swapped, and both say the group routed it (莫 on Discord: group/auto-…
// answered by deepseek/deepseek-v4.1-flash, shown amber). A vendor that
// does serve another model is still marked, through the remote magpie or
// through a group of the computer's own. Here the gateway is both
// computers', as in TestRemoteMagpieNativeAPI.
func TestRemoteMagpieGroupNotSwapped(t *testing.T) {
	fresh(t)
	// the vendor names deepseek-v4.1-flash with its maker, as a relay
	// does, and answers sol with luna
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		served := "deepseek/deepseek-v4.1-flash"
		if gjsonModel(b) == "sol" {
			served = "luna"
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"`+served+`","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`)
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"deepseek-v4.1-flash", "sol"}, Chat: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	for _, g := range []provider.Group{
		{ID: "auto-deepseek-v4-1-flash", Name: "Auto", Members: []string{"fake/deepseek-v4.1-flash"}},
		{ID: "sun", Name: "Sun", Members: []string{"fake/sol"}},
	} {
		if err := provider.SaveGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	s := New()
	h := s.Handler()
	remote := httptest.NewServer(h)
	t.Cleanup(remote.Close)
	id, err := provider.Add(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Chat: strings.TrimPrefix(remote.URL, "http://")})
	if err != nil {
		t.Fatal(err)
	}
	office, err := provider.Find(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := office.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name, model, served string
		swapped, routed     bool
	}{
		{"remote group", id + "/group/auto-deepseek-v4-1-flash", "deepseek/deepseek-v4.1-flash", false, true},
		{"remote model swapped", id + "/fake/sol", "luna", true, false},
		{"remote group swapped below", id + "/group/sun", "luna", false, true},
		{"own group swapped", "group/sun", "luna", true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s.trace.mu.Lock()
			before := len(s.trace.routes)
			s.trace.mu.Unlock()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"`+c.model+`","messages":[{"role":"user","content":"hi"}]}`)))
			if rec.Code != 200 {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			// the request as this computer routed it: the first of those it
			// traced, the remote's own after it
			s.trace.mu.Lock()
			r := *s.trace.routes[before]
			s.trace.mu.Unlock()
			if r.Model != c.model {
				t.Fatalf("route for %q, want %q", r.Model, c.model)
			}
			if r.Served != c.served || r.Swapped != c.swapped || r.Routed != c.routed {
				t.Errorf("route served %q swapped %v routed %v; want %q %v %v", r.Served, r.Swapped, r.Routed, c.served, c.swapped, c.routed)
			}
			if n := len(r.Tries); n == 0 || r.Tries[n-1].Swapped != c.swapped || r.Tries[n-1].Routed != c.routed {
				t.Errorf("tries %+v", r.Tries)
			}
			rows, _, _ := usage.Ledger(usage.All, usage.Filter{})
			var row *usage.Row
			for i := range rows {
				if rows[i].Requested == c.model || rows[i].Requested == strings.TrimPrefix(c.model, "group/") {
					row = &rows[i]
					break
				}
			}
			if row == nil {
				t.Fatalf("no usage row for %q: %+v", c.model, rows)
			}
			if row.Served != c.served || row.Swapped != c.swapped || row.Routed != c.routed {
				t.Errorf("usage row %+v; want served %q swapped %v routed %v", *row, c.served, c.swapped, c.routed)
			}
		})
	}
}

// A call recorded as swapped before reads as routed once ledgered again:
// the ledger judges each row as it lists it.
func TestRemoteMagpieGroupLedgerRow(t *testing.T) {
	fresh(t)
	if err := provider.Save(provider.Provider{ID: "office", Name: "Office", Key: "k", Preset: provider.RemoteMagpiePreset, Chat: "http://192.0.2.1:3425/v1", Models: []string{"group/auto-deepseek-v4-1-flash"}}); err != nil {
		t.Fatal(err)
	}
	usage.Append(usage.Record{Time: time.Now(), Agent: "claude", Provider: "office", Model: "group/auto-deepseek-v4-1-flash",
		Requested: "office/group/auto-deepseek-v4-1-flash", Served: "deepseek/deepseek-v4.1-flash", Input: 5, Output: 2, Status: 200})
	rows, _, _ := usage.Ledger(usage.All, usage.Filter{})
	if len(rows) != 1 || rows[0].Swapped || !rows[0].Routed {
		t.Fatalf("rows %+v", rows)
	}
}

// A route kept in the Routing page's history before Routed was, marked
// swapped for the member a remote magpie's group answered with, reads as
// routed; a real swap kept beside it is still one.
func TestRemoteMagpieGroupHistory(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	now := historyNoon(t)
	saveRoute(Route{ID: 1, Time: now, Model: "office/group/auto-deepseek-v4-1-flash", Done: true, Status: 200,
		Tries:  []Try{{ID: "office", Model: "group/auto-deepseek-v4-1-flash", Done: true, Status: 200, Served: "deepseek/deepseek-v4.1-flash", Swapped: true}},
		Served: "deepseek/deepseek-v4.1-flash", Swapped: true})
	saveRoute(Route{ID: 2, Time: now.Add(time.Second), Model: "office/fake/sol", Done: true, Status: 200,
		Tries:  []Try{{ID: "office", Model: "fake/sol", Done: true, Status: 200, Served: "luna", Swapped: true}},
		Served: "luna", Swapped: true})
	_, rs, _ := History(now.Format(dayForm))
	if len(rs) != 2 {
		t.Fatalf("routes %+v", rs)
	}
	if g := rs[0]; g.Swapped || !g.Routed || g.Tries[0].Swapped || !g.Tries[0].Routed {
		t.Errorf("the group's route %+v", g)
	}
	if s := rs[1]; !s.Swapped || s.Routed || !s.Tries[0].Swapped || s.Tries[0].Routed {
		t.Errorf("the swap's route %+v", s)
	}
}
