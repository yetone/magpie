package provider

// A Claude account is tested as it is used: by Claude Code itself, never
// by a request magpie makes to Anthropic's API with the account's sign-in.

import (
	"context"
	"errors"
	"time"
)

// errClaudeViaCLI is what a request straight to Anthropic's API on a
// Claude account comes to: such requests run Claude Code instead.
var errClaudeViaCLI = errors.New("a Claude account is used through Claude Code, not by requests to Anthropic's API")

// claudeCLIProbe has Claude Code answer one "hi" at model, with configDir
// the config directory of a saved account (claude_dirs.go), "" for the one
// Claude Code is signed in to.
// The gateway, which runs Claude Code, sets it (ProbeClaudeVia).
var claudeCLIProbe func(ctx context.Context, configDir, model string) error

// ProbeClaudeVia sets how a Claude account's test runs Claude Code.
func ProbeClaudeVia(f func(ctx context.Context, configDir, model string) error) { claudeCLIProbe = f }

// claudeWait is how long a test waits on Claude Code, which takes a few
// seconds to start before it asks.
const claudeWait = time.Minute

// testClaude is a Claude account's test of model, run by Claude Code.
func (p Provider) testClaude(ctx context.Context, model string) Result {
	r := Result{Protocol: Anthropic, Model: model}
	if model == "" {
		r.Error = "no model to try: expose one, or refresh the model list"
		return r
	}
	if claudeCLIProbe == nil {
		r.Error = "Claude Code isn't available to run the test"
		return r
	}
	ctx, cancel := context.WithTimeout(ctx, claudeWait)
	defer cancel()
	dir, _, err := p.Account.Token(ctx)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	start := time.Now()
	err = claudeCLIProbe(ctx, dir, model)
	r.Millis = time.Since(start).Milliseconds()
	if err != nil {
		r.Error = err.Error()
		return r
	}
	r.OK = true
	return r
}

// isClaudeAccount is whether p is a Claude subscription account.
func (p Provider) isClaudeAccount() bool { return p.Account != nil && p.Account.Agent == "claude" }
