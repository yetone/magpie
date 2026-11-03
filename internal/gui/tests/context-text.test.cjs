// Run with Node's test runner and Playwright on the module path; see README.md.
// masaka on Discord: a part of the context window card clicked opens what
// the request sent there. A part of the legend opens its items, each
// opened to its text; a row or a cell opens its item alone, the part's
// other items a click away; a long text comes a stretch at a time; a
// request whose text is no longer kept says so. wangbinyq on Discord: with
// the request archive on, an older request's text is read from it, and a
// request's reply opens beside its text, from memory or the archive; one
// the archive has no copy of says why. Nothing moves the page,
// the keyboard opens it too, in every language, at a desk and a phone's
// width.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = Date.now();
const ago = (m) => new Date(now - m * 60000).toISOString();

const latest = {
  window: 272000, tokens: 21400, counted: true, turns: 3,
  parts: [
    { kind: "system", tokens: 5200, items: [{ name: "prompt", tokens: 4900 }, { name: "environment_context", tokens: 300 }] },
    { kind: "tools", tokens: 5000, items: [{ name: "multi_agent_v1", tag: "namespace", n: 5, tokens: 2900 }, { name: "exec_command", tokens: 2100 }] },
    { kind: "skills", tokens: 3300, items: [{ name: "skills", tag: "skills", tokens: 3300 }] },
    { kind: "files", tokens: 5900, items: [{ name: "/work/app/prompt.go", tag: "shell", tokens: 2950 }, { name: "/work/app/context.go", tag: "shell", tokens: 2950 }] },
    { kind: "results", tokens: 1200, items: [{ name: "exec_command", tag: "result", n: 4, tokens: 1200 }] },
    { kind: "chat", tokens: 800, items: [{ name: "turn", tag: "turn", n: 3, tokens: 800 }] },
  ],
};
const context = (days) => ({
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
      latest: { ...latest, window: 200000, tokens: 180000 }, latestId: 30, points: [{ id: 30, time: ago(9), tokens: 180000 }] },
  ],
});

// the text as /api/context/text gives it: request 19's is kept, 30's isn't
const LONG = "You are Codex. ".repeat(20000); // 300,000 characters
const texts = {
  system: [{ name: "prompt", tokens: 4900, pieces: [{ role: "system", text: LONG }] }, { name: "environment_context", tokens: 300, pieces: [{ role: "user", text: "<environment_context>cwd /work/app</environment_context>" }] }],
  tools: [{ name: "multi_agent_v1", tag: "namespace", n: 5, tokens: 2900, pieces: [{ role: "tool", name: "spawn_agent", text: "{\n  \"name\": \"spawn_agent\"\n}" }] },
    { name: "exec_command", tokens: 2100, pieces: [{ role: "tool", name: "exec_command", text: "{\n  \"name\": \"exec_command\",\n  \"description\": \"Runs a command in a PTY\"\n}" }] }],
  files: [{ name: "/work/app/prompt.go", tag: "shell", tokens: 2950, pieces: [{ role: "result", name: "exec_command", text: "package gateway // prompt.go" }] },
    { name: "/work/app/context.go", tag: "shell", tokens: 2950, pieces: [{ role: "result", name: "exec_command", text: "package gui // context.go" }] }],
  skills: [{ name: "skills", tag: "skills", tokens: 3300, pieces: [{ role: "system", text: "## Skills" }] }],
  results: [{ name: "exec_command", tag: "result", n: 4, tokens: 1200, pieces: [{ role: "result", name: "exec_command", text: "ok" }] }],
  chat: [{ name: "1", tag: "turn", tokens: 300, pieces: [{ role: "user", text: "port the parser" }, { role: "thinking", text: "", size: 900 }, { role: "call", name: "exec_command", text: "{\"cmd\": \"ls\"}" }] },
    { name: "2", tag: "turn", tokens: 500, pieces: [{ role: "image" }, { role: "assistant", text: "Done." }] }],
};
// 19's in memory, 31's read from the archive; 30's the archive never
// kept, 32's it named and doesn't have
function text(url, r) {
  const id = url.searchParams.get("id");
  if (id === "30") return r.fulfill({ status: 404, json: { archive: "off" } });
  if (id === "32") return r.fulfill({ status: 404, json: { archive: "missing" } });
  if (id !== "19" && id !== "31") return r.fulfill({ status: 404, body: "not kept" });
  const kind = url.searchParams.get("kind"), item = url.searchParams.get("item");
  const items = (texts[kind] || []).map((it, i) => item !== null && +item === i ? it : { ...it, pieces: undefined });
  return r.fulfill({ json: { id: +id, kind, items, kept: 10, ...(id === "31" ? { from: "archive" } : {}) } });
}
function reply(url, r) {
  const id = url.searchParams.get("id");
  if (id === "30") return r.fulfill({ status: 404, json: { archive: "off" } });
  const pieces = [{ role: "thinking", text: "Look first." }, { role: "assistant", text: "Ported, call me at [PHONE_1]." }, { role: "call", name: "exec_command", text: "{\"cmd\": \"go test\"}" }];
  return r.fulfill({ json: { id: +id, pieces, cut: id === "31", from: id === "31" ? "archive" : "memory" } });
}

