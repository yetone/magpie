package usage

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/testenv"
)

func TestSessionTraceWirePrivacyAndTokenOwnership(t *testing.T) {
	at := time.Now()
	secret := "sk-ant-api03-" + strings.Repeat("Q7x", 30)
	for _, bodies := range []bool{false, true} {
		cfg := settings.OTel{Bodies: bodies}
		model := sessions.TraceSpan{Agent: "pi", Session: "session", Turn: "turn", ID: "1111111111111111", Parent: "2222222222222222", Name: "model m", Kind: "generation", Model: "m", Provider: "magpie", Start: at, End: at.Add(time.Second), Input: `{"api_key":"` + secret + `","prompt":"PRIVATE-PROMPT"}`, Tokens: sessions.Tokens{Input: 10, Output: 5}}
		tool := model
		tool.ID = "3333333333333333"
		tool.Kind = "tool"
		tool.Name = "bash"
		tool.Inferred = true
		root := model
		root.ID = model.Parent
		root.Parent = ""
		root.Kind = "span"
		root.Name = "pi interaction"
		e := newOTelExporter()
		payload := e.traces([]Record{sessionRecord(root, cfg), sessionRecord(model, cfg), sessionRecord(tool, cfg)})
		e.cancel()
		wire, _ := json.Marshal(payload)
		s := string(wire)
		if strings.Contains(s, secret) || !bodies && strings.Contains(s, "PRIVATE-PROMPT") {
			t.Fatalf("private content exported: %s", s)
		}
		if strings.Count(s, `"key":"gen_ai.usage.input_tokens"`) != 1 || strings.Count(s, `"key":"langfuse.session.id"`) != 3 || !strings.Contains(s, `"stringValue":"tool"`) || !strings.Contains(s, `"stringValue":"inferred"`) {
			t.Fatalf("wire: %s", s)
		}
	}
	cfg := settings.OTel{Bodies: true}
	long := strings.Repeat("A", 300<<10)
	if got := sessionBody(long, cfg); !strings.HasSuffix(got, BodyCut) {
		t.Fatal("body limit not applied")
	}
	// Scrub secret-named fields before cutting: truncation must not disable JSON masking.
	if got := sessionBody(`{"api_key":"`+long+`"}`, cfg); strings.Contains(got, strings.Repeat("A", 100)) {
		t.Fatal("truncation bypassed secret-field masking")
	}
	cfg.BodiesWhole = true
	if got := sessionBody(long, cfg); got != long {
		t.Fatal("whole body truncated")
	}
}

func TestSessionExporterDefersPendingRoot(t *testing.T) {
	e := newOTelExporter()
	defer e.cancel()
	cfg := settings.OTel{Bodies: true}
	at := time.Now()
	root := sessions.TraceSpan{Agent: "codex", Session: "session", Turn: "turn", ID: "1111111111111111", Kind: "span", Name: "codex interaction", Start: at, End: at, Pending: true}
	e.offerSession(root, cfg)
	if len(e.queue) != 0 {
		t.Fatal("unfinished root exported")
	}
	root.Pending = false
	root.End = at.Add(time.Minute)
	root.Input = `[{"type":"local_image","path":"/tmp/image.png"},{"type":"text","text":"Describe this image"}]`
	root.Output = "An image"
	e.offerSession(root, cfg)
	if len(e.queue) != 1 {
		t.Fatal("completed root missing")
	}
	record := (<-e.queue).record
	wire, err := json.Marshal(e.traces([]Record{record}))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"langfuse.observation.input", "langfuse.observation.output", "local_image", "Describe this image", "An image"} {
		if !strings.Contains(string(wire), want) {
			t.Fatalf("completed root missing %s", want)
		}
	}
}

