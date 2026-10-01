// Run with Node's test runner and Playwright on the module path; see README.md.
// #408: magpie signs Codex in to the next ticked account when the first is
// all but used up, and back to the first once it has room. While it stands
// in, the account Codex is on says "First for now", and the one it goes back
// to "First again once it has room", each with a title saying why; with
// nothing to go back to, the account Codex is on is just "First". The
// Routing note says magpie moves back. The page doesn't move. In English and
// Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const codex = (returns) => ({
  id: "codex", name: "Codex", icon: "codex-color", chat: "", responses: "", anthropic: "", catalog: "", routing: "smart",
  models: [{ id: "gpt-6", name: "GPT-6", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: {
    agent: "codex", agentName: "Codex", user: "spare@example.com", plan: "PLUS",
    logins: [
      { user: "spare@example.com", plan: "PLUS", active: true, on: true },
      { user: "work@example.com", plan: "PRO", on: true, returns },
    ],
  },
});

function serve(lang, list) {
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
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { first: "First", now: "First for now", again: "First again once it has room", note: /and back to the first once that has room again/, why: /work@example\.com was nearly used up/ },
  zh: { first: "首选", now: "暂为首选", again: "额度恢复后切回首选", note: /等首选账号额度恢复后再切回去/, why: /work@example\.com 额度快用完了/ },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    const open = async (t, returns) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, [codex(returns)]));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: "Codex" }).first().click();
      await page.locator(".editor .acc").first().waitFor();
      return { page, errors };
    };

    test(`${engine} ${lang}: the account magpie moved Codex off says it is first again once it has room`, async (t) => {
      const { page, errors } = await open(t, true);
      const top = await page.evaluate(() => document.scrollingElement.scrollTop);
      const spare = page.locator(".editor .acc", { hasText: "spare@example.com" });
      const work = page.locator(".editor .acc", { hasText: "work@example.com" });
      assert.equal(await spare.locator(".using").textContent(), w.now);
      assert.match(await spare.locator(".using").getAttribute("title"), w.why);
      assert.equal(await work.locator(".using").textContent(), w.again);
      assert.ok(await work.locator(".using").getAttribute("title"));
      assert.match(await page.locator(".editor").textContent(), w.note);
      const missing = await page.evaluate(() => [
        "First for now", "First again once it has room",
        "{user} was nearly used up, so magpie signed {agent} in to this one; it goes back to {user} once that has room again",
        "magpie signs {agent} back in to this account once it has room again",
        "Routing picks the account for each request through magpie; {agent} on its own uses the one it is signed in to, which magpie moves to the next ticked account with room once it is 98% used, and back to the first once that has room again.",
      ].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      await work.locator(".using").click();
      assert.equal(await page.evaluate(() => document.scrollingElement.scrollTop), top, "the page doesn't move");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: with nothing to go back to, the account Codex is on is first`, async (t) => {
      const { page, errors } = await open(t, false);
      const spare = page.locator(".editor .acc", { hasText: "spare@example.com" });
      const work = page.locator(".editor .acc", { hasText: "work@example.com" });
      assert.equal(await spare.locator(".using").textContent(), w.first);
      assert.equal(await spare.locator(".using").getAttribute("title"), null);
      assert.equal(await work.locator(".using").count(), 0);
      assert.deepEqual(errors, []);
    });
  }
}
