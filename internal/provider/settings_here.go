package provider

import (
	"reflect"
	"sync"
)

// A provider's settings are everything about it but its keys and its
// place in the list: which models are ticked, routing, limits and rests,
// headers, the proxy, prices, balances, per-account caps, whether it is
// on. Sync can keep them to each computer (davsync's
// Config.ProviderSettingsHere; StudyingPython on X: "只希望同步供应商的key
// 和列表，不希望同步供应商设置"), while the list and each provider's keys
// sync. What goes with a key syncs with it: its name, its URLs and API,
// the header it is sent in, the account behind it (AWS and Vertex,
// Zhipu's team), a client certificate and the server's pin, and a secret
// header (SecretHeader) among the headers. A provider new on a computer
// comes with the settings it was made with.
//
// settingFields names them; TestProviderFieldsSortedForSync fails on a
// field of Provider that is in neither it nor the fields that sync.
var settingFields = []string{
	// models
	"ListOff", "Models", "NoModels", "BestInFamily", "PickedFrom", "Delisted", "DelistedAt",
	"RefreshEvery", "AutoInclude", "Unlisted", "HiddenDrawers", "HiddenVideomakers", "Contexts", "Family",
	"AccountModels", "AccountModelsAll",
	// on or off
	"Off",
	// routing
	"Fallback", "Routing", "Sink", "UseFirst", "NoStandIn", "Affinity",
	// limits and rests
	"MaxConcurrency", "MaxAccounts", "AccountConcurrency", "AccountWeights", "QueueLimit", "QueueWait",
	"MaxRPM", "MaxImages", "QuotaRest", "QuotaResetAt", "RestCap", "LimitRestOff",
	"AccountCaps", "AccountWindowCaps", "AccountWindowAmounts", "CapStop", "CapCareful", "KeySpendCaps",
	// prices and balances
	"PriceRate", "CountFree", "Billing", "RateWindows", "KeyOwnBalances", "KeyPriceRates", "TopUpRate",
	"RateReadHours", "KeyRateFactors", "BalanceURL", "BalancePath", "BalanceMethod", "NoBalanceProbe", "BalanceToken",
	// requests
	"Headers", "ExtraBody", "ReplyHeaders", "PassHeaders", "Searches", "CodexAsSent", "ImageFilesOff",
	"UpstreamProviders", "PromptAsWritten", "AdditionalTools", "AdditionalToolsOff", "PassErrors", "Patience",
	"Socket", "NativeCompaction", "MagpieUserAgent", "PinUpstream", "Unredacted",
	// the connection
	"InsecureTLS", "HTTP1", "Resolve", "Proxy", "AccountProxies",
}

var settingIndex = sync.OnceValue(func() [][]int {
	t := reflect.TypeFor[Provider]()
	out := make([][]int, len(settingFields))
	for i, name := range settingFields {
		f, ok := t.FieldByName(name)
		if !ok {
			panic("provider: no field " + name)
		}
		out[i] = f.Index
	}
	return out
})

// WithoutSettings is p with no settings, its secret headers left: what is
// compared when they are this computer's own.
func WithoutSettings(p Provider) Provider {
	v := reflect.ValueOf(&p).Elem()
	secret := p.Headers
	for _, ix := range settingIndex() {
		f := v.FieldByIndex(ix)
		f.SetZero()
	}
	p.Headers = headersOf(secret, nil)
	return p
}

// WithSettingsOf is p with from's settings in place of its own, p's
// secret headers kept.
func WithSettingsOf(p, from Provider) Provider {
	v, w := reflect.ValueOf(&p).Elem(), reflect.ValueOf(from)
	secret := p.Headers
	for _, ix := range settingIndex() {
		v.FieldByIndex(ix).Set(w.FieldByIndex(ix))
	}
	p.Headers = headersOf(secret, from.Headers)
	return p
}

// headersOf is a new map of secret's secret headers and rest's others;
// nil when that is none.
func headersOf(secret, rest map[string]string) map[string]string {
	var out map[string]string
	add := func(k, v string) {
		if out == nil {
			out = map[string]string{}
		}
		out[k] = v
	}
	for k, v := range rest {
		if !SecretHeader(k) {
			add(k, v)
		}
	}
	for k, v := range secret {
		if SecretHeader(k) {
			add(k, v)
		}
	}
	return out
}
