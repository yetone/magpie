package sessions

import (
	"encoding/json"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/settings"
)

// A provider the user added in ZCode by hand, pointed at this magpie's
// gateway, has an id of ZCode's own ("new-provider", whatever its settings
// gave it), not the "magpie" magpie writes. ZCode records that id beside each
// reply, so ocGatewayProvider did not know the call for the gateway's, and a
// call the gateway also logged was listed a second time as a local session's
// whenever the two records did not pair by tokens and time
// (jiangshangmuyun on X: one Trae CN DeepSeek call under "new-provider ·
// 本地会话" among the gateway's own rows for the same window).
//
// So the provider's address decides, read from both of ZCode's provider
// files (internal/agent's zcode.go says which ZCode reads which): a
// provider whose base URL is this machine's, on the gateway's port, is the
// gateway. Like workbuddyGateway, this reads the user's configuration as it
// is now, not a record of the call: a session read again after its provider
// was pointed elsewhere has its earlier calls read as its own.

var zcGateway struct {
	sync.Mutex
	stamp string
	ids   map[string]bool
}

// zcodeGateway is whether ZCode's provider id is magpie's gateway: the id
// magpie writes, or one whose base URL is this gateway's.
func zcodeGateway(id string) bool {
	return ocGatewayProvider(id) || id != "" && zcodeGatewayIDs()[id]
}

// zcodeGatewayIDs are the ids of ZCode's providers whose base URL is this
// gateway's, in either of its provider files; an empty set when neither
// names one (or can be read).
func zcodeGatewayIDs() map[string]bool {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(appdir.ZCodeDataIn(home), ".zcode", "v2")
	config, rules := filepath.Join(dir, "config.json"), filepath.Join(dir, "provider_config.json")
	stamp := ocStamp(config) + ocStamp(rules) + settings.GatewayAddr() + "|" + settings.SavedAddr()
	zcGateway.Lock()
	defer zcGateway.Unlock()
	if zcGateway.ids != nil && stamp == zcGateway.stamp {
		return zcGateway.ids
	}
	ids := map[string]bool{}
	var c struct {
		Provider map[string]struct {
			Options struct {
				BaseURL string `json:"baseURL"`
			} `json:"options"`
		} `json:"provider"`
	}
	if b, err := os.ReadFile(config); err == nil && json.Unmarshal(b, &c) == nil {
		for id, p := range c.Provider {
			if gatewayBase(p.Options.BaseURL) {
				ids[id] = true
			}
		}
	}
	var r struct {
		Config struct {
			ProviderConfigRules struct {
				ProviderRules []struct {
					ProviderID string `json:"providerId"`
					Config     struct {
						API struct {
							BaseURL string `json:"baseUrl"`
						} `json:"api"`
					} `json:"config"`
				} `json:"providerRules"`
			} `json:"providerConfigRules"`
		} `json:"config"`
	}
	if b, err := os.ReadFile(rules); err == nil && json.Unmarshal(b, &r) == nil {
		for _, p := range r.Config.ProviderConfigRules.ProviderRules {
			if p.ProviderID != "" && gatewayBase(p.Config.API.BaseURL) {
				ids[p.ProviderID] = true
			}
		}
	}
	zcGateway.stamp, zcGateway.ids = stamp, ids
	return ids
}

// gatewayBase is whether an agent's base URL is this magpie's gateway: http
// or https to this machine (loopback, localhost, or one of its own
// addresses, as a LAN-shared gateway is reached) on the gateway's port, the
// one it listens on (MAGPIE_ADDR) or the one its settings save.
func gatewayBase(base string) bool {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return false
	}
	port := u.Port()
	if port == "" {
		return false // magpie's gateway is never on 80 or 443
	}
	ours := false
	for _, a := range []string{settings.GatewayAddr(), settings.SavedAddr()} {
		if _, p, err := net.SplitHostPort(a); err == nil && p == port {
			ours = true
		}
	}
	if !ours {
		return false
	}
	h := u.Hostname()
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsUnspecified() {
		return true
	}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
			return true
		}
	}
	return false
}
