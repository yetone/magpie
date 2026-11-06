// Run with Node's test runner and Playwright on the module path; see README.md.
// The context window card draws its window as a map by default (after
// flohoeller's context map on X): each item a tile sized by its tokens
// inside its part's, the free space a hatched tile. A tile hovered tells
// what it holds and dims the others; clicked, it opens its text, where an
// image the request sent inline is shown. The block grid is a switch
// away, and stays the reader's pick. A refresh keeps the tiles, and with
// motion reduced nothing moves. In every language, at a desk and a
// phone's width.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const ago = (m) => new Date(now - m * 60000).toISOString();

const LATEST = {
  window: 272000, tokens: 21400, counted: true, turns: 3,
  parts: [
    { kind: "system", tokens: 5200, items: [{ name: "prompt", tokens: 4900 }, { name: "environment_context", tokens: 300 }] },
    { kind: "tools", tokens: 5000, items: [{ name: "multi_agent_v1", tag: "namespace", n: 5, tokens: 2900 }, { name: "exec_command", tokens: 2100 }] },
    { kind: "skills", tokens: 2600, items: [{ name: "skills", tag: "skills", tokens: 2600 }] },
    { kind: "memory", tokens: 700, items: [{ name: "/work/app/AGENTS.md", tag: "project", tokens: 700 }] },
    { kind: "files", tokens: 5900, items: [{ name: "/work/app/prompt.go", tag: "shell", tokens: 2950 }, { name: "/work/app/context.go", tag: "shell", tokens: 2950 }] },
    { kind: "results", tokens: 1200, items: [{ name: "exec_command", tag: "result", n: 4, tokens: 1200 }] },
    { kind: "chat", tokens: 800, items: [{ name: "turn", tag: "turn", n: 3, tokens: 800 }] },
  ],
};
const context = (days, latest = LATEST) => ({
  days,
  agents: [
    { agent: "codex", requests: 9, sessions: 1, errors: 0, calls: 9, median: 17700, p90: 21400, baseline: 13300, window: 272000, cache: 0.74, growth: 228, peakFill: 0.08, compacts: 0,
      shares: { system: 0.29, tools: 0.28, memory: 0.18, files: 0.18, results: 0.04, chat: 0.03 }, mcp: 0,
      score: 94, scores: [{ key: "cache", points: 29, most: 35 }, { key: "lean", points: 25, most: 25 }, { key: "pace", points: 20, most: 20 }, { key: "reliable", points: 20, most: 20 }],
      tags: [{ key: "lean", tone: "good", value: 13300 }, { key: "memory-heavy", tone: "info", value: 0.18 }],
      models: ["live/codex/gpt-5.6-luna"], latestId: 19, latestTime: ago(4) },
    { agent: "claude", requests: 12, sessions: 2, errors: 3, calls: 15, median: 140000, p90: 180000, baseline: 70000, window: 200000, cache: 0.2, growth: 9000, peakFill: 0.9, compacts: 1,
      shares: { system: 0.1, tools: 0.6, memory: 0.05, files: 0.1, results: 0.1, chat: 0.05 }, mcp: 28000,
      score: 31, scores: [{ key: "cache", points: 8, most: 35 }, { key: "lean", points: 3, most: 25 }, { key: "pace", points: 10, most: 20 }, { key: "reliable", points: 10, most: 20 }],
      tags: [{ key: "cache-misses", tone: "warn", value: 0.2 }, { key: "heavy-start", tone: "warn", value: 70000 }, { key: "mcp-heavy", tone: "info", value: 28000 }],
      models: ["claude-sonnet-5"], latestId: 30, latestTime: ago(9) },
  ],
  sessions: [
    { agent: "codex", key: "01a117b1-aecd-76e2-9b2f-3c1d", title: "Port the parser", model: "live/codex/gpt-5.6-luna", first: ago(12), last: ago(4), requests: 3, peak: 21400, window: 272000,
      latest, latestId: 19, points: [{ id: 17, time: ago(12), tokens: 13000, cache: 0 }, { id: 18, time: ago(8), tokens: 17700, cache: 12000 }, { id: 19, time: ago(4), tokens: 21400, cache: 20900 }] },
    { agent: "claude", key: "ce0c93f6-1111-2222-3333-444444444444", model: "claude-sonnet-5", first: ago(30), last: ago(9), requests: 1, peak: 180000, window: 200000,
      latest: { ...LATEST, window: 200000, tokens: 180000 }, latestId: 30, points: [{ id: 30, time: ago(9), tokens: 180000 }] },
  ],
});

