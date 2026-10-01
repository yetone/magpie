package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// A provider's own picture lives in the icons folder beside providers.json,
// named after its content, and the provider's Icon reads "file:<name>". The
// built-in icons are plain names ("deepseek-color").
const iconPrefix = "file:"

// MaxIcon is the largest picture magpie keeps for a provider.
const MaxIcon = 1 << 20

var iconName = regexp.MustCompile(`^[0-9a-f]{16}\.(png|jpg|gif|webp|ico|svg)$`)

// IconDir is where pictures given to providers are kept.
func IconDir() string { return filepath.Join(filepath.Dir(Path()), "icons") }

// IconFile is the path of a stored picture, for a name as StoreIcon made it;
// "" for anything else, so a request can't reach outside the folder.
func IconFile(name string) string {
	if !iconName.MatchString(name) {
		return ""
	}
	return filepath.Join(IconDir(), name)
}

// StoreIcon keeps a picture (PNG, JPEG, GIF, WebP, ICO or SVG) and returns
// the Icon value that points at it. The same picture is stored once.
func StoreIcon(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("the picture is empty")
	}
	if len(data) > MaxIcon {
		return "", errors.New("the picture is over 1 MB; pick a smaller one")
	}
	ext := iconExt(data)
	if ext == "" {
		return "", errors.New("not a picture magpie can show: use PNG, JPEG, GIF, WebP, ICO or SVG")
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:8]) + "." + ext
	if err := os.MkdirAll(IconDir(), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(IconDir(), name), data, 0o644); err != nil {
		return "", err
	}
	return iconPrefix + name, nil
}

// FetchIcon downloads the picture an import link named and keeps it like
// any other, returning the Icon value that points at it. It runs when the
// user confirms the import, never at parse time; the URL is re-checked here
// so a crafted request can't point magpie at anything but a public https
// picture, and the read stops one byte past MaxIcon so a huge body is
// refused rather than buffered.
func FetchIcon(ctx context.Context, rawURL string) (string, error) {
	u, err := iconURL(rawURL)
	if err != nil {
		return "", err
	}
	return fetchIcon(ctx, guardClient(), u)
}

// guardClient is the http.Client icon fetches use: the default transport —
// so the proxy settings still apply — but with dialing wrapped in publicDial.
//
// Through a proxy the connection goes to the proxy, often on this computer
// (Clash's 127.0.0.1:7890), so the proxy's own address is dialed as it is;
// the site's name is judged before the proxy is asked for, since the proxy
// resolves it where magpie can't see.
func guardClient() *http.Client {
	c := *http.DefaultClient
	t := http.DefaultTransport.(*http.Transport).Clone()
	dial := t.DialContext
	var proxies sync.Map // host:port of the proxies this client was sent to
	if proxy := t.Proxy; proxy != nil {
		t.Proxy = func(req *http.Request) (*url.URL, error) {
			u, err := proxy(req)
			if err != nil || u == nil {
				return u, err
			}
			if err := publicHost(req.Context(), req.URL.Hostname()); err != nil {
				return nil, err
			}
			proxies.Store(proxyAddr(u), true)
			return u, nil
		}
	}
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if _, ok := proxies.Load(addr); ok {
			return dial(ctx, network, addr)
		}
		return publicDial(ctx, dial, network, addr)
	}
	c.Transport = t
	return &c
}

// publicHost checks a site's answers with the same rules as publicDial,
// for a fetch the proxy will make. A name that doesn't resolve
// here is left to the proxy, which may be the only way to reach it.
func publicHost(ctx context.Context, host string) error {
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil
	}
	_, err = iconIPs(host, ips)
	return err
}

// Loon synthesizes AAAA records by embedding its fake IPv4 in fd27:712::/96.
// Prefer the matching A record already accepted by our IPv4 guard. Do not
// dial the private IPv6 address or exempt the whole ULA prefix: an unpaired
// record (or any other private answer) still fails the check.
var loonIconPrefix = netip.MustParsePrefix("fd27:712::/96")

func iconIPs(host string, ips []net.IPAddr) ([]net.IPAddr, error) {
	var allowed []net.IPAddr
	for _, ip := range ips {
		if publicIP(ip.IP) {
			allowed = append(allowed, ip)
			continue
		}
		if addr, ok := netip.AddrFromSlice(ip.IP); ok && loonIconPrefix.Contains(addr) {
			paired := false
			for _, other := range ips {
				if v4 := other.IP.To4(); v4 != nil && publicIP(v4) && v4.Equal(ip.IP[12:]) {
					paired = true
					break
				}
			}
			if paired {
				continue
			}
		}
		return nil, errorf("the icon host %s resolves to %s, which is not public", host, ip.IP)
	}
	return allowed, nil
}