func TestOTelSessionWatcherExportsNewInteractionWithoutGatewayDuplicates(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("OPENCODE_DB", "")
	t.Setenv("GEMINI_CLI_HOME", t.TempDir())
	root := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", root)
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", "")
	t.Setenv("CODEX_HOME", t.TempDir())
	dir := filepath.Join(root, "sessions", "work")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "2026-10-02T00-00-00-000Z_session.jsonl")
	old := time.Now().Add(-time.Minute)
	header, _ := json.Marshal(map[string]any{"type": "session", "id": "session", "timestamp": old})
	historical, _ := json.Marshal(map[string]any{"type": "message", "id": "old-user", "timestamp": old, "message": map[string]any{"role": "user", "content": "PRIVATE-OLD"}})
	if err := os.WriteFile(path, append(append(append(header, '\n'), historical...), '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	received := make(chan []byte, 4)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		received <- b
		io.WriteString(w, `{}`)
	}))
	defer collector.Close()
	otelConfig(t, settings.OTel{Endpoint: collector.URL})
	t.Setenv("MAGPIE_OTEL_SESSIONS", "true")
	t.Setenv("MAGPIE_OTEL_BODIES", "false")
	stop := StartOTel()
	t.Cleanup(stop)
	now := time.Now()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range []map[string]any{
		{"type": "message", "id": "new-user", "timestamp": now, "message": map[string]any{"role": "user", "content": "PRIVATE-NEW"}},
		{"type": "message", "id": "assistant", "parentId": "new-user", "timestamp": now.Add(time.Millisecond), "message": map[string]any{"role": "assistant", "timestamp": now.UnixMilli(), "model": "model", "provider": "magpie", "stopReason": "stop", "content": "PRIVATE-REPLY", "usage": map[string]int{"input": 10, "output": 5}}},
		{"type": "custom", "parentId": "assistant", "customType": "timing-final", "timestamp": now.Add(2 * time.Millisecond), "data": map[string]any{"totalMs": 2, "endAt": now.Add(2 * time.Millisecond).UnixMilli()}},
	} {
		b, _ := json.Marshal(o)
		f.Write(append(b, '\n'))
	}
	f.Close()
	// Linux VFS mtime can lag a write. Event timestamps, not a precise stat
	// timestamp, decide whether this immediately-after-start interaction is new.
	coarse := now.Add(-time.Second)
	if err := os.Chtimes(path, coarse, coarse); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(6 * time.Second)
	for !OTelSessionAgent("pi") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !OTelSessionAgent("pi") {
		t.Fatal("reader did not become available")
	}
	Append(Record{Time: time.Now(), Agent: "pi", Local: true, Model: "gateway-duplicate", Input: 999, Status: 200})
	select {
	case b := <-received:
		text := string(b)
		if strings.Contains(text, "PRIVATE-") || strings.Contains(text, "gateway-duplicate") || strings.Contains(text, `"intValue":"999"`) {
			t.Fatalf("history, bodies or duplicate exported: %s", text)
		}
		if !strings.Contains(text, `"stringValue":"pi interaction"`) || !strings.Contains(text, `"key":"parentSpanId"`) && !strings.Contains(text, `"parentSpanId"`) {
			t.Fatalf("no hierarchy: %s", text)
		}
		if strings.Count(text, `"key":"gen_ai.usage.input_tokens"`) != 1 {
			t.Fatalf("duplicate model usage: %s", text)
		}
		var wire struct {
			ResourceSpans []struct {
				ScopeSpans []struct {
					Spans []struct {
						SpanID string `json:"spanId"`
					}
				}
			}
		}
		if err := json.Unmarshal(b, &wire); err != nil {
			t.Fatal(err)
		}
		ids := map[string]bool{}
		for _, resource := range wire.ResourceSpans {
			for _, scope := range resource.ScopeSpans {
				for _, span := range scope.Spans {
					if ids[span.SpanID] {
						t.Fatalf("duplicate span %s: %s", span.SpanID, text)
					}
					ids[span.SpanID] = true
				}
			}
		}
	case <-time.After(10 * time.Second):
		t.Fatal("session watcher did not export")
	}
	stop()
}

