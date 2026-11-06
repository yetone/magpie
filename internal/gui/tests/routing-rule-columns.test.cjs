// Run with Node's test runner and Playwright on the module path; see README.md.
// A group's rules have columns of their own, not a subgrid of the list
// (#3955: the requests' subgrid of fit-content() columns crashed Safari
// 16's WebKit, #3731, and the rules were the same). No rule of the rules'
// CSS says subgrid, and the rules still line up as they did: every rule's
// → and model start at one x, at each width; a condition typed longer, or
// shorter again, moves them all. On a phone's width what a rule does goes
// under its conditions, past nothing. In every language.
// MAGPIE_ROUTING_ASSETS points the page at an older asset tree, to watch
// this go red there.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ROUTING_ASSETS || path.join(__dirname, "../assets"));
const models = [
  { id: "a/opus", name: "opus", providerName: "A", icon: "generic", context: 1000000, ready: true },
  { id: "b/flash", name: "flash", providerName: "B", icon: "generic", context: 1000000, ready: true },
];
const member = (id) => ({ id, ready: true, context: 1000000, images: true });
const rule = (use, more) => ({ use, pause: false, tokens: 0, images: false, effort: "", agents: [], intent: "", compact: false, folders: [], exceptFolders: [], time: null, ...more });
const RULES = [
  rule("b/flash", { intent: "a quick question" }),
  rule("a/opus", { tokens: 50000, images: true }),
  rule("a/opus", { pause: true, time: { from: "14:00", to: "18:00", days: [] } }),
];
const groups = () => ({
  models,
  deciders: [{ id: "typesafe/jev-latest", name: "Jev", providerName: "TypeSafe", icon: "generic" }],
  groups: [{ id: "main", name: "Main", members: ["a/opus", "b/flash"], routing: "order", ready: true, classifier: "typesafe/jev-latest",
    rules: structuredClone(RULES), memberInfo: [member("a/opus"), member("b/flash")] }],
  pools: [],
});
const edit = { en: "Edit", zh: "编辑", "zh-TW": "編輯", ja: "編集", de: "Bearbeiten" };

function serve(lang, posts) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/claude", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
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
    try { await r.fulfill({ body: await fs.readFile(file), contentType }); } catch { await r.fulfill({ status: 404 }); }
  };
}

// wheel to a control, as the reader would (the page keeps still for any
// other scroll), and return where it is
async function reach(page, loc) {
  const height = page.viewportSize().height;
  await page.mouse.move(Math.min(550, (page.viewportSize().width / 2) | 0), 300);
  for (let i = 0; i < 30; i++) {
    const y = (await loc.boundingBox()).y;
    if (y > 80 && y < height - 160) break;
    await page.mouse.wheel(0, y < 80 ? -300 : 300);
    await page.waitForTimeout(150);
  }
  return loc.boundingBox();
}

// reached, then clicked with the mouse
async function press(page, loc) {
  const b = await reach(page, loc);
  await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
}

async function openEditor(page, w) {
  const group = page.locator(".rt-group").first();
  await group.waitFor();
  await press(page, group.locator("button", { hasText: w.edit }));
  const ed = page.locator(".rt-gedit");
  await ed.locator(".rt-rule").first().waitFor();
  return ed;
}

async function openEditor(page, lang) {
  const group = page.locator(".rt-group").first();
  await group.waitFor();
  await press(page, group.locator("button", { hasText: edit[lang] }));
  const ed = page.locator(".rt-gedit");
  await ed.locator(".rt-rule").first().waitFor();
  return ed;
}

test("no rule of the rules says subgrid", async () => {
  const css = await fs.readFile(path.join(assets, "plugins/routing/routing.css"), "utf8");
  const bad = [];
  for (const m of css.replace(/\/\*[\s\S]*?\*\//g, "").matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
    if (/\.rt-rule\b|\.rt-rules\b/.test(m[1]) && /subgrid/.test(m[2])) bad.push(m[1].trim());
  }
  assert.deepEqual(bad, []);
});

// each rule: where its conditions start and end, where what it does starts
const rulesOf = (page) => page.evaluate(() => [...document.querySelectorAll(".rt-gedit .rt-rule")].map((r) => {
  const b = (s) => r.querySelector(s).getBoundingClientRect();
  const when = b(":scope > .rt-line.when"), then = b(":scope > .rt-then");
  return { whenX: Math.round(when.left * 2) / 2, whenW: when.width, thenX: Math.round(then.left * 2) / 2, thenY: then.top, whenBottom: when.bottom };
}));
const frame = (page) => page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(() => r()))));
// waited for until the rules line up (they are measured a frame after a
// change), or two seconds
async function lined(page, ok = (rs) => new Set(rs.map((r) => r.thenX)).size === 1) {
  let rs = await rulesOf(page);
  for (const until = Date.now() + 2000; Date.now() < until && !ok(rs);) { await frame(page); rs = await rulesOf(page); }
  return rs;
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(edit)) {
    test(`${engine} ${lang}: the rules line up without a subgrid`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 1400 } })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=groups");
      const ed = await openEditor(page, lang);
      for (const width of [1100, 900, 700]) {
        await page.setViewportSize({ width, height: 1400 });
        const rs = await lined(page);
        assert.equal(rs.length, RULES.length);
        assert.deepEqual([...new Set(rs.map((r) => r.thenX))].length, 1, `${width}px: → starts at ${rs.map((r) => r.thenX)}`);
        assert.equal(new Set(rs.map((r) => r.whenX)).size, 1, `${width}px: the conditions start at one x`);
      }
      // a phone's width: what a rule does under its conditions, past nothing
      await page.setViewportSize({ width: 390, height: 1400 });
      let rs = await lined(page, (rs) => rs.every((r) => r.thenY >= r.whenBottom - 1));
      for (const r of rs) assert(r.thenY >= r.whenBottom - 1, `390px: what a rule does is under its conditions (${JSON.stringify(r)})`);
      const over = await page.evaluate(() => {
        const e = document.querySelector(".rt-gedit").getBoundingClientRect();
        return [...document.querySelectorAll(".rt-rule, .rt-rule *")].filter((x) => x.offsetParent && x.getBoundingClientRect().right > e.right + 1).map((x) => x.className || x.tagName);
      });
      assert.deepEqual(over, []);
      // a condition typed longer moves every rule's →; shorter, back left
      await page.setViewportSize({ width: 1100, height: 1400 });
      const before = (await lined(page))[0].thenX;
      const intent = ed.locator(".rt-rule").first().locator(".rt-cond.in textarea");
      await intent.fill("a question about the build, the tests and the release, all at once");
      rs = await lined(page, (rs) => new Set(rs.map((r) => r.thenX)).size === 1 && rs[0].thenX > before);
      assert(rs[0].thenX > before, `typed longer: → at ${rs.map((r) => r.thenX)}, was ${before}`);
      assert.equal(new Set(rs.map((r) => r.thenX)).size, 1, `typed longer: → starts at ${rs.map((r) => r.thenX)}`);
      const long = rs[0].thenX;
      await intent.fill("a quick question");
      rs = await lined(page, (rs) => new Set(rs.map((r) => r.thenX)).size === 1 && rs[0].thenX < long);
      assert(rs[0].thenX < long, `typed shorter again: → at ${rs.map((r) => r.thenX)}, was ${long}`);
      assert.equal(new Set(rs.map((r) => r.thenX)).size, 1, `typed shorter again: → starts at ${rs.map((r) => r.thenX)}`);
      assert.deepEqual(errors, []);
    });
  }
}
