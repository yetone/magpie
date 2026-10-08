package settings

import (
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
)

// CleanOrigins is the web origins a user typed for CORSOrigins, as a
// browser's Origin header spells them: scheme://host[:port], lower-cased,
// with no path and the scheme's default port left out. Only http and
// https are origins; "*" is refused, since it would let every web page the
// user opens spend their subscriptions.
func CleanOrigins(in []string) ([]string, error) {
	var out []string
	for _, raw := range in {
		o, err := cleanOrigin(raw)
		if err != nil {
			return nil, err
		}
		if o != "" && !slices.Contains(out, o) {
			out = append(out, o)
		}
	}
	return out, nil
}

func cleanOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if strings.Contains(raw, "*") {
		return "", fmt.Errorf("%q: name each origin; a wildcard would let every web page use magpie", raw)
	}
	u, err := url.Parse(strings.TrimSuffix(raw, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("%q is not an origin: write it as http://localhost:3000 or https://app.example.com", raw)
	}
	host, port := strings.ToLower(u.Hostname()), u.Port()
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host = net.JoinHostPort(strings.Trim(host, "[]"), port)
	}
	return u.Scheme + "://" + host, nil
}