func TestSessionTracingRetainsRemoteGatewayTraces(t *testing.T) {
	received := make(chan []byte, 1)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		received <- b
		io.WriteString(w, `{}`)
	}))
	defer collector.Close()
	otelConfig(t, settings.OTel{Endpoint: collector.URL})
	t.Setenv("MAGPIE_OTEL_SESSIONS", "true")
	stop := StartOTel()
	t.Cleanup(stop)
	Append(Record{Time: time.Now(), Agent: "pi", Via: "office", Model: "remote-model", Input: 10, Status: 200})
	stop()
	select {
	case b := <-received:
		if !strings.Contains(string(b), "remote-model") {
			t.Fatal("missing remote usage")
		}
	case <-time.After(time.Second):
		t.Fatal("remote gateway trace suppressed")
	}
}

func TestSessionTracingSuppressesSupportedLocalClientsOnly(t *testing.T) {
	otelConfig(t, settings.OTel{Endpoint: "http://localhost:4318"})
	t.Setenv("MAGPIE_OTEL_SESSIONS", "true")
	e := newOTelExporter()
	previous := otel.Swap(e)
	defer func() { otel.Store(previous); e.cancel() }()
	cfg, err := settings.OTelExport()
	if err != nil {
		t.Fatal(err)
	}
	ready := &sessionAvailability{config: cfg, agents: map[string]time.Time{}}
	for _, agent := range []string{"codex", "pi", "omp", "opencode", "gemini", "claude", "claude-desktop"} {
		if OTelSessionAgent(agent) {
			t.Fatal("unreadable store suppressed gateway")
		}
		offerOTel(Record{Agent: agent, Local: true, Time: time.Now(), Status: 200})
		if len(e.queue) != 1 {
			t.Fatal("fallback gateway record lost")
		}
		<-e.queue
		ready.agents[agent] = time.Now()
	}
	e.sessions.Store(ready)
	for agent := range ready.agents {
		if !OTelSessionAgent(agent) {
			t.Fatalf("adapter not enabled: %s", agent)
		}
		offerOTel(Record{Agent: agent, Local: true, Time: time.Now(), Status: 200})
	}
	if len(e.queue) != 0 {
		t.Fatal("supported clients duplicated gateway records")
	}
	offerOTel(Record{Agent: "cursor", Time: time.Now(), Status: 200})
	offerOTel(Record{Agent: "opencode", Via: "remote", Time: time.Now(), Status: 200})
	offerOTel(Record{Agent: "claude", OTel: &OTelSpan{Session: true}, Time: time.Now(), Status: 200})
	if len(e.queue) != 3 {
		t.Fatal("fallback, remote or session record suppressed")
	}
	// Empty Via does not prove locality: LAN/container/WSL clients may call
	// this gateway directly while its own same-agent session reader is active.
	offerOTel(Record{Agent: "codex", Time: time.Now(), Status: 200})
	offerOTel(Record{Agent: "pi", Local: true, CallerKeyID: "gateway-key", Time: time.Now(), Status: 200})
	if len(e.queue) != 5 {
		t.Fatal("remote or keyed gateway trace suppressed")
	}
	// Gateway spans already started before availability changed must finish.
	offerOTel(Record{Agent: "codex", OTel: &OTelSpan{TraceID: "existing"}, Time: time.Now(), Status: 200})
	if len(e.queue) != 6 {
		t.Fatal("in-flight gateway span suppressed")
	}
	stale := &sessionAvailability{config: cfg, agents: map[string]time.Time{"codex": time.Now().Add(-6 * time.Minute)}}
	e.sessions.Store(stale)
	if OTelSessionAgent("codex") {
		t.Fatal("stale availability suppressed gateway")
	}
	cfg.Bodies = !cfg.Bodies
	e.sessions.Store(&sessionAvailability{config: cfg, agents: map[string]time.Time{"codex": time.Now()}})
	if OTelSessionAgent("codex") {
		t.Fatal("availability survived changed config")
	}
}

