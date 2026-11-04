package otlp

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

// otelResource is what both signals' resources were last built from, so an
// export doesn't parse the variables again and a bad value is logged once,
// not on every flush.
var otelResource struct {
	sync.Mutex
	attrs, name string
	built       bool
	out         []otelAttribute
}

// otelResourceAttributes is the resource every export carries:
// service.name=magpie, with the standard OTEL_RESOURCE_ATTRIBUTES merged
// over it and OTEL_SERVICE_NAME over that (#5167). Only what the user set
// is added; nothing about the machine is looked up.
//
// magpie's "magpie" is the fallback the spec gives the SDK
// (unknown_service), not identity the user supplied, so a service.name the
// user set wins. An empty service.name counts as unset, as an empty
// OTEL_SERVICE_NAME does.
func otelResourceAttributes() []otelAttribute {
	attrs, name := os.Getenv("OTEL_RESOURCE_ATTRIBUTES"), strings.TrimSpace(os.Getenv("OTEL_SERVICE_NAME"))
	otelResource.Lock()
	defer otelResource.Unlock()
	if otelResource.built && otelResource.attrs == attrs && otelResource.name == name {
		return otelResource.out
	}
	values, err := parseResourceAttributes(attrs)
	if err != nil {
		// The spec discards the whole value on any error. The message
		// never quotes it: the attributes may name private hosts.
		log.Printf("otel: OTEL_RESOURCE_ATTRIBUTES ignored: %s", err)
		values = map[string]string{}
	}
	if values["service.name"] == "" {
		values["service.name"] = "magpie"
	}
	if name != "" {
		values["service.name"] = name
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]otelAttribute, 0, len(keys))
	for _, key := range keys {
		out = append(out, otelString(key, values[key]))
	}
	otelResource.attrs, otelResource.name, otelResource.built, otelResource.out = attrs, name, true, out
	return out
}

// parseResourceAttributes reads key1=value1,key2=value2: keys and values
// trimmed and percent-decoded, every value a string, the last of a
// repeated key kept. An empty or blank value is no attributes. An error
// names the entry by position only.
func parseResourceAttributes(raw string) (map[string]string, error) {
	values := map[string]string{}
	if strings.TrimSpace(raw) == "" {
		return values, nil
	}
	for i, entry := range strings.Split(raw, ",") {
		key, value, found := strings.Cut(entry, "=")
		if !found {
			return nil, fmt.Errorf("entry %d has no '='", i+1)
		}
		key, err := decodeResourcePart(key)
		if err != nil {
			return nil, fmt.Errorf("entry %d's key %s", i+1, err)
		}
		if key == "" {
			return nil, fmt.Errorf("entry %d has an empty key", i+1)
		}
		if value, err = decodeResourcePart(value); err != nil {
			return nil, fmt.Errorf("entry %d's value %s", i+1, err)
		}
		values[key] = value
	}
	return values, nil
}

// decodeResourcePart percent-decodes one key or value. PathUnescape leaves
// '+' as it is: this is not form encoding.
func decodeResourcePart(s string) (string, error) {
	s, err := url.PathUnescape(strings.TrimSpace(s))
	if err != nil {
		return "", fmt.Errorf("has a bad percent escape")
	}
	if !utf8.ValidString(s) {
		return "", fmt.Errorf("is not UTF-8 once decoded")
	}
	return s, nil
}
