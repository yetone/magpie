// Package otel is Settings' Observability: each request's traces and
// metrics sent to an OpenTelemetry collector, such as Langfuse. While it
// sends, it stays on.
//
// It is the first consumer of the gateway's observer point (P3 of
// docs/subsystems/plugin-runtime.md): Apply registers internal/otlp's
// exporter as a usage.Observer, and off, the gateway exports nothing and
// keeps no reply whole for it.
package otel

import (
	"github.com/yetone/magpie/internal/otlp"
	"github.com/yetone/magpie/internal/pluginrt"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

func init() {
	pluginrt.Register(pluginrt.Manifest{
		ID: "otel", API: "^1", Requires: []string{"window", "settings", "gateway"},
		Name: "Observability", Sub: "Send each request's traces and metrics to an OpenTelemetry collector, such as Langfuse",
		One: "Sending to a collector", Many: "Sending to a collector",
		// on while settings.json or MAGPIE_OTEL_ENABLED says to send: off,
		// nothing would
		InUse: func() (int, error) {
			if _, err := settings.LoadChecked(); err != nil {
				return 0, err
			}
			config, err := settings.OTelExport()
			if err != nil {
				return 0, err
			}
			if config.Enabled {
				return 1, nil
			}
			return 0, nil
		},
		SetAt:  "otel #otelList",
		Parts:  []pluginrt.Part{{Slot: "settings.section", Key: "otel"}},
		Client: []string{"i18n.js", "page.js"},
		Apply: func(c *pluginrt.Ctx) error {
			c.OnDispose(usage.Observe(otlp.New()))
			return nil
		},
	})
}