// the text as /api/context/text gives it: request 19's is kept, 30's isn't
// a 2×2 PNG, as the gateway gives an image the request sent inline
const PNG = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAYAAABytg0kAAAAFklEQVR4AWP4z8DwHwyBGBMgEmAAAP0eB/k1J7hZAAAAAElFTkSuQmCC";
const LONG = "You are Codex. ".repeat(20000); // 300,000 characters
const texts = {
  system: [{ name: "prompt", tokens: 4900, pieces: [{ role: "system", text: LONG }] }, { name: "environment_context", tokens: 300, pieces: [{ role: "user", text: "<environment_context>cwd /work/app</environment_context>" }] }],
  tools: [{ name: "multi_agent_v1", tag: "namespace", n: 5, tokens: 2900, pieces: [{ role: "tool", name: "spawn_agent", text: "{\n  \"name\": \"spawn_agent\"\n}" }] },
    { name: "exec_command", tokens: 2100, pieces: [{ role: "tool", name: "exec_command", text: "{\n  \"name\": \"exec_command\",\n  \"description\": \"Runs a command in a PTY\"\n}" }] }],
  files: [{ name: "/work/app/prompt.go", tag: "shell", tokens: 2950, pieces: [{ role: "result", name: "exec_command", text: "package gateway // prompt.go" }] },
    { name: "/work/app/context.go", tag: "shell", tokens: 2950, pieces: [{ role: "result", name: "exec_command", text: "package gui // context.go" }] }],
  skills: [{ name: "skills", tag: "skills", tokens: 2600, pieces: [{ role: "system", text: "## Skills" }] }],
  memory: [{ name: "/work/app/AGENTS.md", tag: "project", tokens: 700, pieces: [{ role: "user", text: "# AGENTS.md" }] }],
  results: [{ name: "exec_command", tag: "result", n: 4, tokens: 1200, pieces: [{ role: "result", name: "exec_command", text: "ok" }] }],
  chat: [{ name: "1", tag: "turn", tokens: 300, pieces: [{ role: "user", text: "port the parser" }, { role: "thinking", text: "", size: 900 }, { role: "call", name: "exec_command", text: "{\"cmd\": \"ls\"}" }] },
    { name: "2", tag: "turn", tokens: 500, pieces: [{ role: "image", src: PNG }, { role: "image" }, { role: "assistant", text: "Done." }] }],
};
function text(url, r) {
  if (url.searchParams.get("id") !== "19") return r.fulfill({ status: 404, body: "not kept" });
  const kind = url.searchParams.get("kind"), item = url.searchParams.get("item");
  const items = (texts[kind] || []).map((it, i) => item !== null && +item === i ? it : { ...it, pieces: undefined });
  return r.fulfill({ json: { id: 19, kind, items, kept: 10 } });
}

function serve(lang, asked, latest) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }, { id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/context") {
      asked.push(url.searchParams.get("days"));
      return json(context(+url.searchParams.get("days"), latest));
    }
    // the text comes a moment after it is asked, as it does under load:
    // the dialog says "Reading…" first, and the test waits past it
    if (url.pathname === "/api/context/text") { await new Promise((ok) => setTimeout(ok, 200)); return text(url, r); }
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 0, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file).catch(() => ""), contentType, status: 200 });
  };
}

// the reader scrolls (the wheel) until what they'll point at is in view:
// the app puts back a scroll that isn't the reader's, a test's own included
async function wheelTo(page, l) {
  // over the page, not the sidebar beside it
  const v = await page.locator("#view-usage").boundingBox();
  await page.mouse.move(v.x + v.width / 2, 300);
  for (let i = 0; i < 40 && await l.evaluate((b) => b.getBoundingClientRect().bottom > innerHeight - 70); i++) {
    await page.mouse.wheel(0, 120);
    await page.waitForTimeout(50);
  }
  // a wheel scroll is eased, and a window's cells scale in one after
  // another (ctx-cell): hovered while either runs, Chromium scrolls the
  // view to the cell, the app puts that scroll back, the cell moves out
  // from under the pointer and its tip closes again. Wait until the target
  // stops moving.
  // Under load an eased scroll can pause for a frame and go on, so the
  // target has to hold still for three looks in a row.
  for (let last = null, same = 0, i = 0; i < 60 && same < 2; i++) {
    await page.waitForTimeout(100);
    const now = await l.evaluate((b) => { const r = b.getBoundingClientRect(); return r.x + "," + r.y + "," + document.querySelector("#view-usage")?.scrollTop; });
    same = now === last ? same + 1 : 0;
    last = now;
  }
}

