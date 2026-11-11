// Run with Node's test runner and Playwright on the module path; see README.md.
// An agent's picks can be kept to this computer, as an MCP server can
// (chenxiang0010 on Discord: 「agent当前选择的模型可不可以像mcp那样 只对当前设备生效」).
// The row's menu has "Don't sync picks to your other computers" (said as
// sync, not "This computer only", which read as sharing: bin.90 on
// Discord): it posts agents/here, is ticked once on, and the window's row
// says "Not synced" beside the version, in every
// language, in Chromium and WebKit, in the window and the tray panel.
// Off again, the tick and the words go. No backend: faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const atPointer = async (page, target, menu) => {
  const b = await target.boundingBox(), vw = page.viewportSize().width;
  const px = b.x + b.width / 2, py = b.y + b.height / 2;
  // the menu's own placement, not its bounding box, which an entrance transform scales
  const m = await menu.evaluate((e) => ({ x: parseFloat(e.style.left), y: parseFloat(e.style.top), w: e.offsetWidth, h: e.offsetHeight }));
  // opens at the pointer, or pushed in to the window's edge (mmmg_btc on X)
  const x = Math.max(8, Math.min(px - 4, vw - m.w - 8));
  assert(Math.abs(m.x - x) <= 2, `menu at x=${m.x}, expected ${x} for a pointer at ${px}`);
  // below the pointer, or above it where there is no room below
  assert((m.y >= py - 2 && m.y <= py + 14) || (m.y + m.h >= py - 14 && m.y + m.h <= py + 2), `menu at y=${m.y}-${m.y + m.h}, pointer at ${py}`);
};
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const CHECK = "m3.5 8.5 3 3 6-7";

function fixture(lang, posts) {
  let here = [];
  const settings = () => ({ lang, theme: "light", agentsHere: here });
  const state = () => ({
    agents: [{
      id: "codex", name: "Codex", path: "~/.codex/config.toml", icon: "codex", wired: true,
      fields: [{ key: "model", label: "model", value: "glm-5.3-flash", options: [{ value: "glm-5.3-flash", label: "GLM-5.3-Flash", icon: "generic" }] }],
    }],
    profiles: [], settings: settings(),
  });
  async function serve(route) {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state());
    if (url.pathname === "/api/agents/here") {
      const b = req.postDataJSON();
      posts.push(b);
      here = here.filter((x) => x !== b.agent);
      if (b.on) here.push(b.agent);
      return json(settings());
    }
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: { codex: { version: "0.166.0" } }, pending: false });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await route.fulfill(body ? { body, contentType } : { status: 404, body: "" });
  }
  return { serve };
}

const row = '.row.agent[data-id="codex"]';

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const where of ["window", "narrow", "panel"]) {
      test(`${engine} ${lang} ${where}: an agent's picks kept to this computer`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const size = { window: { width: 1100, height: 700 }, narrow: { width: 640, height: 700 }, panel: { width: 360, height: 640 } }[where];
        const page = await browser.newPage({ viewport: size });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", fixture(lang, posts).serve);
        await page.goto("http://magpie.test/" + (where === "panel" ? "?mode=panel" : ""));
        await page.locator(row).waitFor();
        const name = await page.evaluate(() => t("Don't sync picks to your other computers"));
        const word = await page.evaluate(() => t("Not synced"));
        if (lang !== "en") assert.notEqual(name, "Don't sync picks to your other computers", "not translated");
        if (lang !== "en") assert.notEqual(word, "Not synced", "not translated");
        const item = () => page.locator(".row-menu .rm-item", { hasText: name });
        const open = async () => {
          await page.locator(row).click({ button: "right" });
          await item().waitFor();
          await atPointer(page, page.locator(row), page.locator(".row-menu"));
        };
        const ticked = () => item().locator("path").getAttribute("d").then((d) => d === CHECK);

        await open();
        assert.equal(await ticked(), false, "ticked before it was turned on");
        await item().click();
        await page.waitForFunction(() => !document.querySelector(".row-menu"));
        assert.deepEqual(posts, [{ agent: "codex", on: true }]);
        const tag = page.locator(`${row} .ag-here`);
        if (where === "panel") assert.equal(await tag.count(), 0, "the panel's row has no room for it");
        else {
          await tag.waitFor();
          assert.equal(await tag.textContent(), word);
          // the row keeps its width and the name stays whole
          const fit = await page.locator(row).evaluate((r) => {
            const n = r.querySelector(".who .name");
            return { over: r.scrollWidth - r.clientWidth, cut: n.scrollWidth - n.clientWidth };
          });
          assert.ok(fit.over <= 0 && fit.cut <= 0, `the row overflows: ${JSON.stringify(fit)}`);
        }
        await open();
        assert.equal(await ticked(), true, "not ticked once on");

        // off again: the tick and the words go
        await item().click();
        await page.waitForFunction(() => !document.querySelector(".row-menu"));
        assert.deepEqual(posts.at(-1), { agent: "codex", on: false });
        await page.waitForFunction((sel) => !document.querySelector(sel), `${row} .ag-here`);
        await open();
        assert.equal(await ticked(), false, "still ticked once off");
        assert.deepEqual(errors, []);
      });
    }
  }
}
