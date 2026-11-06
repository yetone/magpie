// Run with Node's test runner and Playwright on the module path; see README.md.
// What a routing group's card says can be selected and copied (jiabubu on
// Discord): its patterns, its models, its id. A drag across them selects
// and leaves the editor shut; a click still opens it, and the page doesn't
// move. In the editor its models' names and its patterns can be selected
// too. Its controls (the routing, Edit, the handle) can't. In both engines,
// in English and Chinese.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "or/a:free", name: "a:free", providerName: "OpenRouter", icon: "generic" },
  { id: "or/c:free", name: "c:free", providerName: "OpenRouter", icon: "generic" },
  { id: "an/claude-opus-5-5", name: "claude-opus-5-5", providerName: "Anthropic", icon: "generic" },
];
const members = ["an/claude-opus-5-5", "or/a:free", "or/c:free"];
const groups = () => ({
  models, pools: [],
  groups: [{ id: "free", name: "Free", members, match: ["or/*:free"], matched: ["or/a:free", "or/c:free"], fast: [], off: [], routing: "order", ready: true,
    patterns: [{ pattern: "or/*:free", models: 2 }],
    memberInfo: members.map((id) => ({ id, ready: true })) },
  { id: "two", name: "Two", members: ["an/claude-opus-5-5"], match: [], matched: [], fast: [], off: [], routing: "order", ready: true, patterns: [],
    memberInfo: [{ id: "an/claude-opus-5-5", ready: true }] }],
});

function serve(lang, posts) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {}); // nothing more comes
      return json({ mine: true, now: new Date().toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups());
    if (url.pathname.startsWith("/api/groups/")) {
      posts.push({ path: url.pathname, body: JSON.parse(r.request().postData() || "{}") });
      return json(groups());
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// drag drags the mouse across loc's text, as a reader selecting it does,
// scrolled to with the wheel as a reader does, clear of the window's foot,
// and gives what ended up selected
async function drag(page, loc) {
  await page.evaluate(() => getSelection().removeAllRanges());
  let b = await loc.boundingBox();
  for (let i = 0; i < 20 && (b.y < 120 || b.y + b.height > 1200); i++) {
    await page.mouse.move(10 + b.width / 2, 600);
    await page.mouse.wheel(0, b.y - 600);
    await page.waitForTimeout(120);
    b = await loc.boundingBox();
  }
  const y = b.y + b.height / 2;
  await page.mouse.move(b.x + 1, y);
  await page.mouse.down();
  await page.mouse.move(b.x + b.width / 2, y, { steps: 4 });
  await page.mouse.move(b.x + b.width - 1, y, { steps: 4 });
  await page.mouse.up();
  await page.waitForTimeout(100); // a click the drag ended in has run
  return page.evaluate(() => String(getSelection()));
}
const selectOf = (loc) => loc.evaluate((e) => { const s = getComputedStyle(e); return s.webkitUserSelect || s.userSelect; });
const scrolls = (page) => page.evaluate(() => [...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => e.scrollTop).concat(scrollY).join(","));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: a group's card and editor can be selected and copied`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 1400 }, reducedMotion: "reduce" })).newPage();
      t.after(() => browser.close());
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=groups");
      const card = page.locator(".rt-group", { hasText: "Free" });
      await card.waitFor();
      const ed = page.locator(".rt-gedit");
      const at = await scrolls(page);

      // the pattern, the models, the id: dragged across, selected, and the editor stays shut
      assert.equal(await drag(page, card.locator(".mem.pat code")), "or/*:free", "the pattern is selected");
      assert.equal(await ed.count(), 0, "a drag across the pattern doesn't open the editor");
      const mem = card.locator(".mem:not(.pat)").first();
      const said = (await mem.textContent()).trim();
      assert.equal((await drag(page, mem)).trim(), said, "the models are selected");
      assert.equal(await ed.count(), 0);
      assert.equal(await drag(page, card.locator("code.mdl")), "group/free", "its id is selected");
      assert.equal(await ed.count(), 0);
      assert.equal(await scrolls(page), at, "selecting moves nothing");
      // its controls stay controls
      for (const c of [card.locator(".rt-groute"), card.locator(".rt-ghandle"), card.locator("button.text").last()]) assert.equal(await selectOf(c), "none");

      // a click, with words still selected on the card, opens the editor
      await card.locator(".mem.pat").click({ position: { x: 3, y: 3 } });
      await ed.waitFor();
      assert.equal(await scrolls(page), at, "the click moves nothing either");
      // in the editor, a model's name and a pattern are selected too
      const name = ed.locator(".fallback:not(.rt-pats) .fbrow .n > span:first-child", { hasText: /^claude-opus-5-5$/ });
      assert.equal(await drag(page, name), "claude-opus-5-5");
      const pat = ed.locator(".rt-pats .fbrow code");
      assert.equal(await drag(page, pat), "or/*:free");
      assert.equal(await selectOf(ed.locator(".rt-pats button", { hasText: lang === "zh" ? "移除" : "Remove" }).first()), "none");
      assert.deepEqual(posts, [], "selecting saves nothing");
      assert.deepEqual(errors, []);
    });
  }
}
