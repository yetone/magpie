package settings

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// normalBark is Bark's server, device key and daily time as they are kept:
// trimmed, the server without a closing slash, the time as 09:00.
func normalBark(server, key, daily string) (string, string, string) {
	server, key, daily = strings.TrimRight(strings.TrimSpace(server), "/"), strings.TrimSpace(key), strings.TrimSpace(daily)
	if h, m, ok := Clock(daily); ok {
		daily = fmt.Sprintf("%02d:%02d", h, m)
	}
	return server, key, daily
}

// CheckBark says what is wrong with a Bark server ("" is the app's own,
// https://api.day.app), device key and daily time ("" is off), or nil. The
// key is never named in the error.
func CheckBark(server, key, daily string) error {
	server, key, daily = normalBark(server, key, daily)
	if server != "" {
		u, err := url.Parse(server)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(server, " \t\r\n") {
			return errors.New("a Bark server is an http:// or https:// address, like https://api.day.app or https://bark.example.com")
		}
	}
	if strings.ContainsFunc(key, func(c rune) bool { return c <= ' ' || c == 0x7f || c == '/' || c == '?' || c == '#' || c == '%' }) {
		return errors.New("a Bark device key is the one word the app shows, with no spaces or slashes (not the whole push address)")
	}
	if daily != "" {
		if _, _, ok := Clock(daily); !ok {
			return fmt.Errorf("the daily summary's time of day must look like 21:00, not %q", daily)
		}
	}
	return nil
}
