// Run with Node's test runner and Playwright on the module path; see README.md.
// EastonXvii on X: a ChatGPT account can be kept "access token only", so
// magpie never renews its sign-in and another computer signed in to it
// stays signed in. The import box offers it for ChatGPT and says the
// source stays signed in; the Import posts it. A saved account that isn't
// the one Codex is on offers it under ⋯ and posts login/access-only; one
// kept so says "magpie only" in place of Use, and once its token has run
// out, "Access expired" with Import again, which opens the import with it
// ticked. In English and Chinese, Chromium and WebKit, every string in
// every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const file = '{"auth_mode":"chatgpt","tokens":{"access_token":"a","id_token":"i"}}';
const lapse = "kept's access token has expired, and it is kept access token only, so magpie doesn't renew it — import it again, or sign in to it again";
const account = () => ({
  id: "codex", name: "ChatGPT", icon: "openai", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "gpt-6", name: "GPT-6", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: {
    agent: "codex", agentName: "Codex", user: "live@example.com", plan: "plus", logins: [
      { user: "live@example.com", plan: "plus", active: true, on: true },
      { user: "spare@example.com", plan: "plus", on: true },
      { user: "kept@example.com", plan: "plus", on: true, accessOnly: true },
      { user: "gone@example.com", plan: "plus", on: true, accessOnly: true, lapsed: lapse },
    ],
  },
});

function serve(lang, posted) {
  const providers = { providers: [account()], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (route.request().method() === "POST" && (url.pathname === "/api/signin/import" || url.pathname.startsWith("/api/login/"))) {
      posted.push([url.pathname, route.request().postDataJSON()]);
      if (url.pathname === "/api/signin/import") return json({ providers, results: [{ user: "new@example.com", status: "added", plan: "plus" }] });
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const f = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(f)];
    await route.fulfill({ body: await fs.readFile(f), contentType });
  };
}

const strings = [
  "Access expired", "Import again", "Access token only",
  "Kept access token only: requests through magpie use it until it expires; Codex itself isn't signed in to it, as Codex would renew it",
  "magpie never renews this sign-in, so another computer signed in to it stays signed in. Once its access token expires, import it again.",
  "Each account is kept with the file's access token and never renewed, so the tool it came from stays signed in. Once that token expires, import it again.",
];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const open = async (t, posted) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posted));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: "ChatGPT" }).click();
      await page.locator(".editor .acc").first().waitFor();
      const T = (s) => page.evaluate((s) => t(s), s);
      return { page, errors, T };
    };

    test(`${engine} ${lang}: the import keeps ChatGPT accounts access token only when ticked`, async (t) => {
      const posted = [];
      const { page, errors, T } = await open(t, posted);
      const missing = await page.evaluate((ks) => ks.flatMap((k) => ["zh", "zh-TW", "ja", "de"].filter((l) => !I18N[l][k]).map((l) => l + ": " + k)), strings);
      assert.deepEqual(missing, [], "every string in every language");

      await page.locator(".editor .accts .acc.add").nth(1).click();
      const box = page.locator(".signing.import");
      await box.waitFor();
      const tick = box.locator("label.import-access-only");
      assert.equal(await tick.locator("span").textContent(), await T("Access token only"));
      const spent = box.locator(".tt > .s").nth(1);
      assert.match(await spent.textContent(), lang === "en" ? /signed out of that account/ : /退出该账号/);
      await tick.locator("input").check();
      assert.equal(await spent.textContent(), await T(strings[5]));
      await box.locator("textarea").fill(file);
      await box.locator("button.primary").click();
      await box.locator(".results").waitFor();
      assert.deepEqual(posted, [["/api/signin/import", { agent: "codex", files: [file], accessOnly: true }]]);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a saved account is kept access token only from its row`, async (t) => {
      const posted = [];
      const { page, errors, T } = await open(t, posted);
      const row = (u) => page.locator(".editor .acc", { hasText: u });

      // the one Codex is on doesn't offer it; a spare does, under ⋯
      assert.equal(await row("live@example.com").locator(".acc-access-only").count(), 0);
      const spare = row("spare@example.com");
      await spare.locator(".acc-more").click();
      const tick = spare.locator(".acc-tools .acc-access-only");
      assert.equal(await tick.locator("input").isChecked(), false);
      // clicked, not check(): the fake's answer draws it unticked again
      await tick.locator("input").click();
      for (let i = 0; i < 100 && !posted.length; i++) await page.waitForTimeout(20);
      assert.deepEqual(posted[0], ["/api/login/access-only", { agent: "codex", user: "spare@example.com" }]);

      // kept so: "magpie only", never Use, and its tick shows on its line
      const kept = row("kept@example.com");
      assert.equal(await kept.locator(".acc-line .using").textContent(), await T("magpie only"));
      assert.equal(await kept.locator(".acc-line button.text:not(.quiet)").count(), 0, "no Use");
      assert.equal(await kept.locator(".acc-line .acc-access-only input").isChecked(), true);

      // run out: Access expired, and Import again opens the import ticked
      const gone = row("gone@example.com");
      assert.equal(await gone.locator(".acc-line .using").textContent(), await T("Access expired"));
      await gone.getByRole("button", { name: await T("Import again") }).click();
      const box = page.locator(".signing.import");
      await box.waitFor();
      assert.equal(await box.locator("label.import-access-only input").isChecked(), true);
      assert.deepEqual(errors, []);
    });
  }
}
