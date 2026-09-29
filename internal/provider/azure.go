package provider

import (
	"context"
	"net/url"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
)

// Azure OpenAI is reached at the user's own resource, never at one host
// magpie knows: https://<resource>.openai.azure.com (or the
// cognitiveservices.azure.com and services.ai.azure.com hosts newer
// resources are given). magpie asks it on the v1 API under /openai/v1 —
// Azure's generally available one since August 2025 — where chat
// completions, Responses and the model list sit at the paths OpenAI's do,
// the model in the body is the deployment's name, and no api-version is
// wanted. The classic /openai/deployments/<deployment>/…?api-version=
// paths are not used: every endpoint, however it was pasted, is asked at
// /openai/v1. The key goes in api-key, never as a Bearer: a Bearer on
// Azure is an Entra ID token.

// AzurePreset is the preset's id.
const AzurePreset = "azure"

// azureHosts end the hosts an Azure OpenAI resource is served at.
var azureHosts = []string{".openai.azure.com", ".cognitiveservices.azure.com", ".services.ai.azure.com"}

// AzureHost reports whether host is an Azure OpenAI resource's.
func AzureHost(host string) bool {
	host = strings.ToLower(host)
	if h, _, ok := strings.Cut(host, ":"); ok {
		host = h
	}
	for _, s := range azureHosts {
		if strings.HasSuffix(host, s) && len(host) > len(s) {
			return true
		}
	}
	return false
}

// IsAzure reports whether the provider is Azure OpenAI: made from its
// preset, or at a resource's host.
func (p Provider) IsAzure() bool {
	if p.Preset == AzurePreset {
		return true
	}
	for _, u := range []string{p.Chat, p.Responses} {
		if u != "" && AzureHost(HostOf(u)) {
			return true
		}
	}
	return false
}

// AzureBase is the v1 API base of the resource an endpoint names, as it
// was pasted from the portal or another app — the bare resource
// (https://r.openai.azure.com), …/openai, …/openai/v1, a classic
// deployment's URL with its api-version, or only the resource's name —
// and ok false when it names no Azure resource.
func AzureBase(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", false
	}
	host := strings.ToLower(u.Host)
	if !strings.ContainsAny(host, ".:") {
		// the resource's name alone, as the portal lists it
		host += ".openai.azure.com"
	}
	if !AzureHost(host) {
		return "", false
	}
	return "https://" + host + "/openai/v1", true
}

// azureEndpoints puts an Azure provider's chat completions and Responses
// on its resource's v1 API: both are served there, whichever of them the
// user or the app it came from gave. A provider at a host that is no
// resource's (an API Management gateway in front of one, or a test's
// server) keeps its URLs.
func (p *Provider) azureEndpoints() {
	given := p.Chat
	if given == "" {
		given = p.Responses
	}
	if given == "" {
		return
	}
	if h := HostOf(given); strings.HasPrefix(h, "localhost") || p.Preset != AzurePreset && !AzureHost(h) {
		return
	}
	if base, ok := AzureBase(given); ok {
		p.Chat, p.Responses = base, base
	}
}

// asAzure makes a provider brought from another app the Azure preset's,
// its endpoints on the resource's v1 API.
func asAzure(p Provider) Provider {
	pr := Preset(AzurePreset)
	p.Preset, p.Icon, p.Catalog, p.Website, p.KeysURL = pr.ID, pr.Icon, pr.Catalog, pr.Website, pr.KeysURL
	if p.ID == "" || p.ID == "default" {
		p.ID = pr.ID
	}
	if n := strings.ToLower(p.Name); n == "" || n == "default" || n == "azure" {
		p.Name = pr.Name
	}
	p.azureEndpoints()
	return p
}

// azureDeploymentsVersion is the api-version the data plane's list of
// deployments is asked with: later versions dropped the list, this one
// still answers a key.
const azureDeploymentsVersion = "2022-12-01"

// azureModels lists the resource's deployments — their names are the model
// ids its v1 API takes — and, where the data plane won't list them, the
// models the v1 API lists, which are the deployments' ids only when each is
// named after its model. Asked with the key in api-key alone.
func (p Provider) azureModels(ctx context.Context) ([]catalog.Model, string, error) {
	base := strings.TrimRight(p.Chat, "/")
	if base == "" {
		base = strings.TrimRight(p.Responses, "/")
	}
	root := strings.TrimSuffix(base, "/v1")
	headers := map[string]string{}
	for k, v := range AuthHeaders(p, Chat) {
		headers[k] = v
	}
	for k, v := range p.Headers {
		headers[k] = v
	}
	var errs []string
	for _, u := range []string{root + "/deployments?api-version=" + azureDeploymentsVersion, base + "/models"} {
		ms, err := catalog.FetchURL(ctx, u, "", false, headers)
		if err == nil {
			return ms, base, nil
		}
		errs = append(errs, err.Error())
		if ctx.Err() != nil {
			break
		}
	}
	return nil, "", errorf("%s — type your deployments' names in as model ids", strings.Join(errs, "; "))
}