func TestSessionObservationUpdatesDoNotDuplicateMetrics(t *testing.T) {
	var metric string
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.HasSuffix(r.URL.Path, "/metrics") {
			metric = string(b)
		}
		io.WriteString(w, `{}`)
	}))
	defer collector.Close()
	otelConfig(t, settings.OTel{Endpoint: collector.URL, Metrics: true})
	cfg, err := settings.OTelExport()
	if err != nil {
		t.Fatal(err)
	}
	e := newOTelExporter()
	defer e.cancel()
	now := time.Now()
	span := sessions.TraceSpan{Agent: "gemini", Session: "s", Turn: "t", ID: "1111111111111111", Parent: "2222222222222222", Kind: "generation", Model: "m", Start: now, End: now.Add(time.Second), Tokens: sessions.Tokens{Input: 10, Output: 2}}
	first := sessionRecord(span, cfg)
	span.Update = true
	second := sessionRecord(span, cfg)
	e.flush([]otelItem{{record: first, config: cfg}, {record: second, config: cfg}})
	if metric == "" || strings.Contains(metric, `"count":"2"`) {
		t.Fatalf("update metrics: %s", metric)
	}
}

func TestSessionTracingMatchesNativeConversation(t *testing.T) {
	otelConfig(t, settings.OTel{Endpoint: "http://localhost:4318"})
	t.Setenv("MAGPIE_OTEL_SESSIONS", "true")
	e := newOTelExporter()
	previous := otel.Swap(e)
	defer func() { otel.Store(previous); e.cancel() }()
	cfg, err := settings.OTelExport()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	e.sessions.Store(&sessionAvailability{config: cfg,
		agents: map[string]time.Time{"codex": now},
		seen: map[observedSession]time.Time{
			{"codex", "local"}:    now,
			{"codex", "stale"}:    now.Add(-6 * time.Minute),
			{"pi", "other-agent"}: now,
		},
	})
	for _, tc := range []struct {
		name, session, native string
		dedup                 bool
	}{
		{"visible", "local", "", true},
		{"WSL mirrored", "wsl", "", false},
		{"Docker Desktop", "docker", "", false},
		{"native takes precedence", "routing", "local", true},
		{"unseen native", "local", "wsl", false},
		{"stale session despite recent agent", "stale", "", false},
		{"same ID different agent", "other-agent", "", false},
		{"no ID fallback", "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			offerOTel(Record{Agent: "codex", Local: true, Session: tc.session, NativeSession: tc.native, Time: now, Status: 200})
			want := 1
			if tc.dedup {
				want = 0
			}
			if len(e.queue) != want {
				t.Fatalf("queued=%d want=%d", len(e.queue), want)
			}
			if want != 0 {
				<-e.queue
			}
		})
	}
	cfg.Bodies = !cfg.Bodies
	e.sessions.Store(&sessionAvailability{config: cfg, seen: map[observedSession]time.Time{{"codex", "local"}: now}})
	if OTelSession("codex", "local") {
		t.Fatal("session readiness survived changed config")
	}
}

func TestSessionTracingRetainsUnrepresentedCalls(t *testing.T) {
	otelConfig(t, settings.OTel{Endpoint: "http://localhost:4318"})
	t.Setenv("MAGPIE_OTEL_SESSIONS", "true")
	e := newOTelExporter()
	previous := otel.Swap(e)
	defer func() { otel.Store(previous); e.cancel() }()
	cfg, err := settings.OTelExport()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	e.sessions.Store(&sessionAvailability{config: cfg, agents: map[string]time.Time{"claude": now, "claude-desktop": now, "codex": now}, seen: map[observedSession]time.Time{{"claude", "parent"}: now, {"claude-desktop", "parent"}: now, {"codex", "parent"}: now}})
	for _, tc := range []struct{ agent, kind string }{
		{"claude", "title_generation"},
		{"claude-desktop", "title_generation"}, {"codex", "thread_title"}, {"codex", "review"},
	} {
		offerOTel(Record{Agent: tc.agent, Kind: tc.kind, Session: "parent", Local: true, Time: now, Status: 200})
		if len(e.queue) != 1 {
			t.Fatalf("%s/%s unrepresented request lost", tc.agent, tc.kind)
		}
		<-e.queue
	}
}
