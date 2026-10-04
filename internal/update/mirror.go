package update

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/settings"
)

// A network that can't reach GitHub can have a release's file come
// through a GitHub download mirror: its prefix goes before the file's
// github.com URL (https://mirror.example/https://github.com/…). magpie
// names no mirror of its own; one is used only when given (magpie update
// --mirror, or Settings' UpdateMirror, which magpie update mirror sets).
//
// A mirror is a host magpie doesn't trust: the update feed, with each
// file's SHA-256, is still asked at usemagpie.ai and never through the
// mirror, the file is checked against that hash, and the macOS app must
// still be signed by the team that signed the one installed. A mirror
// that changes a byte gets its download refused.

type mirrorKey struct{}

// WithMirror has the downloads made under ctx go through the mirror
// prefix; "off" goes through none, whatever Settings say, and "" leaves
// ctx as it is.
func WithMirror(ctx context.Context, prefix string) context.Context {
	if prefix = strings.TrimSpace(prefix); prefix == "" {
		return ctx
	}
	return context.WithValue(ctx, mirrorKey{}, prefix)
}

// WithProxy has the update feed and the downloads asked under ctx go
// through proxy (http://, https://, socks5://, socks5h://; host:port is
// http; "direct" for none), over Settings' Proxy and the environment's.
func WithProxy(ctx context.Context, proxy string) context.Context {
	return netproxy.With(ctx, proxy)
}

// CheckMirror says what is wrong with a mirror prefix, nil when it is one.
func CheckMirror(prefix string) error {
	u, err := url.Parse(strings.TrimSpace(prefix))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("a mirror is an http(s) address put before the github.com URL, like https://mirror.example/, not %q", prefix)
	}
	return nil
}

// CheckProxy says what is wrong with a proxy address, nil when it is one.
func CheckProxy(proxy string) error {
	if proxy == "direct" {
		return nil
	}
	u, err := netproxy.Parse(proxy)
	if err != nil || u.Host == "" {
		return fmt.Errorf("a proxy is an address like http://127.0.0.1:7890 or socks5://127.0.0.1:1080, not %q", proxy)
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
		return nil
	}
	return fmt.Errorf("a proxy is http://, https://, socks5:// or socks5h://, not %q", proxy)
}

// mirrorOf is the mirror downloads under ctx go through: WithMirror's,
// else Settings', "" for none.
func mirrorOf(ctx context.Context) string {
	m, _ := ctx.Value(mirrorKey{}).(string)
	if m == "" {
		m = strings.TrimSpace(settings.Load().UpdateMirror)
	}
	if m == "off" || m == "direct" {
		return ""
	}
	return m
}

// mirrored is raw fetched through the mirror prefix: only a file on
// GitHub is; anything else (a test's server, a feed pointed elsewhere)
// goes as it is.
func mirrored(prefix, raw string) string {
	if prefix == "" {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || !githubHost(u.Hostname()) {
		return raw
	}
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return prefix + raw
}

func githubHost(h string) bool {
	h = strings.ToLower(h)
	return h == "github.com" || strings.HasSuffix(h, ".github.com") || strings.HasSuffix(h, ".githubusercontent.com")
}

// Mirror is the mirror downloads under ctx go through, "" for none.
func Mirror(ctx context.Context) string { return mirrorOf(ctx) }
