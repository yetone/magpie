package provider

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/yetone/magpie/internal/qoder"
)

// Qoder's and Qoder CN's accounts on @magpie-community/opencode-qoder-auth,
// which serves both sites as providers "qoder" and "qoder-cn", as its device
// sign-in keeps one: the job token pair as access and refresh (the refresh
// token is spent on use, so it moves rather than being copied), the device
// token pair and machine id beside them. A Qoder CN account that chats on
// its device token (DeviceChat) says so, its device pair as access and
// refresh.
func init() {
	for _, id := range []string{"qoder", QoderCNID} {
		movers[id] = qoderMover(id)
	}
}

func qoderMover(id string) *mover {
	return &mover{
		pkg:    "@magpie-community/opencode-qoder-auth",
		min:    "0.2.64", // 0.2.64: a model the account's listing has off while its usage says no credits are left is a 429 usage limit, so a routing group fails over and rests the account, not the request's 400 "unknown or disabled model" (plugins #880, zane668); the Windows check-in finds the runtime qodercli 1.21.2 caches as one file, runtime-info-win32-<arch>-<hash>.exe (plugins #879, ArmedHelicopter); 0.2.63: a request's max_tokens is never sent above the model's ceiling (QoDuck lmodel 131072, a listing entry's max_output_tokens, or what Qoder's "Range of max_tokens should be [1, N]" named, asked again once at it), never raised when asked lower, and that refusal is the request's 400 with Qoder's words, not a 502 that rests every account (plugins #822, Gruu6y); 0.2.62: a streamed request Qoder queues waits up to Qoder's hour, kept alive with comments (plugins #807); 0.2.61: the business record names the model, as qodercli 1.1.82 sends it (business.model_key), so Qoder and Qoder CN bill the model chosen, not Auto (plugins #692, #638); 0.2.60: a 429 with no quota in it shows Qoder's status, code and own reply; 0.2.59: usage carries each pool's count (amount, limit, unit) beside its share, Total credits too, and the plan's Credits window resets when the plan refreshes (expiresAt), so Routing and the Usage card say "1,183 / 5,400 credits · 22% left" as Qoder's own panel counts the plan and the resource pack, not a bare share (jzing.draw on Discord); 0.2.58: loads on OpenCode 2.x through a default {id, setup} (plugins #657); 0.2.57: QwenWork's gateway calls (device poll, account-context, wallets, device token refresh) name the client as QwenWork 1.2.1 does (User-Agent qoderwork/1.2.1, X-Request-Id, X-QwenWork-*), and a biz account's device token is refreshed for its own team (target biz, team_id) with the client id and redirect_uri, not always as personal; the model list's 403 "User identity could not be verified" says to sign in again (tnt1223 on Discord); 0.2.56: a Lingma 1.0 dedicated domain (404 at /algo/api/v1/version) signs in with the account Qoder CN IDE holds on this computer (its SharedClientCache cache/id and cache/user), chats on the domain itself as the IDE's worker signs it (Cosy-ClientType 0, version 2.5.18) with the IDE's models qmodel_latest, dashscope_qmodel and q36fmodel, and takes the IDE's renewed token again (Milktea on Discord); 0.2.55: a request that names no effort and a max_tokens of 1024 or less does not think at the model's default, the same rule as WorkBuddy 0.2.15 (plugins #643); 0.2.54: a request's model_config is built as Qoder's own client builds it (key, display_name, format, is_vl, is_reasoning, source, max_input_tokens), not sent as the model-list entry with its minimal_version, strategies and prices, which Qoder CN billed DeepSeek-Flash (dfmodel) as Auto beside (likely cause, not confirmed); a model with no key of its own, or key auto, is refused, never sent as Auto (plugins #638, square-235); 0.2.53: Qoder CN signs in to an enterprise dedicated domain (企业版专属域, any https host such as <corp>.rdc.aliyuncs.com, as Qoder IDE's settings take it): a Qoder CN 2.0 domain uses the VPC protocol on its -openapi/-gateway hosts, and a Lingma 1.0 one (404 at /algo/api/v1/version) is refused with what it is (Milktea on Discord); 0.2.52: a model list Qoder answers with only the free models on (every paid model enable:false, what Qoder CN gives a caller it doesn't take for the account) renews the job token once and is read again, so the account and its agents see every model, not the free Flash alone (star21321321124_93024 on Discord); 0.2.51: a native tool call the model never names no longer fails the turn: with nothing said before it the request is asked once more, then a 502 the gateway retries or fails over; after the answer began it is dropped and the answer ends tool_calls or stop, its arguments never sent (plugins #579); 0.2.50: a phone-signed account is named by its phone, not its uuid (plugins #487); 0.2.49: a model Qoder serves that an account's listing has off (an enterprise account saw only the free dfmodel and qfmodel on) renews the job token once and reads the list again before "unknown or disabled model" (plugins #484); 0.2.48: a job token Qoder refuses as "Login expired" is renewed once and the call asked again, where an international account met bursts of those 403s about every 24h (plugins #474); 0.2.47: every model a site ships says whether it takes images (plugins #470); 0.2.46: credits used up (or a free account's "0 credits left") leave the free models (0x in the account's list) counted out of the credit windows (notModels), so routing still serves them, as Qoder serves free models after the credits run out (coeo91 on Discord); 0.2.45: a free account (personal_standard) with no credits in any pool shows "0 credits left" instead of no credits at all, as Qoder's client shows it 0/0 (Ariel on Discord); 0.2.44: a plan used up beside add-on credits left is no account used up: the plan, add-on and shared pools Qoder spends in turn are counted together as Total credits, each shown beside it (plugins #454, chansee97); 0.2.43: the daily check-in is asked again as Qoder's 10:00 (UTC+8) campaign opens (sdytman on Discord); 0.2.42: an earlier turn's thinking goes back in reasoning_content, as Qoder's client sends it (uk.jent on Discord); 0.2.41: a chat carries the headers Qoder.app 0.4.3 puts on its chat, not the machine id as Cosy-ClientIp and -MachineToken: no ClientIp, the device identity's MachineToken and MachineType when a check-in has read it, and Cosy-MachineHostname; the signed request id is a dashed UUID; an ia32 machine is ia32_<platform> (plugins #443, unifilitence); 0.2.40: a queued request gives way at once when magpie has another candidate; 0.2.39: a call Qoder queues (10605) has its held-open stream closed before the wait, and each resend is a new request_id and chat_record_id in the same request_set_id, as Qoder.app's client resends (plugins #427); 0.2.38: a reply names the model as the model-list entry it was sent with (its display name), not the slot id, streaming and not (plugins #412); 0.2.37: a QwenWork personal account's usage shows the credits left as its balance, with the daily, monthly and long-term pools from its wallets, as QwenWork's client reads them, instead of "the account reports no credits" (plugins #385); 0.2.36: tool calls written in the text are read as calls (plugins #323); 0.2.35: an untyped object oneOf goes as type "object" (plugins #378); 0.2.34: QwenWork (plugins #319); 0.2.33: a task's business.name is the prompt's first words, as Qoder.app 0.4.3 names it, not "magpie session" (plugins #354); 0.2.32: a check-in asked with this machine's own device identity and offered nothing says so, and that Qoder desktop can claim it, instead of a bare inactive (plugins #304); 0.2.31: every call of one prompt's tool loop is one Qoder task (one request_set_id, business.id and begin_at; stage start, then processing), as Qoder.app's client sends it, and a harness's session id names the session (plugins #330); 0.2.30: Qoder CN's daily check-in sends Qoder CN's own device identity (its runtime-info, environment 0) when Qoder CN desktop or CLI is installed, and says to install it when nothing is offered without one (#2911, plugins #304); 0.2.28: a request Qoder queues (10605) waits its turn in Qoder's queue, asking its queue status as Qoder's client does, and is sent again, for up to 4 minutes, before the 503 (wallance on Discord); 0.2.27: a check-in on a computer without Qoder's runtime-info helper settles for the day with what to install, instead of retrying all day (plugins #304); 0.2.26: a Codex request marks its prompt for Qoder's cache as Qoder's client does, so the Ultimate model's cache hits are no longer 0% (plugins #290), and the intl check-in finds a machine-wide Windows install's runtime info under Program Files; 0.2.25: the international sign-in sends no redirect_uri, so a Mac with Qoder.app installed finishes in the browser tab instead of being handed to the desktop app (plugins #276); 0.2.24: Qoder's 400 "Error in upstream response" (the model's own vendor failing) is a 502, so magpie asks again or falls back, not the client's fault (SlouchyQuill507 on Discord); 0.2.23: Cosy-Version claims the desktop app's worker runtime, 1.1.69, beside its desktop client type and machine identity (plugins #281); 0.2.22: a tool result whose call lost its id answers that call, not an orphan Qoder refuses (plugins #277, #278); 0.2.21: Qoder CN signs in with a personal access token, and a PAT account gets the daily check-in; 0.2.20: unnamed tool-call fragments stay out of the chat history; 0.2.19: a model list that can't be read is a 502, so a group falls back to its next member (plugins #249); 0.2.18: a 10605 trimmed or wrapped is the model's queue busy, not a lapsed sign-in (plugins #185); 0.2.17: the daily check-in through auth.checkin (plugins #179); 0.2.16: a tool call written into the text that can't be read, or that the stream ends inside, ends the turn as an error (a 502, or the stream's error frame with no finish), never as the answer (plugins #132); 0.2.14: Qoder's 403 code 10605 (the model's queue busy) is a 503 with Retry-After, not a lapsed sign-in; quota and credit codes (110, 112–119, 122) are 429, refusals of the request (406, 416, 430, 80411, 80412, 100400, 100401, 100403) are 400, and only 105 or no code is "sign in again" (plugins #98); 0.2.13: models are listed from Qoder's app scene first, so its new default Fast 0.8× shows (plugins #97); 0.2.12: a chat Qoder refuses keeps Qoder's own reason in the error; 0.2.11: a Qoder CN enterprise VPC account keeps its model list and renewal mark apart from a public account with the same uid (plugins#40); international check-in finds the device identity runtime Qoder CLI caches on macOS and Linux and the Linux desktop installs in /opt/Qoder (ARNO on Discord); sends Qoder's native device identity, without which the daily 100 credits aren't listed (#1268); Qoder CN enterprise VPC (0.2.8); the daily credits claimed through the account's fetch (ARNO on Discord); Qoder CN as "qoder-cn", its device-token chat as the built-in's; each model's credit rate; the sign-in renewed ahead of time through auth.refresh; token usage read; each model's context the largest window Qoder offers it (#722); the window a request is sent holds the reply's max_tokens (#700)
		agents: []string{id},
		out: func() ([]Moving, error) {
			// nothing else writes the pairs while they are read
			ls := qoderLoginsOf(id)
			qoderMu.Lock()
			defer qoderMu.Unlock()
			var out []Moving
			for _, l := range ls {
				c, ok := qoderSaved(l.saved)
				if !ok {
					continue
				}
				out = append(out, Moving{User: l.User, First: l.Active, On: l.On, Lapsed: l.Lapsed != "", Plan: l.Plan, Auth: map[string]any{
					"type":          "oauth",
					"access":        c.Token,
					"refresh":       c.RefreshToken,
					"expires":       c.ExpiresAt,
					"accountId":     l.User,
					"uid":           c.UID,
					"email":         c.Email,
					"name":          c.Name,
					"machineId":     c.MachineID,
					"deviceToken":   c.DeviceToken,
					"deviceRefresh": c.DeviceRefresh,
				}})
				if c.DeviceChat {
					out[len(out)-1].Auth["deviceChat"] = true
				}
			}
			return out, nil
		},
		back: func(ls []savedLogin, user string, auth map[string]any) ([]savedLogin, string, error) {
			access, uid := str(auth["access"]), str(auth["uid"])
			if access == "" || uid == "" {
				return ls, "", errors.New(qoder.SiteOf(id).Name + ": an unreadable plugin sign-in")
			}
			if user == "" {
				user = firstNonEmpty(str(auth["accountId"]), str(auth["email"]), uid)
			}
			i := backInto(&ls, id, user)
			c, _ := qoderSaved(ls[i])
			c.UID, c.Token, c.RefreshToken, c.ExpiresAt = uid, access, str(auth["refresh"]), num(auth["expires"])
			c.Email = firstNonEmpty(str(auth["email"]), c.Email)
			c.Name = firstNonEmpty(str(auth["name"]), c.Name)
			c.MachineID = firstNonEmpty(str(auth["machineId"]), c.MachineID)
			c.DeviceToken = firstNonEmpty(str(auth["deviceToken"]), c.DeviceToken)
			c.DeviceRefresh = firstNonEmpty(str(auth["deviceRefresh"]), c.DeviceRefresh)
			c.DeviceChat = auth["deviceChat"] == true
			b, err := json.Marshal(c)
			if err != nil {
				return ls, "", err
			}
			ls[i].Auth, ls[i].Lapsed, ls[i].Seen = b, "", time.Now().UTC().Truncate(time.Second)
			return ls, ls[i].User, nil
		},
	}
}

// num is a JSON number read into any (a float64, or an int64 before it
// was written out) as an int64.
func num(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	}
	return 0
}
