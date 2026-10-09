package provider

import (
	"net/url"
	"strconv"
	"strings"
)

// A local server's preset (KindLocal: Ollama, LM Studio, oMLX,
// MLX-Serve) has the default port on this machine. The server can listen on another port, or
// run on another computer, so its address is the user's to give: only the
// origin of each URL changes, each API keeping its own path (/v1 for chat,
// the root for Anthropic).

// AtAddress is pr reached at addr: the scheme, host and port of each of its
// URLs replaced by addr's. An empty addr leaves pr as it is.
func AtAddress(pr PresetDef, addr string) (PresetDef, error) {
	err := atAddress(addr, &pr.Chat, &pr.Responses, &pr.Anthropic)
	return pr, err
}

// AtAddress moves the provider's URLs to addr as the package's AtAddress
// does; a refused address leaves them as they were.
func (p *Provider) AtAddress(addr string) error {
	return atAddress(addr, &p.Chat, &p.Responses, &p.Anthropic)
}

// AddressOf is the address the URLs are at, the origin of the first one
// set, for an editor to start from; "" when none is.
func AddressOf(urls ...string) string {
	for _, raw := range urls {
		if u, err := url.Parse(raw); err == nil && u.Scheme != "" && u.Host != "" {
			return u.Scheme + "://" + u.Host
		}
	}
	return ""
}

func atAddress(addr string, urls ...*string) error {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil
	}
	origin, err := parseAddress(addr)
	if err != nil {
		return err
	}
	moved := make([]string, len(urls))
	for i, at := range urls {
		if *at == "" {
			continue
		}
		u, err := url.Parse(*at)
		if err != nil {
			return errorf("%q can't be moved to %s: %v", *at, addr, err)
		}
		u.Scheme, u.Host, u.User = origin.Scheme, origin.Host, nil
		moved[i] = u.String()
	}
	for i, at := range urls {
		*at = moved[i]
	}
	return nil
}

// parseAddress takes what a user types for a server's address: a scheme is
// optional (http), and a path pasted with it (…/v1) is dropped.
func parseAddress(addr string) (*url.URL, error) {
	bad := errorf("%q is not an address like http://localhost:11434", addr)
	if strings.ContainsAny(addr, " \t\r\n") {
		return nil, bad
	}
	s := addr
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, bad
	}
	if port := u.Port(); port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return nil, bad
		}
	}
	return &url.URL{Scheme: u.Scheme, Host: u.Host}, nil
}
