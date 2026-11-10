package settings

import (
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/lazyre"
)

// CleanOrigins is the web origins a user typed for CORSOrigins, as a
// browser's Origin header spells them: scheme://host[:port], lower-cased,
// with no path and the scheme's default port left out. http and https are
// origins, and so is a browser extension's page by its id (#1051,
// SkyAerope: an extension without host permissions is held to CORS like a
// page), as extensionIDs spells them; "*" is refused, since it would let
// every web page or extension the user has spend their subscriptions.
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
	if o, ok, err := extensionOrigin(raw); ok {
		return o, err
	}
	u, err := url.Parse(strings.TrimSuffix(raw, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("%q is not an origin: write it as http://localhost:3000 or https://app.example.com, or an extension's as chrome-extension://<its ID>", raw)
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

// extensionIDs is the host each browser's extension origin has, as it
// sends it in Origin for a fetch from the extension's own pages, popup or
// service worker (a content script's fetch carries the page's origin):
// Chromium's chrome-extension://<id>, the id 32 letters a–p; Firefox's
// moz-extension://<uuid>, its per-install Internal UUID; Safari's
// safari-web-extension://<uuid>, which Safari picks anew on each launch.
var extensionIDs = map[string]*lazyre.RE{
	"chrome-extension":     lazyre.New(`^[a-p]{32}$`),
	"moz-extension":        lazyre.New(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`),
	"safari-web-extension": lazyre.New(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`),
}

// extensionOrigin is raw as an extension's origin, lower-cased, with a
// trailing "/" let go; ok says raw names an extension scheme at all, and
// then err says when its id isn't one that browser gives.
func extensionOrigin(raw string) (o string, ok bool, err error) {
	scheme, rest, found := strings.Cut(strings.ToLower(raw), "://")
	id := extensionIDs[scheme]
	if !found || id == nil {
		return "", false, nil
	}
	rest = strings.TrimSuffix(rest, "/")
	if !id.MatchString(rest) {
		return "", true, fmt.Errorf("%q is not an extension's origin: write it as %s://<its ID>, the ID as the browser's extensions page shows it", raw, scheme)
	}
	return scheme + "://" + rest, true, nil
}
