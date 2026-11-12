// Run with Node's test runner and Playwright on the module path; see README.md.
// A proxy that isn't Custom takes no room for an address it doesn't have
// (the owner, 11-12: "这些空白看起来好难受"): under the provider's Global
// proxy | Direct | Custom and under each account's Provider's proxy |
// Direct | Custom, the address and its login show for Custom only, and
// nothing hidden keeps a strip of its own. An account's options sit as far
// from the next account's name as every other's do, and picking Custom
// and back gives the same gap again. In every language, both engines, at
// a phone's width too.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const claude = {
  id: "claude", name: "Claude", icon: "claude-color", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "claude-opus-5", name: "Claude Opus 5", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: {
    agent: "claude", agentName: "Claude Code", user: "one@example.com", plan: "MAX",
    logins: [
      { user: "one@example.com", plan: "MAX", active: true, on: true },
      { user: "two@example.com", plan: "MAX", on: true },
      { user: "three@example.com", plan: "PRO", on: true },
      { user: "four@example.com", plan: "PRO", on: true },
    ],
  },
  proxy: "",
  accountProxies: { "two@example.com": "direct", "four@example.com": "socks5://10.0.0.5:1080" },
};

function serve(lang) {
  const providers = { providers: [claude], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// the room under each proxy row: from the options' bottom to the top of
// what is shown next (the hint under the provider's, the next account's
// name under an account's), and whether its address is shown
const gaps = (page) => page.evaluate(() => {
  // shown: takes room and can be seen (a hidden one keeping its room is the bug)
  const shown = (e) => !!e.getBoundingClientRect().height && getComputedStyle(e).visibility !== "hidden";
  const next = (e) => { let n = e.nextElementSibling; while (n && !shown(n)) n = n.nextElementSibling; return n; };
  const under = (row, after) => Math.round(after.getBoundingClientRect().top - row.getBoundingClientRect().bottom);
  const own = document.querySelector(".editor .proxy-pick");
  const ownRow = own.querySelector(".proxy-row");
  const lines = [...document.querySelectorAll(".editor .acct-proxy")];
  return {
    provider: under(ownRow, next(ownRow)),
    accounts: lines.map((l, i) => {
      const row = l.querySelector(".proxy-row");
      const after = lines[i + 1]?.querySelector(".who") || next(l);
      const custom = shown(l.querySelector(".proxy-url"));
      return { user: l.dataset.user, gap: under(custom ? l.querySelector(".proxy-auth") : row, after), custom };
    }),
  };
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [900, 420]) {
      test(`${engine} ${lang} ${width}px: a proxy that isn't Custom leaves no empty strip`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.locator(".editor .proxy-pick").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-proxy-gaps.png`) }).catch(() => {});
          }
          await browser.close();
        });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "Claude" }).first().click();
        const adv = page.locator(".editor details.advanced");
        await adv.waitFor({ state: "attached" });
        if (!(await adv.evaluate((d) => d.open))) await adv.locator(":scope > summary").click();
        await page.locator(".editor .acct-proxy").first().waitFor();

        const first = await gaps(page);
        // the provider's Global proxy: its hint right under the options
        assert(first.provider <= 12, `${first.provider}px under the provider's options`);
        assert.deepEqual(first.accounts.map((a) => a.custom), [false, false, false, true], JSON.stringify(first.accounts));
        // every account but the last: the same room before the next name,
        // Custom's address and login counted as its own
        const between = first.accounts.slice(0, -1).map((a) => a.gap);
        assert(between.every((g) => g >= 0 && g <= 12), `gaps ${between}`);
        assert(Math.max(...between) - Math.min(...between) <= 1, `uneven gaps ${between}`);

        // Custom on the first, then back: its gap comes back as it was
        const opts = page.locator(".editor .acct-proxy").first().locator(".proxy-mode .opt");
        await opts.nth(2).click();
        assert.equal((await gaps(page)).accounts[0].custom, true);
        await opts.nth(0).click();
        assert.deepEqual((await gaps(page)).accounts.map((a) => a.gap), first.accounts.map((a) => a.gap));
        // the provider's own Custom shows its address, Global proxy hides it
        const own = page.locator(".editor .proxy-pick > .proxy-row .proxy-mode .opt");
        await own.nth(2).click();
        assert(await page.locator(".editor .proxy-pick > .proxy-row .proxy-url").isVisible());
        await own.nth(0).click();
        assert.equal((await gaps(page)).provider, first.provider);
        assert.deepEqual(errors, []);
      });
    }
  }
}
