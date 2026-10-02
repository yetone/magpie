package usage

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

func otelConfig(t *testing.T, config settings.OTel) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("MAGPIE_OTEL_ENABLED", "true")
	t.Setenv("MAGPIE_OTEL_ENDPOINT", config.Endpoint)
	t.Setenv("MAGPIE_OTEL_HEADERS", "Authorization=Bearer%20collector-test")
	t.Setenv("MAGPIE_OTEL_METRICS", "false")
	if config.Metrics {
		t.Setenv("MAGPIE_OTEL_METRICS", "true")
	}
}

func TestOTelBatchWireAndPrivacy(t *testing.T) {
	type received struct {
		path, auth, ct string
		body           []byte
	}
	requests := make(chan received, 4)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		requests <- received{r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), b}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{}`)
	}))
	defer collector.Close()
	otelConfig(t, settings.OTel{Endpoint: collector.URL, Metrics: true})
	stop := StartOTel()
	t.Cleanup(stop)
	r := Record{RouteID: 42, Time: time.Now(), Agent: "pi", Provider: "deepseek", Model: "deepseek-chat", Requested: "group/mine", Served: "deepseek-v4", Input: 10, CacheRead: 3, CacheWrite: 2, Output: 4, Reasoning: 1, Millis: 1200, TTFT: 200, Status: 200,
		ProviderKeyName: "PRIVATE-ACCOUNT", ProviderKeyID: "PRIVATE-KEY", Session: "PRIVATE-SESSION", Via: "PRIVATE-HOST"}
	Append(r)
	r.Status = 502
	Append(r)
	stop() // drain without waiting for the periodic batch
	receive := func() received {
		select {
		case r := <-requests:
			return r
		case <-time.After(time.Second):
			t.Fatal("collector received no export")
			return received{}
		}
	}
	first, second := receive(), receive()
	if first.path != "/v1/traces" || second.path != "/v1/metrics" || first.auth != "Bearer collector-test" || first.ct != "application/json" {
		t.Fatalf("requests: %+v %+v", first, second)
	}
	for _, request := range []received{first, second} {
		if strings.Contains(string(request.body), "PRIVATE-") || strings.Contains(string(request.body), "collector-test") {
			t.Fatalf("private data exported: %s", request.body)
		}
	}
	var traces struct {
		Resources []struct {
			Scopes []struct {
				Spans []struct {
					TraceID    string `json:"traceId"`
					SpanID     string `json:"spanId"`
					Kind       int    `json:"kind"`
					Start      string `json:"startTimeUnixNano"`
					End        string `json:"endTimeUnixNano"`
					Attributes []struct {
						Key   string            `json:"key"`
						Value map[string]string `json:"value"`
					} `json:"attributes"`
					Status struct {
						Code int `json:"code"`
					} `json:"status"`
				} `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
	}
	if err := json.Unmarshal(first.body, &traces); err != nil {
		t.Fatal(err)
	}
	spans := traces.Resources[0].Scopes[0].Spans
	if len(spans) != 2 || len(spans[0].TraceID) != 32 || spans[0].TraceID != spans[1].TraceID || len(spans[0].SpanID) != 16 || spans[0].SpanID == spans[1].SpanID || spans[0].Kind != 3 || spans[1].Status.Code != 2 {
		t.Fatalf("spans: %+v", spans)
	}
	values := map[string]string{}
	for _, a := range spans[0].Attributes {
		values[a.Key] = a.Value["stringValue"] + a.Value["intValue"]
	}
	if values["gen_ai.usage.input_tokens"] != "15" || values["gen_ai.usage.output_tokens"] != "4" || values["magpie.route.id"] != "42" || values["magpie.ttft_ms"] != "200" {
		t.Fatalf("attributes: %v", values)
	}
	var metrics struct {
		Resources []struct {
			Scopes []struct {
				Metrics []struct {
					Name      string `json:"name"`
					Unit      string `json:"unit"`
					Histogram struct {
						Temporality int `json:"aggregationTemporality"`
						Points      []struct {
							Count   string    `json:"count"`
							Sum     float64   `json:"sum"`
							Buckets []string  `json:"bucketCounts"`
							Bounds  []float64 `json:"explicitBounds"`
						} `json:"dataPoints"`
					} `json:"histogram"`
				} `json:"metrics"`
			} `json:"scopeMetrics"`
		} `json:"resourceMetrics"`
	}
	if err := json.Unmarshal(second.body, &metrics); err != nil {
		t.Fatal(err)
	}
	ms := metrics.Resources[0].Scopes[0].Metrics
	if len(ms) != 2 || ms[0].Name != "gen_ai.client.operation.duration" || ms[0].Unit != "s" || ms[0].Histogram.Temporality != 1 || ms[1].Name != "gen_ai.client.token.usage" {
		t.Fatalf("metrics: %+v", ms)
	}
	for _, p := range ms[0].Histogram.Points {
		if p.Count != "1" || p.Sum != 1.2 || len(p.Buckets) != len(p.Bounds)+1 {
			t.Fatalf("point: %+v", p)
		}
	}
}

