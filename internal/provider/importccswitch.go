package provider

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// CCSwitchScheme is the URL scheme CC Switch (github.com/farion1231/cc-switch)
// registers. Relays put its one-click import links on their pages, so magpie
// reads them too:
//
//	ccswitch://v1/import?resource=provider&app=claude&name=My%20Relay&endpoint=https://relay.example&apiKey=sk-…
//	ccswitch://v1/import?resource=provider&app=claude&name=…&configFormat=json&config=<base64 of {"env":{…}}>
//
// The format is read from CC Switch's own parser
// (src-tauri/src/deeplink/parser.rs, provider.rs). Only resource=provider
// is something magpie has (prompts, MCP servers and skills are other
// resources). Parameters:
//
//	app          claude, claude-desktop, codex, gemini, agy, copilot,
//	             grokbuild, opencode, openclaw, hermes, pi, mcode, kimi, dsh
//	             or omp; it picks which endpoint the address is (below)
//	name         required
//	endpoint     the base URL; with several, comma separated, the first
//	apiKey       the API key
//	apiFormat    anthropic, openai_chat, openai_responses or gemini_native:
//	             which API the endpoint speaks. Without it, the app's own:
//	             Anthropic for claude, Responses for codex and grokbuild,
//	             Gemini for gemini and agy, Chat Completions for the rest
//	model, haikuModel, sonnetModel, opusModel   the model ids to show
//	homepage     the vendor's site
//	iconUrl      a picture, fetched only after the user confirms
//	configFormat json (the default) or toml, with config: the base64 of the
//	             app's own settings. Its address, key and models fill what
//	             the other parameters leave out, as CC Switch does (claude:
//	             env.ANTHROPIC_*; codex: auth.OPENAI_API_KEY and
//	             config.toml's model provider; gemini: GEMINI_API_KEY and
//	             GOOGLE_GEMINI_BASE_URL). The rest of that config is not
//	             taken: it is the app's settings, not a provider's
//
// Everything else (enabled, notes, usage*, the CC Switch icon names) is
// dropped. The link goes through the same checks and the same confirmation
// as a magpie:// one: it becomes a magpie://import's parameters (see
// ParseImport), so nothing is saved until the user says yes.
const CCSwitchScheme = "ccswitch"

// ccSwitchLinkApps are the app= values CC Switch's parser accepts.
var ccSwitchLinkApps = []string{"agy", "copilot", "claude", "claude-desktop", "codex", "gemini", "grokbuild", "opencode", "openclaw", "hermes", "pi", "mcode", "kimi", "dsh", "omp"}

// ccSwitchConfigMax bounds the base64 config: a provider's settings are a
// few hundred bytes, and a URL much past this is not one.
const ccSwitchConfigMax = 256 << 10

// ccSwitchQuery translates a ccswitch://v1/import link into the parameters
// of a magpie import.
func ccSwitchQuery(u *url.URL) (url.Values, error) {
	if !strings.EqualFold(u.Host, "v1") {
		return nil, errorf("ccswitch://%s: magpie reads ccswitch://v1/import links only", u.Host)
	}
	if u.Path != "/import" {
		return nil, errorf("ccswitch link path %q is not /import", u.Path)
	}
	in := u.Query()
	get := func(k string) string { return strings.TrimSpace(in.Get(k)) }

	if r := get("resource"); r != "provider" {
		if r == "" {
			return nil, errorf("the ccswitch link has no resource=")
		}
		return nil, errorf("magpie takes providers from a ccswitch link; this one is a %q", r)
	}
	app := get("app")
	if !slices.Contains(ccSwitchLinkApps, app) {
		return nil, errorf("the ccswitch link's app= is not an app CC Switch knows: %q", app)
	}
	name := get("name")
	if name == "" {
		return nil, errorf("the ccswitch link has no name=")
	}
	if in.Has("configUrl") {
		return nil, errorf("the ccswitch link's configUrl= is not supported: the config is inline, or not at all")
	}

	endpoint, key := get("endpoint"), get("apiKey")
	format := strings.TrimSpace(in.Get("apiFormat"))
	models := []string{get("model"), get("haikuModel"), get("sonnetModel"), get("opusModel")}

	if raw := get("config"); raw != "" {
		cfg, err := ccSwitchConfig(raw, get("configFormat"))
		if err != nil {
			return nil, err
		}
		c, err := cfg.read(app)
		if err != nil {
			return nil, err
		}
		if key == "" {
			key = c.key
		}
		if endpoint == "" {
			endpoint = c.endpoint
		}
		if format == "" {
			format = c.format
		}
		for i, m := range c.models {
			if models[i] == "" {
				models[i] = m
			}
		}
	}

	if i := strings.IndexByte(endpoint, ','); i >= 0 {
		endpoint = endpoint[:i] // the first is the one requests go to
	}
	endpoint = strings.TrimSpace(endpoint)

	kind, err := ccSwitchKind(app, format)
	if err != nil {
		return nil, err
	}
	out := url.Values{}
	out.Set("name", name)
	if endpoint != "" {
		out.Set(kind, endpoint)
	}
	if key != "" {
		out.Set("key", key)
	}
	var ids []string
	for _, m := range models {
		if m = strings.TrimSpace(m); m != "" && !slices.Contains(ids, m) {
			ids = append(ids, m)
		}
	}
	out.Set("models", strings.Join(ids, ","))
	// a picture or a site that isn't an https URL is dropped, not a reason
	// to turn the whole link away: CC Switch does the same
	if h := get("homepage"); h != "" && page(h) != "" {
		out.Set("website", h)
	}
	if ic := get("iconUrl"); ic != "" {
		if _, err := iconURL(ic); err == nil {
			out.Set("icon", ic)
		}
	}
	return out, nil
}

