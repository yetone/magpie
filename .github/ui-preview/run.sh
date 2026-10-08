#!/usr/bin/env bash
# Builds the PR's magpie, runs it in a home of its own with a real DeepSeek
# key and made-up data beside it (seed.mjs: providers, a month of requests,
# sessions, two ChatGPT accounts' quotas), sends a few requests through its
# gateway, and records it (record.mjs).
#
#   run.sh <magpie source> <out dir>
#
# env: DEEPSEEK_API_KEY, DIFF_FILE, PR_TITLE, PR_BODY_FILE; PLAYWRIGHT_BROWSERS_PATH
# must already point at the installed Chromium, since HOME moves.
set -euo pipefail
# the home below isolates magpie on Linux only: Windows reads USERPROFILE
# and APPDATA, macOS keeps sign-ins in the Keychain
[ "$(uname -s)" = Linux ] || { echo "run.sh: Linux only" >&2; exit 1; }
src=$(cd "$1" && pwd)
mkdir -p "$2"
out=$(cd "$2" && pwd)
# record.mjs empties the out folder when a secret showed: never one that
# holds the source, the home or everything
case "$src/" in "${out%/}"/*) echo "run.sh: out folder $out holds the source" >&2; exit 1 ;; esac
case "$HOME/" in "${out%/}"/*) echo "run.sh: out folder $out holds the home" >&2; exit 1 ;; esac
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'kill "${web:-}" "${mock:-}" 2>/dev/null || true' EXIT

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
# the made-up data, and the mock that answers for it: the fake providers,
# the relay's usage and, as the proxy magpie goes out through, chatgpt.com
# under a certificate of our own CA (every other host is passed through)
node "$here/seed.mjs" write
ca=$work/ca
mkdir -p "$ca"
openssl req -x509 -newkey rsa:2048 -nodes -days 2 -subj "/CN=magpie ui preview CA" \
  -keyout "$ca/ca.key" -out "$ca/ca.pem" 2>/dev/null
openssl req -newkey rsa:2048 -nodes -subj "/CN=chatgpt.com" -keyout "$ca/key.pem" -out "$ca/req.csr" 2>/dev/null
printf 'subjectAltName=DNS:chatgpt.com,DNS:auth.openai.com\n' >"$ca/san"
openssl x509 -req -in "$ca/req.csr" -CA "$ca/ca.pem" -CAkey "$ca/ca.key" -CAcreateserial -days 2 \
  -extfile "$ca/san" -out "$ca/cert.pem" 2>/dev/null
cat /etc/ssl/certs/ca-certificates.crt "$ca/ca.pem" >"$ca/bundle.pem"
mock=127.0.0.1:3440
MOCK=$mock CA_DIR=$ca node "$here/seed.mjs" serve >"$work/mock.log" 2>&1 &
mock_pid=$!
for _ in $(seq 20); do curl -fsS -o /dev/null "http://$mock/v1/usage" 2>/dev/null && break; sleep 0.5; done
mock=$mock_pid

HTTPS_PROXY=http://127.0.0.1:3440 SSL_CERT_FILE=$ca/bundle.pem \
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
# and some to the made-up providers and the group, in each API, so Routing's
# live view has more than DeepSeek in it
for m in coding relay/claude-opus-4-7 acme/gpt-5.4-mini; do
  curl -fsS -o /dev/null --max-time 30 "http://$gw/v1/chat/completions" \
    -H "Authorization: Bearer magpie" -H "Content-Type: application/json" \
    -d "{\"model\":\"$m\",\"stream\":true,\"messages\":[{\"role\":\"user\",\"content\":\"Summarise the diff.\"}]}" || echo "warm-up $m failed"
done
curl -fsS -o /dev/null --max-time 30 "http://$gw/v1/messages" -H "Authorization: Bearer magpie" \
  -H "Content-Type: application/json" -H "anthropic-version: 2023-06-01" \
  -d '{"model":"relay/claude-sonnet-4-6","max_tokens":200,"stream":true,"messages":[{"role":"user","content":"Fix the flaky test."}]}' || echo "warm-up messages failed"
curl -fsS -o /dev/null --max-time 30 "http://$gw/v1/responses" -H "Authorization: Bearer magpie" \
  -H "Content-Type: application/json" -d '{"model":"acme/gpt-5.5","stream":true,"input":"Review my change."}' || echo "warm-up responses failed"

status=0
MAGPIE_URL=$url OUT_DIR=$out SRC_DIR=$src SECRETS="$DEEPSEEK_API_KEY"$'\n'"$MAGPIE_WEB_KEY" \
  node "$here/record.mjs" || status=$?
sed -E 's/k=[A-Za-z0-9._~-]+/k=***/g' "$work/web.log" | tail -40
exit $status
