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
// This package supports the global Qoder client.
package qoder

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

// ChatURL is the full chat endpoint on the inference host.
func ChatURL() string { return APIHost + ChatPath }
