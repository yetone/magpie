package provider

import (
	"errors"
	"net"
	"net/url"
	"strings"
	"unicode/utf8"
)

// Import links let a vendor or a relay hand its users a ready-made provider:
//
//	magpie://import?preset=deepseek&key=sk-…
//	magpie://import?name=My%20Relay&chat=https://relay.example/v1&key=sk-…
//
// Parameters (every value URL-encoded):
//
//	preset     a preset id (magpie presets); the preset's endpoints are used.
//	           Not google-vertex, asked at the user's own Google Cloud project
//	region     with a preset that has regions, which one
//	name       the provider's name; required without a preset
//	id         its id; derived from the name when absent
//	key        the API key; the user can paste one when absent
//	chat       OpenAI Chat Completions base URL (…/v1)
//	responses  OpenAI Responses base URL (…/v1)
//	anthropic  Anthropic Messages base URL (the root)
//	gemini     Google Gemini API base URL (…/v1beta), as the app's editor
//	           and the TUI take one (#1346, #2201)
//	models     model ids to show agents, comma separated
//	catalog    models.dev provider id, for names and reasoning levels
//	website    the vendor's site; keys: where keys are made
//	icon       an https picture of the vendor's own, fetched only after the
//	           user confirms; falls back to the generic mark when absent
//	balance    where the key's balance is asked (BalanceURL), with the key
//	           as a chat request carries it: https, on the host of one of
//	           the provider's own endpoints, so a link never sends the key
//	           anywhere its requests don't already go. {key} in it is the key
//	balance_path    the amount's field in the reply (BalancePath), e.g.
//	           data.balance; magpie's guess for a known reply when absent
//	balance_method  GET (the default) or POST
//
// An account-wide token (BalanceToken, a new-api relay's access token) or
// a header is never taken from a link: either is worth more than the key,
// and a URL is kept in browser history and logs. The user adds them in the
// provider's editor.
//
// A link never saves anything by itself: the app shows what it would add
// and the user says yes.

// Scheme is the URL scheme magpie registers with the system.
const Scheme = "magpie"

// ParseImport reads an import link into the provider it describes.
func ParseImport(link string) (Provider, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err == nil && strings.EqualFold(u.Scheme, CCSwitchScheme) {
		q, err := ccSwitchQuery(u)
		if err != nil {
			return Provider{}, err
		}
		return importFromQuery(q)
	}
	if err != nil || !strings.EqualFold(u.Scheme, Scheme) {
		return Provider{}, errors.New("not a magpie:// link")
	}
	action := u.Host
	if action == "" { // magpie:import?… or magpie:///import?…
		action = strings.Trim(u.Opaque+u.Path, "/")
	}
	if action != "import" {
		return Provider{}, errorf("magpie://%s is not something magpie knows; links start magpie://import?", action)
	}
	return importFromQuery(u.Query())
}

