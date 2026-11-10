package provider

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/lazyre"
)

// FaviconFor finds the site behind a provider's base URL and keeps its icon
// like a picture picked by hand (#12). The API's own host is asked first,
// then the domains it sits under (api.deepseek.com, then deepseek.com),
// since an API host seldom has a page of its own. On each, the home page's
// <link rel="icon"> pictures come before /favicon.ico. Only public https
// hosts are reached, as for an import link's icon.
//
// A new-api or one-api site names its logo only in /api/status, which its
// page's script reads to set the tab's icon; the page itself names none, or
// the panel's own /logo.png, and its /favicon.ico is the panel's too, the
// same on every such site (wiixdede on X: every relay got new-api's icon).
// Its logo is taken from there, or one its page was given by hand, and one
// with neither gets name's first letters on a tile.
func FaviconFor(ctx context.Context, base, name string) (string, error) {
	sites, err := faviconSites(base)
	if err != nil {
		return "", err
	}
	return favicon(ctx, guardClient(), sites, name, iconURL)
}

// IconFromURL keeps the picture at an address the user typed for a custom
// provider, since many sites have no /favicon.ico (usrname1833). It is
// fetched as a site's icon is: a public https host only, through the guarded
// client, within fetchIcon's time and MaxIcon, and kept only when StoreIcon
// knows it as a picture. With no address, or one that fails, it keeps the
// letter tile of name (or of base's site) instead, and failed says why.
func IconFromURL(ctx context.Context, raw, name, base string) (icon, failed string, err error) {
	return iconFromURL(ctx, guardClient(), raw, cmp.Or(strings.TrimSpace(name), siteLabel(base)), iconURL)
}

func iconFromURL(ctx context.Context, c *http.Client, raw, name string, check func(string) (string, error)) (icon, failed string, err error) {
	if raw = strings.TrimSpace(raw); raw != "" {
		u, err := check(raw)
		if err != nil {
			failed = fmt.Sprintf("%s is not a picture's https address on a public host", raw)
		} else if icon, err = fetchIcon(ctx, c, u); err == nil {
			return icon, "", nil
		} else {
			failed = err.Error()
		}
	}
	icon, err = letterIcon(name)
	return icon, failed, err
}

// faviconSites are the https origins to look on for base's icon: its host
// with its port, then each domain above it down to two labels.
func faviconSites(base string) ([]string, error) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Hostname() == "" {
		return nil, errors.New("type the provider's base URL first: its site's icon is looked for there")
	}
	host := u.Hostname()
	if local(host) {
		return nil, errors.New("the base URL is on this computer or the local network; its icon can't be fetched")
	}
	sites := []string{"https://" + u.Host}
	if host != u.Host {
		sites = append(sites, "https://"+host)
	}
	if strings.Trim(host, "0123456789.") != "" && !strings.Contains(host, ":") { // not an IP
		labels := strings.Split(host, ".")
		for i := 1; len(labels)-i >= 2; i++ {
			sites = append(sites, "https://"+strings.Join(labels[i:], "."))
		}
	}
	return sites, nil
}

// favicon tries each site in turn; check vets a picture's URL before it is
// fetched (iconURL; tests pass one that lets a local server through).
func favicon(ctx context.Context, c *http.Client, sites []string, name string, check func(string) (string, error)) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var last error
	for _, site := range sites {
		panel, logo, sysName := statusLogo(ctx, c, site)
		var cands []string
		if !panel {
			cands = append(pageIcons(ctx, c, site), site+"/favicon.ico")
		} else {
			// the logo it names, then one its page was given by hand: never
			// the panel's own, which every such site has
			if logo != "" {
				cands = append(cands, logo)
			}
			for _, cand := range pageIcons(ctx, c, site) {
				if p := strings.TrimPrefix(cand, site); p != "/favicon.ico" && p != "/logo.png" {
					cands = append(cands, cand)
				}
			}
		}
		for _, cand := range cands {
			if data, ok := strings.CutPrefix(cand, "data:"); ok {
				if b := dataURI(data); b != nil {
					if icon, err := StoreIcon(b); err == nil {
						return icon, nil
					}
				}
				continue
			}
			u, err := check(cand)
			if err != nil {
				last = err
				continue
			}
			icon, err := fetchIcon(ctx, c, u)
			if err == nil {
				return icon, nil
			}
			last = err
		}
		if panel {
			// the panel's own icon would look like every other relay's
			return letterIcon(cmp.Or(strings.TrimSpace(name), panelName(sysName), siteLabel(site)))
		}
		if ctx.Err() != nil {
			break
		}
	}
	if last == nil {
		last = errors.New("no icon found")
	}
	return "", errorf("couldn't find the site's icon (%s): %v", strings.Join(sites, ", "), last)
}

// statusLogo asks site's /api/status, where a new-api or one-api panel
// keeps its name and logo: panel is whether it answered as one, logo the
// picture's URL (or data: URI) when it has one of its own.
func statusLogo(ctx context.Context, c *http.Client, site string) (panel bool, logo, name string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, site+"/api/status", nil)
	if err != nil {
		return false, "", ""
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", iconUA)
	res, err := c.Do(req)
	if err != nil {
		return false, "", ""
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return false, "", ""
	}
	var st struct {
		Success bool
		Data    struct {
			SystemName *string `json:"system_name"`
			Logo       string
		}
	}
	if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&st) != nil || !st.Success || st.Data.SystemName == nil {
		return false, "", ""
	}
	logo = strings.TrimSpace(st.Data.Logo)
	if logo != "" && !strings.HasPrefix(logo, "data:") {
		ref, err := url.Parse(logo)
		if err != nil {
			logo = ""
		} else {
			logo = res.Request.URL.ResolveReference(ref).String()
		}
	}
	return true, logo, *st.Data.SystemName
}

