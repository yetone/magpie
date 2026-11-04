package otlp

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// exportResources flushes one record and returns the resource attributes
// the collector got on /v1/traces and on /v1/metrics, in that order.
func exportResources(t *testing.T, attrs, name string) [2]map[string]string {
	t.Helper()
	type received struct {
		path string
		body []byte
	}
	requests := make(chan received, 2)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		requests <- received{r.URL.Path, body}
		io.WriteString(w, `{}`)
	}))
	defer collector.Close()
	otelConfig(t, settings.OTel{Endpoint: collector.URL, Metrics: true})
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", attrs)
	t.Setenv("OTEL_SERVICE_NAME", name)
	config, err := settings.OTelExport()
	if err != nil {
		t.Fatal(err)
	}
	e := newOTelExporter()
	defer e.cancel()
	defer e.client.CloseIdleConnections()
	e.flush([]otelItem{{record: Record{Time: time.Now(), Status: 200, Input: 1}, config: config}})
	var out [2]map[string]string
	for i, signal := range []string{"Spans", "Metrics"} {
		var request received
		select {
		case request = <-requests:
		default:
			t.Fatalf("collector received no %s export", signal)
		}
		if want := map[string]string{"Spans": "/v1/traces", "Metrics": "/v1/metrics"}[signal]; request.path != want {
			t.Fatalf("path = %q, want %q", request.path, want)
		}
		var payload map[string][]struct {
			Resource struct {
				Attributes []otelAttribute `json:"attributes"`
			} `json:"resource"`
		}
		if err := json.Unmarshal(request.body, &payload); err != nil {
			t.Fatal(err)
		}
		resources := payload["resource"+signal]
		if len(resources) != 1 {
			t.Fatalf("%s: got %d resources, want 1", signal, len(resources))
		}
		got := map[string]string{}
		for _, attr := range resources[0].Resource.Attributes {
			value, ok := attr.Value["stringValue"].(string)
			if !ok {
				t.Fatalf("%s: attribute %q is not a string: %v", signal, attr.Key, attr.Value)
			}
			if _, dup := got[attr.Key]; dup {
				t.Fatalf("%s: duplicate attribute %q", signal, attr.Key)
			}
			got[attr.Key] = value
		}
		out[i] = got
	}
	return out
}

// A server identity set in OTEL_RESOURCE_ATTRIBUTES reaches the resource
// of both signals, as the collector receives them (#5167).
func TestOTelResourceAttributes(t *testing.T) {
	magpie := map[string]string{"service.name": "magpie"}
	for _, tc := range []struct {
		name, attrs, service string
		want                 map[string]string
	}{
		{"default", "", "", magpie},
		{"whitespace", " \t ", "", magpie},
		{"server", "service.instance.id=magpie-prod-01,host.name=server-a,deployment.environment.name=production", "", map[string]string{
			"service.name": "magpie", "service.instance.id": "magpie-prod-01", "host.name": "server-a", "deployment.environment.name": "production",
		}},
		{"override and duplicate", "service.name=first,service.name=custom,host.name=old,host.name=new", "", map[string]string{"service.name": "custom", "host.name": "new"}},
		{"encoding and empty", " label%2Ckey = a%2Cb%3Dc%20d+e , empty=,host.name=%E6%9C%BA%E5%99%A8", "", map[string]string{
			"service.name": "magpie", "label,key": "a,b=c d+e", "empty": "", "host.name": "机器",
		}},
		{"unencoded UTF-8 kept", "host.name=机器", "", map[string]string{"service.name": "magpie", "host.name": "机器"}},
		{"empty service name is unset", "service.name=,host.name=a", "", map[string]string{"service.name": "magpie", "host.name": "a"}},
		{"OTEL_SERVICE_NAME wins", "service.name=from-attrs,host.name=a", " gw-eu ", map[string]string{"service.name": "gw-eu", "host.name": "a"}},
		{"OTEL_SERVICE_NAME alone", "", "gw-eu", map[string]string{"service.name": "gw-eu"}},
		{"blank OTEL_SERVICE_NAME is unset", "service.name=custom", "  ", map[string]string{"service.name": "custom"}},
		{"OTEL_SERVICE_NAME over a discarded variable", "host.name=a,broken", "gw-eu", map[string]string{"service.name": "gw-eu"}},
		{"missing equals", "host.name=server-a,broken", "", magpie},
		{"trailing comma", "host.name=server-a,", "", magpie},
		{"empty key", "host.name=server-a, =value", "", magpie},
		{"bad key escape", "host.name=server-a,bad%ZZ=value", "", magpie},
		{"bad value escape", "host.name=server-a,label=bad%ZZ", "", magpie},
		{"not UTF-8 once decoded", "host.name=server-a,label=%FF", "", magpie},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := exportResources(t, tc.attrs, tc.service)
			for i, signal := range []string{"traces", "metrics"} {
				if !reflect.DeepEqual(got[i], tc.want) {
					t.Errorf("%s resource attributes = %v, want %v", signal, got[i], tc.want)
				}
			}
		})
	}
}

// A discarded variable is reported once, by position, never by its
// contents, however many exports read it.
func TestOTelResourceAttributesLoggedOnceWithoutContents(t *testing.T) {
	var buf bytes.Buffer
	prev, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() { log.SetOutput(prev); log.SetFlags(flags) }()
	attrs := "host.name=secret-host-7731,label=bad%ZZ"
	exportResources(t, attrs, "")
	exportResources(t, attrs, "")
	out := buf.String()
	if n := strings.Count(out, "OTEL_RESOURCE_ATTRIBUTES ignored"); n != 1 {
		t.Fatalf("logged %d times, want once:\n%s", n, out)
	}
	if !strings.Contains(out, "entry 2") {
		t.Errorf("log doesn't name the entry: %q", out)
	}
	for _, secret := range []string{"secret-host-7731", "host.name", "bad%ZZ"} {
		if strings.Contains(out, secret) {
			t.Errorf("log quotes %q: %q", secret, out)
		}
	}
}
