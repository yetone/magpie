// Run with Node's test runner and Playwright on the module path; see README.md.
// agy-cli's reasons for no reading this time (its usage.mjs at 0.1.147:
// agy's sign-in due to be renewed, agy still reading, its read too slow, no
// agy, an agy too old) are said in the user's language on the account's
// row, the Usage page's card and the tray's card, and beside the last
// reading kept from before ("As of … — couldn't be read just now"), never
// only agy-cli's English line (punk2sang on X: "好像没办法看 antigravity
// 额度了", an hour after the account's last chat).
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function server(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: "light", web: true })};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// agy-cli's words, verbatim, and the line each is said as
const said = [
  ["agy's sign-in is due to be renewed, and agy renews it itself on this account's next chat; the quota is read after that. magpie doesn't run agy just for usage meanwhile: a renewal that fails there can open a Google sign-in in the browser.",
    "agy renews this account's sign-in on its next chat, and reads its allowance after that"],
  ["agy is still reading this account's quota (its own read takes longer here: a slow network or proxy, or agy's first start). It shows on the next refresh.",
    "agy is still reading this allowance — it shows on the next refresh"],
  ["agy's own usage read didn't finish in 45s. agy asks Google about twelve times, one after another, before it has the quota, which takes longer behind a slow network or proxy. It's asked again in a minute; if this stays, check agy's network or proxy.",
    "agy took too long to read its usage — it is asked again soon; if this stays, check agy's network or proxy"],
  ["agy didn't say its version in 45s (its first start after an update can take that long), so the quota wasn't read. It's asked again in a minute.",
    "agy took too long to read its usage — it is asked again soon; if this stays, check agy's network or proxy"],
  ["agy CLI not found. Sign in to this account again in magpie to have it installed, or install Antigravity CLI yourself.",
    "agy isn't installed — sign in to this account again in magpie to install it"],
  ["agy usage reports require a verified agy version 1.1.11 or later. Update the CLI.",
    "agy is too old to tell its usage — update Antigravity CLI"],
];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine}: agy-cli's reason for no reading is said in the user's words (${lang})`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 440, height: 760 } })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang));
      await page.goto("http://magpie.test/?view=providers");
      await page.waitForFunction(() => typeof quotaError === "function");
      for (const [err, line] of said) {
        const want = await page.evaluate((l) => t(l), line);
        if (lang !== "en") assert.notEqual(want, line, `${lang} has its own words for ${line}`);
        // nothing read yet: the row, the Usage page's card and the tray's
        // card each say why, not only in their tooltip
        const q = { provider: "plugin-agy-cli", name: "Antigravity CLI", user: "a@gmail.com", windows: [], error: err, pluginError: true };
        const none = await page.evaluate((q) => ({
          row: accountQuota({ [q.user]: q }, q.user).textContent,
          usage: quotaWindows(q).textContent,
          panel: panelQuotaCard(q).querySelector(".pq-sub.err").textContent,
        }), q);
        for (const [where, text] of Object.entries(none)) {
          assert.ok(text.includes(want), `${where}: ${text}`);
          assert.ok(!text.includes(err.slice(0, 30)), `${where} says agy-cli's English: ${text}`);
        }
        // the last reading kept, as of when it was read, with why beside it
        const kept = { provider: "plugin-agy-cli", name: "Antigravity CLI", user: "a@gmail.com", asOf: new Date(Date.now() - 3600e3).toISOString(), readError: err,
          windows: [{ name: "7 days", used: 66.39, span: 604800e9, aside: true, pool: "Claude & GPT", resetsAt: new Date(Date.now() + 72 * 3600e3).toISOString() }] };
        const asOf = await page.evaluate((q) => ({
          text: asOfText(q),
          row: accountQuota({ [q.user]: q }, q.user).querySelector(".aq-asof").textContent,
          card: readWhen(q).textContent,
          lead: t("As of {when} — couldn't be read just now", { when: new Date(q.asOf).toLocaleString(intlLang()) }),
        }), kept);
        for (const where of ["text", "row", "card"]) {
          assert.ok(asOf[where].includes(want), `${where}: ${asOf[where]}`);
          assert.ok(asOf[where].startsWith(asOf.lead), `${where} dates the reading: ${asOf[where]}`);
          assert.ok(!asOf[where].includes(err.slice(0, 30)), `${where} says agy-cli's English: ${asOf[where]}`);
        }
      }
      // any other plugin's reason stays its own
      const other = await page.evaluate(() => asOfText({ asOf: new Date().toISOString(), readError: "usage is down" }));
      assert.ok(other.endsWith(" · usage is down"), other);
      assert.deepEqual(errors, []);
    });
  }
}