const words = {
  en: { tools: "Tools", skills: "Skills", memory: "Memory", close: "Close", free: "Free space", map: "Context map", grid: "Block grid" },
  zh: { tools: "工具", skills: "技能", memory: "记忆", close: "关闭", free: "剩余空间", map: "上下文地图", grid: "方格" },
  "zh-TW": { tools: "工具", skills: "技能", memory: "記憶", close: "關閉", free: "剩餘空間", map: "上下文地圖", grid: "方格" },
  ja: { tools: "ツール", skills: "スキル", memory: "メモリ", close: "閉じる", free: "空き", map: "コンテキストマップ", grid: "ブロックグリッド" },
  de: { tools: "Tools", skills: "Skills", memory: "Memory", close: "Schließen", free: "Frei", map: "Kontextkarte", grid: "Blockraster" },
};


async function open(page, lang, width, latest) {
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, [], latest));
  await page.addInitScript(() => { try { if (!sessionStorage.getItem("set")) { localStorage.clear(); localStorage.setItem("magpie.usageTab", "context"); sessionStorage.setItem("set", "1"); } } catch {} });
  await page.goto("http://magpie.test/?view=usage");
  const card = await openSession(page);
  return { errors, card };
}
async function openSession(page) {
  const row = page.locator(".ctx-sess-row", { hasText: "Port the parser" });
  await row.waitFor();
  await wheelTo(page, row);
  await row.click();
  const card = page.locator(".ctx-sess.open .ctx-card");
  await card.waitFor();
  return card;
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [1100, 420]) {
      const w = words[lang];
      test(`${engine} ${lang} ${width}px: the context window as a map`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 900 } })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        let { errors, card } = await open(page, lang, width);

        // the map is the default: tiles and the free space, the grid hidden
        const map = card.locator(".ctx-map");
        await map.locator(".ctx-tile.k-tools").first().waitFor();
        assert.equal(await card.locator(".ctx-viz").getAttribute("data-look"), "map");
        assert.ok(await card.locator(".ctx-waffle").isHidden(), "the grid is hidden under the map");
        assert.equal(await map.locator(".ctx-tile.free").count(), 1);
        assert.match(await map.locator(".ctx-tile.free").innerText(), new RegExp(w.free));
        assert.equal(await card.locator(".ctx-look-b.on").getAttribute("aria-label"), w.map);
        // every tile inside the map, none on another
        // read once the tiles have come in and the card has opened, the
        // map and its tiles in one look
        await page.waitForFunction(() => !document.getAnimations().some((a) => a.playState === "running" && a.effect?.target?.closest?.(".ctx-sess.open")));
        const { rects, box } = await map.evaluate((m) => {
          const rc = (n) => { const r = n.getBoundingClientRect(); return [r.left, r.top, r.right, r.bottom]; };
          return { rects: [...m.querySelectorAll(".ctx-tile")].map(rc), box: rc(m) };
        });
        for (const [i, a] of rects.entries()) {
          assert.ok(a[0] >= box[0] - 0.5 && a[1] >= box[1] - 0.5 && a[2] <= box[2] + 0.5 && a[3] <= box[3] + 0.5, `tile ${i} is outside the map: ${a} in ${box}`);
          assert.ok(a[2] - a[0] >= 10 && a[3] - a[1] >= 10, `tile ${i} is a sliver: ${a}`);
          for (const b of rects.slice(i + 1)) assert.ok(a[2] <= b[0] + 0.5 || b[2] <= a[0] + 0.5 || a[3] <= b[1] + 0.5 || b[3] <= a[1] + 0.5, "two tiles overlap");
        }

        // a tile hovered tells its part and dims the others
        const tile = map.locator(".ctx-tile.k-tools").first();
        await wheelTo(page, card.locator(".ctx-look"));
        await tile.hover();
        const tip = card.locator(".ctx-tip");
        await tip.waitFor({ state: "visible" });
        assert.match(await tip.innerText(), new RegExp(w.tools));
        assert.equal(await map.getAttribute("data-f"), "tile");
        assert.match(await tile.getAttribute("class"), /\bon\b/);
        await page.mouse.move(2, 2);
        await tip.waitFor({ state: "hidden" });

        // the skills are their own part, not memory (the owner 10-24: 技能 / 记忆)
        await map.locator(".ctx-tile.k-skills").hover();
        await tip.waitFor({ state: "visible" });
        const said = await tip.innerText();
        assert.match(said, new RegExp(w.skills));
        assert.doesNotMatch(said, new RegExp(w.memory), "the skills' tip names memory");
        assert.equal(await map.locator(".ctx-tile.k-memory").count(), 1, "the skills are a memory tile too");
        assert.notEqual(await map.locator(".ctx-tile.k-skills").evaluate((n) => getComputedStyle(n).backgroundColor),
          await map.locator(".ctx-tile.k-memory").evaluate((n) => getComputedStyle(n).backgroundColor), "the skills are memory's colour");
        await page.mouse.move(2, 2);
        await tip.waitFor({ state: "hidden" });

        // a refresh with the same window keeps the tiles where they are
        const kept = await tile.elementHandle();
        await page.evaluate(() => window.loadContext());
        await page.waitForTimeout(300);
        assert.ok(await kept.evaluate((n) => n.isConnected), "the refresh drew the tiles again");

        // clicked, a tile opens its text
        await tile.click();
        const view = page.locator("#modal .ctx-view");
        await view.locator(".ctx-text").first().waitFor();
        await view.locator(".bar button", { hasText: w.close }).click();
        await page.locator("#modal").waitFor({ state: "hidden" });

        // an image the request sent inline is shown in its turn; one it
        // didn't send that way is named
        await page.evaluate(() => window.openCtxText({ id: 19 }, "chat"));
        await view.locator(".ctx-vi-head").nth(1).click();
        const pic = view.locator(".ctx-vi.open .ctx-pic");
        await pic.locator("img").waitFor();
        await page.waitForFunction(() => document.querySelector("#modal .ctx-pic img")?.naturalWidth === 2);
        assert.equal(await view.locator(".ctx-vi.open .ctx-piece-note").count(), 1, "the image not sent inline is named");
        await pic.click();
        assert.equal(await pic.getAttribute("aria-expanded"), "true", "a picture clicked opens larger");
        assert.ok(await page.evaluate(() => { const d = document.querySelector("#modal .dialog").getBoundingClientRect(); return d.left >= 0 && d.right <= innerWidth; }), "the dialog fits the window");
        await view.locator(".bar button", { hasText: w.close }).click();
        await page.locator("#modal").waitFor({ state: "hidden" });

        // the grid a switch away, and still the pick after a reload
        await card.locator('.ctx-look-b[data-k="grid"]').click();
        await page.waitForFunction(() => document.querySelector(".ctx-sess.open .ctx-viz")?.dataset.look === "grid");
        assert.equal(await card.locator(".ctx-waffle > i").count(), 400);
        assert.ok(await map.isHidden());
        assert.equal(await card.locator(".ctx-look-b.on").getAttribute("aria-label"), w.grid);
        assert.equal(await page.evaluate(() => localStorage.getItem("magpie.ctxLook")), "grid");
        await page.reload();
        card = await openSession(page);
        await card.locator(".ctx-waffle > i").first().waitFor();
        assert.equal(await card.locator(".ctx-viz").getAttribute("data-look"), "grid");
        // and back: the tiles come in again
        await wheelTo(page, card.locator(".ctx-look"));
        await card.locator('.ctx-look-b[data-k="map"]').click();
        await card.locator(".ctx-map .ctx-tile.free").waitFor();
        assert.equal(await page.evaluate(() => localStorage.getItem("magpie.ctxLook")), "map");

        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), "the page scrolls sideways");
        assert.deepEqual(errors, []);
      });
    }
  }

  // a part of a few tokens beside parts of thousands is still a tile one
  // sees and points at: the memory's 49 tokens beside 72K, the window not
  // known, were laid as a strip thinner than the gap between tiles and
  // hidden (AIGCFREE on Discord)
  for (const width of [1100, 420]) {
    test(`${engine} ${width}px: a part of a few tokens is a tile one can point at`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const files = Array.from({ length: 16 }, (_, i) => ({ name: `/work/app/f${i}.rs`, tag: "shell", tokens: [2000, 1400, 1300, 830][i] || 530 }));
      const { errors, card } = await open(page, "en", width, {
        window: 0, tokens: 72100, counted: true, turns: 1,
        parts: [
          { kind: "system", tokens: 3100, items: [{ name: "prompt", tokens: 3100 }] },
          { kind: "tools", tokens: 3900, items: [{ name: "bash", tokens: 2400 }, { name: "read", tokens: 1500 }] },
          { kind: "memory", tokens: 49, items: [{ name: "system-reminder", tokens: 49 }] },
          { kind: "files", tokens: 11900, items: files },
          { kind: "results", tokens: 21600, items: [{ name: "bash", tag: "result", n: 21, tokens: 19400 }, { name: "read", tag: "result", tokens: 2200 }] },
          { kind: "chat", tokens: 31600, items: [{ name: "1", tag: "turn", tokens: 31600 }] },
        ],
      });
      const memory = card.locator(".ctx-map .ctx-tile.k-memory");
      await memory.waitFor();
      const box = await memory.boundingBox();
      assert.ok(box.width >= 12 && box.height >= 12, `the memory is a sliver: ${box.width}×${box.height}`);
      await wheelTo(page, memory);
      await memory.hover();
      await card.locator(".ctx-tip").waitFor({ state: "visible" });
      assert.match(await card.locator(".ctx-tip").innerText(), /Memory/);
      assert.deepEqual(errors, []);
    });
  }

  // two small parts beside a large one are laid as slim tiles, and every
  // tile keeps the map's gap to its neighbours at every width: a tile's
  // padding made one too slim for it wider than its place, so it touched
  // or ran over the next (noahhrreion on Discord, 11-06: the system
  // prompt and tools leaning on "N more" beside them)
  for (const lang of Object.keys(words)) {
    test(`${engine} ${lang}: slim tiles keep their gap at every width`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 900 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const turns = Array.from({ length: 110 }, (_, i) => ({ name: String(i + 1), tag: "turn", tokens: 1300 + (i % 7) * 40 }));
      const chat = turns.reduce((s, it) => s + it.tokens, 0);
      const { errors, card } = await open(page, lang, 1100, {
        window: 200000, tokens: 2300 + 2800 + chat, counted: true, turns: turns.length,
        parts: [
          { kind: "system", tokens: 2300, items: [{ name: "prompt", tokens: 2300 }] },
          { kind: "tools", tokens: 2800, items: [{ name: "bash", tokens: 1500 }, { name: "read", tokens: 1300 }] },
          { kind: "chat", tokens: chat, items: turns },
        ],
      });
      const map = card.locator(".ctx-map");
      await map.locator(".ctx-tile.k-system").waitFor();
      let slim = 0;
      for (const width of [1100, 980, 860, 760, 680, 600, 560, 520, 480, 440, 420, 390, 360]) {
        await page.setViewportSize({ width, height: 900 });
        await page.waitForFunction(() => { const m = document.querySelector(".ctx-sess.open .ctx-map"); return m && m.dataset.size === m.clientWidth + "x" + m.clientHeight; });
        const rects = await map.evaluate((m) => [...m.querySelectorAll(".ctx-tile:not(.gone)")].map((n) => {
          const r = n.getBoundingClientRect();
          return { k: n.className, l: r.left, t: r.top, r: r.right, b: r.bottom };
        }));
        for (const [i, a] of rects.entries()) {
          if (a.r - a.l < 24) slim++;
          for (const b of rects.slice(i + 1)) {
            const gap = Math.max(b.l - a.r, a.l - b.r, b.t - a.b, a.t - b.b);
            assert.ok(gap >= 2.5, `${width}px: "${a.k}" and "${b.k}" are ${gap.toFixed(2)}px apart: ${JSON.stringify([a, b])}`);
          }
        }
      }
      assert.ok(slim > 0, "no width laid a slim tile, so the gap beside one was never looked at");
      assert.deepEqual(errors, []);
    });
  }

  // with motion reduced the tiles are simply there
  test(`${engine}: the map holds still with motion reduced`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await (await browser.newContext({ viewport: { width: 1100, height: 900 }, reducedMotion: "reduce" })).newPage();
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    const { errors, card } = await open(page, "en", 1100);
    await card.locator(".ctx-map .ctx-tile.free").waitFor();
    const moving = () => page.evaluate(() => document.getAnimations().filter((a) => a.effect?.target?.closest?.(".ctx-viz")).length);
    assert.equal(await moving(), 0, "a tile came in with motion reduced");
    await wheelTo(page, card.locator(".ctx-look"));
    await card.locator('.ctx-look-b[data-k="grid"]').click();
    assert.equal(await moving(), 0, "the switch moved with motion reduced");
    assert.deepEqual(errors, []);
  });
}