function serve(lang, asked) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }, { id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/context") {
      asked.push("days=" + url.searchParams.get("days"));
      return json(context(+url.searchParams.get("days")));
    }
    // the text comes a moment after it is asked, as it does under load:
    // the dialog says "Reading…" first, and the test waits past it
    if (url.pathname === "/api/context/text") { asked.push("text?" + url.searchParams); await new Promise((ok) => setTimeout(ok, 200)); return text(url, r); }
    if (url.pathname === "/api/context/reply") { await new Promise((ok) => setTimeout(ok, 200)); return reply(url, r); }
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
  en: { tools: "Tools", files: "Files", close: "Close", sys: "System prompt", all: "All of", reply: "Reply", archive: "request archive" },
  zh: { tools: "工具", files: "文件", close: "关闭", sys: "系统提示词", all: "全部", reply: "回复", archive: "请求存档" },
  "zh-TW": { tools: "工具", files: "檔案", close: "關閉", sys: "系統提示詞", all: "全部", reply: "回覆", archive: "請求存檔" },
  ja: { tools: "ツール", files: "ファイル", close: "閉じる", sys: "システムプロンプト", all: "すべて", reply: "返信", archive: "リクエストアーカイブ" },
  de: { tools: "Tools", files: "Dateien", close: "Schließen", sys: "Systemprompt", all: "Alle", reply: "Antwort", archive: "Anfragearchiv" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [1100, 420]) {
      const w = words[lang];
      test(`${engine} ${lang} ${width}px: a part of the context window opens to its text`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 900 } })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], asked = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, asked));
        await page.addInitScript(() => { try { if (!sessionStorage.getItem("set")) { localStorage.clear(); localStorage.setItem("magpie.usageTab", "context"); localStorage.setItem("magpie.ctxLook", "grid"); sessionStorage.setItem("set", "1"); } } catch {} });
        await page.goto("http://magpie.test/?view=usage");
        const row = page.locator(".ctx-sess-row", { hasText: "Port the parser" });
        await row.waitFor();
        await wheelTo(page, row);
        await row.click();
        const card = page.locator(".ctx-sess.open .ctx-card");
        await card.waitFor();
        const top = () => page.evaluate(() => document.querySelector("#view-usage").scrollTop);
        const modal = page.locator("#modal");
        const view = modal.locator(".ctx-view");
        const close = async () => {
          await view.locator(".bar button", { hasText: w.close }).click();
          await modal.waitFor({ state: "hidden" });
        };

        // a part of the legend: its items, each opened to its text
        const leg = card.locator(".ctx-leg", { has: page.locator(".k-tools") });
        await wheelTo(page, leg);
        let y = await top();
        await leg.click();
        await view.locator(".ctx-vi-head").first().waitFor();
        assert.equal(await top(), y, "a click doesn't move the page");
        assert.match(await view.locator(".ehead b").innerText(), new RegExp(w.tools));
        assert.deepEqual((await view.locator(".ctx-vi-head .ctx-name").allInnerTexts()).map((s) => s.trim()), ["multi_agent_v1", "exec_command"]);
        assert.equal(await view.locator(".ctx-text").count(), 0, "the text is read when an item is opened");
        const exec = view.locator(".ctx-vi-head", { hasText: "exec_command" });
        await exec.click();
        await view.locator(".ctx-text", { hasText: "Runs a command in a PTY" }).waitFor();
        assert.equal(await exec.getAttribute("aria-expanded"), "true");
        assert.ok(await page.evaluate(() => { const d = document.querySelector("#modal .dialog").getBoundingClientRect(); return d.left >= 0 && d.right <= innerWidth; }), "the dialog fits the window");
        await close();

        // a row of the contents: its item alone, the rest a click away
        const seg = card.locator(".ctx-contents .segs button", { hasText: w.files }).first();
        await wheelTo(page, seg);
        await seg.click();
        const fileRow = card.locator(".ctx-row", { hasText: "prompt.go" });
        await wheelTo(page, fileRow);
        y = await top();
        await fileRow.click();
        await view.locator(".ctx-text", { hasText: "package gateway // prompt.go" }).waitFor();
        assert.equal(await top(), y, "a click doesn't move the page");
        assert.equal(await view.locator(".ctx-vi").count(), 1);
        await view.locator(".ctx-view-all", { hasText: w.all }).click();
        assert.equal(await view.locator(".ctx-vi").count(), 2);
        await close();

        // a cell: the system prompt, 300,000 characters a stretch at a time
        const cell = card.locator(".ctx-waffle > i.k-system").first();
        await wheelTo(page, cell);
        y = await top();
        await cell.click();
        const pre = view.locator(".ctx-text");
        await pre.waitFor();
        assert.equal(await top(), y, "a click doesn't move the page");
        assert.match(await view.locator(".ehead b").innerText(), new RegExp(w.sys));
        assert.equal(await pre.evaluate((e) => e.textContent.length), 20000);
        await view.locator(".ctx-text-more").click();
        assert.equal(await pre.evaluate((e) => e.textContent.length), 40000);
        await close();

        // the keyboard: a part of the legend, Enter; a turn's pieces by
        // whose they are, an image and sealed reasoning named
        const chat = card.locator(".ctx-leg", { has: page.locator(".k-chat") });
        await chat.focus();
        await page.keyboard.press("Enter");
        await view.waitFor();
        await view.locator(".ctx-vi-head").first().click();
        await view.locator(".ctx-text", { hasText: "port the parser" }).waitFor();
        assert.equal(await view.locator(".ctx-vi.open .ctx-piece").count(), 3);
        assert.equal(await view.locator(".ctx-vi.open .ctx-piece-note").count(), 1, "the sealed reasoning is named");
        await close();

        // the text asks for the request's day, for the archive to find it
        assert.ok(asked.some((q) => /^text\?id=19&kind=\w+&day=\d{4}-\d\d-\d\d/.test(q)), asked.join(" "));

        // a request's reply beside its text, and back
        const replyBtn = () => view.locator(".bar button", { hasText: w.reply });
        await leg.click();
        await view.locator(".ctx-vi-head").first().waitFor();
        await replyBtn().click();
        await view.locator(".ctx-text", { hasText: "Ported, call me at [PHONE_1]." }).waitFor();
        assert.equal(await view.locator(".ctx-piece").count(), 3);
        assert.match(await view.locator(".ehead b").innerText(), new RegExp(w.reply));
        assert.doesNotMatch(await view.locator(".ctx-view-note").first().innerText(), new RegExp(w.archive));
        await view.locator(".bar button", { hasText: w.tools }).click();
        await view.locator(".ctx-vi-head", { hasText: "exec_command" }).waitFor();
        assert.match(await view.locator(".ehead b").innerText(), new RegExp(w.tools));
        await close();

        // an older request's text from the archive, its reply too, cut short
        const old = { id: 31, time: ago(60 * 30) };
        await page.evaluate((r) => window.openCtxText(r, "tools"), old);
        await view.locator(".ctx-vi-head", { hasText: "exec_command" }).waitFor();
        assert.match(await view.locator(".ctx-view-note").innerText(), new RegExp(w.archive));
        await replyBtn().click();
        await view.locator(".ctx-text", { hasText: "Ported" }).waitFor();
        assert.equal(await view.locator(".ctx-view-note").count(), 2, "the reply says it is read from the archive, and cut short");
        assert.match(await view.locator(".ctx-view-note").first().innerText(), new RegExp(w.archive));
        await close();

        // a request whose text is no longer kept says so, its reply too
        await page.evaluate(() => window.openCtxText({ id: 30, time: new Date().toISOString() }, "tools"));
        await view.locator(".ctx-view-gone").waitFor();
        assert.match(await view.locator(".ctx-view-gone").innerText(), /10/);
        await replyBtn().click();
        await view.locator(".ctx-view-gone").waitFor();
        assert.match(await view.locator(".ctx-view-gone").innerText(), /10/);
        await close();

        // one the archive named and doesn't have says the archive lost it
        await page.evaluate(() => window.openCtxText({ id: 32, time: new Date().toISOString() }, "tools"));
        await view.locator(".ctx-view-gone").waitFor();
        const lost = await view.locator(".ctx-view-gone").innerText();
        assert.match(lost, new RegExp(w.archive));
        assert.doesNotMatch(lost, /10/);
        await close();

        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), "the page scrolls sideways");
        assert.deepEqual(errors, []);
      });
    }
  }
}
