package otlp

import (
	"context"
	"reflect"
	"time"

	"github.com/yetone/magpie/internal/beat"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/redact"
	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

type sessionAvailability struct {
	config settings.OTel
	agents map[string]time.Time
	seen   map[observedSession]time.Time
}

type observedSession struct {
	agent, id string
}

// SessionAgent reports recent observations for callers without a session ID.
func (x *Exporter) SessionAgent(agent string) bool { return x.Session(agent, "") }

// Session reports whether this exact agent/session has recent local
// observations. Only requests without an ID fall back to agent-level readiness.
// Callers must also verify loopback and absence of a gateway key.
func (x *Exporter) Session(agent, session string) bool {
	e := x.running.Load()
	if e == nil || !sessions.TraceAgent(agent) {
		return false
	}
	config, err := settings.OTelExport()
	return err == nil && e.sessionObserved(agent, session, config)
}

// SessionFresh resolves a cached miss once before the gateway exports its
// first attempt. Requests without a native ID retain observed-agent readiness.
func (x *Exporter) SessionFresh(agent, session string) bool {
	if x.Session(agent, session) {
		return true
	}
	e := x.running.Load()
	if e == nil || session == "" {
		return false
	}
	config, err := settings.OTelExport()
	return err == nil && config.Enabled && config.Sessions && e.identities.VisibleFresh(agent, session)
}

func (e *otelExporter) sessionObserved(agent, session string, config settings.OTel) bool {
	if !config.Enabled || !config.Sessions {
		return false
	}
	a := e.sessions.Load()
	if a != nil && reflect.DeepEqual(a.config, config) {
		at := a.agents[agent]
		if session != "" {
			at = a.seen[observedSession{agent, session}]
		}
		if time.Since(at) < 5*time.Minute {
			return true
		}
	}
	if session == "" {
		return false
	}
	return e.identities.Visible(agent, session)
}

func sessionBody(body string, config settings.OTel) string {
	if !config.Bodies {
		return ""
	}
	body = string(redact.ScrubJSONWith([]byte(body), settings.Shared().Scrubbing()))
	cut := false
	if !config.BodiesWhole && len(body) > 256<<10 {
		body = body[:256<<10]
		cut = true
	}
	if cut {
		body += usage.BodyCut
	}
	return body
}
func sessionRecord(s sessions.TraceSpan, config settings.OTel) (Record, *OTelSpan) {
	status := 200
	if s.Error {
		status = 500
	}
	typ := s.Kind
	return Record{Agent: s.Agent, Model: s.Model, Served: s.Served, Provider: s.Provider, Time: s.Start, Millis: s.End.Sub(s.Start).Milliseconds(), Status: status,
			Input: s.Tokens.Input, Output: s.Tokens.Output, CacheRead: s.Tokens.CacheRead, CacheWrite: s.Tokens.CacheWrite, CacheWrite1h: s.Tokens.CacheWrite1h, Reasoning: s.Reasoning,
			BodyIn: sessionBody(s.Input, config), BodyOut: sessionBody(s.Output, config)},
		&OTelSpan{TraceID: sessions.TraceID(s.Agent, s.Session, s.Turn), SpanID: s.ID, ParentID: s.Parent, Root: s.Parent == "", End: s.End,
			Name: s.Name, Type: typ, SessionID: s.Session, TraceName: s.Agent + " interaction", Session: true, Inferred: s.Inferred, Update: s.Update}
}

func (e *otelExporter) offerSession(s sessions.TraceSpan, config settings.OTel) {
	// OTLP spans are complete observations, not mutable start/end events.
	// Sending the empty start root and later reusing its ID can leave Langfuse
	// displaying the empty copy even though the completed copy has bodies.
	if !s.Pending {
		r, span := sessionRecord(s, config)
		e.offer(otelItem{record: r, span: span, config: config})
	}
}

func (e *otelExporter) watchSessions(ctx context.Context, started time.Time, every time.Duration) {
	var reader *sessions.TraceReader
	var prior settings.OTel
	written := filememo.Written() // before each look, so a save during it isn't missed
	if config, err := settings.OTelExport(); err == nil && config.Enabled && config.Sessions {
		reader = sessions.NewTraceReader(started)
		reader.SetSessionIndex(&e.identities)
		prior = config
	}
	// while sessions aren't exported, a save here turns them on at once
	// (Written), and OthersEvery only catches the CLI's (#3185: a 2s tick
	// woke an idle magpie that exports nothing)
	wait := every
	for {
		select {
		case <-ctx.Done():
			return
		case <-beat.Next(wait):
		case <-written:
		}
		written = filememo.Written()
		config, err := settings.OTelExport()
		if err != nil || !config.Enabled || !config.Sessions {
			reader = nil
			e.sessions.Store(nil)
			wait = filememo.OthersEvery()
			continue
		}
		wait = every
		if reader == nil || !reflect.DeepEqual(config, prior) {
			reader = sessions.NewTraceReader(time.Now())
			reader.SetSessionIndex(&e.identities)
			prior = config
			e.sessions.Store(nil)
		}
		spans := reader.Poll(config.Bodies)
		available := &sessionAvailability{config: config, agents: map[string]time.Time{}, seen: map[observedSession]time.Time{}}
		if previous := e.sessions.Load(); previous != nil {
			for agent, at := range previous.agents {
				if time.Since(at) < 5*time.Minute {
					available.agents[agent] = at
				}
			}
			for key, at := range previous.seen {
				if time.Since(at) < 5*time.Minute {
					available.seen[key] = at
				}
			}
		}
		for _, identity := range reader.VisibleSessions() {
			available.seen[observedSession{identity.Agent, identity.ID}] = time.Now()
		}
		for _, s := range spans {
			if s.End.After(available.agents[s.Agent]) {
				available.agents[s.Agent] = s.End
			}
			key := observedSession{s.Agent, s.Session}
			if s.Session != "" && s.End.After(available.seen[key]) {
				available.seen[key] = s.End
			}
		}
		e.sessions.Store(available)
		for _, s := range spans {
			if ctx.Err() != nil {
				return
			}
			e.offerSession(s, config)
		}
	}
}