// proxyAddr is the host:port the transport dials to reach proxy u.
func proxyAddr(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = map[string]string{"https": "443", "socks5": "1080", "socks5h": "1080"}[u.Scheme]
		if port == "" {
			port = "80"
		}
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// publicDial resolves the host and refuses to connect when any address it
// maps to is not public (except a paired Loon AAAA discarded by iconIPs).
// Checking the resolved address here, not just the URL's hostname, closes
// the door on a name that answers public once and
// private the next time (DNS rebinding): the same resolution this dial uses
// is the one that is judged.
func publicDial(ctx context.Context, dial func(context.Context, string, string) (net.Conn, error), network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, errorf("could not resolve %s", host)
	}
	ips, err = iconIPs(host, ips)
	if err != nil {
		return nil, err
	}
	// Dial each resolved address by IP, so nothing resolves twice.
	var lastErr error
	for _, ip := range ips {
		conn, err := dial(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// publicIP is true for an address on the open internet: not loopback,
// private, link-local, unspecified, multicast or otherwise reserved.
//
// 198.18.0.0/15, RFC 2544's benchmarking range, is let through: Clash's TUN
// mode with fake-ip and Surge's enhanced mode answer every name with an
// address there and carry the connection to the real host through the proxy
// (#252). No machine on the user's network sits in that range, so a fetch
// aimed at it reaches the internet host the name stands for, not this
// computer or the LAN.
func publicIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		// 100.64/10 carrier NAT, 192.0.0.0/24, 192.0.2/24 and friends:
		// never a public web host.
		switch {
		case v4[0] == 100 && v4[1]&0xc0 == 64:
			return false
		case v4[0] == 192 && v4[1] == 0 && v4[2] == 0:
			return false
		case v4[0] == 192 && v4[1] == 0 && v4[2] == 2:
			return false
		case v4[0] == 198 && v4[1] == 51 && v4[2] == 100:
			return false
		case v4[0] == 203 && v4[1] == 0 && v4[2] == 113:
			return false
		}
		return true
	}
	// IPv6: unique local (fc00::/7) and 6to4/teredo tunnels are not public
	// web hosts either.
	if len(ip) == net.IPv6len {
		if ip[0]&0xfe == 0xfc {
			return false
		}
		if ip[0] == 0x20 && ip[1] == 0x02 {
			return false // 2002::/16 6to4
		}
	}
	return true
}

// fetchIcon is the download itself, split out so tests can point it at a
// local server; the URL has already passed iconURL.
func fetchIcon(ctx context.Context, c *http.Client, u string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "image/*")
	req.Header.Set("User-Agent", iconUA)
	res, err := c.Do(req)
	if err != nil {
		return "", errorf("could not fetch the icon: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", errorf("the icon URL answered %s", res.Status)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, MaxIcon+1))
	if err != nil {
		return "", errorf("could not read the icon: %v", err)
	}
	return StoreIcon(b)
}

// StoreIconFile is StoreIcon for a picture on disk.
func StoreIconFile(path string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if st.Size() > MaxIcon {
		return "", errors.New("the picture is over 1 MB; pick a smaller one")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return StoreIcon(b)
}

func iconExt(b []byte) string {
	switch http.DetectContentType(b) {
	case "image/png":
		return "png"
	case "image/jpeg":
		return "jpg"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	case "image/x-icon", "image/vnd.microsoft.icon":
		return "ico"
	}
	head := bytes.ToLower(b[:min(len(b), 1024)])
	if bytes.Contains(head, []byte("<svg")) {
		return "svg"
	}
	return ""
}

// pruneIcons removes pictures no provider points at any more, nor a plugin
// gave its provider (PluginIcon). One picked in the editor is stored
// before its provider is saved, so a fresh one stays.
func pruneIcons(f file) {
	used := map[string]bool{}
	for _, p := range f.Providers {
		if name, ok := strings.CutPrefix(p.Icon, iconPrefix); ok {
			used[name] = true
		}
	}
	for _, name := range keptPluginIcons() {
		used[name] = true
	}
	ents, _ := os.ReadDir(IconDir())
	for _, e := range ents {
		if !iconName.MatchString(e.Name()) || used[e.Name()] {
			continue
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > time.Hour {
			os.Remove(filepath.Join(IconDir(), e.Name()))
		}
	}
}
