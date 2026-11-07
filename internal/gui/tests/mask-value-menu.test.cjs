// Run with Node's test runner and Playwright on the module path; see README.md.
// Insights › Masking, a request opened: a masked value in its text, as
// written here or as the vendor got it, opens a menu (yew_fence on
// Discord): Copy copies what the value was, and Never mask puts it on
// Settings › Privacy's Never mask list through /api/masking/allow, so the
// next request sends it as written. A value already there, or one of the
// user's own Masked words, says why Never mask is off. A value from
// before a restart has nothing to copy and opens nothing. A click scrolls
// nothing, nothing is left in English, nothing is wider than the window.
// Every language, Chromium and WebKit, 1100 and 640px; the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const KEY = "{{API_KEY_aaaa1111}}", WORD = "{{WORD_eeee5555}}", NAME = "{{PERSON_bbbb2222}}";
const SECRET = "sk-proj-abcdEFGH1234ijkl", NIGHTJAR = "Nightjar";
const K1 = "1792987654321000001", K2 = "1792987654321000002";
const ago = (ms) => new Date(Date.now() - ms).toISOString();
const SENT = JSON.stringify({ model: "claude-opus-4-1", messages: [
  { role: "user", content: [{ type: "text", text: `export OPENAI_API_KEY=${KEY} # project ${WORD}` }] },
] });
const records = [
  { key: K2, t: ago(120e3), route: "messages", what: "chat", agent: "claude", provider: "anthropic", model: "claude-opus-4-1", n: 2, took: 1800,
    values: [{ p: KEY, k: "API_KEY", b: "rule", h: "sk-p", l: 24, n: 1, known: true }, { p: WORD, k: "WORD", b: "word", h: "N", l: 8, n: 1, known: true }],
    snips: { [KEY]: `export OPENAI_API_KEY=${KEY}`, [WORD]: `# project ${WORD}` } },
  // from before a restart: no values in memory
  { key: K1, t: ago(300e3), route: "search", what: "search", agent: "codex", provider: "openai", model: "gpt-5", n: 1,
    values: [{ p: NAME, k: "PERSON", b: "model", h: "", l: 3, n: 1 }], snips: { [NAME]: `ask ${NAME} about it` } },
];

function serve(lang, asked) {
  const settings = { theme: "light", lang, tray: "panel", currency: "usd", redact: true, redactPersonal: false, redactWords: [NIGHTJAR], redactAllow: ["build-01"] };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "claude", name: "Claude Code", icon: "claudecode-color", fields: [] }], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/copy") { asked.push("copy " + JSON.parse(req.postData()).text); return route.fulfill({ status: 204, body: "" }); }
    if (url.pathname === "/api/masking/allow") {
      const p = JSON.parse(req.postData()).p;
      asked.push("allow " + p);
      if (p !== KEY) return route.fulfill({ status: 404, body: "404 page not found" });
      settings.redactAllow = [...settings.redactAllow, SECRET];
      return json(settings);
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, mine: true, url: "http://127.0.0.1:3999", models: 0, groups: [], calls: [] } });
    if (url.pathname === "/api/masking/summary") return json({ days: [], requests: 2, n: 3, distinct: 3, blocked: 0, hours: [], kinds: [{ name: "API_KEY", n: 1, distinct: 1 }], by: { rule: 1 }, agents: [], providers: [], values: [], since: ago(7 * 86400e3) });
    if (url.pathname === "/api/masking/records") return json({ records, more: false });
    if (url.pathname === "/api/masking/record") {
      const r = records.find((r) => r.key === url.searchParams.get("key"));
      return r ? json(r) : route.fulfill({ status: 404, body: "404 page not found" });
    }
    if (url.pathname === "/api/masking/sent") return url.searchParams.get("key") === K2 ? json({ body: SENT }) : route.fulfill({ status: 404, body: "404 page not found" });
    if (url.pathname === "/api/masking/values") {
      const known = { [KEY]: SECRET, [WORD]: NIGHTJAR };
      const ps = JSON.parse(req.postData()).ps;
      return json({ values: Object.fromEntries(ps.filter((p) => known[p]).map((p) => [p, known[p]])) });
    }
    if (url.pathname === "/api/masking/value") return url.searchParams.get("p") === KEY ? json({ value: SECRET }) : route.fulfill({ status: 404, body: "404 page not found" });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