func TestOTelQueueDoesNotBlock(t *testing.T) {
	e := newOTelExporter()
	defer e.cancel()
	for range otelQueueSize {
		e.offer(otelItem{})
	}
	done := make(chan struct{})
	go func() { e.offer(otelItem{}); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("full queue blocked producer")
	}
	if e.dropped.Load() != 1 {
		t.Fatalf("dropped=%d", e.dropped.Load())
	}
}

func TestOTelDisabledAndChangedDestination(t *testing.T) {
	var calls atomic.Int32
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); io.WriteString(w, `{}`) }))
	defer collector.Close()
	otelConfig(t, settings.OTel{Endpoint: collector.URL})
	e := newOTelExporter()
	defer e.cancel()
	cfg, _ := settings.OTelExport()
	batch := []otelItem{{record: Record{Time: time.Now(), Status: 200}, config: cfg}}
	t.Setenv("MAGPIE_OTEL_ENABLED", "false")
	e.flush(batch)
	t.Setenv("MAGPIE_OTEL_ENABLED", "true")
	t.Setenv("MAGPIE_OTEL_ENDPOINT", collector.URL+"/other")
	e.flush(batch)
	if calls.Load() != 0 {
		t.Fatal("sent a disabled batch or sent it to a new destination")
	}
}

func TestOTelRetriesOnlyRetryableResponses(t *testing.T) {
	for _, status := range []int{503, 401} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					w.WriteHeader(status)
				} else {
					io.WriteString(w, `{}`)
				}
			}))
			defer collector.Close()
			otelConfig(t, settings.OTel{Endpoint: collector.URL})
			e := newOTelExporter()
			defer e.cancel()
			cfg, _ := settings.OTelExport()
			e.flush([]otelItem{{record: Record{Time: time.Now(), Status: 200}, config: cfg}})
			want := int32(1)
			if status == 503 {
				want = 2
			}
			if calls.Load() != want {
				t.Fatalf("attempts=%d want %d", calls.Load(), want)
			}
		})
	}
}

func TestOTelUsesConfiguredProxy(t *testing.T) {
	var calls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.String() != "http://collector.invalid/api/public/otel/v1/traces" || r.Header.Get("Authorization") != "Bearer collector-test" {
			t.Errorf("proxy request: %s %s", r.URL, r.Header.Get("Authorization"))
		}
		io.WriteString(w, `{}`)
	}))
	defer proxy.Close()
	otelConfig(t, settings.OTel{Endpoint: "http://collector.invalid/api/public/otel"})
	if err := settings.Save(settings.Settings{Proxy: proxy.URL}); err != nil {
		t.Fatal(err)
	}
	e := newOTelExporter()
	defer e.cancel()
	defer e.client.CloseIdleConnections()
	cfg, _ := settings.OTelExport()
	e.flush([]otelItem{{record: Record{Time: time.Now(), Status: 200}, config: cfg}})
	if calls.Load() != 1 {
		t.Fatalf("proxy calls=%d", calls.Load())
	}
}

func TestOTelDoesNotFollowRedirects(t *testing.T) {
	var leaked atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer other.Close()
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer collector.Close()
	otelConfig(t, settings.OTel{Endpoint: collector.URL})
	e := newOTelExporter()
	defer e.cancel()
	defer e.client.CloseIdleConnections()
	cfg, _ := settings.OTelExport()
	e.flush([]otelItem{{record: Record{Time: time.Now(), Status: 200}, config: cfg}})
	if leaked.Load() != 0 {
		t.Fatal("collector redirected telemetry and credentials")
	}
}

func TestOTelShutdownCancelsSlowCollector(t *testing.T) {
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer collector.Close()
	otelConfig(t, settings.OTel{Endpoint: collector.URL})
	stop := StartOTel()
	Append(Record{Time: time.Now(), Status: 200})
	start := time.Now()
	stop()
	if time.Since(start) > 4*time.Second {
		t.Fatal("shutdown did not cancel slow export")
	}
}

