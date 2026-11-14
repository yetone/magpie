package provider

// Whether a Claude subscription account spends extra usage. Once a plan
// window that stops a Claude account (its 5 hours, its week) is used up,
// Anthropic goes on serving it on the account's extra usage — usage credits
// bought beyond the plan, when the account has it turned on — and bills it.
// magpie can't turn that off at Anthropic, so it holds back its own
// requests instead (baksili_ on X): with Settings' ClaudeNoExtraUsage on,
// routing holds such an account at 100% of any window with no cap of its
// own, as a Codex account set not to spend its credits is held
// (codex_credits.go), and the user's other accounts, groups and fallbacks
// take the request. The hold goes by magpie's latest reading, so a request
// in the minute after a window fills may still reach the account; and a
// Claude Code signed in to the account directly, not through magpie, is
// Anthropic's alone to bill. Claude Code's sign-in already moves off an
// account on its overages (usedPast counts the "Extra usage" window).
// Warm-ups (codex_warmup.go) send nothing to an account held so.
//
// The extra usage's own window, "Usage credits" (claudeExtraWindow), set
// aside from the allowance, also takes a cap of the user's: a share of its
// monthly limit, or an amount of its currency. Reached, it doesn't hold
// the account outright — the plan's own windows still have what they have
// — but holds it as the setting would, at 100% of a plan window, so no
// more extra usage is spent this month (Allowance.extraCapped).

import "github.com/yetone/magpie/internal/settings"

// ClaudeExtraUsage says whether magpie sends a Claude account requests
// once its plan is used up, for Anthropic to bill as extra usage: true
// unless the user turned it off.
func ClaudeExtraUsage() bool { return !settings.Shared().ClaudeNoExtraUsage }
