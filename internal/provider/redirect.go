package provider

import (
	"errors"
	"net"
	"net/http"
	"strings"
)

// A request that carries a vendor's credential is followed through a
// redirect only to its own origin (the scheme, host and port it was sent
// to), or from http to https on the same host. Go follows any redirect to
// any host, https to http included, and on a hop to another host drops
// only Authorization, Cookie and WWW-Authenticate: x-api-key,
// x-goog-api-key, api-key and a provider's own headers went to wherever
// the vendor's Location named. A redirect elsewhere is not followed, and
// its 3xx is the answer, as it came, so the caller sees where the vendor
// sent it and no credential goes there. No vendor magpie speaks to was
// found to redirect its API to another origin (a download that does is
// fetched by its own URL, unsigned), and the search APIs had this rule
// already (search_api.go, #1580).

// KeepsCredentials is the CheckRedirect of a client that sends a vendor's
// credential: same-origin redirects are followed, at most 10 as Go's
// default, and any other is given back as the answer
// (http.ErrUseLastResponse).
func KeepsCredentials(next *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if !SameOrigin(via[0].URL.Scheme, via[0].URL.Host, next.URL.Scheme, next.URL.Host) {
		return http.ErrUseLastResponse
	}
	return nil
}

// SameOrigin says whether a redirect from scheme://host to
// toScheme://toHost may carry a credential: the same scheme, host and
// port (a port left out is the scheme's own), or http to https on the
// same host and default ports.
func SameOrigin(scheme, host, toScheme, toHost string) bool {
	scheme, toScheme = strings.ToLower(scheme), strings.ToLower(toScheme)
	h, port := splitOrigin(scheme, host)
	th, tport := splitOrigin(toScheme, toHost)
	if h == "" || h != th {
		return false
	}
	if scheme == toScheme {
		return port == tport
	}
	// an upgrade: the key went over http already, to this same host
	return scheme == "http" && toScheme == "https" && port == "80" && tport == "443"
}

func splitOrigin(scheme, host string) (string, string) {
	h, port, err := net.SplitHostPort(host)
	if err != nil {
		h, port = host, ""
	}
	if port == "" {
		switch scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
	}
	return strings.ToLower(strings.Trim(h, "[]")), port
}

// Keyed is c with KeepsCredentials after c's own CheckRedirect, for a
// request that carries a vendor's credential. c is left as it is.
func Keyed(c *http.Client) *http.Client {
	if c == nil {
		c = http.DefaultClient
	}
	k := *c
	own := c.CheckRedirect
	k.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if own != nil {
			if err := own(next, via); err != nil {
				return err
			}
		}
		return KeepsCredentials(next, via)
	}
	return &k
}
