// Run with Node's test runner and Playwright on the module path; see README.md.
// danzi002 on Discord: after Accounts and keys got compact, "路由里面的的账号
// 与秘钥显示的好乱啊，没有之前一目了然". Cards of three to five lines, each its
// own height, ragged columns, names cut to "bob.work@gm…", and red on every
// resting account (twice: its line and its tally), on a healthy one with
// old failures too. Now each account is one cell like every other: a dot
// for how it is (green in use, amber resting, red to act on), its name,
// plan and Try now on one line, what it says under it; how it fared on
// hover.
// Red only where the reader has to do something. Every language, at a
// window's width and at 420px, in both engines.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const H = 3600e3, M = 60e3;
const iso = (ms) => new Date(ms).toISOString();
const rest = (why, until, status) => ({ key: "r-" + why + until, why, until: iso(until), status, error: why === "quota" ? "You've hit your usage limit." : "" });
const cx = (u, plan, extra, tally) => ({ seat: { id: "codex@" + u, provider: "codex", name: "Codex", who: u + "@example.com", kind: "account", agent: "codex", model: "gpt-5.5", plan, ...extra }, tally });
const fine = (n, ago) => ({ tried: n, ok: n, fails: {}, last: now - ago });
const seats = [
  cx("main", "Pro", { known: true, used: 34, renews: [iso(now + 3 * H)], usedRenews: iso(now + 3 * H) }, fine(120, 20e3)),
  cx("alice", "Plus", { rest: rest("quota", now + 2 * H, 429) }, { tried: 30, ok: 26, fails: { quota: 4 }, last: now - 50 * M }),
  cx("bob", "Plus", { rest: rest("quota", now + 28 * H, 429) }, { tried: 22, ok: 20, fails: { quota: 2 }, last: now - 3 * H }),
  cx("carol", "Plus", { rest: rest("quota", now + 72 * H, 429) }, { tried: 18, ok: 15, fails: { quota: 3 }, last: now - 5 * H }),
  cx("dave", "Team", { rest: rest("rate", now + 90e3, 429) }, { tried: 41, ok: 38, fails: { rate: 3 }, last: now - 4 * M }),
  cx("erin", "Plus", { known: true, used: 82, renews: [iso(now + 50 * M)], usedRenews: iso(now + 50 * M) }, fine(16, 9 * M)),
  // answering again after two refusals: nothing to do, so no red
  cx("frank", "Plus", { known: true, used: 12, renews: [iso(now + 4 * H)], usedRenews: iso(now + 4 * H) }, { tried: 9, ok: 7, fails: { quota: 2 }, last: now - 12 * M }),
  cx("henry", "Plus", {}, { tried: 0, ok: 0, fails: {}, last: 0 }),
  // refused for its sign-in since it last answered: the reader signs in again
  cx("ivy", "Plus", {}, { tried: 2, ok: 1, fails: { auth: 1 }, last: now - 10 * M, authAt: now - 5 * M }),
];
const tallies = {};
for (const { seat, tally } of seats) tallies[JSON.stringify(seat.plan ? [seat.provider, seat.who, seat.plan.toLowerCase()] : [seat.provider, seat.who])] = { ...seat, ...tally, models: tally.ok ? [seat.model] : [] };
const order = seats.map((s) => s.seat);
const routes = [{ id: 1, seq: 1, time: iso(now - 20e3), agent: "codex", model: "gpt-5.5", provider: "codex", done: true, status: 200, ms: 4200, order,
  tries: [{ id: "codex@main", model: "gpt-5.5", start: iso(now - 20e3), done: true, ms: 4100, status: 200 }] }];
const langs = ["en", "zh", "zh-TW", "ja", "de"];

