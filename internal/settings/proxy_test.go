package settings

import (
	"strings"
	"testing"
)

func TestCheckProxyDoesNotExposeCredentials(t *testing.T) {
	for _, raw := range []string{
		"socks5://private-user:private-password%zz@127.0.0.1:1080",
		"ftp://private-user:private-password@127.0.0.1:1080",
		"socks5://private-user:private-password@",
		"socks5://private-user:private-password@127.0.0.1:bad-port",
	} {
		err := CheckProxy(raw)
		if err == nil {
			t.Fatal("invalid proxy was accepted")
		}
		msg := err.Error()
		if strings.Contains(msg, "private-user") || strings.Contains(msg, "private-password") {
			t.Error("invalid proxy error exposed credentials")
		}
		if !strings.Contains(msg, "http://") || !strings.Contains(msg, "socks5://") {
			t.Errorf("invalid proxy error lost address guidance: %s", msg)
		}
	}
}

func TestCheckProxyAcceptsEncodedCredentials(t *testing.T) {
	for _, raw := range []string{
		"socks5://ali%40ce:p%3Aa%2Fss%25@127.0.0.1:1080",
		"socks5h://ali%40ce:p%3Aa%2Fss%25@[::1]:1080",
		"socks5://127.0.0.1:1080",
	} {
		if err := CheckProxy(raw); err != nil {
			t.Errorf("valid proxy was refused: %v", err)
		}
	}
}
