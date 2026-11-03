// Run with Node's test runner and Playwright on the module path; see README.md.
// #3850 (prgrmrwy): magpie on a home server, and the office's remote magpie
// card said "1 reset" with no way to use it there. A remote magpie's Codex
// card now offers the reset as this computer's does: the same in-app
// confirmation (no confirm()), which says the magpie on that computer uses
// it, and nothing is spent before the user says so. The use names the
// card's remote provider and the account. A GLM team's resets (byWindow),
// spent on the vendor's page, and a remote's other cards offer none. What
// the remote answered goes in the user's words (a read-only key, no answer
// in time). The Usage page at 900px and 390px and the menu bar panel at
// 440px; English, Chinese (both), Japanese and German; Chromium and WebKit.
// No backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.ASSET_DIR || path.resolve(__dirname, "../assets"));
const day = 864e5, base = Date.now();
const soon = new Date(base + 2 * day).toISOString();
const windows = [{ name: "5 hours", used: 100, resetsAt: new Date(base + 3600e3).toISOString() }, { name: "7 days", used: 60, resetsAt: new Date(base + 3 * day).toISOString() }];
const quotas = [
  { provider: "office/codex", name: "Codex · Office", icon: "codex-color", from: "Office", user: "me@example.com", plan: "Plus", kind: "subscription", windows,
    resets: { count: 1, until: soon } },
  // a GLM team's, read on the remote: spent on bigmodel.cn, not from here
  { provider: "office/zhipu-team", name: "GLM Team · Office", icon: "zhipu", from: "Office", user: "team@example.com", kind: "subscription", windows,
    resets: { count: 2, byWindow: true } },
];

const READ_ONLY = "this key is read-only on the remote magpie; give the provider a key with full access to use a reset from here";
const LATE = "Office didn't say in time whether the reset was used; it may have been. Refresh the card to see before asking again";

function serve(lang, posted, answer) {
  const settings = { theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd", trayUsages: ["office/codex", "office/zhipu-team"], trayUsageEvery: 3 };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data, status = 200) => route.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/usage/codex-reset") {
      posted.push(JSON.parse(req.postData() || "{}"));
      return answer.error ? json({ error: answer.error }, 400) : json({ code: "reset", windows: 2 });
    }
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file).catch(() => ""), contentType });
  };
}

const KEYS = ["The magpie on {computer}, where this account is signed in, uses it.",
  "This key is read-only on that computer's magpie — give this provider a key with full access to use a reset from here",
  "That computer didn't say in time whether the reset was used — it may have been. Refresh the card before asking again",
  "That computer's magpie can't use a reset from here yet — update magpie there",
  "That computer's magpie holds this key to a budget, models or accounts, so it uses none of its resets — give this provider a key with full access"];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: a remote magpie's Codex reset is used from here, asked first`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const errors = [];
      const say = (page, key, vars) => page.evaluate(([lang, key, vars]) => {
        if (lang !== "en" && !I18N[lang]?.[key]) return "untranslated: " + key;
        return t(key, vars);
      }, [lang, key, vars]);

      const run = async (url, viewport, useSel, cardSel) => {
        const posted = [], answer = {};
        const page = await (await browser.newContext({ viewport, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        page.on("dialog", (d) => { errors.push("a browser dialog: " + d.message()); d.dismiss(); });
        await page.route("**/*", serve(lang, posted, answer));
        await page.goto(url);
        if (url.includes("panel")) await page.locator('#ptabs [data-ptab="usage"]').click();
        for (const k of KEYS) assert.notEqual(await say(page, k, { computer: "x" }), "untranslated: " + k);

        // the remote's Codex card offers its reset; the GLM team's doesn't
        await page.locator(cardSel).first().waitFor();
        assert.equal(await page.locator(useSel).count(), 1, "one Use, on the remote Codex card");
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "nothing wider than the page");
        const ask = page.locator(".reset-ask");
        await page.locator(useSel).click();
        await ask.waitFor();
        assert.equal((await ask.locator(".reset-remote").textContent()).trim(), await say(page, "The magpie on {computer}, where this account is signed in, uses it.", { computer: "Office" }));
        assert.equal(await page.evaluate(() => { const e = document.querySelector(".reset-ask").getBoundingClientRect(); return e.right <= innerWidth && e.left >= 0; }), true, "the confirmation fits");
        // Cancel spends nothing
        await page.keyboard.press("Escape");
        await ask.waitFor({ state: "detached" });
        assert.equal(posted.length, 0, "nothing spent before the user says so");

        // a read-only key there: said in the user's words, the sheet stays
        answer.error = READ_ONLY;
        await page.locator(useSel).click();
        await ask.locator("button.primary").click();
        await page.locator("#status.err").waitFor();
        assert.equal((await page.locator("#status").textContent()).trim(), await say(page, KEYS[1]));
        assert.equal(await ask.isVisible(), true, "the confirmation stays to try again");
        // no answer in time: it may have been used
        answer.error = LATE;
        await ask.locator("button.primary").click();
        await page.waitForFunction((want) => document.querySelector("#status")?.textContent.trim() === want, await say(page, KEYS[2]));
        await page.keyboard.press("Escape");
        await ask.waitFor({ state: "detached" });

        // used: the remote card's provider and account are named
        delete answer.error;
        await page.locator(useSel).click();
        await ask.locator("button.primary").click();
        await ask.waitFor({ state: "detached" });
        assert.deepEqual(posted, [
          { user: "me@example.com", provider: "office/codex" },
          { user: "me@example.com", provider: "office/codex" },
          { user: "me@example.com", provider: "office/codex" },
        ]);
        await page.context().close();
      };

      for (const width of [900, 390]) await run("http://magpie.test/?view=usage", { width, height: 800 }, ".quota-resets button", ".quota-resets");
      await run("http://magpie.test/?mode=panel", { width: 440, height: 700 }, ".pq-use", ".pq-use");
      assert.deepEqual(errors, []);
    });
  }
}
