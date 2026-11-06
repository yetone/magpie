// Run with Node's test runner and Playwright on the module path; see README.md.
// A menu opened while a text box has focus stays open. Both engines put a
// box whose text is longer than it back to its start as the box loses focus
// to the click that opens the menu, and that scroll of the box's own text
// closed the menu at once: a routing rule's folder list typed past its box,
// then its ⋯ or + clicked, showed nothing. Tried on the rule's ⋯
// (openRowMenu) and its + (openProtoMenu), in both engines.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [{ id: "a/opus", name: "opus", providerName: "A", icon: "generic", context: 1000000, ready: true }];
const groups = () => ({
  models,
  groups: [{ id: "main", name: "Main", members: ["a/opus"], routing: "order", ready: true,
    memberInfo: [{ id: "a/opus", ready: true, context: 1000000 }],
    rules: [{ use: "a/opus", pause: true, exceptFolders: ["~/big-task"] }] }],
  pools: [],
});
const LONG = "~/work/a-long-project-name, ~/work/another-long-project, ~/scratch/third";
const words = { en: { edit: "Edit" }, zh: { edit: "编辑" } };

function serve(lang) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 0, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await r.fulfill({ body: await fs.readFile(file), contentType }); } catch { await r.fulfill({ status: 404 }); }
  };
}

// wheeled to the control as the reader would (the page keeps still for any
// other scroll), then clicked with the mouse
async function press(page, loc) {
  await page.mouse.move(Math.min(550, (page.viewportSize().width / 2) | 0), 300);
  for (let i = 0; i < 20; i++) {
    const y = (await loc.boundingBox()).y;
    if (y > 80 && y < page.viewportSize().height - 160) break;
    await page.mouse.wheel(0, y < 80 ? -300 : 300);
    await page.waitForTimeout(150);
  }
  const b = await loc.boundingBox();
  await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    test(`${engine} ${lang}: a menu opened from a long text box's rule stays open`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 1800 } })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      await page.goto("http://magpie.test/?view=groups");
      const group = page.locator(".rt-group").first();
      await group.waitFor();
      await press(page, group.locator("button", { hasText: words[lang].edit }));
      const row = page.locator(".rt-gedit .rt-rule").first();
      const box = row.locator(".rt-cond.xf input");
      for (const [opener, menu] of [[".rt-rmore", ".row-menu"], [".rt-addc", ".proto-menu"]]) {
        await box.fill(LONG);
        await box.press("End");
        assert(await box.evaluate((b) => b.scrollWidth > b.clientWidth), "the folders run past their box");
        await press(page, row.locator(opener));
        await page.waitForTimeout(400);
        assert.equal(await page.locator(menu).count(), 1, `${opener}'s menu stays open`);
        await page.keyboard.press("Escape");
        await page.locator(menu).waitFor({ state: "detached" });
      }
      assert.deepEqual(errors, []);
    });
  }
}