async function until(ok, ms = 8000) {
  for (const end = Date.now() + ms; !ok();) {
    if (Date.now() > end) throw new Error("never happened: " + ok);
    await new Promise((r) => setTimeout(r, 25));
  }
}
const scrolled = (page) => page.evaluate(() => [scrollX, scrollY, document.querySelector("#view-masking").scrollTop, document.querySelector("main, .main, #main")?.scrollTop ?? 0]);
async function stillClick(page, loc) {
  await loc.scrollIntoViewIfNeeded();
  const before = await scrolled(page);
  await loc.click();
  await page.waitForTimeout(100);
  assert.deepEqual(await scrolled(page), before, "no click moved the page");
}
const wide = (page) => page.evaluate(() => {
  const over = [...document.querySelectorAll("#view-masking *, .row-menu, .row-menu *")].filter((n) => n.getClientRects().length && n.getBoundingClientRect().right > innerWidth + 1 && !n.closest(".msk-text, .msk-same")).map((n) => n.className?.baseVal ?? n.className);
  return { page: document.documentElement.scrollWidth > document.documentElement.clientWidth, over };
});
const english = ["Copy", "Never mask", "Value copied", "One of your Masked words", "In Never mask already", "Sent as written from the next request on"];
async function untranslated(page, lang, where) {
  if (lang === "en") return;
  const said = await page.locator(where).innerText();
  for (const w of english) assert(!said.includes(w), `${w} left in English: ${said}`);
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 640]) {
      test(`${engine} ${lang} ${width}px: a masked value in a request's text copies, and goes on Never mask`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 1000 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(8000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], asked = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, asked));
        await page.goto("http://magpie.test/?view=masking");
        await page.locator("#maskList .msk-row").first().waitFor();
        await page.locator("#maskList .msk-row").first().click();
        const one = page.locator("#maskOne");
        await one.locator(".msk-msg").first().waitFor();
        const here = one.locator(".msk-msg .msk-text").first().locator("mark"), sent = one.locator(".msk-msg .msk-text").last().locator("mark");
        await here.first().waitFor();
        assert.equal(await one.locator("mark.acts").count(), 4, "each value, in both columns, opens a menu");
        assert.equal(await here.first().getAttribute("aria-haspopup"), "menu");

        // as written here: Copy copies the value
        const menu = page.locator(".row-menu");
        await stillClick(page, here.first());
        await menu.waitFor();
        assert.equal(await menu.locator(".rm-item").count(), 2);
        assert.equal(await menu.locator(".rm-item:disabled").count(), 0, "a key not on the list yet");
        await untranslated(page, lang, ".row-menu");
        assert.deepEqual(await wide(page), { page: false, over: [] });
        await stillClick(page, menu.locator(".rm-item").first());
        await until(() => asked.includes("copy " + SECRET));
        assert.equal(await menu.count(), 0, "the menu closes");
        await page.waitForFunction(() => document.querySelector("#status")?.classList.contains("ok"));
        await untranslated(page, lang, "#status");

        // as the vendor got it: Never mask asks the server, by placeholder
        await stillClick(page, sent.first());
        await menu.waitFor();
        await stillClick(page, menu.locator(".rm-item").nth(1));
        await until(() => asked.includes("allow " + KEY));
        await page.waitForFunction((s) => document.querySelector("#status")?.textContent && !document.querySelector("#status").textContent.includes(s), "copied");
        if (lang === "en") await page.waitForFunction(() => document.querySelector("#status").textContent === "Sent as written from the next request on");
        await untranslated(page, lang, "#status");
        assert.equal(asked.filter((a) => a.startsWith("allow")).length, 1);

        // once on the list, Never mask is off and says why
        await stillClick(page, here.first());
        await menu.waitFor();
        assert.equal(await menu.locator(".rm-item:disabled").count(), 1);
        if (lang === "en") assert.match(await menu.locator(".rm-item:disabled").innerText(), /In Never mask already/);
        await untranslated(page, lang, ".row-menu");
        // the same value clicked again closes its menu
        await stillClick(page, here.first());
        assert.equal(await menu.count(), 0);

        // one of the user's Masked words: Copy, but Never mask is off
        await stillClick(page, sent.nth(1));
        await menu.waitFor();
        assert.equal(await menu.locator(".rm-item:disabled").count(), 1);
        if (lang === "en") assert.match(await menu.locator(".rm-item:disabled").innerText(), /One of your Masked words/);
        await untranslated(page, lang, ".row-menu");
        assert.deepEqual(await wide(page), { page: false, over: [] });
        await page.keyboard.press("Escape");
        assert.equal(await menu.count(), 0);
        assert.equal(asked.filter((a) => a.startsWith("allow")).length, 1, "nothing more was put on the list");
        assert.deepEqual(errors, []);
      });
    }
  }
  test(`${engine}: a value from before a restart opens nothing`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await (await browser.newContext({ viewport: { width: 640, height: 1000 }, reducedMotion: "reduce" })).newPage();
    page.setDefaultTimeout(8000);
    const errors = [], asked = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.route("**/*", serve("en", asked));
    await page.goto("http://magpie.test/?view=masking");
    await page.locator("#maskList .msk-row").nth(1).waitFor();
    await page.locator("#maskList .msk-row").nth(1).click();
    const one = page.locator("#maskOne");
    await one.locator(".msk-gone").waitFor();
    assert.equal(await one.locator("mark.acts").count(), 0);
    await one.locator("mark").first().click();
    await page.waitForTimeout(100);
    assert.equal(await page.locator(".row-menu").count(), 0);
    assert.deepEqual(errors, []);
  });
}
