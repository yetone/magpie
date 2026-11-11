// mmmg_btc on X: a right-click on a routing group opened its menu at the
// far left, under the drag handle, wherever the pointer was. It opens at
// the pointer, kept inside the window. Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "a/m", name: "m", providerName: "A", icon: "generic" },
  { id: "a/x", name: "x", providerName: "A", icon: "generic" },
];
const info = (ids) => ids.map((id) => ({ id, ready: true }));
const group = (id, name, members, more = {}) => ({ id, name, members, ready: true, memberInfo: info(members), ...more });

const SHARED = '{"magpie":"routing-group","version":1,"group":{"name":"One","members":["a/x","a/m"]}}';

function serve(lang, posts) {
  let list = [
    group("one", "One", ["a/x", "a/m"], { routing: "order", rules: [{ use: "a/m", agents: ["codex"] }] }),
    group("two", "Two", ["a/m"]),
  ];
  const groups = () => ({ models, pools: [], deciders: [], found: true, groups: list });
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname === "/api/groups/share") {
      posts.push(["share", JSON.parse(r.request().postData() || "{}")]);
      return json({ ...groups(), shared: SHARED });
    }
    if (url.pathname === "/api/copy") {
      posts.push(["copy", JSON.parse(r.request().postData() || "{}").text]);
      return json({});
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a routing group's right-click menu opens at the pointer`, async () => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    try {
      const page = await (await browser.newContext({ viewport: { width: 700, height: 700 }, reducedMotion: "reduce" })).newPage();
      await page.route("**/*", serve("en", []));
      await page.goto("http://magpie.test/?view=groups");
      const row = page.locator(".rt-group[data-id=one] .main");
      await row.waitFor();
      const rb = await row.boundingBox();
      const x = rb.x + rb.width / 2, y = rb.y + rb.height / 2;
      await page.mouse.click(x, y, { button: "right" });
      const menu = page.locator(".row-menu");
      await menu.waitFor();
      const at = await menu.evaluate((e) => ({ x: parseFloat(e.style.left), y: parseFloat(e.style.top), w: e.offsetWidth }));
      const want = Math.max(8, Math.min(x - 4, 700 - at.w - 8));
      assert(Math.abs(at.x - want) <= 2, `menu at x=${at.x}, expected ${want} for a pointer at ${x}`);
      assert(at.y >= y - 2 && at.y <= y + 14, `menu at y=${at.y}, pointer at ${y}`);
      // the pointer near the right edge keeps the menu inside the window
      await page.keyboard.press("Escape");
      await menu.waitFor({ state: "detached" });
      await page.mouse.click(rb.x + rb.width - 4, y, { button: "right" });
      await menu.waitFor();
      const edge = await menu.boundingBox();
      assert(edge.x + edge.width <= 700, `menu off the window: ${JSON.stringify(edge)}`);
    } finally {
      await browser.close();
    }
  });
}
