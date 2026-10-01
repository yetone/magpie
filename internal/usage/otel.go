package usage

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/settings"
)

const otelQueueSize = 128
const otelBatchSize = 32

var otel atomic.Pointer[otelExporter]

type otelItem struct {
	record Record
	config settings.OTel
}

type otelExporter struct {
	queue      chan otelItem
	stop, done chan struct{}
	ctx        context.Context
	cancel     context.CancelFunc
	client     *http.Client
	salt       [8]byte
	last       time.Time
	dropped    atomic.Uint64
}

// StartOTel runs only in the process serving the gateway. Stopping drains
// accepted metadata for at most three seconds, after in-flight calls finish.
func StartOTel() func() {
	e := newOTelExporter()
	otel.Store(e)
	if _, err := settings.OTelExport(); err != nil {
		log.Printf("otel: %s", err)
	}
	go e.run()
	var once sync.Once
	return func() {
		once.Do(func() {
			otel.CompareAndSwap(e, nil)
			close(e.stop)
			timer := time.NewTimer(3 * time.Second)
			defer timer.Stop()
			select {
			case <-e.done:
			case <-timer.C:
				e.cancel()
				<-e.done
			}
		})
	}
}

func newOTelExporter() *otelExporter {
	ctx, cancel := context.WithCancel(context.Background())
	e := &otelExporter{queue: make(chan otelItem, otelQueueSize), stop: make(chan struct{}), done: make(chan struct{}), ctx: ctx, cancel: cancel,
		last: time.Now(), client: &http.Client{Timeout: 3 * time.Second,
			Transport:     &http.Transport{Proxy: netproxy.Func, MaxIdleConnsPerHost: 2, IdleConnTimeout: 90 * time.Second},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	rand.Read(e.salt[:])
	return e
}

func offerOTel(r Record) {
	e := otel.Load()
	if e == nil {
		return
	}
	config, err := settings.OTelExport()
	if err != nil || !config.Enabled {
		return
	}
	e.offer(otelItem{record: r, config: config})
}

func (e *otelExporter) offer(item otelItem) {
	select {
	case e.queue <- item:
	default:
		e.dropped.Add(1) // telemetry must never wait for a slow collector
	}
}

func (e *otelExporter) run() {
	defer close(e.done)
	defer e.cancel()
	defer e.client.CloseIdleConnections()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	batch := make([]otelItem, 0, otelBatchSize)
	flush := func() { e.flush(batch); batch = batch[:0] }
	for {
		select {
		case it := <-e.queue:
			batch = append(batch, it)
			if len(batch) == otelBatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-e.stop:
			for {
				select {
				case it := <-e.queue:
					batch = append(batch, it)
					if len(batch) == otelBatchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		}
	}
}

func (e *otelExporter) flush(batch []otelItem) {
	if len(batch) == 0 {
		return
	}
	config, err := settings.OTelExport()
	if err != nil || !config.Enabled {
		return
	}
	var records []Record
	for _, it := range batch {
		// Never send queued records to a new destination or with changed credentials.
		if reflect.DeepEqual(it.config, config) {
			records = append(records, it.record)
		}
	}
	if len(records) == 0 {
		return
	}
	now := time.Now()
	e.send(config, "traces", e.traces(records))
	if config.Metrics {
		e.send(config, "metrics", otelMetrics(records, e.last, now))
	}
	e.last = now
}

func (e *otelExporter) send(config settings.OTel, signal string, payload any) {
	b, _ := json.Marshal(payload)
	base := strings.TrimRight(config.Endpoint, "/")
	base = strings.TrimSuffix(strings.TrimSuffix(base, "/v1/traces"), "/v1/metrics")
	endpoint := base + "/v1/" + signal
	delay := time.Second
	for attempt := 0; attempt < 3; attempt++ {
		current, err := settings.OTelExport()
		if err != nil || !current.Enabled || !reflect.DeepEqual(current, config) {
			return
		}
		req, err := http.NewRequestWithContext(e.ctx, "POST", endpoint, bytes.NewReader(b))
		if err != nil {
			return
		}
		for k, v := range config.Headers {
			req.Header.Set(k, v)
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := e.client.Do(req)
		retry := err != nil
		if res != nil {
			response, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				var result struct {
					PartialSuccess struct {
						RejectedSpans      json.Number `json:"rejectedSpans"`
						RejectedDataPoints json.Number `json:"rejectedDataPoints"`
					} `json:"partialSuccess"`
				}
				json.Unmarshal(response, &result)
				if result.PartialSuccess.RejectedSpans != "" && result.PartialSuccess.RejectedSpans != "0" || result.PartialSuccess.RejectedDataPoints != "" && result.PartialSuccess.RejectedDataPoints != "0" {
					log.Printf("otel: %s export partially rejected", signal)
				}
				return
			}
			retry = res.StatusCode == 429 || res.StatusCode == 502 || res.StatusCode == 503 || res.StatusCode == 504
			if n, err := strconv.Atoi(res.Header.Get("Retry-After")); err == nil && n > 0 {
				delay = time.Duration(min(n, 60)) * time.Second
			} else if at, err := http.ParseTime(res.Header.Get("Retry-After")); err == nil && time.Until(at) > 0 {
				delay = time.Until(at)
			}
		}
		if !retry || attempt == 2 {
			if e.ctx.Err() == nil {
				log.Printf("otel: %s export failed; batch dropped", signal)
			}
			return
		}
		timer := time.NewTimer(min(delay, 60*time.Second))
		select {
		case <-e.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		delay *= 2
	}
}
