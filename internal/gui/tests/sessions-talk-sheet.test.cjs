// Run with Node's test runner and Playwright on the module path; see README.md.
// A session's conversation opens in a sheet (Wing8Aqua on X: it was very long
// inline). The sheet's head counts the user's turns and jumps between them,
// to the top and to the bottom, like DSH's viewer; the long text scrolls in
// the sheet, never the page; Close and Escape end it. Chromium and WebKit,
// every language, a wide and a narrow window.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const at = (min) => new Date(Date.now() - min * 60e3).toISOString();
const TURNS = 8;
const PARTS = [];
for (let i = 1; i <= TURNS; i++) {
  PARTS.push({ role: "user", kind: "text", text: `Question ${i}` });
  PARTS.push({ role: "assistant", kind: "text", text: Array.from({ length: 30 }, (_, j) => `Answer ${i} line ${j}`).join("\n") });
}

const nav = { en: "Sessions", zh: "会话", "zh-TW": "會話", ja: "セッション", de: "Sitzungen" };
const labels = {
  en: ["Top", "Previous turn", "Next turn", "Bottom"],
  zh: ["回到顶部", "上一轮", "下一轮", "到底部"],
  "zh-TW": ["回到頂部", "上一輪", "下一輪", "到底部"],
  ja: ["先頭へ", "前のターン", "次のターン", "末尾へ"],
  de: ["Nach oben", "Vorherige Runde", "Nächste Runde", "Nach unten"],
};

function serve(lang) {
  const sessions = [{ agent: "pi", id: "p-1", cwd: "/work/pi", title: "Rename foo", start: at(10), last: at(5), models: [], resume: "pi --session p-1", path: "~/p-1.jsonl", size: 4096, messages: 6, files: 1, deletable: true, transcript: true }];
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/sessions/manage") return json({ agents: [{ agent: "pi", count: 1, deletable: true, name: "Pi", icon: "pi" }], agent: "pi", sessions, terminal: true, trash: [], trashDir: "~/trash" });
    if (url.pathname === "/api/sessions/transcript") return json({ parts: PARTS });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: a long conversation opens in a sheet with turn navigation`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1000, height: 700 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator("#nav").getByRole("button", { name: nav[lang], exact: true }).click();
      const row = page.locator('.row.sm-sess[data-id="p-1"]');
      await row.locator(".who").click();
      await page.locator(".sess-talk-btn").click();
      const sheet = page.locator(".sess-sheet");
      await sheet.waitFor();
      await sheet.locator(".cx-part").first().waitFor();
      assert.equal(await page.locator(".sess-detail .sess-talk").count(), 0, "not inline");
      const body = sheet.locator(":scope > .ebody");
      const btn = (n) => sheet.locator(`.sess-nav[data-nav="${n}"]`);
      const metrics = () => body.evaluate((e) => ({ top: e.scrollTop, max: e.scrollHeight - e.clientHeight, client: e.clientHeight }));
      const m0 = await metrics();
      assert(m0.max > m0.client * 2, "the conversation is longer than the sheet");
      assert(await sheet.evaluate((e) => e.getBoundingClientRect().height <= innerHeight), "the sheet fits the window");
      assert.deepEqual(await sheet.locator(".sess-nav").evaluateAll((l) => l.map((e) => e.getAttribute("aria-label"))), [labels[lang][0], labels[lang][1], labels[lang][2], labels[lang][3]]);
      const turns = sheet.locator(".sess-turns");
      assert.equal((await turns.textContent()).trim(), `1 / ${TURNS}`);
      assert.equal(await btn("prev").isDisabled(), true);
      assert.equal(await btn("top").isDisabled(), true);
      await btn("next").click();
      assert.equal((await turns.textContent()).trim(), `2 / ${TURNS}`);
      const turnTop = await sheet.locator('.sess-talk [data-turn="1"]').nth(1).evaluate((e) => e.getBoundingClientRect().top - e.closest(".ebody").getBoundingClientRect().top);
      assert(Math.abs(turnTop) < 24, "the second question is at the sheet's top: " + turnTop);
      await btn("next").click();
      assert.equal((await turns.textContent()).trim(), `3 / ${TURNS}`);
      await btn("prev").click();
      assert.equal((await turns.textContent()).trim(), `2 / ${TURNS}`);
      await btn("bottom").click();
      const mb = await metrics();
      assert(Math.abs(mb.top - mb.max) < 2, "at the bottom");
      assert.equal(await btn("bottom").isDisabled(), true);
      assert.equal((await turns.textContent()).trim(), `${TURNS} / ${TURNS}`);
      assert.equal(await btn("next").isDisabled(), true);
      await btn("top").click();
      assert.equal((await metrics()).top, 0);
      assert.equal((await turns.textContent()).trim(), `1 / ${TURNS}`);
      assert.equal(await page.evaluate(() => scrollY), 0, "the page never scrolled");

      // narrow
      await page.setViewportSize({ width: 390, height: 640 });
      await page.waitForTimeout(100);
      assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "no page overflow");
      assert(await sheet.evaluate((e) => { const r = e.getBoundingClientRect(); return r.left >= 0 && r.right <= innerWidth && r.bottom <= innerHeight; }), "the sheet fits a phone");
      for (const n of ["top", "prev", "next", "bottom"]) assert(await btn(n).isVisible(), n + " reachable");
      await btn("next").click();
      await btn("bottom").click();
      const mn = await metrics();
      assert(Math.abs(mn.top - mn.max) < 2);

      // Escape ends it, and it opens again from what was read
      await page.keyboard.press("Escape");
      await sheet.waitFor({ state: "detached" });
      await page.locator(".sess-talk-btn").click();
      await sheet.locator(".cx-part").first().waitFor();
      await sheet.locator(".bar .primary").click();
      await sheet.waitFor({ state: "detached" });
      assert.deepEqual(errors, []);
    });
  }
}
