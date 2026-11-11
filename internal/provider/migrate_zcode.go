package provider

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ZCode's accounts on @magpie-community/opencode-zcode-auth: the plan's
// key (else ZCode's session token) as the oauth entry's access, the rest
// the requests need as its refresh, as the plugin's own sign-in keeps
// them. Nothing here rotates: a key lasts until it is deleted.
//
// ZCode's own sign-in goes marked as ZCode's (source "zcode"), with a copy
// of what magpie reads now: the plugin, 0.1.2 on (the move installs no
// older), reads ZCode's own credentials in its place for each request, as
// the built-in does, so it follows ZCode's switches and the session token
// ZCode renews (the Start Plan).
func init() {
	movers["zcode"] = &mover{
		pkg:    "@magpie-community/opencode-zcode-auth",
		min:    "0.1.60", // 0.1.60: a coding plan key that can't be read for a network reason (ECONNRESET, a timeout) keeps the last key read, or answers a wall that magpie sends once and rests, rather than a thrown error retried about 8 times over six minutes, and free first no longer replays onto a coding plan whose usage read showed a window full (plugins#1020, takomitkm); 0.1.59: a reply the gift answered names its -Trial entry (GLM-5.3-Flash-Trial), so Usage, the sessions and the request log show free quota was spent when free first moved the plain model there (WJaunita45604 on X); 0.1.58: a Start Plan account ZCode serves fewer than three at a time is narrowed to what it serves for five minutes, and a request refused again with others of that account in flight counts toward no busy hold, so one agent with two in flight no longer holds every account (plugins#438, takomitkm); a -Trial entry is marked free, so a coding plan model's weekly or monthly limit (1310) rests that model and not the account's trial entries; 0.1.57: a Start Plan request the busy hold answered while in flight is not counted as a spent gift, so a -Trial model or free first is no longer rested for a minute with quota left (plugins#859, zxy20250120); 0.1.56: a Start Plan account has at most three requests in flight, the rest waiting in the plugin rather than sent to be refused, a busy cap (3008/3009/3010) is asked again twice, after 1s and 2s, as ZCode 3.17.2 does, and the 3-minute busy hold counts only a request refused again at its re-ask, its message saying what happened and what to do (star21321321124 on Discord); 0.1.55: no daily claim is posted while ZCode's client config asks its security check for claims (captcha.enabled), each plan saying to claim it in the ZCode app, and Start Plan requests carry the desktop's X-Title "Z Code@electron" and a per-turn x-query-id, as ZCode 3.17.2 sends (young02036 on Discord); 0.1.54: a busy hold also stops Start Plan requests already in flight (plugins#801); 0.1.53: a spent bucket's HTTP 200 ("exceed quota limit" 1005, 1113) says 429 on every path, so magpie rests that model and not the account (zane668, plugins#817); 0.1.52: a rate-limited claim or balance read holds that account, and only that account, until ZCode's wait is over, rather than every account asking again at once (kongbai01203, plugins#793); 0.1.51: a gift-only account (no GLM Coding Plan) lists only the models its Start Plan balance names, as ZCode offers it, not every model of the Start Plan config, so magpie no longer routes GLM-5.2 or GLM-5-Turbo to an account whose balance holds only GLM-5.3-Flash (young02036 on Discord); 0.1.50: a daily claim answered with code 1004 reads the account as not eligible for that plan now and asks again in half an hour, not a failed claim (kongbai01203, plugins#729); 0.1.49: a free-first request zcode.z.ai turned away (405/3012) no longer makes the -Trial entry answer a local "quota exhausted" 429 without asking ZCode, the local 429 keeps the words zcodeSpentWords reads at its end, and free first is a switch in the provider's editor (Can4ry_ on X). 0.1.48: the Start Plan request carries ZCode 3.17.2's prompt texts, not 3.14.3's under a 3.17.2 header, which zcode.z.ai turned away with 405/3012 while the app worked on the same accounts (jinpy666's #682; plugins#348: bitjerry, issennknight, jovial, supermc); 0.1.47: a free gift model (GLM-5.3-Flash-Trial) goes to ZCode as the registry spells it, as the app sends it, not the lowercase bucket name, and the plugin's own 429 says when ZCode answered 429 a minute ago (Discord reports); 0.1.46: every call goes as ZCode 3.17.2, whose Start Plan balance judges by app_version, and a sign-in whose only Start Plan starts later is taken, as ZCode counts it, not refused as "ended or never started" (sliyrainbow on Discord); the feed is asked in zh-CN or en-US only, as ZCode does; 0.1.45: the daily claim reads the marketing feed under 3.17.2's version header, since under 3.16.3 it answered empty and nothing was claimed from 11-03, and no longer reads billing/preview, which the 3.17 client dropped (plugins#676, #678, jinpy666); 0.1.43: a block page from zcode.z.ai's edge firewall holds every account's Start Plan and -Trial requests off zcode.z.ai for three minutes, answered at once with the same 405 and BlockedHint so the gateway still falls back, and free first stays on the coding plan meanwhile, so a blocked address is not knocked on by every account and session (frank6com, plugins #649); 0.1.42: a plain GLM model spends a live ZCode gift first by default, falling back to the coding plan; "freeFirst": false in the plugin's or provider's options keeps the plan first (sgfvdsg86265 on X); 0.1.41: an account the daily claim found nothing for is asked again in half an hour rather than settled for the day, so a gift plan ZCode drops into its feed later (the weekend plan from 19:00 Beijing) is still claimed (plugins#600); 0.1.40: maxAccounts 2 is declared, so magpie sends requests to the first two ZCode accounts of the list and holds the others in reserve, since three at once from one address got it refused with a 405 (01huadalang on Discord); 0.1.39: the daily claim also reads the ZCode app's claim banners (GET /api/v1/marketing/touch, each claim_zcode_plan button), so a banner plan billing/preview never lists (ZCODE WEEKEND BUILD) is claimed and named on the card (plugins#503, kongbai01203); 0.1.38: freeFirst, an option (Plugins › Options or provider.zcode.options, off by default), lets a plain model spend a dual account's live gift first, under the name the gift lists, and replays any gift failure on the coding plan in the same request (plugins#505, jinpy666); 0.1.37: an account with no gift plan to claim, which ZCode's billing/preview answers {"code":0,"msg":""} with no data at all, is "no check-in event now" in the daily check-in, not "Check-in failed · no plan list in the reply" (hykql on Discord); 0.1.36: beside a GLM Coding Plan a gift bucket is named by its model first, as ZCode's "Today's balance" lists it, then its plan ("GLM-5.3-Trial · ZCODE GLOBAL BUILD"), since the ring and account row cut "ZCODE GLOBAL BUI…" short and the Start Plan read as missing (plugins#504, Danbuth); 0.1.35: the daily check-in (off until the user turns ZCode on in Settings → Daily check-in) claims each account's ZCode gift plans that billing/preview lists, judged by the claim reply's code (0 claimed, 1003 claimed before, 3007 captcha: claimed in the ZCode app), an unreadable reply failed, never claimed (plugins#503, kongbai01203); 0.1.34: a gift plan whose starts_at is still to come (a Start Plan taking effect at 23:00 today) is named on the card with its start ("ZCode gift plan not started yet") and gets no requests before it, and beside a GLM Coding Plan every gift bucket carries its plan's name ("Start Plan · GLM-5.1-Trial") (windy on Discord, plugins 3b3209e); 0.1.33: ZCode 3.16's key-location sign-in lists its GLM Coding Plan's models (GLM-5.3 among them) from the key it reads, not the Start Plan's, and a key that can't be read keeps the list magpie had (plugins#439, dinghao-8); 0.1.32: a 429 that is the plan's in-flight cap (3008/3009) is asked again, the same bytes, for about ten seconds before it goes on, is not counted as a spent gift, and maxConcurrency 3 is declared as a default magpie's per-account queue applies (plugins#395, ANDY-JK); 0.1.31: ZCode 3.16's sign-in that keeps only a project-key-location record reads that key, so its GLM Coding Plan is found, not only the gift (plugins#410); conversation named in x-session-id as a bare UUID (plugins#388); 0.1.30: a GLM Coding Plan or team request names the device in metadata.user_id as ZCode's client does on every request, when the agent sent none (plugins #387); 0.1.29: a sign-in keeps the account's user id, for two accounts of one name to be told apart (wakkana on Discord); 0.1.28: plugins #369, #379; 0.1.27: the Start Plan's 405 code 3012 says ZCode's risk control blocked the account (provider.ZCodeRestrictedHint), which rests it as restricted, not "a network block of this IP" (plugins #348); 0.1.26: a signed-in account's ZCode gift plans keep their -Trial models when ZCode's config is out of reach (no fall back to the last list), a failed gift read is asked again in a minute rather than ten, and a card with an API key or an expired ZCode session says its gift plans need ZCode's sign-in (bigsteven on Discord, plugins cc0bd90); 0.1.25: a GLM Coding Plan key on an account with no plan says ZCode's gift plans (the Start Plan, ZCode 1024 Dev Day) are spent only with ZCode's sign-in, on its card and on a 1113 refusal, rather than an empty card and a bare "余额不足" (rtbeilin on X, plugins 71021b8); 0.1.24: Claude Code's ToolSearch goes to ZCode's plans as ToolSearchTool and tools go without defer_loading, since api.z.ai shows the model no tool but a tool_reference's beside a tool named ToolSearch, so a Claude Code subagent saw no Bash, Read or Write (小八 on Discord, plugins d1b6500); 0.1.23: with two gift plans (the 1024 Dev Day and the new-user plan) each bucket is named after its plan and the ones a request spends come first, so a spent plan's GLM-5.3 no longer hides another plan's (01huadalang on Discord, plugins 1f8d38b); 0.1.22: a Start Plan request goes with ZCode 3.16.3's headers alone (x-zcode-query-source, X-Os-Version as os.release()), never the agent's (Claude Code's anthropic-beta, x-app, x-stainless-*), and without ?beta=true (01huadalang on Discord); 0.1.21: a Start Plan request sends the agent's system prompt at the end of ZCode's third system block, as ZCode joins every dynamic section, since zcode.z.ai refuses a fourth block with 405/3012 (plugins #343); 0.1.20: a team's GLM Coding Plan signs in beside the account's own, as an account of its own named "<email> (team)" (plugins #285); 0.1.19: the personal plan's resets, and the team's, carry each reset's end and window; a Start Plan refusal carries the built-in's hint; 0.1.18: the ZCode app's sign-in reads ZCode 3.15's GLM Coding Plan key from config.json (provider["builtin:*-coding-plan"].options.apiKey), so a valid plan no longer reads as "no plan", as the built-in does since 1d8e83a79 (#2130); 0.1.17: the ZCode app's sign-in reads credentials.json from the folder ZCode's data was moved to in its settings (数据存储路径, dataBaseDir in the home folder's setting.json), as the built-in does since 41972b25f (flower on Discord); 0.1.16's ZCode app's sign-in follows ZCode only while it is signed in to the same account, so an entry added for one account never sends with another's credentials after ZCode switched (LingYi_Stu on X); 0.1.15's Start Plan request says the agent's folder and git repository as magpie tells them, never magpie's own folder, and leaves out what it doesn't know (plugins #86); 0.1.14's gift-only account lists its gift's models by their own names, as ZCode does, so a Start Plan GLM-5.3 bucket's model is listed as GLM-5.3, not only GLM-5.3-Trial (#1261, plugins aca7c59); 0.1.13's claim line has one fixed name, which the GUI translates, so it no longer shares the "ZCode Trust Build" bucket window's name (plugins #43); 0.1.12's counted window carries its amount and limit, so the card's count follows used/left (#659, plugins #35); 0.1.11's gift plan ZCode holds but hasn't granted is named on the card, to claim in the ZCode app (plugins #30, #1001); 0.1.10's gift-only buckets and 0.1.9's trial entries (plugins #21) kept
		agents: []string{"zcode"},
		out: func() ([]Moving, error) {
			var out []Moving
			for _, l := range zcodeLogins() {
				// the own account's copy goes back to nothing: ZCode keeps it
				out = append(out, Moving{User: l.User, First: l.Active, On: l.On, Lapsed: l.Lapsed != "", Plan: l.Plan, Own: l.Own, Auth: zcodeOut(l.User, l.Plan, l.key, l.Own)})
			}
			return out, nil
		},
		back: func(ls []savedLogin, user string, auth map[string]any) ([]savedLogin, string, error) {
			var s struct {
				Site, Device, Key, Base, JWT, Token, Org, Project, Plan, Source string
			}
			if json.Unmarshal([]byte(str(auth["refresh"])), &s) != nil {
				// a key signed in to in the plugin
				if str(auth["type"]) != "api" || str(auth["key"]) == "" {
					return ls, "", errors.New("ZCode: an unreadable plugin sign-in")
				}
				s.Key = str(auth["key"])
				if m, _ := auth["metadata"].(map[string]any); m != nil {
					s.Site = str(m["site"])
				}
			} else if s.Key == "" && s.JWT == "" {
				s.Key = str(auth["access"])
			}
			// ZCode's own goes back to nothing: ZCode keeps it
			if _, _, ok := zcodeOwn(); ok && s.Source == "zcode" {
				return ls, ownUser(ls, "zcode", firstNonEmpty(user, str(auth["accountId"]))), nil
			}
			k := zcodeKey{Key: s.Key, Base: s.Base, JWT: s.JWT, Token: s.Token, Org: s.Org, Project: s.Project}
			if k.Base == "" {
				k.Base = ZCodeZaiBase
				if s.Site == "bigmodel" {
					k.Base = ZCodeBigModelBase
				}
			}
			if k.Key == "" && k.JWT == "" && !k.team() {
				return ls, "", errors.New("ZCode: an unreadable plugin sign-in")
			}
			if user == "" {
				user = firstNonEmpty(str(auth["accountId"]), "ZCode")
			}
			i := backInto(&ls, "zcode", user)
			b, err := json.Marshal(k)
			if err != nil {
				return ls, "", err
			}
			ls[i].Auth, ls[i].Lapsed, ls[i].Seen = b, "", time.Now().UTC().Truncate(time.Second)
			if s.Plan != "" {
				ls[i].Plan = s.Plan
			}
			return ls, ls[i].User, nil
		},
	}
}

// zcodeOut is a ZCode account as the plugin's sign-in keeps one;
// ZCode's own is marked as ZCode's, for the plugin to follow.
func zcodeOut(user, plan string, k zcodeKey, own bool) map[string]any {
	site := "zai"
	if strings.Contains(k.Base, "bigmodel.cn") {
		site = "bigmodel"
	}
	state := map[string]any{"site": site, "device": zcodeDeviceMid(), "base": k.Base}
	if own {
		state["source"] = "zcode"
	}
	for name, v := range map[string]string{"key": k.Key, "jwt": k.JWT, "token": k.Token, "org": k.Org, "project": k.Project, "plan": plan} {
		if v != "" {
			state[name] = v
		}
	}
	expires := int64(0)
	if k.Key == "" && !k.team() && !own { // ZCode's own is read anew each time
		if exp, _ := jwtClaims(k.JWT)["exp"].(float64); exp > 0 {
			expires = int64(exp) * 1000
		}
	}
	return map[string]any{"type": "oauth", "access": firstNonEmpty(k.Key, k.JWT), "refresh": jsonText(state), "expires": expires, "accountId": user}
}
