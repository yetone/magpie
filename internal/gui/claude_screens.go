package gui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/yetone/magpie/internal/gateway"
)

// claudeScreensFrom is the gateway the Claude editor's look at the hidden
// Claude Code asks; a test's stand-in.
var claudeScreensFrom = gateway.URL

// claudeScreens is what the gateway's runs list answers with their screens
// (gateway.ClaudeRunsPath, ?screen=1), for the Claude editor's look at
// each hidden Claude Code while it runs (#8075): masked there, and asked
// from this process, as the gateway answers no page.
func claudeScreens(ctx context.Context) (gateway.ClaudeRuns, error) {
	var out gateway.ClaudeRuns
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, claudeScreensFrom()+gateway.ClaudeRunsPath+"?screen=1", nil)
	if err != nil {
		return out, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return out, err
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &out) != nil || out.Object != "claude-runs" {
		return out, errors.New("the gateway answered " + resp.Status)
	}
	return out, nil
}
