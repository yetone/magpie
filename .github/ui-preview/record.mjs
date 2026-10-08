// Records a PR's UI changes in the real app: magpie is already running
// (run.sh), this reads the PR's diff, has DeepSeek plan what to show from it
// and the pages as they really are, then walks the plan in Chromium with
// the mouse drawn in (cursor.js) — screenshots always, a video when the
// change spans pages or needs clicking to see. Everything lands in OUT_DIR
// with a manifest.json that publish.mjs turns into the PR's preview.
//
// env: MAGPIE_URL (the web link, key included), DIFF_FILE, PR_TITLE,
// PR_BODY_FILE, OUT_DIR, SRC_DIR (the PR's source), DEEPSEEK_API_KEY,
// SECRETS (words that must never be on screen, one per line), UI_LOCALE
// (zh-CN), PLAN_MODEL, PLAN_FILE (a plan to walk instead of asking for one).
import { chromium } from "playwright";
import { execFileSync } from "node:child_process";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const env = (k, d) => process.env[k] ?? d;
const OUT = path.resolve(process.env.OUT_DIR || "ui-preview-out");
// a leak empties OUT, so it must be a folder of its own: not the working
// folder, the home, the root or one holding them
for (const keep of [process.cwd(), os.homedir(), path.parse(OUT).root]) {
  const rel = path.relative(OUT, keep);
  if (rel === "" || (!rel.startsWith("..") && !path.isAbsolute(rel))) {
    console.error(`record: OUT_DIR ${OUT} holds ${keep}; give it a folder of its own`);
    process.exit(1);
  }
}
const BASE = env("MAGPIE_URL");
const LOCALE = env("UI_LOCALE", "zh-CN");
const MODEL = env("PLAN_MODEL", "deepseek-flash");
const VIEW = { width: 1280, height: 800 };
const VIEWS = ["agents", "providers", "gateway", "routing", "usage", "library", "settings"];
// never pressed: they end the run, reach out of the sandbox or throw away
// what the recording needs
const FORBIDDEN = /退出|quit|删除|delete|remove|移除|更新到|update to|restart|重启|sign out|登出|卸载|uninstall/i;
const secrets = env("SECRETS", "").split("\n").map((s) => s.trim()).filter((s) => s.length >= 8);

const log = (...a) => console.log("[ui-preview]", ...a);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const manifest = { summary: "", scenes: [], video: null, poster: null, errors: [], leak: false, skipped: null };

function urlFor(where) {
  const u = new URL(BASE);
  u.searchParams.delete("view");
  u.searchParams.delete("mode");
  if (where === "panel") u.searchParams.set("mode", "panel");
  else if (where && where !== "agents") u.searchParams.set("view", where);
  return u.toString();
}

// The page as a compact outline the planner can pick real selectors from:
// visible elements, their tag, id, classes, a few data- attributes and
// their own text, indented; svg insides and invisible parts left out.
function outline() {
  const lines = [];
  const MAX = 420;
  const own = (e) => [...e.childNodes].filter((n) => n.nodeType === 3).map((n) => n.textContent.trim()).join(" ").replace(/\s+/g, " ").slice(0, 70);
  const shown = (e) => {
    const r = e.getBoundingClientRect();
    if (!r.width || !r.height) return false;
    const s = getComputedStyle(e);
    return s.visibility !== "hidden" && s.display !== "none" && +s.opacity !== 0;
  };
  // fold: where the view it is in ends on screen
  const walk = (e, depth, fold) => {
    if (lines.length >= MAX || e.hasAttribute?.("data-ui-preview")) return;
    const tag = e.tagName.toLowerCase();
    if (["script", "style", "svg", "link", "meta", "noscript", "template"].includes(tag) || !shown(e)) return;
    const cls = [...e.classList].slice(0, 4).map((c) => "." + c).join("");
    const data = [...e.attributes].filter((a) => a.name.startsWith("data-") && a.value.length < 40).slice(0, 3).map((a) => `[${a.name}="${a.value}"]`).join("");
    const aria = ["role", "aria-label", "title", "placeholder", "type"].map((k) => e.getAttribute(k) ? `${k}="${e.getAttribute(k).slice(0, 40)}"` : "").filter(Boolean).join(" ");
    const text = own(e);
    const r = e.getBoundingClientRect();
    const away = r.top >= fold ? " (below the fold)" : "";
    const interesting = cls || data || e.id || text || aria || /^(button|a|input|select|textarea|label|h\d)$/.test(tag);
    if (interesting) lines.push(`${"  ".repeat(Math.min(depth, 12))}${tag}${e.id ? "#" + e.id : ""}${cls}${data}${aria ? " " + aria : ""}${text ? ` "${text}"` : ""}${away}`);
    const scrolls = /auto|scroll|overlay/.test(getComputedStyle(e).overflowY) && e.scrollHeight > e.clientHeight + 1;
    for (const c of e.children) walk(c, interesting ? depth + 1 : depth, scrolls ? Math.min(fold, r.bottom) : fold);
  };
  walk(document.body, 0, innerHeight);
  if (lines.length >= MAX) lines.push("… (cut)");
  return lines.join("\n");
}

