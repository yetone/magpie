// Run with Node's test runner and Playwright on the module path; see README.md.
// Routing, the context window card's "This session" line (#5561): the line
// under the card is the session's own conversation, drawn by the same
// attribution the Usage page's Context tab uses (contextOfRecs) — the
// lead's turns alone on the lead's line, a subagent on a line of its own
// (its prompt is its own conversation's, not the session's), and a
// tool-less call kept out of a tool-carrying session's line (the agent's
// side call under its session id — a classifier, a title — whose tokens
// cannot stand for the conversation, e98440ae). The card drew every one of
// the session's requests into one line, so a lead holding at 96K–108K read
// as swings to 298K and back. The live strip of sessions (renderCtxSessions)
// reads its numbers off the session's own newest turn too, not off whatever
// asked last. Attribution is data logic, not layout, so one language (in
// both engines) carries the file; the row order, the fmtK shapes and the
// strip's percentages are what the English page draws.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const iso = (ms) => new Date(ms).toISOString();

const tool = (tokens) => ({
  window: 512000, tokens, counted: true, turns: 3,
  parts: [
    { kind: "system", tokens: 3000, items: [{ name: "prompt", tokens: 3000 }] },
    { kind: "tools", tokens: 15000, items: [{ name: "Bash", tokens: 15000 }] },
    { kind: "chat", tokens: tokens - 18000, items: [{ name: "turn", tag: "turns", n: 3, tokens: tokens - 18000 }] },
  ],
});
// a call with no tool definitions: the agent's side call under its
// session id (Claude Code's auto-mode classifier, a title, a quota check)
const plain = (tokens) => ({
  window: 512000, tokens, counted: true, parts: [
    { kind: "system", tokens: 2000, items: [{ name: "prompt", tokens: 2000 }] },
    { kind: "chat", tokens: tokens - 2000, items: [{ name: "turn", tag: "turns", n: 1, tokens: tokens - 2000 }] },
  ],
});

const seat = { id: "acc@me", provider: "anthropic", name: "Acme", who: "me@example.com", kind: "account", model: "gpt-6.1-sol", known: true };
function req(id, session, tokens, kind) {
  const t0 = now - (600 - id) * 1000;
  return {
    id, seq: id * 10, time: iso(t0), agent: "claude", session, model: "gpt-6.1-sol", provider: "anthropic",
    ...(kind === "sub" ? { sub: "agent-1" } : {}),
    order: [seat], tries: [{ id: seat.id, model: seat.model, start: iso(t0), done: true, status: 200, ms: 3000 }],
    done: true, status: 200, ms: 3000, tokens: 900, usage: [{ in: tokens, out: 900 }],
    prompt: kind === "plain" ? plain(tokens) : tool(tokens),
  };
}
// the reporter's session (WK-Apollo, 2026-11-07): a lead at 96K–108K, a
// subagent at 298K, two tool-less side calls at 7K and 13K, interleaved —
// ids in the order the requests came
const session = [
  req(1, "s-1", 96085), req(2, "s-1", 298358, "sub"), req(3, "s-1", 96732), req(4, "s-1", 7167, "plain"),
  req(5, "s-1", 98917), req(6, "s-1", 105701), req(7, "s-1", 12682, "plain"), req(8, "s-1", 108378),
];
// a second session sending at the same time, so the live strip of sessions
// shows; its own lead's newest is 52K
const other = [req(9, "s-2", 50000), req(10, "s-2", 52000)];
// the strip's fixture: a subagent asked last, so whatever asked last is not
// the session's own reading
const strip = [
  req(1, "s-1", 102000), req(2, "s-2", 50000), req(3, "s-1", 104000), req(4, "s-1", 298358, "sub"), req(5, "s-2", 52000),
];

function serve(routes) {
  const state = { agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color", path: "/test/claude", fields: [] }], profiles: [], settings: { lang: "en", theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: 'window.bootPrefs = {lang:"en",theme:"light",web:true};' });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // no more requests come in
      return json({ mine: true, now: new Date().toISOString(), seq: 10, totals: { requests: routes.length, rerouted: 0, errors: 0 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file).catch(() => ""), contentType });
  };
}

async function open(engine, routes) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
  page.setDefaultTimeout(8000);
  page.setDefaultNavigationTimeout(60000);
  await page.route("**/*", serve(routes));
  await page.goto("http://magpie.test/?view=routing");
  await page.locator(".rt-req").first().waitFor();
  await page.locator(".rt-ctx .ctx-card").waitFor();
  await page.waitForTimeout(400);
  return { browser, page };
}

