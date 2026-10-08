package usage

import (
	"sort"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/sessions"
)

// GatewaySessionWindow returns gateway sessions and their daily usage for a
// local-date range. It is kept separate from the GUI conversion so callers
// can use the same single usage-log snapshot for list and stats.
func GatewaySessionWindow(since time.Time) ([]GatewaySession, []GatewayDayUsage) {
	return gatewaySessionSnapshot(since, nil)
}

func GatewaySessionWindowExcept(since time.Time, nativeKeys map[string]bool) ([]GatewaySession, []GatewayDayUsage) {
	return gatewaySessionSnapshot(since, nativeKeys)
}

type GatewayDayUsage struct {
	Date, Agent, Model string
	sessions.Tokens
	Cost   float64
	Priced bool
}

// GatewaySession is a read-only projection of requests served by this
// gateway. Its identity is the agent name and the session id supplied by the
// client or assigned by the gateway. Older records without an id are left out.
type GatewaySession struct {
	Agent  string            `json:"agent"`
	ID     string            `json:"id"`
	Start  time.Time         `json:"start"`
	Last   time.Time         `json:"last"`
	Models []GatewayModel    `json:"models"`
	Daily  []GatewayDayUsage `json:"-"`
	sessions.Tokens
	Cost     float64 `json:"cost"`
	Unpriced int     `json:"unpriced"`
}

type GatewayModel struct {
	Model string `json:"model"`
	sessions.Tokens
	Cost   float64 `json:"cost"`
	Priced bool    `json:"priced"`
}

// GatewaySessions aggregates one usage snapshot without reading routing
// history or agent files. NativeKeys contains identities already represented
// by local session files and is used to suppress the duplicate projection.
func GatewaySessions(since time.Time, nativeKeys map[string]bool) []GatewaySession {
	ss, _ := gatewaySessionSnapshot(since, nativeKeys)
	return ss
}

func GatewaySessionByID(agent, id string, nativeKeys map[string]bool) (GatewaySession, bool) {
	for _, s := range GatewaySessions(time.Time{}, nativeKeys) {
		if s.Agent == AgentOf(agent) && s.ID == id {
			return s, true
		}
	}
	return GatewaySession{}, false
}

func gatewaySessionSnapshot(since time.Time, nativeKeys map[string]bool) ([]GatewaySession, []GatewayDayUsage) {
	priceOf := pricer()
	type key struct{ agent, id string }
	groups := map[key]*GatewaySession{}
	models := map[key]map[string]*GatewayModel{}
	type dayKey struct{ date, agent, model string }
	daily := map[dayKey]*GatewayDayUsage{}
	perSession := map[key]map[dayKey]*GatewayDayUsage{}
	Visit(since, func(r Record) {
		if r.IsRejected() || r.Session == "" {
			return
		}
		a := AgentOf(r.Agent)
		k := key{a, r.Session}
		if nativeKeys != nil && nativeKeys[a+"|"+r.Session] {
			return
		}
		s := groups[k]
		if s == nil {
			s = &GatewaySession{Agent: a, ID: r.Session, Models: []GatewayModel{}}
			groups[k] = s
			models[k] = map[string]*GatewayModel{}
			perSession[k] = map[dayKey]*GatewayDayUsage{}
		}
		if s.Start.IsZero() || r.Time.Before(s.Start) {
			s.Start = r.Time
		}
		if end := r.Time.Add(time.Duration(max(0, r.Millis)) * time.Millisecond); end.After(s.Last) {
			s.Last = end
		}
		p := priceOf(r)
		cost, priced := 0.0, true
		if r.Input+r.Output > 0 {
			if p == nil {
				priced = false
			} else {
				cost = r.CostAt(*p)
			}
		}
		tokens := sessions.Tokens{Input: r.Input, Output: r.Output, CacheRead: r.CacheRead, CacheWrite: r.CacheWrite, CacheWrite1h: r.CacheWrite1h}
		dk := dayKey{r.Time.In(time.Local).Format(time.DateOnly), a, r.Model}
		for _, kept := range []map[dayKey]*GatewayDayUsage{daily, perSession[k]} {
			d := kept[dk]
			if d == nil {
				d = &GatewayDayUsage{Date: dk.date, Agent: a, Model: r.Model, Priced: true}
				kept[dk] = d
			}
			d.Input += tokens.Input
			d.Output += tokens.Output
			d.CacheRead += tokens.CacheRead
			d.CacheWrite += tokens.CacheWrite
			d.CacheWrite1h += tokens.CacheWrite1h
			d.Cost += cost
			d.Priced = d.Priced && priced
		}
		s.Input += r.Input
		s.Output += r.Output
		s.CacheRead += r.CacheRead
		s.CacheWrite += r.CacheWrite
		s.CacheWrite1h += r.CacheWrite1h
		m := models[k][r.Model]
		if m == nil {
			m = &GatewayModel{Model: r.Model, Priced: true}
			models[k][r.Model] = m
		}
		m.addRecord(r, p)
	})
	for k, s := range groups {
		for _, d := range perSession[k] {
			s.Daily = append(s.Daily, *d)
		}
		sort.Slice(s.Daily, func(i, j int) bool {
			if s.Daily[i].Date != s.Daily[j].Date {
				return s.Daily[i].Date < s.Daily[j].Date
			}
			return s.Daily[i].Model < s.Daily[j].Model
		})
		for _, m := range models[k] {
			s.Models = append(s.Models, *m)
			s.Cost += m.Cost
			if !m.Priced && m.Input+m.Output > 0 {
				s.Unpriced++
			}
		}
		sort.Slice(s.Models, func(i, j int) bool {
			a, b := s.Models[i], s.Models[j]
			if a.Input+a.Output != b.Input+b.Output {
				return a.Input+a.Output > b.Input+b.Output
			}
			return a.Model < b.Model
		})
	}
	out := make([]GatewaySession, 0, len(groups))
	for _, s := range groups {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Last.Equal(out[j].Last) {
			return out[i].Last.After(out[j].Last)
		}
		return out[i].Agent+"|"+out[i].ID < out[j].Agent+"|"+out[j].ID
	})
	days := make([]GatewayDayUsage, 0, len(daily))
	for _, d := range daily {
		days = append(days, *d)
	}
	sort.Slice(days, func(i, j int) bool {
		if days[i].Date != days[j].Date {
			return days[i].Date < days[j].Date
		}
		if days[i].Agent != days[j].Agent {
			return days[i].Agent < days[j].Agent
		}
		return days[i].Model < days[j].Model
	})
	return out, days
}

func (m *GatewayModel) addRecord(r Record, p *catalog.Price) {
	m.Input += r.Input
	m.Output += r.Output
	m.CacheRead += r.CacheRead
	m.CacheWrite += r.CacheWrite
	m.CacheWrite1h += r.CacheWrite1h
	if r.Input+r.Output == 0 {
		return
	}
	if p == nil {
		m.Priced = false
		return
	}
	m.Cost += r.CostAt(*p)
}