async function screenText(page) {
  return page.evaluate(() => document.documentElement.innerText + "\n" +
    [...document.querySelectorAll("input, textarea")].map((e) => e.value).join("\n")).catch(() => "");
}
async function checkLeak(page, where) {
  if (!secrets.length) return;
  const text = await screenText(page);
  for (const s of secrets) {
    // the whole key, or a long enough run of its middle
    const mid = s.slice(4, -4);
    if (text.includes(s) || (mid.length >= 12 && text.includes(mid))) {
      manifest.leak = true;
      throw new Error(`a secret is on screen (${where}); nothing will be published`);
    }
  }
}

async function deepseek(messages) {
  // a reply cut short or not JSON is asked for again
  let last;
  for (let i = 0; i < 3; i++) {
    const res = await fetch("https://api.deepseek.com/v1/chat/completions", {
      method: "POST",
      headers: { "Content-Type": "application/json", Authorization: `Bearer ${env("DEEPSEEK_API_KEY")}` },
      body: JSON.stringify({ model: MODEL, messages, response_format: { type: "json_object" }, max_tokens: 16000 }),
      signal: AbortSignal.timeout(240e3),
    });
    if (!res.ok) throw new Error(`DeepSeek ${res.status}: ${(await res.text()).slice(0, 300)}`);
    const j = await res.json();
    const text = j.choices?.[0]?.message?.content ?? "";
    try { return JSON.parse(text.replace(/^```(?:json)?\s*|\s*```$/g, "")); }
    catch (e) { last = new Error(`reply not JSON (${j.choices?.[0]?.finish_reason}): ${e.message}`); log(last.message, "— asking again"); }
  }
  throw last;
}

