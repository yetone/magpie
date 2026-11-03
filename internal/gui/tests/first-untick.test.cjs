// Run with Node's test runner and Playwright on the module path; see README.md.
// Rime_Nan on X: in a subscription magpie's gateway serves (Copilot, a
// plugin's), the first account's tick was fixed, so it could be ticked off
// only after dragging it down the list. With another account ticked, its
// tick posts login/off, and the next ticked one is first (the server's);
// with none, it stays fixed and says to tick another. Both engines, every
// language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const copilot = (spareOn) => ({
  id: "copilot", name: "Copilot", icon: "githubcopilot", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "gpt-6", name: "GPT-6", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: {
    agent: "copilot", agentName: "Copilot", user: "first", plan: "PRO",
    logins: [
      { user: "first", plan: "PRO", active: true, on: true },
      { user: "spare", plan: "PRO", on: spareOn },
    ],
  },
});

function serve(lang, list, posts) {
  const providers = { providers: list, presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/") && route.request().method() === "POST") {
      posts.push({ action: url.pathname.slice("/api/".length), body: route.request().postDataJSON() });
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const fixedTitle = "The gateway uses this account first; tick another to stop using it";
const offTitle = "Stop using this account; the next ticked one is used first";

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    const open = async (t, list) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const posts = [];
      await page.route("**/*", serve(lang, list, posts));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: "Copilot" }).first().click();
      await page.locator(".editor .acc").first().waitFor();
      return { page, errors, posts };
    };
    const tr = (page, k) => page.evaluate(([l, k]) => (l === "en" ? k : I18N[l][k]), [lang, k]);

    test(`${engine} ${lang}: the first account ticks off while another is ticked`, async (t) => {
      const { page, errors, posts } = await open(t, [copilot(true)]);
      const first = page.locator(".editor .acc", { hasText: "first" }).first();
      const tick = first.locator(".tick");
      assert.equal(await tick.evaluate((e) => e.classList.contains("fixed")), false, "not fixed");
      assert.equal(await tick.getAttribute("title"), await tr(page, offTitle));
      const n = posts.length;
      await tick.click();
      for (let i = 0; i < 50 && posts.length === n; i++) await page.waitForTimeout(50);
      const p = posts.at(-1);
      assert.equal(p?.action, "login/off");
      assert.deepEqual(p.body, { agent: "copilot", user: "first" });
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: the only ticked account stays, and says to tick another`, async (t) => {
      const { page, errors, posts } = await open(t, [copilot(false)]);
      const tick = page.locator(".editor .acc", { hasText: "first" }).first().locator(".tick");
      assert(await tick.evaluate((e) => e.classList.contains("fixed")), "fixed");
      const title = await tick.getAttribute("title");
      assert.equal(title, await tr(page, fixedTitle));
      assert(title, "translated");
      const n = posts.length;
      await tick.click();
      await page.waitForTimeout(300);
      assert.equal(posts.length, n);
      assert.deepEqual(errors, []);
    });
  }
}