// the card's "This session" row: the count it says and every point's
// tokens, in the order the line draws them
const line = (page) => page.evaluate(() => {
  const k = [...document.querySelectorAll(".rt-ctx .ctx-card .ctx-k")].find((e) => e.textContent === "This session");
  const row = k && k.parentElement;
  if (!row) return null;
  const hits = [...row.querySelectorAll("svg rect.hit")];
  return {
    count: row.querySelector(".ctx-sub")?.textContent || "",
    n: hits.length,
    tip: hits.map((h) => h.querySelector("title")?.textContent || ""),
    // the card's own figure: the Context used stat's number (the first stat
    // of the card, not the cache's)
    used: document.querySelector(".rt-ctx .ctx-card .ctx-stat .ctx-big b")?.textContent || "",
    usedN: document.querySelector(".rt-ctx .ctx-card .ctx-stat .ctx-big b")?.dataset.n || "",
  };
});
// the rows list newest first, so the row for id is nth(idCount - id); the
// card is the picked request's once its Context used figure is that
// request's own tokens. A click that lands while the list redraws under
// a busy run picks nothing, so it is clicked again until the card is that
// request's, and a card that never gets there names what it showed.
const pick = async (page, id, total, tokens) => {
  const shown = () => page.evaluate(() => document.querySelector(".rt-ctx .ctx-card .ctx-stat .ctx-big b")?.dataset.n || "");
  for (let i = 0; i < 4; i++) {
    await page.locator(".rt-req").nth(total - id).click();
    try {
      await page.waitForFunction((n) => document.querySelector(".rt-ctx .ctx-card .ctx-stat .ctx-big b")?.dataset.n === String(n), tokens, { timeout: 3000 });
      return;
    } catch {}
  }
  assert.fail(`request ${id}'s card never showed its ${tokens} tokens, it shows ${JSON.stringify(await shown())}`);
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
test(`${engine}: the card's session line is the conversation's, as Usage's Context draws it`, async () => {
  const { browser, page } = await open(engine, [...session, ...other]);
  try {
    const total = session.length + other.length;

    // the lead's line: its turns alone — 96K, 97K, 99K, 106K, 108K; the
    // subagent's 298K and the side calls' 7K and 13K are not swings in it
    await pick(page, 8, total, 108378);
    const lead = await line(page);
    assert.ok(lead, "the lead's card draws a session line");
    assert.equal(lead.n, 5, `the lead's line holds the lead's turns alone, got ${lead.n} (${lead.tip.join(", ")})`);
    assert.equal(lead.count, "5 requests", `the lead's line's count, got ${lead.count}`);
    assert.deepEqual(lead.tip, ["Request 1: 96.1K tokens", "Request 2: 96.7K tokens", "Request 3: 98.9K tokens", "Request 4: 106K tokens", "Request 5: 108K tokens"],
      `the lead's line's points, got ${lead.tip.join(", ")}`);

    // a tool-less side call on its own card: its session's line, drawn by
    // the same attribution, without a point of its own on it — its figure
    // is the card's own, not a swing in the conversation's
    await pick(page, 4, total, 7167);
    const side = await line(page);
    assert.ok(side, "the side call's card draws");
    assert.equal(side.n, 5, `a side call is no turn of the conversation, got ${side.n} (${side.tip.join(", ")})`);
    assert.equal(side.count, "5 requests", `the side call's line's count, got ${side.count}`);
    assert.deepEqual(side.tip, ["Request 1: 96.1K tokens", "Request 2: 96.7K tokens", "Request 3: 98.9K tokens", "Request 4: 106K tokens", "Request 5: 108K tokens"],
      `the side call's card draws the session's own line, got ${side.tip.join(", ")}`);
    assert.equal(side.used, "7.2K", "the side call's own figure");

    // a subagent on its own card: its own line alone — a single request of
    // its own draws no session line over the lead's, and its figure is its
    // own 298K, not a swing in the conversation's
    await pick(page, 2, total, 298358);
    const sub = await line(page);
    assert.equal(sub, null, `the subagent's card draws no session line but its own, got ${sub && sub.n + " points (" + sub.tip.join(", ") + ")"}`);
    assert.equal(await page.evaluate(() => document.querySelector(".rt-ctx .ctx-card .ctx-stat .ctx-big b")?.dataset.n), "298358", "the subagent's own figure");
  } finally {
    await browser.close();
  }
});

test(`${engine}: a session's strip block reads its own newest turn, not whatever asked last`, async () => {
  const { browser, page } = await open(engine, strip);
  try {
    await page.locator(".rt-ctx-s").first().waitFor();
    const blocks = await page.evaluate(() => [...document.querySelectorAll(".rt-ctx-s")].map((b) => b.querySelector(".sub")?.textContent || ""));
    assert.ok(blocks.length >= 2, `both live sessions have a block, got ${blocks.length}`);
    // s-1's own reading is its lead's newest, 104K — not the subagent's
    // 298K that asked last
    const own = blocks.find((s) => s.includes("104K"));
    assert.ok(own, `the strip reads the session's own newest turn (104K), got ${blocks.join(" | ")}`);
    assert.ok(!blocks.some((s) => s.includes("298K")), `no block rides the subagent's reading, got ${blocks.join(" | ")}`);
  } finally {
    await browser.close();
  }
});
}
