package provider

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Factory's accounts on @magpie-community/opencode-factory-auth, as its
// device sign-in keeps one: the WorkOS pair as access and refresh (the
// refresh token rotates, so it moves rather than being copied), the org
// and region droid needs beside them.
func init() {
	movers["factory"] = &mover{
		pkg:    "@magpie-community/opencode-factory-auth",
		min:    "0.1.56", // 0.1.56: a Gemini model's request (Codex's or pi's, Responses or chat made generateContent by the gateway) has its prompt, user turns and tool descriptions adapted as on Factory's other routes, so Codex's "context provided by the harness" and the other wording 0.1.52 to 0.1.54 change no longer reach Factory's Gemini route as written (content_policy_rejected; plugins #1310, gengyubo, xiaozuoyou, @zuoyou_dev on X); 0.1.55: Claude Code's first turn after /compact on the chat route (GLM, GPT and other /api/llm/o models), whose summary and restored-file reminders magpie joins into one text, is split back into its blocks and each adapted, so "Called the Read tool with the following input:" and the other restored-context openings are not sent as written (@dannykkg on X); 0.1.54: dsh's Web GUI sentence "The browser provides no implicit DOM, route, or screenshot context." goes with "gives" (a fingerprint Antigravity refused as dsh's), "harness" in dsh's skill catalog goes as runtime, and a refusal on a new session's first turn says to turn skills, MCP servers and plugins off and on to find it (a1pha1nthefog on X, still refused on 0.1.53); 0.1.53: dsh's opening identity line ("You are an AI agent powered by DeepSeek Harness.") and model line ("You are a coding agent powered by the <model> model.") go as "You are an AI agent." and "Current model: <model>.", so a fresh dsh session's first turn is not content_policy_rejected (a1pha1nthefog on X, still refused on 0.1.52); 0.1.52: a client's "harness" read as runtime in its prompt and tool descriptions, which Factory's policy check refuses (content_policy_rejected for Claude Code, Pi and Codex, @DennisYan4 on X), and Claude Code's tool search (defer_loading, DeferredToolPlaceholder) sent as tools declared in full; 0.1.51: dsh's web_search, and read_file and execute_command, go out as droid's WebSearch, Read and Execute, a listed name behind leading underscores is renamed too, and a request refused while it declares tools goes once more with every tool renamed, so a name Factory adds next refuses it once, not for good (a1pha1nthefog on X, still content_policy_rejected on 0.1.50); 0.1.50: a tool declared under a name Factory refuses since 2026-11-18 (bash, read, edit, write, glob, grep, task, run_shell, Bash, Write, Agent, Shell, StrReplace, ...) goes out under droid's own name for the job (Execute, Read, Edit, Create, Glob, Grep, Task) or suffixed, and comes back under its own name, so dsh, OpenCode, pi and Claude Code are not refused with content_policy_rejected (a1pha1nthefog on X; plugins #1314, heggria); 0.1.49: a Gemini request's fields go in droid's order, the model first, not after the contents, a long session's megabytes, which Factory answered at 0.6 to 1.6 MB with 400 "Invalid model ID in request body" on every retry (marovole on X, gemini-4-hydrogen); 0.1.48: each conversation, and each subagent in it, is a Factory session of its own (x-session-id), as droid opens them, not one session for every request of the plugin, which Factory answered with 400 "Invalid model ID in request body" now and then on Gemini 4 Argon, Hydrogen and Carbon (marovole on X); 0.1.41: ZCode's environment paragraph and its restored-read reminder (Called the Read tool with the following input) reach Factory renamed as runtime context and a previously read file, not 403 Forbidden (plugins #211, #213, William-AIdev); 0.1.40: Factory's usage policy refusal (content_policy_rejected) is a 400 invalid_request_error with its code, not a 403 permission_error an agent reads as a failed sign-in (sweanng on Discord; plugins #494); 0.1.39: DeepSeek V4.1 Flash 1017, from droid 0.251.0, on its registry default upstream Fireworks (sweanng on Discord; plugins #480); 0.1.38: an account stops once its Standard window is used up when the Standard usage only option is on (plugins #349); 0.1.37: a billing header joined with instructions loses only its own line, so Claude Code auto mode keeps its policy (plugins #302); Claude Code 2.1.292's instructions re-read after compaction (changed: true), and a 403 that keeps Factory's request id (plugins #243); the update-config entry in a system prompt's configuration section, as claude-code-guide renders it with a slash, adapted the way Factory takes it (plugins #198); a model's token multiplier reaching magpie as its rate, with a running promotion's discount (plugins #158); an OpenAI model's request without temperature and top_p, which OpenAI's reasoning models turn away with 400 (sweanng424 on Discord, Nowledge Mem on gpt-6.1-sol); requests saying droid 0.242.0, the current release (sky杨 on Discord: every model 403 "Factory: Forbidden" from Claude Code); Claude Code on GLM and other /api/llm/o models without the phrases Factory refuses (Echoo on Discord); the Fast Mode models listed last, so GPT-5.3-Codex, Grok and GLM are in the first 24 again (plugins #119); droid's Fast Mode models (Opus 5.5/5/4.8, GPT-5.5/5.4/5.3-Codex), and fast asked of a base model sent to its Fast Mode model (plugins #119); Gemini 4 Argon and Gemini 3.x on Factory's Gemini route (#2080); a 413 asked once more with older images left out, then said as Factory's byte limit (#2082); Haiku 5.5 and Mistral Large 4, in droid 0.240.0's picker (JL); a session continued after Claude Code compaction, whose restored context opens with the invoked-skills reminder (plugins #66); Claude Code's streamed context_management left out, which Factory's streaming route refuses (plugins #71); Claude Code's skill list sent alone as a system message, as text blocks or a string, gets update-config's line Factory takes (plugins #72); Claude Code 2.1.280's restored-context turn after /compact (plugins #68); pi's compaction and branch summary openings reworded (#1316); hook notifications kept in Claude Code compaction context only stop changed-file quoting, summaries quoted like tool output, a cut after a hunk separator (plugins #42); Claude Code compaction/runtime metadata and quoted session summaries (#1132); pi's opening sentence cut to its first half (#952); DeepSeek V4.1 Flash, Kimi K3 and GLM-5.2 offering none, so reasoning turned off stops their thinking (#899); requests saying droid 0.233.0 (JL: every model 403); Claude Code 2.1.288's runtime context as a system message of text blocks (#658); a tool whose input_schema has anyOf/oneOf/allOf at its root (Codex's automation_update) wrapped for Claude models (#646); a failure's status and its sign-in mark as the built-in's; Claude 5's system-role metadata as Factory takes it; Claude Code's standalone model-switch system updates; tool results quoting Claude Code's fixed phrases, and Claude Code's directory updates carrying a model; the sign-in renewed ahead of time through auth.refresh; Claude Code with a global CLAUDE.md or a SessionStart hook, and Claude Desktop's model line (#634)
		agents: []string{"factory"},
		out: func() ([]Moving, error) {
			ls := factoryLogins()
			factoryMu.Lock()
			defer factoryMu.Unlock()
			var out []Moving
			for _, l := range ls {
				c := l.creds
				if c.Key {
					// an API key (fk-…) is the plugin's api account, as its
					// own "Factory API key" sign-in saves one: an oauth one
					// would be renewed with no refresh token and lapse
					out = append(out, Moving{User: l.User, First: l.Active, On: l.On, Lapsed: l.Lapsed != "", Plan: l.Plan, Auth: map[string]any{
						"type": "api",
						"key":  c.Access,
						"metadata": map[string]any{
							"email":        c.Email,
							"userId":       c.UserID,
							"region":       c.Region,
							"premBaseHost": c.Prem,
						},
					}})
					continue
				}
				out = append(out, Moving{User: l.User, First: l.Active, On: l.On, Lapsed: l.Lapsed != "", Plan: l.Plan, Auth: map[string]any{
					"type":                 "oauth",
					"access":               c.Access,
					"refresh":              c.Refresh,
					"expires":              c.ExpiresAt,
					"orgId":                c.Org,
					"email":                c.Email,
					"userId":               c.UserID,
					"activeOrganizationId": c.Active,
					"region":               c.Region,
					"premBaseHost":         c.Prem,
					"accountId":            l.User,
				}})
			}
			return out, nil
		},
		back: func(ls []savedLogin, user string, auth map[string]any) ([]savedLogin, string, error) {
			if str(auth["type"]) == "api" {
				return factoryKeyBack(ls, user, auth)
			}
			access := str(auth["access"])
			if access == "" {
				return ls, "", errors.New("Factory: an unreadable plugin sign-in")
			}
			if user == "" {
				user = firstNonEmpty(str(auth["accountId"]), str(auth["email"]), str(auth["userId"]))
			}
			i := backInto(&ls, "factory", user)
			c, _ := factorySaved(ls[i])
			c.Access, c.Refresh, c.ExpiresAt = access, str(auth["refresh"]), num(auth["expires"])
			c.Org = firstNonEmpty(str(auth["orgId"]), c.Org)
			c.Active = firstNonEmpty(str(auth["activeOrganizationId"]), c.Active)
			c.Email = firstNonEmpty(str(auth["email"]), c.Email)
			c.UserID = firstNonEmpty(str(auth["userId"]), c.UserID)
			c.Region = firstNonEmpty(str(auth["region"]), c.Region)
			c.Prem = firstNonEmpty(str(auth["premBaseHost"]), c.Prem)
			b, err := json.Marshal(c)
			if err != nil {
				return ls, "", err
			}
			ls[i].Auth, ls[i].Lapsed, ls[i].Seen = b, "", time.Now().UTC().Truncate(time.Second)
			return ls, ls[i].User, nil
		},
	}
}

// factoryKeyBack keeps the plugin's api account (an fk- key) as the
// built-in's key account (factory_key.go).
func factoryKeyBack(ls []savedLogin, user string, auth map[string]any) ([]savedLogin, string, error) {
	key := strings.TrimSpace(str(auth["key"]))
	if !strings.HasPrefix(key, factoryKeyPrefix) {
		return ls, "", errors.New("Factory: an unreadable plugin API key")
	}
	md, _ := auth["metadata"].(map[string]any)
	if user == "" {
		user = firstNonEmpty(str(md["email"]), str(md["userId"]), factoryKeyName(key))
	}
	i := backInto(&ls, "factory", user)
	c := factoryCreds{Access: key, Key: true, Email: str(md["email"]), UserID: str(md["userId"]),
		Region: str(md["region"]), Prem: str(md["premBaseHost"])}
	b, err := json.Marshal(c)
	if err != nil {
		return ls, "", err
	}
	ls[i].Auth, ls[i].Lapsed, ls[i].Seen = b, "", time.Now().UTC().Truncate(time.Second)
	return ls, ls[i].User, nil
}
