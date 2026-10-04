// Run with Node's test runner and Playwright on the module path; see README.md.
// The tray panel's Routing tab (v, #feedback: the panel's 用量 showed the
// accounts' allowances while the window's 用量 is per-request usage and 路由
// the gateway's routing): the gateway's latest requests, newest first, from
// the trace the Routing page plays — the agent, the model asked for, the
// provider and account it went to, the model that answered with the amber
// "served …" mark on one a vendor answered with another, how it ended and
// when — under today's calls and tokens. The allowances' tab is named so.
// A click opens the window's Routing page on that request and leaves the
// panel where it is; the window opened so has that request picked. Over the
// list a small stage (#feedback: 这个页面有点乱…迷你版的路由动画): the agents
// that asked lately, magpie and where their requests went, each new request
// a dot flying there and back; the rows' times and totals line up on the
// right, with one mark for how each went. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const at = (min) => new Date(now.getTime() - min * 60e3).toISOString();
const relay = { id: "relay:team", provider: "relay", name: "Relay", kind: "key", who: "team", model: "gpt-6-sol" };
const claude = { id: "claude:a", provider: "claude", name: "Claude", kind: "account", who: "ann@example.com", model: "claude-opus-5" };
const ok = (w, min, extra = {}) => ({ id: w.id, model: w.model, start: at(min), done: true, status: 200, ms: 1500, ...extra });
// not in order: the tab sorts them newest first
const routes = [
  { id: 101, seq: 101, time: at(6), agent: "codex", model: "relay/gpt-6-sol", provider: "relay", order: [relay],
    tries: [{ id: relay.id, model: "gpt-6-sol", start: at(6), done: true, status: 429, ms: 300, fail: "rate" }], done: true, status: 429, ms: 300 },
  { id: 103, seq: 103, time: at(1), agent: "codex", model: "relay/gpt-6-sol", provider: "relay", order: [relay],
    tries: [ok(relay, 1, { served: "gpt-6-luna", swapped: true })], done: true, status: 200, ms: 1500, tokens: 12400, served: "gpt-6-luna", swapped: true },
  { id: 100, seq: 100, time: at(9), agent: "claude", model: "claude/claude-opus-5", provider: "claude", order: [claude],
    tries: [ok(claude, 9, { served: "claude-opus-5-20260901" })], done: true, status: 200, ms: 1500, tokens: 800 },
  { id: 102, seq: 102, time: at(3), agent: "claude", model: "claude/claude-opus-5", provider: "claude", order: [claude],
    tries: [ok(claude, 3)], done: true, status: 200, ms: 1500, tokens: 3000 },
];

