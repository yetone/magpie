package provider

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Kiro's accounts on @magpie-community/opencode-kiro-auth:
//   - kiro-cli's or the IDE's own sign-in goes as a marker ({source:
//     "kiro"}): the plugin reads it where they keep it, as the built-in did;
//   - an account magpie signed in (a home holding kiro-auth-token.json)
//     goes whole, its tokens the plugin's to refresh from then on; the
//     home stays, and moving back writes the plugin's tokens into it;
//   - a key saved on the provider goes as an API key account, first (with
//     a key, the built-in used nothing else), and comes off the provider
//     while the plugin has it.
func init() {
	movers["kiro"] = &mover{
		pkg:    "@magpie-community/opencode-kiro-auth",
		min:    "0.1.36", // 0.1.36: with overage on, the plan's credits are a window of their own, shown only (12589.24 used of a 10000 Power plan is 10000 / 10000), and the overage (currentOverages) is the window that stops the account, against the overage cap, which counts the overage alone; the cap was taken for the month's whole limit, so Kiro Power showed 12589.24 / 37500 (wizcheu on X); 0.1.35: each sign-in way, question and choice gives its words in zh, zh-TW, ja and de (labels, messages, hints) and the API key field a ksk_… placeholder (mahiro233 on Discord); 0.1.34: each model carries the credit multiplier Kiro lists (rateMultiplier) as its rate; 0.1.33: an account Kiro puts in the IDE's create_response_api experiment (its featureConfiguration, read as Kiro IDE 1.4.121 reads it: once per sign-in, kept 15 minutes) is asked on CreateResponse (/v1/responses) with the IDE's body and headers, as the IDE asks it, and every other account stays on generateAssistantResponse; a Builder ID account was answered 429 USER_REQUEST_RATE_EXCEEDED on generateAssistantResponse while the IDE, on CreateResponse, was answered 11s later (ThirdTimes, plugins#842, plugins#848); 0.1.32: a 429 throttle (USER_REQUEST_RATE_EXCEEDED, CREDIT_CONSUMPTION_RATE_EXCEEDED, SERVICE_REQUEST_RATE_EXCEEDED) is waited out and tried again as the Kiro IDE does, up to 3 tries after 1s and 2s or the wait Kiro says (up to 20s), each try counted in amz-sdk-request and x-amzn-kiro-attempt under one invocation id, so a message sent right after another isn't answered 429 where the IDE rides the throttle out (onemezao on Discord, plugins#842, plugins#848); 0.1.31: a chat call goes as the Kiro IDE 1.4.121 sends it, not as kiro-cli: the IDE's AWS SDK user agent with one machine id per install (no random suffix), origin AI_EDITOR, x-amzn-kiro-client-attribution kiro-ide and its attempt header, and one conversationId per magpie session, since Kiro was limiting the CLI's identity (USER_REQUEST_RATE_EXCEEDED) on an account where the IDE worked; reasoning sent with its signature in one event keeps the signature (onemezao on Discord); 0.1.30: a 429 for an account's request rate (USER_REQUEST_RATE_EXCEEDED) says Kiro is limiting this account's requests, to wait a minute, and that one persisting while the Kiro app works is for Kiro support, with the request ID; with no wait from Kiro it says Retry-After 30s, so magpie stops asking the one account again after 1, 2 and 4s (onemezao on Discord, 42s then the 429); 0.1.29: what Kiro's meteringEvents say a request cost (summed over a hosted web search's turns) goes on as usage.credit in the last message_delta or the message's usage, for magpie's Usage page to read once the gateway takes it on /v1/messages (#4328, plugins#823, caozhenyuan); 0.1.28: an IAM Identity Center account enabled in both of its regions (us-east-1, eu-central-1) uses each region's profile, month of credits and overage cap; a region used up rests and the request goes once to the other, and the Usage card shows each region, so the account stays in rotation while either region has credits (plugins#820, caozhenyuan); 0.1.27: a 429 carries the wait Kiro said (x-amzn-kiro-ratelimit-retry-after, x-amz-retry-after, retryAfterMilliseconds or Retry-After) as Retry-After, so magpie waits that long instead of asking the one account again after 1, 2 and 4s (onemezao on Discord, USER_REQUEST_RATE_EXCEEDED); 0.1.25: bonus credits (a first upgrade's 500 trial credits, a promotion's) show on the Usage card as windows of their own, shown only, with when they lapse, and a month used up while a bonus has credits left is shown only too, since Kiro spends the plan's credits first (plugins#602, GiantGKL); 0.1.24: a conversation past the model's window is a 400 "input is too long for the model's context" however Kiro says it (an exception in its event stream was a 502, a reply stopped with MODEL_CONTEXT_WINDOW_EXCEEDED was finished), so magpie asks a member with room or tells the agent to compact (ergou0954_54426 on Discord); 0.1.23: thinking is asked for only when the request's thinking is enabled or adaptive; an effort with none, or with thinking disabled (Claude Code's title requests, a turn with thinking off), is sent no budget and no adaptive fields at any level (plugins#538, zengxiaosong); 0.1.22: a model whose Kiro catalog lists effort levels with adaptive thinking is sent the level as Kiro IDE sends it (additionalModelRequestFields), so xhigh and max stay apart, and a system message ending the conversation (Claude Code's environment context) goes as a turn before the question, not into it (plugins#538, zengxiaosong); 0.1.21: the browser sign-in asks first for AWS Builder ID, IAM Identity Center (Start URL and region) or Kiro's page; the AWS ways open AWS directly, so a magpie on a server or in Docker takes one pasted callback address instead of two, and a sign-in left behind frees its port when the next starts (plugins#507, SemonCat); 0.1.20: a tool whose input_schema has no root type reaches Kiro as an object, and a client's web_search server tool runs as Kiro's own search, at most 10 a request (plugins#492, SemonCat); 0.1.19: a usage read whose sign-in Kiro refused marks the account signed out, so its rows offer Sign in again, and a clean read takes the mark off (blue12345655 on Discord); 0.1.18: an image over Kiro's 5 MiB Base64 limit (a 4.2 MB view_image PNG is 5.6 MB as Base64) is shrunk on the request, or replaced by a note, so the conversation isn't stuck on IMAGE_SIZE_EXCEEDED (plugins#428); 0.1.17: the Free trial and Credits windows carry their credits used and the limit, so the card's Left shows the credits left (plugins#408); a reply with no token counts says its cache is unknown (magpie_cache_unknown), not 0: Kiro caches but counts no tokens (zeewell on X); 0.1.15: an API key is used as kiro-cli uses it: GetProfile in us-east-1 then eu-central-1, and with no profile found the key goes on in us-east-1; its models are the control plane's ListAvailableModels and no profile is named in its calls (Hollowhere: Auto alone, every request "Kiro didn't take the API key: 400"); 0.1.14: each account is named by its email and the way it was signed in to, "email (Google)", "email (GitHub)", not "email (slot)" (plugins#309); 0.1.13: two sign-ins of one email through different login providers (Google, GitHub, Builder ID) stay two accounts (plugins#309); 0.1.12: a request for JSON output (output_config.format) answers in JSON, its schema in the system prompt as the built-in; 0.1.11: a thinking model offers xhigh as its own level (50k), so Claude Code's xhigh isn't routed as max (plugins#120); 0.1.10: the caller's prompt-cache marks reach Kiro as cachePoints (plugins#110)
		agents: []string{"kiro"},
		out: func() ([]Moving, error) {
			var out []Moving
			key := kiroKey()
			if key != "" {
				// named as the built-in named it, which per-account proxies
				// and headers are keyed on
				out = append(out, Moving{User: "Kiro API key", First: true, On: true, Auth: map[string]any{"type": "api", "key": key, "accountId": "Kiro API key"}})
			}
			for _, l := range kiroLogins() {
				// with a key, the built-in used nothing else: the accounts
				// go along off, the key alone in use
				m := Moving{User: l.User, First: l.Active && key == "", On: l.On && key == "", Lapsed: l.Lapsed != "", Plan: l.Plan}
				if l.Home == "" {
					m.Own = true
					m.Auth = map[string]any{"type": "oauth", "source": "kiro", "access": "", "refresh": "", "expires": 0, "accountId": l.User}
				} else {
					a, err := kiroOut(l.Home, l.User, l.Plan)
					if err != nil {
						return nil, err
					}
					m.Auth = a
				}
				out = append(out, m)
			}
			return out, nil
		},
		back: func(ls []savedLogin, user string, auth map[string]any) ([]savedLogin, string, error) {
			switch {
			case str(auth["type"]) == "api":
				// the key goes back onto the provider (give, or settle for
				// one saved in the plugin since)
				return ls, "", nil
			case str(auth["source"]) != "":
				// kiro-cli's or the IDE's: nothing of it was the plugin's
				return ls, firstNonEmpty(user, str(auth["accountId"])), nil
			}
			if str(auth["access"]) == "" {
				return ls, "", errors.New("Kiro: an unreadable plugin sign-in")
			}
			if user == "" {
				user = str(auth["accountId"])
			}
			i := backInto(&ls, "kiro", user)
			if ls[i].Home == "" {
				home, err := newKiroHome()
				if err != nil {
					return ls, "", err
				}
				ls[i].Home = home
			}
			s := kiroSaved{AccessToken: str(auth["access"]), RefreshToken: str(auth["refresh"]), AuthMethod: "social",
				Provider: str(auth["loginProvider"]), Region: str(auth["region"]), ProfileArn: str(auth["profileArn"]),
				ClientID: str(auth["clientId"]), ClientSecret: str(auth["clientSecret"])}
			if str(auth["method"]) == "idc" {
				s.AuthMethod = "IdC"
			}
			if ms, _ := auth["expires"].(float64); ms > 0 {
				s.ExpiresAt = time.UnixMilli(int64(ms)).UTC().Format(time.RFC3339Nano)
			}
			b, err := json.MarshalIndent(s, "", "  ")
			if err != nil {
				return ls, "", err
			}
			if err := writePrivate(filepath.Join(ls[i].Home, kiroTokenFile), b); err != nil {
				return ls, "", err
			}
			if p := str(auth["plan"]); p != "" {
				ls[i].Plan = p
			}
			ls[i].Lapsed, ls[i].Seen = "", time.Now().UTC().Truncate(time.Second)
			kiroForgetHome(ls[i].Home)
			return ls, ls[i].User, nil
		},
		take: func() (json.RawMessage, error) {
			k := kiroKey()
			if k == "" {
				return nil, nil
			}
			return json.RawMessage(jsonText(k)), setKiroKey("")
		},
		settle: func(auths map[string]map[string]any) error {
			for _, a := range auths {
				if k := str(a["key"]); str(a["type"]) == "api" && k != "" && kiroKey() == "" {
					return setKiroKey(k)
				}
			}
			return nil
		},
		give: func(kept json.RawMessage) error {
			var k string
			if json.Unmarshal(kept, &k) != nil || k == "" || kiroKey() != "" {
				return nil
			}
			return setKiroKey(k)
		},
	}
}

