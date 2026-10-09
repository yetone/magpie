package gui

import (
	"net/http"
	"strconv"

	"github.com/yetone/magpie/internal/usage"
)

func analyticsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/analytics", func(rw http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		p := usage.Period(q.Get("period"))
		switch p {
		case usage.Today, usage.Week, usage.Month, usage.All:
		default:
			p = usage.Month
		}

		filter := usage.AnalyticsFilter{
			Model:    q.Get("model"),
			Provider: q.Get("provider"),
			Agent:    q.Get("agent"),
		}

		data := usage.Analyze(p, filter)
		writeJSON(rw, data)
	})

	mux.HandleFunc("GET /api/analytics/calls", func(rw http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		p := usage.Period(q.Get("period"))
		switch p {
		case usage.Today, usage.Week, usage.Month, usage.All:
		default:
			p = usage.Month
		}

		filter := usage.AnalyticsFilter{
			Model:    q.Get("model"),
			Provider: q.Get("provider"),
			Agent:    q.Get("agent"),
		}

		chartID := q.Get("chart_id")
		limit := 50
		if lStr := q.Get("limit"); lStr != "" {
			if l, err := strconv.Atoi(lStr); err == nil && l > 0 {
				limit = l
			}
		}

		calls, err := usage.RecentCalls(p, filter, chartID, limit)
		if err != nil {
			fail(rw, err)
			return
		}

		if calls == nil {
			calls = []usage.CallWithCost{}
		}

		writeJSON(rw, map[string]any{
			"calls": calls,
		})
	})
}
