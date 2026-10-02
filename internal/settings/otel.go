package settings

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// OTel is the optional OTLP/HTTP metadata export. Endpoint is the base URL,
// including /api/public/otel for Langfuse; signals append /v1/traces or metrics.
type OTel struct {
	Enabled  bool              `json:"enabled,omitempty"`
	Endpoint string            `json:"endpoint,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Metrics  bool              `json:"metrics,omitempty"`
	// Bodies adds each call's request and reply, as the gateway captured
	// them (secrets masked, cut at 256 KiB), to its span as Langfuse's
	// observation input and output (#538); off, only metadata leaves
	Bodies bool `json:"bodies,omitempty"`
	// BodiesWhole keeps those bodies entire, not cut at 256 KiB: the reply
	// is written to a temporary file as it streams and read back for the
	// export, so a long prompt or reply reaches the trace whole. Only
	// meaningful with Bodies on
	BodiesWhole bool `json:"bodiesWhole,omitempty"`
}

var otelHeaderName = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

func (o OTel) Check() error {
	if o.Enabled && o.Endpoint == "" {
		return fmt.Errorf("OTLP endpoint is required when export is enabled")
	}
	if o.Endpoint != "" {
		u, err := url.Parse(o.Endpoint)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("OTLP endpoint must be an http or https base URL without credentials, query or fragment")
		}
	}
	for k, v := range o.Headers {
		if !otelHeaderName.MatchString(k) || strings.EqualFold(k, "Content-Type") || strings.EqualFold(k, "Content-Length") || strings.EqualFold(k, "Host") {
			return fmt.Errorf("invalid OTLP header %q", k)
		}
		for _, c := range v {
			if c < 32 && c != '\t' || c == 127 {
				return fmt.Errorf("invalid OTLP header value for %q", k)
			}
		}
	}
	return nil
}

// OTelExport applies explicit MAGPIE_OTEL_* overrides without changing the
// saved preferences. Merely setting an endpoint never enables export.
func OTelExport() (OTel, error) {
	o := Load().OTel
	for _, x := range []struct {
		name string
		dst  *bool
	}{{"MAGPIE_OTEL_ENABLED", &o.Enabled}, {"MAGPIE_OTEL_METRICS", &o.Metrics}, {"MAGPIE_OTEL_BODIES", &o.Bodies}, {"MAGPIE_OTEL_BODIES_WHOLE", &o.BodiesWhole}} {
		if v, ok := os.LookupEnv(x.name); ok {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return OTel{}, fmt.Errorf("%s must be true or false", x.name)
			}
			*x.dst = b
		}
	}
	if v, ok := os.LookupEnv("MAGPIE_OTEL_ENDPOINT"); ok {
		o.Endpoint = strings.TrimRight(strings.TrimSpace(v), "/")
	}
	if v, ok := os.LookupEnv("MAGPIE_OTEL_HEADERS"); ok {
		o.Headers = map[string]string{}
		if v != "" {
			for _, h := range strings.Split(v, ",") {
				k, value, ok := strings.Cut(h, "=")
				value, err := url.PathUnescape(strings.TrimSpace(value))
				if !ok || err != nil {
					return OTel{}, fmt.Errorf("MAGPIE_OTEL_HEADERS must be comma-separated name=value pairs")
				}
				o.Headers[strings.TrimSpace(k)] = value
			}
		}
	}
	return o, o.Check()
}
