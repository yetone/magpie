# UI preview

`.github/workflows/ui-preview.yml` shows reviewers what a pull request does to the UI.
When a PR touches `internal/gui/assets/` or the GUI's Go code, or carries the `ui-preview` label, the workflow does this:

1. **publish.mjs detect** decides whether the PR is a UI change. It puts a "recording…" note in a fixed block at the foot of the PR description, between `<!-- magpie-ui-preview:begin -->` and `<!-- magpie-ui-preview:end -->`.
2. **run.sh** builds the PR's magpie (nogui) and runs `magpie web` in a fresh home, with DeepSeek added as a real provider. Before magpie starts, **seed.mjs** fills that home with made-up data, so a change that only shows with data can be seen:
   - two more providers, one a Sub2API relay with three keys, 5h/1d/7d windows and a month of balance history, and a routing group;
   - 30 days of requests from four agents over several models at different speeds, with sessions, cache reads and a few errors;
   - more than 200 Claude Code and Codex session files;
   - two ChatGPT accounts signed in to Codex, with quota windows and five weeks of quota history;
   - skills in the library from GitHub (one author with two repositories) and a local folder, and a few only in the agents' own folders.

   seed.mjs then stays up as a mock: it answers the fake providers, the relay's usage, and chatgpt.com's account usage. For chatgpt.com it is magpie's HTTPS proxy, with a certificate from a CA that run.sh makes; every other host goes through it untouched. run.sh sends a few real requests through the gateway, then runs:
3. **record.mjs**, which:
   - gives DeepSeek the diff and outlines of the real pages;
   - has it plan scenes: what to click, hover, type and capture;
   - walks the plan in Chromium at 2x, with `cursor.js` drawing the pointer, its trail and each click.
   It always takes screenshots. When the change spans pages or needs interaction, it also records an mp4, never a GIF. If the key shows up on screen at any point, everything is thrown away.
4. **publish.mjs publish** puts the files under `pr-<n>/<sha>/` on the `ui-previews` branch, which GitHub Pages serves, and rewrites the block in the description:
   - the video's poster links to the player page, since GitHub won't embed a video it didn't host;
   - the screenshots are shown inline.

Security: the recording job runs the PR's code without asking anyone. Any PR's code can therefore read `DEEPSEEK_API_KEY` (environment `ui-preview`), so the key there should have a low spending limit. That job has no write access, and the job that writes never runs the PR's code.

To run it locally (run.sh builds and starts magpie itself):

```sh
cd .github/ui-preview && npm install && npx playwright install chromium
DEEPSEEK_API_KEY=… DIFF_FILE=pr.diff PR_TITLE=… ./run.sh ../.. /tmp/out
```

To re-run it for a PR, use `gh workflow run ui-preview.yml -f pr=<n>`.
