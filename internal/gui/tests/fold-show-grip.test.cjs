// Run with Node's test runner and Playwright on the module path; see README.md.
// Hu9956, #842: under the Agents page's fold a hidden agent's Show sat up on
// its name's line, above the row's middle (其余里的显示歪了偏上了); it now
// stands in the row, centred as the row's picker is. And a grip's dots,
// shown on hover only, were cut: its box was 5px wide with its columns
// 3.5px apart, so the right column was half gone (on the Usage page's cards
// beside the logo, 被图标背景盖住一半). Every grip's box now holds its
// columns whole. WebKit or Chromium, English and Chinese; no backend.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const options = [{ value: "relay/m1", label: "m1", ref: "relay/m1", note: "Relay" }];
const agent = (id, name, value) => ({ id, name, icon: "generic", path: "/fixture/" + id, fields: [{ key: "model", label: "model", value, options }] });
const quotas = [
  { provider: "qoder-cn", name: "Qoder CN", icon: "qoder", plan: "Personal", windows: [{ name: "Add-on credits", used: 55 }] },
  { provider: "kimi", name: "Kimi", icon: "kimi-color", windows: [{ name: "Week", used: 10 }] },
];

function server(lang) {
  const settings = { lang, theme: "dark", agentsHidden: ["idle"] };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"dark",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [agent("codex", "Codex", "relay/m1"), agent("idle", "Idle", ""), agent("grok", "Grok Build", "")], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/usage") return json({ calls: 0, cost: 0, days: [], agents: [], models: [], keys: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// a grip's dots: whether it shows, and whether its last column of dots
// (each 1.2px round about its tile's middle) is inside its box. Found in the
// page at the read, as shown() does: a grip drawn again is a new element, and
// the old one, out of the page, has no style to read.
const grip = (page, sel) => page.evaluate((sel) => {
  const s = getComputedStyle(document.querySelector(sel), "::before"), w = parseFloat(s.width), tile = parseFloat(s.backgroundSize);
  const cols = Math.ceil(w / tile);
  return { whole: (cols - 1) * tile + tile / 2 + 1.2 <= w + 0.01, cols, w, tile };
}, sel);
const shown = (page, sel, v) => page.waitForFunction(([sel, v]) => getComputedStyle(document.querySelector(sel), "::before").opacity === v, [sel, v]);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: a hidden agent's Show is centred in its row, and grips show whole`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 980, height: 600 }, colorScheme: "dark" })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang));
      await page.goto("http://magpie.test/?view=agents");
      const tops = () => page.evaluate(() => [document.scrollingElement.scrollTop, document.querySelector("#view-agents")?.scrollTop]);
      const top = await tops();
      await page.locator(".agent-more").click();
      const idle = page.locator('.agent-fold .row.agent[data-id="idle"]');
      const show = idle.locator(".ag-show");
      await show.waitFor();
      assert.equal((await show.textContent()).trim(), lang === "zh" ? "显示" : "Show");
      // the fold's rows slide in (translateY and scale, .56s after a staggered
      // delay): measure Show and the picker at rest, in one read. Read one at a
      // time while the row still moves, the second read lands lower, by more
      // the slower the machine.
      await page.locator(".agent-fold").evaluate((f) => Promise.all(f.getAnimations({ subtree: true }).map((a) => a.finished)));
      const [ms, mp] = await idle.evaluate((row) => [row.querySelector(".ag-show"), row.querySelector(":scope > .field.ag-start")]
        .map((e) => { const b = e.getBoundingClientRect(); return b.y + b.height / 2; }));
      assert.ok(Math.abs(ms - mp) <= 1, `Show (${ms}) is level with the picker (${mp})`);
      // the shown one beside it has none
      assert.equal(await page.locator('.agent-fold .row.agent[data-id="grok"] .ag-show').count(), 0);

      // a row's grip: hidden at rest, whole on hover
      const sel = '#agents > .row.agent[data-id="codex"] .ag-handle';
      await page.mouse.move(600, 590);
      await shown(page, sel, "0");
      await page.locator('#agents > .row.agent[data-id="codex"]').hover();
      await shown(page, sel, "1");
      const ag = await grip(page, sel);
      assert.ok(ag.whole && ag.cols === 2, "the Agents grip's columns are whole: " + JSON.stringify(ag));
      assert.deepEqual(await tops(), top, "no click moved the page");

      // the Usage page's cards
      await page.goto("http://magpie.test/?view=usage");
      const card = page.locator(".subscription-card").first();
      const handle = card.locator(".us-handle");
      await handle.waitFor();
      await page.mouse.move(600, 590);
      await shown(page, ".subscription-card .us-handle", "0");
      await card.hover();
      await shown(page, ".subscription-card .us-handle", "1");
      const us = await grip(page, ".subscription-card .us-handle");
      assert.ok(us.whole && us.cols === 2, "the Usage grip's columns are whole: " + JSON.stringify(us));
      // between the card's edge and the logo. The cards are drawn again as the
      // page's reads come in, so the boxes are read in one go, from one card:
      // a box read from a card already replaced is null.
      const { left, logo, edge } = await page.evaluate(() => {
        const card = document.querySelector(".subscription-card"), h = card.querySelector(".us-handle");
        return { left: h.getBoundingClientRect().x + parseFloat(getComputedStyle(h, "::before").left),
          logo: h.querySelector(".ic").getBoundingClientRect().x, edge: card.getBoundingClientRect().x };
      });
      assert.ok(left > edge + 1 && left + us.w <= logo, `the grip (${left}–${left + us.w}) is between the card (${edge}) and the logo (${logo})`);
      assert.deepEqual(errors, []);
    });
  }
}