// kiroOut is the account magpie signed in in home, as the plugin keeps one.
func kiroOut(home, user, plan string) (map[string]any, error) {
	b, err := os.ReadFile(filepath.Join(home, kiroTokenFile))
	if err != nil {
		return nil, err
	}
	var t kiroSaved
	if json.Unmarshal(b, &t) != nil || t.AccessToken == "" {
		return nil, errors.New("Kiro: " + user + "'s sign-in can't be read")
	}
	a := map[string]any{"type": "oauth", "access": t.AccessToken, "refresh": t.RefreshToken, "expires": 0,
		"method": "social", "loginProvider": t.Provider, "region": firstNonEmpty(t.Region, "us-east-1"),
		"profileArn": t.ProfileArn, "accountId": user}
	if e, err := time.Parse(time.RFC3339Nano, t.ExpiresAt); err == nil {
		a["expires"] = e.UnixMilli()
	}
	// as readKiroFile tells them apart
	if strings.EqualFold(t.AuthMethod, "IdC") && t.ClientID != "" {
		a["method"] = "idc"
	}
	if t.ClientID != "" {
		a["clientId"], a["clientSecret"] = t.ClientID, t.ClientSecret
	}
	if plan != "" {
		a["plan"] = plan
	}
	return a, nil
}

// setKiroKey sets (or, "", clears) the key saved on the Kiro provider.
func setKiroKey(k string) error {
	f, err := read()
	if err != nil {
		return err
	}
	for i := range f.Providers {
		if f.Providers[i].ID == "kiro" {
			if f.Providers[i].Key == k {
				return nil
			}
			f.Providers[i].Key = k
			return store(f)
		}
	}
	if k == "" {
		return nil
	}
	f.Providers = append(f.Providers, Provider{ID: "kiro", Key: k})
	return store(f)
}

// kiroForgetHome drops what was cached of home's sign-in, rewritten.
func kiroForgetHome(home string) {
	kiroAuthCache.Lock()
	delete(kiroAuthCache.m, "\x00"+home)
	kiroAuthCache.Unlock()
}