function serve(lang, opened, live) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }, { id: "claude", name: "Claude Code", path: "/test/settings.json", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      // live: one more request, under way and then answered, after the first
      const after = Number(url.searchParams.get("after"));
      if (live && after === 103) { await live.shown; await new Promise((r) => setTimeout(r, 300)); return json({ mine: true, now: new Date().toISOString(), seq: 104, totals: { requests: 5, rerouted: 0, errors: 1 }, routes: [{ ...live, tries: [{ ...live.tries[0], done: false, status: 0 }], done: false, status: 0 }] }); }
      if (live && after === 104) { await new Promise((r) => setTimeout(r, 2500)); return json({ mine: true, now: new Date().toISOString(), seq: 105, totals: { requests: 5, rerouted: 0, errors: 1 }, routes: [live] }); }
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: now.toISOString(), seq: 103, totals: { requests: 4, rerouted: 0, errors: 1 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/usage" && url.searchParams.get("period") === "today") {
      return json({ calls: 42, input: 1000000, output: 234000, cache_read: 0, cache_write: 0, reasoning: 0, cost: 0, series: [] });
    }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/window/main") { opened.push(url.search); return route.fulfill({ status: 204 }); }
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const want = {
  en: { tab: "Routing", quota: "Allowances", swap: "served gpt-6-luna", today: /today\s*42\s*calls\s*·\s*1\.2M\s*tokens/, fail: /429 · rate limited/, open: "Open Routing", story: /How the request at/ },
  zh: { tab: "路由", quota: "额度", swap: "实际 gpt-6-luna", today: /今天\s*42\s*次调用\s*·\s*1\.2M\s*token/, fail: /429 · 限流/, open: "打开路由", story: /的请求是怎么路由的/ },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: the tray panel's Routing tab`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const errors = [], pages = [];
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          for (const [i, p] of pages.entries()) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-panel-routing-${i}.png`) });
        }
        await browser.close();
      });
      const open = async (url, viewport, opened, live) => {
        const page = await (await browser.newContext({ viewport, reducedMotion: live ? "no-preference" : "reduce" })).newPage();
        pages.push(page);
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, opened, live));
        await page.goto(url);
        return page;
      };

      // a short panel, so its page scrolls
      const opened = [];
      const page = await open("http://magpie.test/?mode=panel", { width: 440, height: 300 }, opened);
      const tab = page.locator('[data-ptab="routing"]');
      assert.equal((await tab.textContent()).trim(), want[lang].tab);
      assert.equal((await page.locator('[data-ptab="usage"]').textContent()).trim(), want[lang].quota, "the allowances' tab says what it shows");
      await tab.click();
      await page.locator(".pr-req").nth(routes.length - 1).waitFor();
      assert(await page.locator("#panelRouting").isVisible());
      assert(!(await page.locator("#agents").isVisible()), "the agents are another tab");

      // newest first: the agent, the model asked, where it went, the mark
      const rows = await page.locator(".pr-req").evaluateAll((rs) => rs.map((r) => ({
        who: r.querySelector(".pr-who").textContent, model: r.querySelector(".pr-a .m").textContent,
        where: r.querySelector(".pr-where").textContent, served: r.querySelector(".pr-m")?.textContent || "",
        swap: r.querySelector(".swap")?.textContent || "", cls: r.className, at: r.querySelector(".at").textContent,
      })));
      assert.deepEqual(rows.map((r) => r.model), ["relay/gpt-6-sol", "claude/claude-opus-5", "relay/gpt-6-sol", "claude/claude-opus-5"]);
      assert.deepEqual(rows.map((r) => r.who), ["Codex", "Claude Code", "Codex", "Claude Code"]);
      assert.equal(rows[0].where, "Relay · team");
      assert.equal(rows[0].served, "gpt-6-sol");
      assert.equal(rows[1].where, "Claude · ann@example.com");
      assert.deepEqual(rows.map((r) => r.swap), [want[lang].swap, "", "", ""], "only the request answered by another model is marked, not a dated name");
      assert.match(rows[2].where, want[lang].fail);
      assert.match(rows[2].cls, /\bbad\b/);
      for (const r of rows) assert.match(r.at, /\d{1,2}:\d{2}:\d{2}/);
      const mark = await page.locator(".pr-req .swap").evaluate((e) => {
        const row = e.closest(".pr-req").getBoundingClientRect(), b = e.getBoundingClientRect(), cs = getComputedStyle(e);
        return { inRow: b.left >= row.left && b.right <= row.right + 0.5 && b.width > 0, full: e.clientWidth >= e.scrollWidth, border: cs.borderLeftWidth, colour: cs.color, bg: cs.backgroundColor };
      });
      assert(mark.inRow && mark.full, JSON.stringify(mark));
      assert.equal(mark.border, "0px", "no border stripe");
      assert.notEqual(mark.bg, "rgba(0, 0, 0, 0)");
      // no row runs out of the panel
      const over = await page.locator(".pr-req").evaluateAll((rs) => rs.filter((r) => r.scrollWidth > r.clientWidth + 1).length);
      assert.equal(over, 0);
      // the times and the totals line up on the right; one mark a row
      const rights = await page.locator(".pr-req").evaluateAll((rs) => rs.map((r) => [r.querySelector(".at").getBoundingClientRect().right, r.querySelector(".meta").getBoundingClientRect().right, r.querySelectorAll("i").length]));
      for (const [at, meta, marks] of rights) {
        assert(Math.abs(at - rights[0][0]) < 1 && Math.abs(meta - rights[0][0]) < 1, JSON.stringify(rights));
        assert.equal(marks, 1);
      }

      // the stage: the agents that asked, magpie, the accounts they went to
      const stage = await page.locator(".pr-stage").evaluate((s) => ({
        from: [...s.querySelectorAll(".ps-ag .ps-name")].map((e) => e.textContent),
        to: [...s.querySelectorAll(".ps-dst")].map((e) => e.querySelector(".ps-name").textContent + ":" + e.className.split(" ").pop()),
        wires: [...s.querySelectorAll(".ps-wire")].filter((w) => (w.getAttribute("d") || "").startsWith("M")).length,
        hub: !!s.querySelector(".ps-hub use"), below: s.getBoundingClientRect().bottom <= document.querySelector(".pr-list").getBoundingClientRect().top,
        inside: [...s.querySelectorAll(".ps-node")].every((n) => { const a = n.getBoundingClientRect(), b = s.getBoundingClientRect(); return a.left >= b.left && a.right <= b.right && a.top >= b.top && a.bottom <= b.bottom; }),
        border: getComputedStyle(s.querySelector(".ps-node")).borderLeftWidth,
      }));
      assert.deepEqual(stage.from, ["Codex", "Claude Code"]);
      assert.deepEqual(stage.to, ["Relayteam:ok", "Claudeann@example.com:ok"]);
      assert.equal(stage.wires, 4);
      assert(stage.hub && stage.below && stage.inside, JSON.stringify(stage));
      assert.equal(stage.border, "0px", "no border stripe");

      // today's calls and tokens, over the list
      await page.waitForFunction((re) => new RegExp(re).test(document.querySelector(".pr-today").textContent), want[lang].today.source);
      assert.equal((await page.locator(".pr-head .text").textContent()).trim(), want[lang].open);

      // a click opens the window on that request; the panel stays put
      const v = page.locator("#view-agents");
      const box = await v.boundingBox();
      await page.mouse.move(box.x + box.width / 2, box.y + 60);
      for (let i = 0; i < 12; i++) { await page.mouse.wheel(0, 40); await page.waitForTimeout(20); }
      await page.waitForTimeout(400);
      const before = await v.evaluate((e) => e.scrollTop);
      assert(before > 0, "the panel must be short enough to scroll");
      const row = page.locator(".pr-req").nth(3);
      const top = await row.evaluate((e) => e.getBoundingClientRect().top);
      await row.click();
      await page.waitForTimeout(400);
      assert.equal(await v.evaluate((e) => e.scrollTop), before, "the click scrolled the panel");
      assert(Math.abs((await row.evaluate((e) => e.getBoundingClientRect().top)) - top) <= 1);
      assert.deepEqual(await page.evaluate(() => [scrollX, scrollY]), [0, 0]);
      assert.deepEqual(opened, ["?view=routing&req=100"]);
      // and Open Routing, over the list, opens the page as it is
      for (let i = 0; i < 12; i++) { await page.mouse.wheel(0, -40); await page.waitForTimeout(20); }
      await page.waitForTimeout(400);
      await page.locator(".pr-head .text").click();
      await page.waitForTimeout(200);
      assert.equal(await v.evaluate((e) => e.scrollTop), 0);
      assert.deepEqual(opened, ["?view=routing&req=100", "?view=routing"]);

      // the window opened so: that request picked, its story told, and the
      // address no longer asking for it
      const win = await open("http://magpie.test/?view=routing&req=101", { width: 1100, height: 760 }, []);
      await win.locator(".rt-req").nth(routes.length - 1).waitFor();
      await win.waitForFunction(() => !location.search.includes("req="));
      const picked = await win.locator('.rt-req[aria-pressed="true"]').evaluateAll((rs) => rs.map((r) => r.querySelector(".to").textContent));
      assert.equal(picked.length, 1);
      assert.match(picked[0], /429/);
      assert.match(await win.locator(".rt-log-head").textContent(), want[lang].story);

      // a request as it comes: a dot flies from its agent, waits at the
      // account while it answers, and comes home green
      const live = { id: 104, seq: 104, time: new Date().toISOString(), agent: "claude", model: "claude/claude-opus-5", provider: "claude", order: [claude],
        tries: [ok(claude, 0)], done: true, status: 200, ms: 900, tokens: 500 };
      let show;
      live.shown = new Promise((r) => { show = r; }); // it comes once the tab is shown
      const lp = await open("http://magpie.test/?mode=panel", { width: 440, height: 640 }, [], live);
      await lp.locator('[data-ptab="routing"]').click();
      await lp.locator(".pr-stage").waitFor();
      show();
      await lp.locator(".ps-dot").waitFor({ state: "attached" });
      await lp.waitForFunction(() => document.querySelector(".ps-wire.on") && document.querySelector(".ps-hub.busy"));
      await lp.locator(".ps-dot.wait").waitFor({ state: "attached" });
      const waitAt = await lp.evaluate(() => { const d = document.querySelector(".ps-dot").getBoundingClientRect(), n = [...document.querySelectorAll(".ps-dst")].find((e) => e.textContent.startsWith("Claude")).getBoundingClientRect(); return Math.abs((d.top + d.bottom) / 2 - (n.top + n.bottom) / 2) < 3 && d.right <= n.left + 1; });
      assert(waitAt, "the dot waits by the account answering");
      await lp.locator(".ps-dot.ok").waitFor({ state: "attached" });
      await lp.waitForFunction(() => !document.querySelector(".ps-dot") && !document.querySelector(".ps-hub.busy") && !document.querySelector(".ps-wire.on"), null, { timeout: 5000 });
      assert.equal(await lp.locator(".pr-req").first().locator(".meta").textContent().then((s) => /500/.test(s)), true);
      assert.deepEqual(errors, []);
    });
  }
}