// The page's own code around what the diff touches, from the PR's source:
// where the changed classes and functions are built and what calls them,
// so the plan knows how to reach a popover or menu the outlines show
// closed. Two hops of callers, at most ~24k characters.
async function codeContext(diff, src) {
  if (!src) return "";
  const dir = path.join(src, "internal/gui/assets");
  const names = new Set(await fs.readdir(dir).catch(() => []));
  const files = {};
  for (const f of names) if (f.endsWith(".js") && !f.includes(".min.")) files[f] = (await fs.readFile(path.join(dir, f), "utf8")).split("\n");
  if (!Object.keys(files).length) return "";
  const tokens = new Set(), fns = new Set();
  for (const line of diff.split("\n")) {
    const hunk = /^@@.*@@\s*(?:async\s+)?(?:function\s+(\w+)|(?:const|let)\s+(\w+)\s*=)/.exec(line);
    if (hunk) fns.add(hunk[1] || hunk[2]);
    if (/^[+-][^+-]/.test(line)) {
      for (const m of line.matchAll(/\.([a-z][\w-]*-[\w-]+|[a-z]{3,}[A-Z]\w*)/g)) tokens.add(m[1]);
      for (const m of line.matchAll(/["' ]([a-z]+(?:-[a-z0-9]+)+)["' ]/g)) tokens.add(m[1]);
      for (const m of line.matchAll(/function\s+(\w+)/g)) fns.add(m[1]);
    }
  }
  const enclosing = (lines, i) => {
    for (let j = i; j >= 0; j--) {
      const m = /^\s*(?:async\s+)?function\s+(\w+)|^\s*(?:const|let)\s+(\w+)\s*=\s*(?:async\s*)?(?:\([^)]*\)|\w+)\s*=>/.exec(lines[j]);
      if (m) return m[1] || m[2];
    }
  };
  const hits = {}; // file -> Set of line numbers
  const mark = (f, i) => (hits[f] ??= new Set()).add(i);
  const esc = (t) => t.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  for (const [f, lines] of Object.entries(files))
    lines.forEach((l, i) => { for (const t of tokens) if (new RegExp(`(^|[^\\w-])${esc(t)}([^\\w-]|$)`).test(l)) { mark(f, i); const e = enclosing(lines, i); if (e) fns.add(e); break; } });
  let seen = new Set();
  for (let hop = 0; hop < 2; hop++) {
    const next = new Set();
    for (const fn of fns) {
      if (seen.has(fn)) continue;
      seen.add(fn);
      const call = new RegExp(`\\b${esc(fn)}\\s*\\(`), def = new RegExp(`function\\s+${esc(fn)}\\b|(const|let)\\s+${esc(fn)}\\s*=`);
      for (const [f, lines] of Object.entries(files))
        lines.forEach((l, i) => { if (call.test(l) && !def.test(l)) { mark(f, i); const e = enclosing(lines, i); if (e) next.add(e); } });
    }
    for (const n of next) fns.add(n);
  }
  let out = "";
  for (const [f, set] of Object.entries(hits)) {
    const lines = files[f];
    const spans = [];
    for (const i of [...set].sort((a, b) => a - b)) {
      const a = Math.max(0, i - 8), b = Math.min(lines.length, i + 9);
      if (spans.length && a <= spans.at(-1)[1]) spans.at(-1)[1] = Math.max(spans.at(-1)[1], b);
      else spans.push([a, b]);
    }
    for (const [a, b] of spans) {
      const chunk = `--- ${f}:${a + 1}\n${lines.slice(a, b).map((l) => l.slice(0, 200)).join("\n")}\n`;
      if (out.length + chunk.length > 24e3) return out + "… (cut)\n";
      out += chunk;
    }
  }
  return out;
}

const GUIDE = `You plan a short screen recording of magpie, a desktop app (shown here in a browser at ${VIEW.width}x${VIEW.height}) that picks the model for every local coding agent. It runs for real on a sandbox machine: a DeepSeek API key added as a provider, with a few real requests already sent through its gateway, and made-up data beside it (seed.mjs) so the pages that need data have some. That data: providers "Sub2API Relay" (three keys, rotate; its usage shows 5h/1d/7d dollar windows and a balance with a month of history) and "Acme Cloud", both answered by a local mock at its own speed per model; a routing group "Coding" with a rule; library skills from GitHub (one author with two repositories, another with one) and one from a local folder, and a few only in the agents' own folders; 30 days of requests from Claude Code, Codex, OpenCode and Gemini CLI over several models (different speeds, TTFT, cache reads, reasoning, a few 429/502 errors, sessions); more than 200 Claude Code and Codex session files, many never sent through magpie; and two ChatGPT accounts signed in to Codex (maya.chen@example.com on Pro with credits, the 5-hour window at 37%; dev.team@example.com on Plus at 91%), with their quota windows read live and five weeks of history behind the charts and forecasts. The UI language is ${LOCALE}. What it doesn't have: Claude, Copilot, Kiro or other subscription accounts besides those two Codex ones, agents other than freshly installed ones.

The diff is what the PR does. The title and description are the author's words and may be out of date or wrong: plan and summarise from the diff alone, and when the description claims something in the UI the diff doesn't do (a setting, a page, a button), don't look for it — say so in "mismatch".

Places ("start" of a scene): agents, providers, gateway, routing, usage, library, settings (tabs of the main window; the tab bar is nav#nav with button[data-view=…]; settings opens from #prefs) and panel (the menu-bar icon's quick panel, a separate page).

You get the PR's title, description and diff, and an outline of each place as it is rendered now: one element per line, indented by nesting, as tag#id.class[data-x="…"] attributes "own text". Elements marked (below the fold) are further down their page, out of sight until it is scrolled: a "shot" without a target shows only what's on screen, so bring them into view first ("scroll" with "to"), or give the shot that target. Click, hover, type and a shot's target scroll to their element themselves, on camera. Use only selectors you can build from what the outline shows, or, for what only appears after an interaction (a popover, menu, dialog, hover state), from the diff itself: the outlines show each place closed, so an element the diff styles or builds being missing from them means you must open it first, not that it isn't there.

Write a plan that shows a reviewer exactly what this PR changes in the UI and whether it works as intended: go where the change is, do what a user would do to see it (open the menu, hover the row, type in the field, switch the tab…), and take a screenshot at each state that matters, before and after an interaction when that is the point. Captions say what is being done or what to look at ("点击「全部隐藏」后的列表"), never what the result is or should be — the reviewer judges that from the picture, and the sandbox may differ from what you expect; and they never name a thing the diff doesn't add. When the change only shows with data this sandbox doesn't have, say so in "unseen" and still show the place it would be. Keep it short: usually 1–3 scenes, under 15 steps each. Don't show unrelated pages. Never press anything that quits, deletes, removes, updates, restarts or signs out. Say ui_change false (and no scenes) only when the diff plainly changes nothing a user can see — only tests, comments, docs, or code that never reaches the screen; any change to the page's CSS, markup, text or behaviour is a UI change.

Reply with JSON only:
{
  "ui_change": true,
  "summary": "one or two sentences, in Chinese, on what the diff changes in the UI",
  "mismatch": "Chinese, or empty: what the description says the UI gets that the diff doesn't do",
  "unseen": "Chinese, or empty: what of the change this sandbox can't show, and why",
  "scenes": [
    { "title": "short Chinese title", "start": "agents",
      "steps": [
        { "do": "click", "target": "css selector", "text": "optional: only elements containing this text", "caption": "Chinese caption shown in the video" },
        { "do": "hover", "target": "…", "caption": "…" },
        { "do": "type", "target": "…", "value": "text to type", "caption": "…" },
        { "do": "press", "key": "Escape" },
        { "do": "scroll", "to": "css selector of what to bring into view", "text": "optional" },
        { "do": "scroll", "target": "optional scroll container", "dy": 400 },
        { "do": "wait", "ms": 800 },
        { "do": "shot", "name": "kebab-case-name", "caption": "Chinese: what this screenshot shows", "target": "optional: capture just this element (with some room around it)" }
      ] }
  ]
}`;

// the mouse glides like a hand would, so its trail reads in the video
let mouse = { x: VIEW.width / 2, y: VIEW.height / 2 };
async function glide(page, x, y) {
  const d = Math.hypot(x - mouse.x, y - mouse.y);
  const ms = Math.min(900, Math.max(250, d * 1.1));
  const n = Math.max(8, Math.round(ms / 16));
  const from = { ...mouse };
  for (let i = 1; i <= n; i++) {
    const t = i / n, e = t < .5 ? 4 * t * t * t : 1 - (-2 * t + 2) ** 3 / 2;
    // a slight arc, not a ruler line
    const bend = Math.sin(Math.PI * t) * Math.min(40, d * .08);
    const nx = from.x + (x - from.x) * e - ((y - from.y) / (d || 1)) * bend;
    const ny = from.y + (y - from.y) * e + ((x - from.x) / (d || 1)) * bend;
    await page.mouse.move(nx, ny);
    await sleep(16);
  }
  mouse = { x, y };
}

async function locate(page, step) {
  if (!step.target) throw new Error(`${step.do} needs a target`);
  let loc = page.locator(step.target);
  if (step.text) loc = loc.filter({ hasText: step.text });
  const n = await loc.count();
  for (let i = 0; i < n; i++) {
    const one = loc.nth(i);
    if (await one.isVisible()) return one;
  }
  throw new Error(`nothing visible matches ${step.target}${step.text ? ` with "${step.text}"` : ""} (${n} in the page)`);
}

// Out of sight is scrolled to as a reader would, with the wheel over what
// scrolls: magpie puts back any scroll the reader didn't ask for
// (scrollOnPurpose in app.js), so scrollIntoView from here was undone and a
// change further down the page never made it into the recording. Each view
// that hides it, the innermost first, is wheeled in small steps until the
// element is in the middle of it, or at its top when it's taller.
async function bringIntoView(page, loc) {
  const PAD = 24;
  for (let pass = 0; pass < 6; pass++) {
    const views = await loc.evaluate((e, PAD) => {
      const out = [];
      const clip = { top: 0, bottom: innerHeight, left: 0, right: innerWidth };
      const scrollers = [];
      for (let s = e.parentElement; s; s = s.parentElement) {
        const root = s === document.scrollingElement;
        const o = getComputedStyle(s).overflowY;
        if ((root || /auto|scroll|overlay/.test(o)) && s.scrollHeight > s.clientHeight + 1) scrollers.push(s);
      }
      const r = e.getBoundingClientRect();
      for (const s of scrollers) {
        const root = s === document.scrollingElement;
        const b = root ? clip : s.getBoundingClientRect();
        // what of it shows: inside the window and every view around it
        let top = Math.max(b.top, 0), bottom = Math.min(b.bottom, innerHeight);
        let left = Math.max(b.left, 0), right = Math.min(b.right, innerWidth);
        for (let p = s.parentElement; p && !root; p = p.parentElement) {
          if (!scrollers.includes(p) || p === document.scrollingElement) continue;
          const pb = p.getBoundingClientRect();
          top = Math.max(top, pb.top); bottom = Math.min(bottom, pb.bottom);
          left = Math.max(left, pb.left); right = Math.min(right, pb.right);
        }
        const room = bottom - top - 2 * PAD;
        const want = r.height <= room ? (r.top + r.bottom) / 2 - (top + bottom) / 2 : r.top - (top + PAD);
        const shown = r.top >= top + Math.min(PAD, Math.max(0, room - r.height) / 2) && (r.height <= room ? r.bottom <= bottom - PAD : r.top <= top + PAD * 2);
        out.push({ shown, dy: Math.max(-s.scrollTop, Math.min(s.scrollHeight - s.clientHeight - s.scrollTop, want)),
          x: (left + right) / 2, y: (top + bottom) / 2, seen: bottom - top > 20 && right - left > 20 });
      }
      return out;
    }, PAD);
    const v = views.find((v) => !v.shown && Math.abs(v.dy) >= 4 && v.seen);
    if (!v) return;
    await glide(page, v.x, v.y);
    await wheel(page, v.dy);
    await sleep(350);
  }
}

// a hand's flick: a few notches at a time, not one jump
async function wheel(page, dy) {
  for (let left = dy; Math.abs(left) >= 1;) {
    const n = Math.sign(left) * Math.min(Math.abs(left), 90);
    await page.mouse.wheel(0, n);
    left -= n;
    await sleep(28);
  }
}

async function pointAt(page, loc) {
  await bringIntoView(page, loc);
  const b = await loc.boundingBox();
  if (!b) throw new Error("the element has no box");
  const x = b.x + Math.min(b.width / 2, 60), y = b.y + b.height / 2;
  await glide(page, x, y);
  return b;
}

// the app polls, so the network never goes quiet: loaded, and its first
// paint of rows in, is ready
async function settle(page) {
  await page.waitForLoadState("load").catch(() => {});
  await page.waitForFunction(() => document.querySelector("#nav button.on, .row, .pane, main") && !document.querySelector(".skel, .skeleton"), null, { timeout: 8000 }).catch(() => {});
  await sleep(700);
}

const caption = (page, text) => page.evaluate((t) => window.__uiPreviewCaption?.(t), text || "").catch(() => {});
let shotN = 0;

async function runStep(page, step, scene) {
  if (step.caption) await caption(page, step.caption);
  switch (step.do) {
    case "click": {
      const loc = await locate(page, step);
      const label = (await loc.innerText().catch(() => "")) + " " + (await loc.getAttribute("title").catch(() => "") || "");
      if (FORBIDDEN.test(label)) throw new Error(`won't press "${label.trim().slice(0, 40)}"`);
      await pointAt(page, loc);
      await sleep(180);
      await page.mouse.down();
      await sleep(90);
      await page.mouse.up();
      await sleep(700);
      break;
    }
    case "hover": {
      await pointAt(page, await locate(page, step));
      await sleep(900);
      break;
    }
    case "type": {
      const loc = await locate(page, step);
      await pointAt(page, loc);
      await page.mouse.down(); await sleep(80); await page.mouse.up();
      await loc.fill("");
      await page.keyboard.type(String(step.value ?? ""), { delay: 70 });
      await sleep(600);
      break;
    }
    case "press":
      await page.keyboard.press(step.key || "Escape");
      await sleep(500);
      break;
    case "scroll": {
      if (step.to) {
        await bringIntoView(page, await locate(page, { ...step, target: step.to }));
        await sleep(400);
        break;
      }
      // over the view to scroll, or the page's middle: the wheel turns what's under the mouse
      if (step.target) await pointAt(page, await locate(page, step));
      else if (mouse.y < 60) await glide(page, VIEW.width / 2, VIEW.height / 2);
      await wheel(page, step.dy ?? 400);
      await sleep(700);
      break;
    }
    case "wait":
      await sleep(Math.min(step.ms ?? 800, 4000));
      break;
    case "shot": {
      // in sight first, scrolled to on camera: the clip is of the screen
      const loc = step.target ? await locate(page, step) : null;
      if (loc) await bringIntoView(page, loc);
      await sleep(250);
      await checkLeak(page, `before ${step.name}`);
      const file = `${String(++shotN).padStart(2, "0")}-${(step.name || "shot").replace(/[^a-z0-9-]/gi, "-").slice(0, 40)}.png`;
      // the screenshot without the drawn mouse and caption
      await page.evaluate(() => document.querySelector("[data-ui-preview]")?.style.setProperty("visibility", "hidden"));
      let clip;
      try {
        if (loc) {
          const b = await loc.boundingBox();
          if (b) {
            // the target with room around it, never so small that a button
            // is shown without where it is
            const w = Math.min(VIEW.width, Math.max(560, b.width + 48)), h = Math.min(VIEW.height, Math.max(340, b.height + 48));
            const x = Math.min(VIEW.width - w, Math.max(0, b.x + b.width / 2 - w / 2));
            const y = Math.min(VIEW.height - h, Math.max(0, b.y + b.height / 2 - h / 2));
            clip = { x, y, width: w, height: h };
          }
        }
        await page.screenshot({ path: path.join(OUT, file), clip });
      } finally {
        await page.evaluate(() => document.querySelector("[data-ui-preview]")?.style.removeProperty("visibility"));
      }
      scene.shots.push({ file, caption: step.caption || step.name || "", width: 2 * (clip?.width ?? VIEW.width) });
      break;
    }
    default:
      throw new Error(`unknown step ${step.do}`);
  }
  if (step.do !== "shot") await checkLeak(page, `after ${step.do}`);
}

async function go(page, where, current) {
  // between tabs of the window, click the tab, so the video shows the way
  if (current && current !== "panel" && where !== "panel" && VIEWS.includes(where)) {
    const tab = where === "settings" ? page.locator("#prefs") : page.locator(`#nav button[data-view="${where}"]`);
    if (await tab.isVisible().catch(() => false)) {
      await pointAt(page, tab);
      await page.mouse.down(); await sleep(90); await page.mouse.up();
      await sleep(900);
      return;
    }
  }
  await page.goto(urlFor(where));
  await settle(page);
  await page.evaluate(([x, y]) => window.__uiPreviewAt?.(x, y), [mouse.x, mouse.y]);
}

async function main() {
  await fs.mkdir(OUT, { recursive: true });
  const diff = await fs.readFile(env("DIFF_FILE"), "utf8");
  const body = await fs.readFile(env("PR_BODY_FILE", "/dev/null"), "utf8").catch(() => "");
  const browser = await chromium.launch();
  const ctxOpts = { viewport: VIEW, deviceScaleFactor: 2, locale: LOCALE, colorScheme: "light" };

  // what each place looks like now, for the plan
  const look = await browser.newContext(ctxOpts);
  const lp = await look.newPage();
  const outlines = {};
  for (const where of [...VIEWS, "panel"]) {
    try {
      await lp.goto(urlFor(where));
      await settle(lp);
      outlines[where] = await lp.evaluate(outline);
    } catch (e) { outlines[where] = `(failed to load: ${e.message})`; }
  }
  await look.close();

  const MAXDIFF = 150e3;
  const code = await codeContext(diff, env("SRC_DIR")).catch((e) => (log("code context:", e.message), ""));
  log(`code context: ${code.length} characters`);
  const ask = [
    { role: "system", content: GUIDE },
    { role: "user", content: `PR title: ${env("PR_TITLE", "")}\n\nPR description:\n${body.slice(0, 6000)}\n\nDiff${diff.length > MAXDIFF ? " (cut)" : ""}:\n${diff.slice(0, MAXDIFF)}\n\n` +
      Object.entries(outlines).map(([k, v]) => `=== outline: ${k} ===\n${v}`).join("\n\n") +
      (code ? `\n\n=== the page's code around the change (the PR's version), to see how to reach it ===\n${code}` : "") },
  ];
  let plan;
  try { plan = env("PLAN_FILE") ? JSON.parse(await fs.readFile(env("PLAN_FILE"), "utf8")) : await deepseek(ask); }
  catch (e) { plan = null; manifest.errors.push(`plan: ${e.message}`); }
  if (!plan || plan.ui_change === false || !plan.scenes?.length) {
    manifest.summary = plan?.summary || "";
    manifest.skipped = plan ? "no visible UI change found in the diff" : "no plan";
    // still something to look at: the first place the diff seems to touch
    plan = { summary: manifest.summary, scenes: plan ? [] : [{ title: "Agent 页", start: "agents", steps: [{ do: "shot", name: "agents", caption: "Agent 页" }] }] };
  }
  manifest.summary = plan.summary || "";
  manifest.mismatch = plan.mismatch || "";
  manifest.unseen = plan.unseen || "";
  log("plan:", JSON.stringify(plan, null, 1));

  if (plan.scenes.length) {
    const rec = await browser.newContext({ ...ctxOpts, recordVideo: { dir: path.join(OUT, "raw"), size: VIEW } });
    await rec.addInitScript({ path: path.join(here, "cursor.js") });
    const page = await rec.newPage();
    const began = Date.now();
    let at = null, repairs = 0, interactive = false, lead = 0;
    const places = new Set();
    for (const s of plan.scenes.slice(0, 5)) {
      const scene = { title: s.title || "", shots: [] };
      manifest.scenes.push(scene);
      try {
        await caption(page, scene.title);
        await go(page, s.start || "agents", at);
        if (!at) lead = Math.max(0, (Date.now() - began) / 1000 - 0.4);
        at = s.start || "agents";
        places.add(at);
      } catch (e) { manifest.errors.push(`${scene.title}: open ${s.start}: ${e.message}`); if (manifest.leak) break; continue; }
      let steps = (s.steps || []).slice(0, 25);
      for (let i = 0; i < steps.length; i++) {
        const step = steps[i];
        if (["click", "hover", "type", "press", "scroll"].includes(step.do)) interactive = true;
        try { await runStep(page, step, scene); }
        catch (e) {
          if (manifest.leak) break;
          manifest.errors.push(`${scene.title} · ${step.do} ${step.target || step.name || ""}: ${e.message.split("\n")[0]}`);
          log("step failed:", e.message);
          if (repairs >= 3) continue;
          repairs++;
          // the page as it is now, and what went wrong: the rest of the scene again
          try {
            const fix = await deepseek([...ask, { role: "assistant", content: JSON.stringify(plan) },
              { role: "user", content: `Step ${i + 1} of scene "${scene.title}" failed: ${e.message.split("\n")[0]}\nThe page now:\n${await page.evaluate(outline)}\n\nReply with JSON {"steps": [...]}: the steps to do instead of that one and the ones after it in this scene.` }]);
            if (Array.isArray(fix.steps)) { steps = [...steps.slice(0, i + 1), ...fix.steps.slice(0, 20)]; log("repaired:", JSON.stringify(fix.steps)); }
          } catch (e2) { manifest.errors.push(`repair: ${e2.message}`); }
        }
      }
      if (manifest.leak) break;
      await caption(page, "");
      await sleep(600);
    }
    for (const s of plan.scenes) for (const st of s.steps || []) if (st.do === "click" && /data-view|#nav|#prefs/.test(st.target || "")) places.add(st.target);
    const video = await page.video().path();
    await rec.close();
    manifest.interactive = interactive;
    manifest.pages = places.size;
    // a video when there's something to watch: more than one page, or an interaction
    if (!manifest.leak && (interactive || places.size > 1)) {
      execFileSync("ffmpeg", ["-y", "-loglevel", "error", "-ss", lead.toFixed(2), "-i", video, "-c:v", "libx264", "-preset", "medium", "-crf", "20",
        "-pix_fmt", "yuv420p", "-movflags", "+faststart", "-r", "30", path.join(OUT, "preview.mp4")]);
      manifest.video = "preview.mp4";
      manifest.poster = await poster(browser);
    }
    await fs.rm(path.join(OUT, "raw"), { recursive: true, force: true });
  }
  await browser.close();
  if (manifest.leak) {
    for (const f of await fs.readdir(OUT)) await fs.rm(path.join(OUT, f), { recursive: true, force: true });
    manifest.scenes = []; manifest.video = manifest.poster = null;
  }
  await fs.writeFile(path.join(OUT, "manifest.json"), JSON.stringify(manifest, null, 2));
  log("done:", JSON.stringify({ shots: manifest.scenes.reduce((n, s) => n + s.shots.length, 0), video: manifest.video, errors: manifest.errors.length, leak: manifest.leak }));
}

// the video's still in the PR: a frame from the middle with a play button
async function poster(browser) {
  const frame = path.join(OUT, "frame.png");
  const dur = parseFloat(execFileSync("ffprobe", ["-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", path.join(OUT, "preview.mp4")]).toString()) || 2;
  execFileSync("ffmpeg", ["-y", "-loglevel", "error", "-ss", String(Math.min(dur / 3, 6)), "-i", path.join(OUT, "preview.mp4"), "-frames:v", "1", frame]);
  const img = (await fs.readFile(frame)).toString("base64");
  const p = await browser.newPage({ viewport: VIEW, deviceScaleFactor: 1 });
  await p.setContent(`<body style="margin:0;background:url(data:image/png;base64,${img}) center/cover;height:100vh;display:grid;place-items:center">
    <div style="position:absolute;inset:0;background:rgba(0,0,0,.28)"></div>
    <div style="position:relative;display:flex;align-items:center;gap:18px;padding:22px 34px 22px 26px;border-radius:999px;background:rgba(17,17,17,.78);color:#fff;font:600 30px -apple-system,'Segoe UI','Noto Sans CJK SC',sans-serif">
      <svg width="46" height="46" viewBox="0 0 24 24"><circle cx="12" cy="12" r="12" fill="#fff"/><path d="M9.5 7.5v9l7-4.5z" fill="#111"/></svg>
      播放录屏 · ${Math.round(dur)} 秒</div></body>`);
  await p.screenshot({ path: path.join(OUT, "poster.png") });
  await p.close();
  await fs.rm(frame);
  return "poster.png";
}

main().catch(async (e) => {
  console.error(e);
  manifest.errors.push(`recorder: ${e.message}`);
  await fs.mkdir(OUT, { recursive: true }).catch(() => {});
  await fs.writeFile(path.join(OUT, "manifest.json"), JSON.stringify(manifest, null, 2)).catch(() => {});
  process.exit(1);
});
