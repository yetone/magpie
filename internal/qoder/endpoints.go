// Package qoder speaks Qoder's inference API (api3.qoder.sh
// agent_chat_generation SSE) the way the Qoder desktop client does, so a
// Qoder subscription can serve every agent through magpie's gateway. The
// protocol (endpoints, the COSY request envelope, the body codec and the
// device-flow sign-in) is ported from CLIProxyAPI's qoder support, which
// reverse-engineered it from the client and verified it against live captures.
// Source: https://github.com/ufec/CLIProxyAPI (MIT); see LICENSE in this directory.
// Copyright (c) 2025-2005.9 Luis Pater
// Copyright (c) 2025.9-present Router-For.ME
//
// This package supports the global Qoder client (qoder.com) and Qoder CN
// (qoder.cn), the China site whose accounts sign in with Alibaba Cloud or a
// phone number and exist only there. A Site holds each one's hosts.
package qoder

// PLUGIN-SERVED (see AGENTS.md): Qoder ("qoder") and Qoder CN ("qoder-cn")
// are deprecated built-in subscriptions served by their plugin,
// @magpie-community/opencode-qoder-auth, each once moved onto it
// (provider.Moved; the default for a new sign-in). A moved one's
// sign-ins, models, requests and usage are all the plugin's, never this
// code's (only the move, in migrate*.go, still reads its accounts). A fix here alone doesn't reach those users; fix the plugin
// (github.com/magpie-community/plugins, packages/qoder) and raise the
// movers' min in internal/provider/migrate_qoder.go.

// OAuth device-flow configuration (verified against live captures).
const (
	// ClientID is Qoder's device-flow client id.
	ClientID = "732aef47-9cf2-46a2-95fe-4cebb5d0d1fa"

	// DeviceFlowHost is the device-flow authorization page host.
	DeviceFlowHost = "https://qoder.com"

	// OpenAPIHost is the API host for token polling and job-token exchange.
	OpenAPIHost = "https://openapi.qoder.sh"

	// APIHost is the model inference API host.
	APIHost = "https://api3.qoder.sh"

	// RedirectURI is Qoder's app scheme used by the device flow.
	RedirectURI = "qoder-app://"
)

// API endpoints.
const (
	DeviceSelectAccountsPath = "/device/selectAccounts"
	DeviceTokenPollPath      = "/api/v1/deviceToken/poll"
	DeviceTokenRefreshPath   = "/api/v1/deviceToken/refresh"
	JobTokenPath             = "/api/v1/me/jobToken"
	JobTokenRefreshPath      = "/api/v1/jobToken/refresh"

	// ChatPath is the SSE chat endpoint. The query asks for the LLM result
	// to be fetched, names the common agent, and says Encode=1 to use the
	// custom base64 body codec.
	ChatPath = "/algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1"

	// ListModelsPath is the live model listing endpoint.
	ListModelsPath = "/algo/api/v2/model/list?Encode=1"

	// AccountUsagePath is the desktop client's account usage endpoint.
	AccountUsagePath = "/sash/api/v2/me/usage"
)

// ProviderKey is how magpie names this subscription.
const ProviderKey = "qoder"

// CNProviderKey is how magpie names the Qoder CN subscription; it is also the
// id of Qoder CN's CLI agent.
const CNProviderKey = "qoder-cn"

// CNClientID is the device-flow client id Qoder CN's own CLI
// (@qodercn-ai/qoderclicn) sends to qoder.cn in production. The global CLI
// carries the same id; the desktop id magpie uses on qoder.com is not known to
// be registered on qoder.cn, so Qoder CN signs in as its CLI does.
const CNClientID = "e883ade2-e6e3-4d6d-adf7-f92ceff5fdcb"

// Site is one Qoder deployment: the sign-in page, the account API and the
// inference gateway, and the client id the sign-in names.
type Site struct {
	ID       string // the provider id magpie keeps its accounts under
	Name     string
	Web      string // device-flow authorization page host
	OpenAPI  string // token poll, job token, refresh, user info and usage
	API      string // model list and chat
	ClientID string
	// RedirectURI goes on the authorization page; Qoder CN's CLI sends none.
	RedirectURI string
}

// Global is qoder.com, what magpie's Qoder subscription has always used.
var Global = &Site{ID: ProviderKey, Name: "Qoder", Web: DeviceFlowHost, OpenAPI: OpenAPIHost,
	API: APIHost, ClientID: ClientID, RedirectURI: RedirectURI}

// CN is qoder.cn, with the hosts Qoder CN's CLI builds in for its "cn" build:
// openapi.qoder.com.cn for accounts and gateway.qoder.com.cn for inference.
var CN = &Site{ID: CNProviderKey, Name: "Qoder CN", Web: "https://qoder.cn",
	OpenAPI: "https://openapi.qoder.com.cn", API: "https://gateway.qoder.com.cn", ClientID: CNClientID}

// SiteOf is the site a provider id or a credential's Site names; anything but
// Qoder CN, including none, is the global site.
func SiteOf(id string) *Site {
	if id == CNProviderKey {
		return CN
	}
	return Global
}

// ChatURL is the full chat endpoint on the site's inference host.
func (s *Site) ChatURL() string { return s.API + ChatPath }

// ChatURL is the global site's chat endpoint.
func ChatURL() string { return Global.ChatURL() }
