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

import (
	"fmt"
	"net/url"
	"strings"
)

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

// NormalizeVPCEndpoint validates an optional enterprise access domain and
// returns its canonical base URL. Empty means the public Qoder service.
func NormalizeVPCEndpoint(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid Qoder enterprise access domain")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", fmt.Errorf("Qoder enterprise access domain must use http or https")
	}
	if u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("Qoder enterprise access domain must not include a path, query, or credentials")
	}
	return strings.TrimRight(u.Scheme+"://"+u.Host, "/"), nil
}

// BaseURL returns the enterprise base URL, or fallback for a public account.
func BaseURL(vpcEndpoint, fallback string) string {
	if strings.TrimSpace(vpcEndpoint) != "" {
		return strings.TrimRight(vpcEndpoint, "/")
	}
	return fallback
}

// ChatURLFor returns the per-account inference endpoint.
func ChatURLFor(vpcEndpoint string) string { return BaseURL(vpcEndpoint, APIHost) + ChatPath }
