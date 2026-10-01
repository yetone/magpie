#!/usr/bin/env bash
# Builds the PR's magpie, runs it in a home of its own with a real DeepSeek
# key, sends a few requests through its gateway so Routing and Usage have
# something to show, and records it (record.mjs).
#
#   run.sh <magpie source> <out dir>
#
# env: DEEPSEEK_API_KEY, DIFF_FILE, PR_TITLE, PR_BODY_FILE; PLAYWRIGHT_BROWSERS_PATH
# must already point at the installed Chromium, since HOME moves.
set -euo pipefail
src=$(cd "$1" && pwd)
mkdir -p "$2"
out=$(cd "$2" && pwd)
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'kill "${web:-}" 2>/dev/null || true' EXIT

: "${DEEPSEEK_API_KEY:?}"
echo "::add-mask::$DEEPSEEK_API_KEY"

(cd "$src" && CGO_ENABLED=0 go build -tags nogui -o "$work/magpie" .)

# nothing of the runner's own: the app sees only this home
export HOME="$work/home" XDG_CONFIG_HOME="$work/home/.config" XDG_CACHE_HOME="$work/home/.cache" XDG_DATA_HOME="$work/home/.local/share" XDG_STATE_HOME="$work/home/.local/state"
mkdir -p "$HOME"
# a few agents, as a fresh install leaves them, so the Agents page has some
mkdir -p "$HOME/.claude" "$HOME/.codex" "$HOME/.gemini" "$XDG_CONFIG_HOME/opencode"

gw=127.0.0.1:3430
webaddr=127.0.0.1:3431
export MAGPIE_ADDR=$gw MAGPIE_WEB_KEY=$(openssl rand -hex 20)
echo "::add-mask::$MAGPIE_WEB_KEY"

"$work/magpie" provider add deepseek "$DEEPSEEK_API_KEY" >/dev/null
# Claude Desktop, as its config folder (no app: on Linux magpie finds it in
# $XDG_CONFIG_HOME/Claude), put on magpie, so the Agents page shows its row
# and settings; the CLI writes the folders where this OS keeps them
mkdir -p "$XDG_CONFIG_HOME/Claude"
"$work/magpie" claude-desktop provider magpie >/dev/null
"$work/magpie" web --addr "$webaddr" --no-open >"$work/web.log" 2>&1 &
web=$!
url="http://$webaddr/?k=$MAGPIE_WEB_KEY"
for _ in $(seq 60); do
  curl -fsS -o /dev/null "$url" 2>/dev/null && curl -fsS -o /dev/null "http://$gw/v1/models" -H "Authorization: Bearer magpie" 2>/dev/null && break
  kill -0 "$web" 2>/dev/null || { cat "$work/web.log"; exit 1; }
  sleep 1
done

# a little traffic, so the request log and usage aren't empty
for q in "Say hi in three words." "What is 17 * 23? Answer with the number." "Name a colour."; do
  curl -fsS -o /dev/null --max-time 60 "http://$gw/v1/chat/completions" \
    -H "Authorization: Bearer magpie" -H "Content-Type: application/json" \
    -d "{\"model\":\"deepseek/deepseek-flash\",\"max_tokens\":40,\"messages\":[{\"role\":\"user\",\"content\":\"$q\"}]}" ||
    echo "warm-up request failed (the recording goes on)"
done

status=0
MAGPIE_URL=$url OUT_DIR=$out SRC_DIR=$src SECRETS="$DEEPSEEK_API_KEY"$'\n'"$MAGPIE_WEB_KEY" \
  node "$here/record.mjs" || status=$?
sed -E 's/k=[A-Za-z0-9._~-]+/k=***/g' "$work/web.log" | tail -40
exit $status