// importFromQuery is ParseImport after the link's shape is known: q holds
// the parameters in the list above, from a magpie:// link or translated from
// a ccswitch:// one (ccSwitchQuery).
func importFromQuery(q url.Values) (Provider, error) {
	var err error
	get := func(k string) string { return strings.TrimSpace(q.Get(k)) }

	var p Provider
	if id := get("preset"); id != "" {
		if p, err = FromPreset(strings.ToLower(id)); err != nil {
			return Provider{}, err
		}
		if p.IsVertex() {
			// asked at the user's own Google Cloud project, with their
			// own credentials: nothing a link could hand over
			return Provider{}, errorf("%s is added with your own Google Cloud project, not from a link: magpie provider add %s project=<id>, or Add in the app", p.Name, VertexPreset)
		}
		if r := get("region"); r != "" {
			found := false
			pr := Preset(p.Preset)
			r = RegionID(p.Preset, r) // a link made before its region was merged still adds
			for _, reg := range pr.Regions {
				if reg.ID == r {
					// atRegionOf is where a region's endpoints land: chat,
					// responses, anthropic and a decision host of its own,
					// plus that region's key page, docs and catalog
					p.atRegionOf(pr, reg)
					found = true
				}
			}
			if !found {
				return Provider{}, errorf("%s has no region %q", p.Name, r)
			}
		}
		if n := get("name"); n != "" {
			p.Name = n
		}
	} else {
		p = Provider{Name: get("name"), Catalog: strings.ToLower(get("catalog"))}
		if p.Name == "" {
			return Provider{}, errors.New("the link names no provider: it needs preset= or name=")
		}
		for k, dst := range map[string]*string{"chat": &p.Chat, "responses": &p.Responses, "anthropic": &p.Anthropic, "gemini": &p.Gemini} {
			if *dst, err = endpoint(k, get(k)); err != nil {
				return Provider{}, err
			}
		}
		if p.Chat == "" && p.Responses == "" && p.Anthropic == "" && p.Gemini == "" {
			return Provider{}, errors.New("the link gives no base URL: it needs chat=, responses=, anthropic= or gemini=")
		}
		if p.Catalog != "" && p.Catalog != Slug(p.Catalog) {
			p.Catalog = ""
		}
		if p.Catalog != "" {
			p.Icon = IconForCatalog(p.Catalog)
		}
		p.Website = page(get("website"))
		p.KeysURL = page(get("keys"))
	}
	if raw := get("icon"); raw != "" {
		u, err := iconURL(raw)
		if err != nil {
			return Provider{}, err
		}
		p.IconURL = u
	}

	if id := get("id"); id != "" {
		p.ID = Slug(id)
	} else if get("preset") == "" {
		p.ID = Slug(p.Name)
	}
	if p.ID == "" {
		return Provider{}, errors.New("the link's name has no letters or digits to make an id from")
	}
	if p.ID == "magpie" {
		return Provider{}, errors.New(`"magpie" is what agents call the gateway itself; the link needs another id`)
	}
	if subscriptionID(p.ID) {
		// named as a subscription is (claude, kiro): a provider of its own
		// beside it, never in its place, where the subscription's sign-in
		// would be lost, or Kiro's take the link's key to its own host
		// (#1487)
		p.ID = freeID(p.ID)
	}
	if len(p.Name) > 80 {
		// 80 bytes at most, cut where a character begins: a name in
		// Chinese reaches 80 bytes at 27 characters (#2416)
		cut := 80
		for cut > 0 && !utf8.RuneStart(p.Name[cut]) {
			cut--
		}
		p.Name = p.Name[:cut]
	}

	if err := linkBalance(&p, get("balance"), get("balance_path"), get("balance_method")); err != nil {
		return Provider{}, err
	}

	p.Key = get("key")
	if len(p.Key) > 4096 || strings.ContainsFunc(p.Key, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		return Provider{}, errors.New("the link's key is not a key")
	}
	for _, m := range strings.Split(q.Get("models"), ",") {
		if m = strings.TrimSpace(m); m != "" && len(p.Models) < 200 {
			p.Models = append(p.Models, m)
		}
	}
	return p, nil
}

// linkBalance sets the balance query a link names: an https URL on one of
// the provider's own endpoint hosts, since it is asked with the key.
func linkBalance(p *Provider, raw, path, method string) error {
	if raw == "" {
		if path != "" || method != "" {
			return errors.New("the link's balance_path= or balance_method= needs balance=")
		}
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return errorf("the link's balance= must be an https URL: %q", raw)
	}
	same := false
	for _, ep := range []string{p.Chat, p.Responses, p.Anthropic, p.Gemini} {
		if e, err := url.Parse(ep); err == nil && ep != "" && strings.EqualFold(e.Host, u.Host) {
			same = true
		}
	}
	if !same {
		return errorf("the link's balance= is on %s, not the host of its endpoints: the key is sent to it", u.Host)
	}
	switch m := strings.ToUpper(method); m {
	case "", "GET":
		method = ""
	case "POST":
		method = m
	default:
		return errorf("the link's balance_method= is GET or POST, not %q", method)
	}
	if len(path) > 200 || strings.ContainsFunc(path, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		return errors.New("the link's balance_path= is not a field")
	}
	p.BalanceURL, p.BalancePath, p.BalanceMethod = u.String(), path, method
	return nil
}

// endpoint accepts an https base URL, or plain http to this machine or the
// local network, where model servers usually run without TLS.
func endpoint(name, raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errorf("the link's %s= is not a base URL: %q", name, raw)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !local(u.Hostname()) {
			return "", errorf("the link's %s= must use https: %q", name, raw)
		}
	default:
		return "", errorf("the link's %s= must use https: %q", name, raw)
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func local(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".local") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

// page keeps a web page link only when it is https.
func page(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil {
		return u.String()
	}
	return ""
}

// iconURL accepts the picture a link names: an https URL on a public host,
// which the app fetches only after the user confirms the import. A plain
// http or a bare host is rejected rather than guessed at, since this is the
// one parameter that makes magpie reach out on the link's behalf.
func iconURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return "", errorf("the link's icon= is not a picture URL: %q", raw)
	}
	if u.Scheme != "https" {
		return "", errorf("the link's icon= must use https: %q", raw)
	}
	if local(u.Hostname()) {
		return "", errorf("the link's icon= must be on a public host: %q", raw)
	}
	return u.String(), nil
}
