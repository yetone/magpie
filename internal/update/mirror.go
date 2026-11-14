package update

import (
	"context"
	"errors"
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
		return fmt.Errorf("a proxy is an address like http://127.0.0.1:7890 or socks5://127.0.0.1:1080%s", notProxy(proxy))
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
		return nil
	}
	return fmt.Errorf("a proxy is http://, https://, socks5:// or socks5h://%s", notProxy(proxy))
}

// notProxy is ", not <proxy>" for CheckProxy's error, the proxy's user
// and password left out (#1886); "" when nothing of it can be shown.
func notProxy(proxy string) string {
	if r := netproxy.Redact(proxy); r != "" {
		return fmt.Sprintf(", not %q", r)
	}
	return ""
}

// mirrorOf is the mirror downloads under ctx go through: WithMirror's,
// else Settings', "" for none.
func mirrorOf(ctx context.Context) string {
	m, _ := ctx.Value(mirrorKey{}).(string)
	if m == "" {
		m = strings.TrimSpace(settings.Shared().UpdateMirror)
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

// MirrorError is a download that failed through a mirror: the mirror
// didn't answer, answered with something other than the file, or sent a
// file whose hash isn't the feed's (never installed). Downloading from
// GitHub itself may still work, which the GUI offers.
type MirrorError struct {
	Mirror string // the prefix the download went through
	Err    error
}

func (e *MirrorError) Error() string {
	if strings.Contains(e.Err.Error(), e.Mirror) {
		return e.Err.Error()
	}
	return "through the mirror " + e.Mirror + ": " + e.Err.Error()
}

func (e *MirrorError) Unwrap() error { return e.Err }

// MirrorOf is the mirror a download error came through, "" when it is no
// mirror's.
func MirrorOf(err error) string {
	var m *MirrorError
	if errors.As(err, &m) {
		return m.Mirror
	}
	return ""
}

// CheckMirrorURL is CheckMirror for one typed into Settings: a full
// https:// address (a download over http would be read by anyone between,
// though the checksum still guards what is installed).
func CheckMirrorURL(prefix string) error {
	prefix = strings.TrimSpace(prefix)
	u, err := url.Parse(prefix)
	if err != nil || u.Scheme != "https" || u.Host == "" || strings.ContainsAny(prefix, " \t") {
		return errors.New("a mirror is a full https:// address, like https://mirror.example/")
	}
	return nil
}

// Mirror is the mirror downloads under ctx go through, "" for none.
func Mirror(ctx context.Context) string { return mirrorOf(ctx) }
