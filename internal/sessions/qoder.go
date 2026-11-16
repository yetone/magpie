package sessions

import (
	"os"
	"path/filepath"

	"github.com/yetone/magpie/internal/appdir"
)

// Qoder's CLI (qodercli, and qoderclicn for the China site: one build)
// keeps its sessions as Claude Code does, in its folder's projects/: a
// session's <id>.jsonl in its project's folder, its subagents' in
// <id>/subagents/agent-<id>.jsonl, the lines Claude Code's — "user" and
// "assistant" with the Anthropic usage (on a reply's last block only), the
// cwd and the session id on each, "ai-title" and "summary" lines. They are
// read as Claude Code's are. `qodercli --resume <id>` picks one up again.
// A reply on Qoder's own models ("auto" among them) has every count 0
// unless QODER_EXPOSE_TOKEN_USAGE is set (qodercli 1.1.0 on), and the
// credits it cost (usage.credits), which its call carries instead.

// QoderDir is the folder of Qoder's build of this id: $QODER_CONFIG_DIR,
// else ~/.qoder, for qoder; $QODERCN_CONFIG_DIR, else ~/.qoder-cn, for
// qoder-cn.
func QoderDir(id string) string {
	env, dir := "QODER_CONFIG_DIR", ".qoder"
	if id == "qoder-cn" {
		env, dir = "QODERCN_CONFIG_DIR", ".qoder-cn"
	}
	if d := appdir.Getenv(env); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, dir)
}
