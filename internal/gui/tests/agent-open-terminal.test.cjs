// Run with Node's test runner and Playwright on the module path; see README.md.
// coolmantou on Discord: start an agent from magpie's Agents list. On the
// Mac app's window, a row whose agent has a program to run (terminal) has
// "Open in terminal" in its menu, connected or under Not set up; it posts
// agents/open with the agent's id and nothing else (the command is made on
// the server). A desktop app with no program has no such item, and neither
// has the web build nor the tray panel. In every language; no backend, the
// API is faked here. lileo0668 on Discord: every agent with a program has
// "Copy launch command" there too, the line the server made, on every
// system, the web build and the panel included; a desktop app has none.
// Narrow too.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const options = ["gpt-5.4", "magpie/relay/m1"].map((m) => ({ value: m, label: "Label " + m, ...(m.startsWith("magpie/") ? { ref: "relay/m1" } : {}) }));
const state = {
  agents: [
    { id: "codex", name: "Codex", icon: "generic", path: "/fixture/codex", wired: true, terminal: true, command: "'/fixture/codex'", fields: [{ key: "model", label: "model", value: "magpie/relay/m1", options }] },
    { id: "desk", name: "Desk App", icon: "generic", path: "/Applications/Desk.app", wired: true, fields: [{ key: "model", label: "model", value: "magpie/relay/m1", options }] },
    { id: "claude", name: "Claude Code", icon: "generic", path: "/fixture/claude", terminal: true, command: "& 'C:\\it''s\\claude.cmd'", fields: [{ key: "model", label: "model", value: "gpt-5.4", options }] },
  ],
  profiles: [],
};

function server(lang, web, posts) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ ...state, settings: { lang, theme: "light" } });
    if (url.pathname === "/api/agents/open") { posts.push([url.pathname, req.postData()]); return route.fulfill({ status: 204, body: "" }); }
    if (url.pathname === "/api/copy") { posts.push([url.pathname, req.postData()]); return route.fulfill({ status: 204, body: "" }); }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { item: "Open in terminal", copy: "Copy launch command" },
  zh: { item: "在终端中打开", copy: "复制启动命令" },
  "zh-TW": { item: "在終端機中開啟", copy: "複製啟動命令" },
  ja: { item: "ターミナルで開く", copy: "起動コマンドをコピー" },
  de: { item: "Im Terminal öffnen", copy: "Startbefehl kopieren" },
};
const row = (id) => `.row.agent[data-id="${id}"]`;

async function open(engine, t, lang, web, query, width = 980) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width, height: 560 }, reducedMotion: "reduce" })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000); // the page loads every core script
  const errors = [], posts = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", server(lang, web, posts));
  await page.goto("http://magpie.test/" + query);
  return { page, errors, posts };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [lang, width] of Object.keys(words).flatMap((l) => [[l, 980], [l, 420]])) {
    const w = words[lang];
    test(`${engine} ${lang} ${width}px: Open in terminal on an agent's row`, async (t) => {
      const { page, errors, posts } = await open(engine, t, lang, false, "?view=agents", width);
      await page.locator(row("codex")).waitFor();
      const item = () => page.locator(".pop.row-menu .rm-item", { hasText: w.item });
      const copyItem = () => page.locator(".pop.row-menu .rm-item", { hasText: w.copy });
      const menu = async (id) => {
        await page.locator(`${row(id)} .ag-handle`).click();
        await page.locator(".pop.row-menu").waitFor();
      };
      const close = async () => { await page.keyboard.press("Escape"); await page.locator(".pop.row-menu").waitFor({ state: "detached" }); };

      await menu("desk");
      assert.equal(await item().count(), 0, "a desktop app with no program has nothing to open");
      assert.equal(await copyItem().count(), 0, "nor a line to copy");
      await close();
      // Claude Code, not connected, is under Not set up: it has a program too
      await page.locator(".agent-more").click();
      await page.waitForFunction(() => !document.querySelector(".agent-fold-inner")?.inert);
      await menu("claude");
      assert.equal(await item().count(), 1, "a row under Not set up opens too");
      assert.equal(await copyItem().count(), 1, "and copies");
      // the menu fits the narrow window
      const box = await page.locator(".pop.row-menu").boundingBox();
      assert.ok(box.x >= 0 && box.x + box.width <= width, `menu at ${box.x}+${box.width} in ${width}`);
      const copied = page.waitForResponse((r) => new URL(r.url()).pathname === "/api/copy");
      await copyItem().click();
      await copied;
      assert.deepEqual(posts.splice(0).map(([p, b]) => [p, JSON.parse(b)]), [["/api/copy", { text: "& 'C:\\it''s\\claude.cmd'" }]], "the server's line, as it is");

      await menu("codex");
      assert.equal(await item().count(), 1);
      const sent = page.waitForResponse((r) => new URL(r.url()).pathname === "/api/agents/open");
      await item().click();
      await sent;
      await page.locator(".pop.row-menu").waitFor({ state: "detached" });
      assert.deepEqual(posts.map(([p, b]) => [p, JSON.parse(b)]), [["/api/agents/open", { agent: "codex" }]], "the id and nothing else");
      assert.deepEqual(errors, []);
    });
  }

  test(`${engine}: the web build has no Open in terminal`, async (t) => {
    const { page, errors } = await open(engine, t, "en", true, "?view=agents");
    await page.locator(row("codex")).waitFor();
    await page.locator(`${row("codex")} .ag-handle`).click();
    await page.locator(".pop.row-menu").waitFor();
    assert.equal(await page.locator(".pop.row-menu .rm-item", { hasText: "Open in terminal" }).count(), 0);
    assert.equal(await page.locator(".pop.row-menu .rm-item", { hasText: "Copy launch command" }).count(), 1, "copying is for the web too");
    assert.deepEqual(errors, []);
  });

  test(`${engine}: the tray panel has no Open in terminal`, async (t) => {
    const { page, errors, posts } = await open(engine, t, "en", false, "?mode=panel");
    // the panel's Agent tab has the same rows and menu, without the item
    await page.locator(`${row("codex")} .ag-handle`).first().click();
    await page.locator(".pop.row-menu").waitFor();
    assert.equal(await page.locator(".rm-item", { hasText: "Open in terminal" }).count(), 0);
    assert.equal(await page.locator(".rm-item", { hasText: "Copy launch command" }).count(), 1, "copying is for the panel too");
    assert.deepEqual(posts, []);
    assert.deepEqual(errors, []);
  });
}