type otelTestTransport func(*http.Request) (*http.Response, error)

func (f otelTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestOTelRetryAfterBound(t *testing.T) {
	for _, value := range []string{"86400", "9223372036854775807", "date", "30"} {
		t.Run(value, func(t *testing.T) {
			otelConfig(t, settings.OTel{Endpoint: "https://collector.test"})
			synctest.Test(t, func(t *testing.T) {
				e := newOTelExporter()
				defer e.cancel()
				start := time.Now()
				calls := 0
				want := 60 * time.Second
				if value == "30" {
					want = 30 * time.Second
				}
				e.client.Transport = otelTestTransport(func(r *http.Request) (*http.Response, error) {
					calls++
					status := http.StatusServiceUnavailable
					h := http.Header{}
					retryAfter := value
					if value == "date" {
						retryAfter = start.Add(24 * time.Hour).UTC().Format(http.TimeFormat)
					}
					h.Set("Retry-After", retryAfter)
					if calls == 3 {
						status = http.StatusOK
					}
					if elapsed := time.Since(start); elapsed != time.Duration(calls-1)*want {
						t.Errorf("attempt %d after %v, want %v", calls, elapsed, time.Duration(calls-1)*want)
					}
					return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader("{}"))}, nil
				})
				cfg, err := settings.OTelExport()
				if err != nil {
					t.Fatal(err)
				}
				e.send(cfg, "traces", e.traces([]Record{{Time: start, Status: 200}}))
				if calls != 3 {
					t.Fatalf("attempts=%d, want 3", calls)
				}
			})
		})
	}
}

// a call's bodies go only to the OTLP export (#538), never to usage.jsonl
func TestBodiesStayOutOfTheLog(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	Append(Record{Time: time.Now(), Model: "m1", Status: 200, BodyIn: "PRIVATE-PROMPT", BodyOut: "PRIVATE-REPLY"})
	b, err := os.ReadFile(Path())
	if err != nil || !strings.Contains(string(b), `"model":"m1"`) || strings.Contains(string(b), "PRIVATE-") {
		t.Fatalf("usage.jsonl: %s %v", b, err)
	}
}

func TestOTelReplyText(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{`{"choices":[{"message":{"content":"hi"}}]}`, `{"choices":[{"message":{"content":"hi"}}]}`},
		{"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"he\"}}\n\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"llo\"}}\n\n", "hello"},
		{"data: {\"type\":\"response.output_text.delta\",\"delta\":\"he\"}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"llo\"}\n\n" + BodyCut, "hello" + BodyCut},
		{"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"id\":\"t\"}]}}]}\n\n", "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"id\":\"t\"}]}}]}\n\n"},
	} {
		if got := otelReplyText(c.body); got != c.want {
			t.Errorf("otelReplyText(%q) = %q, want %q", c.body, got, c.want)
		}
	}
}

// whole bodies (#538) are held by the queue within a byte budget, not by
// record count alone: one past it is dropped as a full queue's is
func TestOTelQueueBodiesBudget(t *testing.T) {
	e := newOTelExporter()
	defer e.cancel()
	body := strings.Repeat("x", otelQueueBytes/2)
	e.offer(otelItem{record: Record{BodyIn: body}})
	e.offer(otelItem{record: Record{BodyIn: body}})
	if got := e.bytes.Load(); got != int64(otelQueueBytes) {
		t.Fatalf("held %d bytes, want %d", got, otelQueueBytes)
	}
	e.offer(otelItem{record: Record{BodyIn: "x"}})
	if e.dropped.Load() != 1 {
		t.Fatalf("dropped=%d, want 1", e.dropped.Load())
	}
}

// Producers racing for the last bytes must reserve them atomically.
func TestOTelQueueBodiesBudgetConcurrent(t *testing.T) {
	body := strings.Repeat("x", 1<<10)
	for range 100 {
		e := newOTelExporter()
		e.bytes.Store(otelQueueBytes - int64(len(body)))
		start := make(chan struct{})
		var wg sync.WaitGroup
		for range 32 {
			wg.Go(func() {
				<-start
				e.offer(otelItem{record: Record{BodyIn: body}})
			})
		}
		close(start)
		wg.Wait()
		e.cancel()
		if got := e.bytes.Load(); got != otelQueueBytes {
			t.Fatalf("held %d bytes, want %d", got, otelQueueBytes)
		}
		if len(e.queue) != 1 || e.dropped.Load() != 31 {
			t.Fatalf("queued=%d dropped=%d, want 1 and 31", len(e.queue), e.dropped.Load())
		}
	}
}