function serve(lang) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light", quotaLeft: false } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 290, rerouted: 12, errors: 21 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [{ day: new Date(now).toISOString().slice(0, 10), requests: 290 }], routes: [], tallies });
    if (url.pathname === "/api/usage/quotas" || url.pathname === "/api/usage/quotas/history") return json([]);
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: each account in Accounts and keys is one calm cell like the others`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const width of [1100, 420]) for (const lang of langs) {
      await t.test(`${lang} at ${width}px`, async () => {
        const ctx = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        try {
          const page = await ctx.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang));
          // the reader opened Codex's accounts
          await page.addInitScript(() => { try { localStorage.setItem("magpie.routingKeysOpen", JSON.stringify(["codex/@"])); } catch {} });
          await page.goto("http://magpie.test/?view=routing");
          await page.locator(".rt-acts .rt-act", { hasText: "ivy@example.com" }).waitFor();
          const rows = await page.evaluate(() => {
            const probe = document.createElement("i");
            probe.style.color = "var(--red)";
            document.body.append(probe);
            const red = getComputedStyle(probe).color;
            probe.remove();
            return [...document.querySelectorAll(".rt-acts .rt-act:not(.rt-keys)")].map((r) => {
              const st = r.querySelector(".st"), box = r.getBoundingClientRect(), nm = r.querySelector(".nm").getBoundingClientRect(), fix = r.querySelector(".fix");
              const seen = (e) => e.getClientRects().length > 0 && getComputedStyle(e).visibility !== "hidden";
              return {
                who: r.querySelector(".nm b").textContent,
                cls: [...r.classList].filter((c) => ["on", "rest", "act"].includes(c)).sort().join(" "),
                dot: getComputedStyle(r.querySelector(".nm .dot")).backgroundColor,
                h: box.height, stLines: st && seen(st) ? Math.round(st.getBoundingClientRect().height / 18) : 0,
                stCut: !!st && st.scrollWidth > st.clientWidth + 1, x: Math.round(box.x), w: box.width,
                cut: r.querySelector(".nm b").scrollWidth > r.querySelector(".nm b").clientWidth + 1,
                fixTop: fix && seen(fix) ? Math.round(fix.getBoundingClientRect().top - nm.top) : null,
                tally: seen(r.querySelector(".tally")),
                lines: [...r.children].filter((e) => seen(e) && !e.classList.contains("bar") && !e.classList.contains("fix")).length,
                reds: [...r.querySelectorAll("*")].filter((e) => seen(e) && e.children.length === 0 && e.textContent.trim() && getComputedStyle(e).color === red).map((e) => e.textContent),
                redDot: getComputedStyle(r.querySelector(".nm .dot")).backgroundColor === red,
              };
            });
          });
          const by = Object.fromEntries(rows.map((r) => [r.who.replace("@example.com", ""), r]));
          assert.deepEqual(Object.keys(by).sort(), ["alice", "bob", "carol", "dave", "erin", "frank", "henry", "ivy", "main"]);
          // how each is, by its dot: in use, resting, to act on, or neither
          assert.equal(by.main.cls, "on");
          for (const u of ["alice", "bob", "carol", "dave"]) assert.equal(by[u].cls, "rest", u);
          for (const u of ["erin", "frank", "henry"]) assert.equal(by[u].cls, "", u);
          assert.equal(by.ivy.cls, "act");
          assert.equal(new Set([by.main.dot, by.alice.dot, by.erin.dot, by.ivy.dot]).size, 4, "four dots apart");
          // red only on the one to sign in again: not on a rest, nor on old refusals
          for (const [u, r] of Object.entries(by)) {
            if (u === "ivy") continue;
            assert.deepEqual(r.reds, [], u + " has no red");
            assert.equal(r.redDot, false, u);
          }
          assert.ok(by.ivy.reds.length > 0 && by.ivy.redDot, "ivy says what to do, in red");
          // one cell each, alike: a name line and one line under it, the
          // same height; how it fared on hover, unless there is something to do
          const calm = rows.filter((r) => r.cls !== "act");
          for (const r of calm) {
            assert.equal(r.lines, 2, r.who + ": its name and what it says");
            assert.equal(r.tally, false, r.who + ": its tally on hover");
            if (r.fixTop !== null) assert.ok(Math.abs(r.fixTop) <= 2, r.who + ": Try now beside its name, " + r.fixTop);
          }
          assert.equal(by.ivy.tally, true, "the one to act on says how it fared");
          // its line in full, when it is back above all: a second line
          // when the column is too narrow, never cut
          assert.deepEqual(rows.filter((r) => r.stCut).map((r) => r.who), [], "a line cut short");
          const one = calm.filter((r) => r.stLines === 1).map((r) => r.h);
          // what is left and when it renews on one line in every language;
          // a rest (when it is back, at and in) may take a second
          for (const u of ["main", "erin", "frank", "henry"]) if (width > 1000) assert.equal(by[u].stLines, 1, u + " in one line");
          assert.ok(one.length > 0 && Math.max(...one) - Math.min(...one) <= 1, "cells of one height: " + one);
          // in columns of one width, each starting where the others do
          const ws = rows.map((r) => r.w);
          assert.ok(Math.max(...ws) - Math.min(...ws) <= 1, "cells of one width: " + ws);
          const xs = [...new Set(rows.map((r) => r.x))];
          assert.ok(xs.length <= (width > 1000 ? 3 : 2), "columns: " + xs);
          // at a window's width, every name in full
          if (width > 1000) assert.deepEqual(rows.filter((r) => r.cut).map((r) => r.who), []);
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth <= 1), true, "nothing past the window");
          assert.deepEqual(errors, []);
        } finally {
          await ctx.close();
        }
      });
    }
  });
}