// panelName is a panel's own name, unless it is still the panel's default.
func panelName(s string) string {
	s = strings.TrimSpace(s)
	switch strings.ToLower(strings.ReplaceAll(s, " ", "")) {
	case "newapi", "oneapi":
		return ""
	}
	return s
}

// siteLabel is the name a site's domain gives it: relay for api.relay.com.
func siteLabel(site string) string {
	u, err := url.Parse(site)
	if err != nil {
		return "?"
	}
	labels := strings.Split(u.Hostname(), ".")
	if len(labels) >= 2 {
		return labels[len(labels)-2]
	}
	return labels[0]
}

// letterIcon keeps a tile with name's first letters on it, its colour
// picked by the name, so providers with no picture of their own still look
// apart: one character for a CJK name, else the first letters of its first
// two words, or of its one word.
func letterIcon(name string) (string, error) {
	words := strings.FieldsFunc(name, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if len(words) == 0 {
		words = []string{"?"}
	}
	first := []rune(words[0])
	var mark string
	switch {
	case unicode.In(first[0], unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul):
		mark = string(first[0])
	case len(words) > 1:
		mark = strings.ToUpper(string(first[0]) + string([]rune(words[1])[0]))
	case len(first) > 1:
		mark = strings.ToUpper(string(first[0])) + strings.ToLower(string(first[1]))
	default:
		mark = strings.ToUpper(string(first[0]))
	}
	size := 26
	if utf8.RuneCountInString(mark) == 1 {
		size = 32
	}
	h := fnv.New32a()
	h.Write([]byte(name))
	var text strings.Builder
	xml.EscapeText(&text, []byte(mark))
	svg := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">`+
		`<rect width="64" height="64" rx="14" fill="hsl(%d,52%%,46%%)"/>`+
		`<text x="32" y="32" dy=".35em" text-anchor="middle" font-family="-apple-system,BlinkMacSystemFont,'Segoe UI','PingFang SC','Microsoft YaHei',sans-serif" font-size="%d" font-weight="600" fill="#fff">%s</text></svg>`,
		h.Sum32()%360, size, text.String())
	return StoreIcon([]byte(svg))
}

// iconUA names magpie to the sites icons are fetched from: some (deepseek.com)
// turn Go's own User-Agent away with a 429.
const iconUA = "magpie"

var (
	linkTag  = lazyre.New(`(?is)<link\b[^>]*>`)
	tagAttr  = lazyre.New(`(?is)\b(rel|href|sizes|type)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
	headDone = lazyre.New(`(?i)</head>|<body\b`)
)

// pageIcons are the pictures a site's home page names as its icon, the
// likeliest to look good first: an SVG, then the larger apple-touch-icon,
// then the rest in page order.
func pageIcons(ctx context.Context, c *http.Client, site string) []string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, site+"/", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Accept", "text/html")
	req.Header.Set("User-Agent", iconUA)
	res, err := c.Do(req)
	if err != nil {
		return nil
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 512<<10))
	html := string(b)
	if i := headDone.FindStringIndex(html); i != nil {
		html = html[:i[0]]
	}
	var svg, touch, rest []string
	for _, tag := range linkTag.FindAllString(html, -1) {
		attrs := map[string]string{}
		for _, m := range tagAttr.FindAllStringSubmatch(tag, -1) {
			attrs[strings.ToLower(m[1])] = m[2] + m[3] + m[4]
		}
		rel := strings.Fields(strings.ToLower(attrs["rel"]))
		href := strings.TrimSpace(attrs["href"])
		if href == "" {
			continue
		}
		isIcon, isTouch := false, false
		for _, r := range rel {
			switch r {
			case "icon":
				isIcon = true
			case "apple-touch-icon", "apple-touch-icon-precomposed":
				isTouch = true
			}
		}
		if !isIcon && !isTouch {
			continue
		}
		if !strings.HasPrefix(href, "data:") {
			ref, err := url.Parse(href)
			if err != nil {
				continue
			}
			href = res.Request.URL.ResolveReference(ref).String()
		}
		switch {
		case attrs["type"] == "image/svg+xml" || strings.HasSuffix(strings.ToLower(strings.SplitN(href, "?", 2)[0]), ".svg"):
			svg = append(svg, href)
		case isTouch:
			touch = append(touch, href)
		default:
			rest = append(rest, href)
		}
	}
	return append(append(svg, touch...), rest...)
}

// dataURI decodes what follows "data:" in a data URI, nil when it isn't one.
func dataURI(s string) []byte {
	meta, data, ok := strings.Cut(s, ",")
	if !ok {
		return nil
	}
	if strings.HasSuffix(strings.ToLower(meta), ";base64") {
		b, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return nil
		}
		return b
	}
	d, err := url.PathUnescape(data)
	if err != nil {
		return nil
	}
	return []byte(d)
}