// ccSwitchKind is which of magpie's endpoints the link's address is.
func ccSwitchKind(app, format string) (string, error) {
	switch format {
	case "":
		switch app {
		case "claude", "claude-desktop":
			return "anthropic", nil
		case "codex", "grokbuild":
			return "responses", nil
		case "gemini", "agy":
			return "gemini", nil
		}
		return "chat", nil
	case "anthropic":
		return "anthropic", nil
	case "openai_chat":
		return "chat", nil
	case "openai_responses":
		return "responses", nil
	case "gemini_native":
		return "gemini", nil
	}
	return "", errorf("the ccswitch link's apiFormat= is anthropic, openai_chat, openai_responses or gemini_native, not %q", format)
}

// ccSwitchSettings is what a link's config yields.
type ccSwitchSettings struct {
	endpoint, key, format string
	models                [4]string // model, haiku, sonnet, opus
}

type ccSwitchCfg map[string]any

// ccSwitchConfig decodes the config parameter as CC Switch does: base64
// with the quirks of a URL ("+" arriving as a space, padding left off, the
// URL-safe alphabet), then JSON or TOML.
func ccSwitchConfig(raw, format string) (ccSwitchCfg, error) {
	if len(raw) > ccSwitchConfigMax {
		return nil, errorf("the ccswitch link's config= is too large")
	}
	raw = strings.ReplaceAll(strings.Trim(raw, "\r\n"), " ", "+")
	raw = strings.TrimRight(raw, "=")
	b, err := base64.RawStdEncoding.DecodeString(raw)
	if err != nil {
		if b, err = base64.RawURLEncoding.DecodeString(raw); err != nil {
			return nil, errorf("the ccswitch link's config= is not base64")
		}
	}
	var m map[string]any
	switch format {
	case "", "json":
		err = json.Unmarshal(b, &m)
	case "toml":
		err = toml.Unmarshal(b, &m)
	default:
		return nil, errorf("the ccswitch link's configFormat= is json or toml, not %q", format)
	}
	if err != nil || m == nil {
		return nil, errorf("the ccswitch link's config= is not a %s object", orElse(format, "json"))
	}
	return m, nil
}

func orElse(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func (c ccSwitchCfg) str(path ...string) string {
	var v any = map[string]any(c)
	for _, k := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return ""
		}
		v = m[k]
	}
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func (c ccSwitchCfg) obj(k string) ccSwitchCfg {
	m, _ := c[k].(map[string]any)
	return m
}

// read takes the address, key and models an app's own settings hold.
func (c ccSwitchCfg) read(app string) (ccSwitchSettings, error) {
	var s ccSwitchSettings
	switch app {
	case "claude", "claude-desktop":
		env := c.obj("env")
		if env == nil {
			return s, errorf("the ccswitch link's claude config needs an env object")
		}
		s.key = orElse(env.str("ANTHROPIC_AUTH_TOKEN"), env.str("ANTHROPIC_API_KEY"))
		s.endpoint = env.str("ANTHROPIC_BASE_URL")
		s.models = [4]string{env.str("ANTHROPIC_MODEL"), env.str("ANTHROPIC_DEFAULT_HAIKU_MODEL"), env.str("ANTHROPIC_DEFAULT_SONNET_MODEL"), env.str("ANTHROPIC_DEFAULT_OPUS_MODEL")}
	case "gemini", "agy":
		env := c
		if e := c.obj("env"); e != nil {
			env = e
		}
		s.key = env.str("GEMINI_API_KEY")
		s.endpoint = orElse(env.str("GOOGLE_GEMINI_BASE_URL"), env.str("GEMINI_BASE_URL"))
		s.models[0] = env.str("GEMINI_MODEL")
	case "codex":
		return c.readCodex()
	}
	return s, nil
}

// readCodex reads {"auth":{"OPENAI_API_KEY":…},"config":"<config.toml>"},
// or a config.toml itself. The key a provider table reads from an
// environment variable (env_key) is the computer's own, not the link's, and
// is never taken.
func (c ccSwitchCfg) readCodex() (ccSwitchSettings, error) {
	var s ccSwitchSettings
	doc := map[string]any(c)
	if text := c.str("config"); text != "" {
		doc = nil
		if err := toml.Unmarshal([]byte(text), &doc); err != nil {
			return s, errorf("the ccswitch link's codex config is not TOML")
		}
	} else if c.obj("auth") != nil {
		doc = nil
	}
	root := ccSwitchCfg(doc)
	s.key = c.obj("auth").str("OPENAI_API_KEY")
	s.models[0] = root.str("model")
	id := root.str("model_provider")
	var tbl ccSwitchCfg
	if provs := root.obj("model_providers"); provs != nil {
		if tbl = provs.obj(id); tbl == nil && len(provs) == 1 {
			for _, v := range provs {
				tbl, _ = v.(map[string]any)
			}
		}
	}
	if tbl != nil {
		s.endpoint = tbl.str("base_url")
		if s.key == "" {
			s.key = tbl.str("experimental_bearer_token")
		}
		switch tbl.str("wire_api") {
		case "chat":
			s.format = "openai_chat"
		case "responses":
			s.format = "openai_responses"
		}
		if s.key == "" && tbl.str("env_key") != "" {
			return s, errorf("the ccswitch link gives its key through the environment variable %s, which a link can't read: add the key yourself", tbl.str("env_key"))
		}
	}
	return s, nil
}
