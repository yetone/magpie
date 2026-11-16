// Run with Node's test runner and Playwright on the module path; see README.md.
// lexrus on X: on the Routing page's Accounts and keys, with many accounts
// laid out in columns, a window's reset ("resets in 3h") was drawn over
// the column beside it, and the right column's were cut at the card's
// edge. Each window's parts were as wide as the widest of the whole list
// (WorkBuddy's "deepseek-v4.1-flash tokens (est.)", its 200,000,000 /
// 200,000,000 · 100% used), wider than a column, and a part kept that
// width past its row. Now a window's line lines up with the rows of its
// own provider, and no part runs past its row at any width, every part
// in full. In English and Chinese, from a phone's width to a wide window,
// in both engines. And with that many accounts, dragging the window's
// edge stuttered: the list was CSS columns, balanced again on every
// layout; now its boxes are placed in their columns by the page.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.join(__dirname, "../assets"));
const now = Date.now();
const iso = (ms) => new Date(ms).toISOString();
const H = 3600e3;

// the screenshot's providers: accounts with their Usage cards, and keys
const subs = [
  ["codex", "Codex", [["plus", "a@example.com", [{ name: "5 hours", used: 92, resetsAt: iso(now + 3 * H) }, { name: "7 days", used: 94, resetsAt: iso(now + 5 * 24 * H) }, { name: "Image generation", display: "Unknown" }]]]],
  ["copilot", "Copilot", [["Pro", "b-user", [{ name: "Premium requests", used: 100, limit: 1500, amount: 1569.2, resetsAt: iso(now + 14 * 24 * H) }, { name: "Chat requests", unlimited: true }, { name: "Completions", unlimited: true }]]]],
  ["cursor", "Cursor", [["Premium", "c@example.com", [{ name: "Weekly", used: 96.6, resetsAt: iso(now + 26 * H) }, { name: "Grok Bot", used: 96.6, resetsAt: iso(now + 26 * H) }, { name: "On-demand", display: "$0.00" }]]]],
  ["workbuddy", "WorkBuddy", [["Free plan", "w-user", [
    { name: "Credits", used: 100, limit: 500, amount: 500, unit: "credits", resetsAt: iso(now + 20 * H) },
    { name: "Extra credits", used: 98.5, limit: 2517, amount: 2479.82, unit: "credits", resetsAt: iso(now + 9 * 24 * H) },
    { name: "Total credits", used: 98.8, limit: 3017, amount: 2979.82, unit: "credits", resetsAt: iso(now + 9 * 24 * H) },
    { name: "deepseek-v4.1-flash tokens (est.)", used: 100, limit: 200000000, amount: 200000000, resetsAt: iso(now + 30 * H) },
  ]]]],
  ["workbuddy-ai", "WorkBuddy AI", [["Free plan", "x-user", [
    { name: "Credits", used: 100, limit: 100, amount: 100, unit: "credits", resetsAt: iso(now + 20 * H) },
    { name: "Extra credits", used: 100, limit: 90, amount: 90, unit: "credits", resetsAt: iso(now + 9 * 24 * H) },
    { name: "Total credits", used: 100, limit: 190, amount: 190, unit: "credits", resetsAt: iso(now + 9 * 24 * H) },
  ]]]],
  ["zcode", "ZCode", [["GLM Coding Lite", "z@example.com", [{ name: "5 hours", used: 98, resetsAt: iso(now + 2 * H) }, { name: "Weekly", used: 10, resetsAt: iso(now + 6 * 24 * H) }]]]],
  ["qoder", "Qoder", [["personal_pro", "q@example.com", [{ name: "Add-on credits", used: 35, limit: 100, amount: 35, unit: "credits", resetsAt: iso(now + 26 * H) }]]]],
];
// many: the screenshot's list; long: a user with a score of Codex
// accounts, more rows than one column holds, which run on into the next
function fixture(codexes) {
  const accts = [], cards = [];
  for (const [p, name, as] of subs) {
    for (const [plan, who, windows] of as) {
      accts.push({ id: p + "@" + who, provider: p, name, who, kind: "account", agent: p, model: "m-" + p, known: false, plan });
      cards.push({ provider: p, name, kind: "subscription", user: who, plan, windows });
    }
  }
  for (let i = 2; i <= codexes; i++) {
    const who = `codex${i}@example.com`;
    accts.push({ id: "codex@" + who, provider: "codex", name: "Codex", who, kind: "account", agent: "codex", model: "m-codex", known: false, plan: "plus" });
    cards.push({ provider: "codex", name: "Codex", kind: "subscription", user: who, plan: "plus", windows: [{ name: "5 hours", used: 10 * (i % 10), resetsAt: iso(now + i * H) }, { name: "7 days", used: 9 * (i % 11), resetsAt: iso(now + i * 24 * H) }, { name: "Image generation", display: "Unknown" }] });
  }
  const keys = ["Google Gemini", "NVIDIA NIM", "OpenAI", "OpenCode Zen Free", "OpenRouter", "Our Free Model"].map((name, i) => ({
    id: "key" + i, provider: "k" + i, name, kind: "provider", model: "deepseek-ai/deepseek-v4.1-flash-" + i,
  }));
  const order = [...accts, ...keys];
  const route = {
    id: 7, seq: 7, time: iso(now - 2000), agent: "claude", model: "m", provider: "codex", done: true, status: 200, ms: 900, tokens: 1500,
    order, tries: [{ id: order[0].id, model: "m-codex", start: iso(now - 2000), done: true, ms: 800, status: 200 }],
  };
  return { cards, route };
}

function serve(lang, { cards, route }) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }], profiles: [], settings: { lang, theme: "light", quotaLeft: false } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 7, totals: { requests: 1, rerouted: 0, errors: 0 }, routes: [route] });
    }
    if (url.pathname === "/api/usage/quotas") return json(cards);
    if (url.pathname === "/api/usage/quotas/history") return json([]);
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// every part of every window's line inside its row, and in full
const overruns = () => [...document.querySelectorAll(".rt-act .wq > div > span")].flatMap((s) => {
  const row = s.closest(".rt-act"), rr = row.getBoundingClientRect(), r = s.getBoundingClientRect();
  if (!r.width) return [];
  const pad = parseFloat(getComputedStyle(row).paddingRight);
  const out = [];
  if (r.right > rr.right - pad + 1 || r.left < rr.left - 1) out.push(`${s.textContent} [${Math.round(r.left)}–${Math.round(r.right)}] past its row [${Math.round(rr.left)}–${Math.round(rr.right - pad)}]`);
  if (s.scrollWidth > s.clientWidth + 1) out.push(`${s.textContent} cut`);
  return out;
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a window's line stays in its row at every width`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const [kind, codexes] of [["many", 4], ["long", 20]]) for (const lang of ["en", "zh"]) {
      const fx = fixture(codexes);
      await t.test(`${kind} ${lang}`, async () => {
        const ctx = await browser.newContext({ viewport: { width: 1200, height: 2400 }, reducedMotion: "reduce" });
        try {
          // every account in sight: the list whole, and Codex's opened
          await ctx.addInitScript(() => { try { localStorage.setItem("magpie.routingActsAll", "1"); localStorage.setItem("magpie.routingKeysOpen", JSON.stringify(["codex/@"])); } catch {} });
          const page = await ctx.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang, fx));
          await page.goto("http://magpie.test/?view=routing");
          await page.waitForFunction((n) => document.querySelectorAll(".rt-act .wq").length === n, fx.cards.length);
          for (const width of [420, 560, 700, 765, 840, 960, 1100, 1300, 1600]) {
            await page.setViewportSize({ width, height: 2400 });
            // the layout's classes are set a frame after the size is seen
            await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(() => requestAnimationFrame(r)))));
            const bad = await page.evaluate(overruns);
            if (process.env.ARTIFACT_DIR) {
              await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
              await page.locator(".rt-acts").first().screenshot({ path: path.join(process.env.ARTIFACT_DIR, `fit-${engine}-${kind}-${lang}-${width}.png`) }).catch(() => {});
            }
            assert.deepEqual(bad, [], `${lang} at ${width}px`);
            // a provider's rows still line up: each part starts at one
            // place on every line of every row of it
            const starts = await page.evaluate(() => [...document.querySelectorAll(".rt-pgrp")].map((g) => {
              const ls = [...g.querySelectorAll(".rt-act .wq > div")];
              return [...new Set(ls.map((l) => {
                const left = l.closest(".rt-act").getBoundingClientRect().left;
                const [k, , v] = [...l.children].map((c) => c.getBoundingClientRect());
                return [k.left - left, v.left - left].map(Math.round).join(",");
              }))];
            }).filter((s) => s.length > 1));
            assert.deepEqual(starts, [], `${lang} at ${width}px: a provider's lines start at one place`);
            // and resizing stays smooth: the list isn't CSS columns, which
            // balanced every row again on each layout (~24ms a layout in
            // WebKit with this list), but boxes placed in order, none over
            // another and all inside the list. A long box's rows run on
            // into the next column in order, its heading never last in one
            const lay = await page.evaluate(() => {
              const acts = document.querySelector(".rt-acts"), ar = acts.getBoundingClientRect();
              const items = [...acts.children].flatMap((g) => g.classList.contains("long") ? [...g.children] : [g]);
              const rs = items.map((e) => e.getBoundingClientRect()), bad = [];
              const name = (i) => `${items[i].className.split(" ")[0]} ${i}`;
              rs.forEach((r, i) => {
                if (r.bottom > ar.top - acts.scrollTop + acts.scrollHeight + 1 || r.right > ar.right + 1 || r.left < ar.left - 1) bad.push(`${name(i)} outside the list`);
                const p = rs[i - 1];
                if (p && !(r.left > p.left + 1 || (Math.abs(r.left - p.left) <= 1 && r.top >= p.bottom - 1))) bad.push(`${name(i)} out of order or over ${name(i - 1)}`);
                if (p && items[i - 1].classList.contains("rt-prov") && Math.abs(r.left - p.left) > 1) bad.push(`${name(i - 1)} last in its column`);
              });
              return { cols: getComputedStyle(acts).columnWidth, lefts: new Set(rs.map((r) => Math.round(r.left))).size, long: acts.querySelectorAll(".long").length, bad };
            });
            assert.equal(lay.cols, "auto", `${lang} at ${width}px: the list isn't CSS columns`);
            assert.deepEqual(lay.bad, [], `${lang} at ${width}px`);
            assert.equal(lay.long > 0, kind === "long", `${kind}: a box long enough to run on`);
            if (width >= 700 && width < 1100) assert.ok(lay.lefts >= 2, `${lang} at ${width}px: ${lay.lefts} column(s)`);
          }
          assert.deepEqual(errors, []);
        } finally {
          await ctx.close();
        }
      });
    }
  });
}
