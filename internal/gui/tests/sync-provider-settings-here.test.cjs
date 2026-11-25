// Run with Node's test runner and Playwright on the module path; see README.md.
// Only the providers' list and keys synced, each computer keeping its own
// provider settings (StudyingPython on X). Under Advanced in Sync's form,
// Sync only the providers' list and API keys:
// - a setup from before the choice has Advanced folded, the tick off, and
//   posts no providerSettingsHere at all;
// - ticked, Save posts providerSettingsHere: true;
// - opened again, Advanced is open with it ticked, unticking Providers
//   hides it, and unticking it posts providerSettingsHere: false.
// In all five languages, at 1100 and 420px, Chromium and WebKit, nothing
// wider than the window. The API is faked; nothing is synced.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { reach, openPart } = require("./reader.cjs");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const { I18N } = strings();
const tr = (lang, k, vars = {}) => {
  const s = lang === "en" ? k : I18N[lang][k];
  assert.equal(typeof s, "string", `${lang} has ${JSON.stringify(k)}`);
  return s.replace(/\{(\w+)\}/g, (_, v) => vars[v]);
};

const base = { on: true, kind: "s3", url: "s3://bkt/magpie", user: "AKID", passwordSet: true, passphraseSet: true, keys: true, agents: true, library: true, last: new Date(Date.now() - 60000).toISOString() };

function serve(lang, posts) {
  const settings = {
    theme: "light", lang, tray: "panel", version: "test", dir: "/tmp/magpie",
    gateway: "http://127.0.0.1:3425", visionModels: [], imageGenModels: [],
    fx: { rate: 7.2, stale: false },
  };
  let sync = { ...base };
  return async (route) => {
    const request = route.request();
    const pathname = new URL(request.url()).pathname;
    const json = (data) => route.fulfill({ json: data });
    if (pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (pathname === "/api/settings") return json(settings);
    if (pathname === "/api/usage/quotas") return json([]);
    if (pathname === "/api/groups") return json({ groups: [], models: [] });
    if (pathname === "/api/plugins") return json({ plugins: [] });
    if (pathname === "/api/providers") return json({ providers: [], presets: [] });
    if (pathname === "/api/davsync") return json(sync);
    if (pathname === "/api/davsync/save") {
      const b = request.postDataJSON();
      posts.push(b);
      sync = { ...sync, providerSettingsHere: !!b.providerSettingsHere };
      return json(sync);
    }
    if (pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, pathname === "/" ? "index.html" : pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    return route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

async function noneWider(page) {
  const wide = await page.evaluate(() => {
    const out = [];
    if (document.scrollingElement.scrollWidth > innerWidth) out.push("page " + document.scrollingElement.scrollWidth);
    for (const r of document.querySelectorAll("#syncList .sync-form, #syncList .sync-advanced label.tick")) {
      const b = r.getBoundingClientRect();
      if (b.right > innerWidth + 0.5) out.push(r.className + " " + b.right);
    }
    return out;
  });
  assert.deepEqual(wide, [], "wider than the window");
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 420]) {
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      const w = (k, vars) => tr(lang, k, vars);
      test(`${engine} ${width}px ${lang}: only the providers' list and keys can sync`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts));
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${width}-${lang}-sync-provider-settings-here.png`), fullPage: true });
          }
          await browser.close();
        });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const waitPost = async (n) => {
          for (let i = 0; i < 250 && posts.length < n; i++) await page.waitForTimeout(20);
          assert.equal(posts.length, n, "saves posted");
          return posts[n - 1];
        };
        const click = async (loc) => { await reach(page, loc); await loc.click(); };
        const exact = (s) => new RegExp("^" + s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&") + "$");

        await page.goto("http://magpie.test/");
        await page.locator("#prefs").click();
        await openPart(page, "sync");
        const list = page.locator("#syncList");
        const first = list.locator(".row.pref").first();
        await first.waitFor({ state: "visible" });
        const edit = async () => {
          await click(first.locator("button", { hasText: exact(w("Edit")) }));
          return list.locator(".sync-form");
        };
        const label = w("Sync only the providers' list and API keys");

        // a setup from before the choice: folded, off, posted without it
        let form = await edit();
        const adv = form.locator("details.sync-advanced");
        assert.equal(await adv.evaluate((d) => d.open), false, "Advanced open on an old setup");
        const tickL = adv.locator("label.tick", { hasText: label });
        const tick = tickL.locator("input");
        assert.equal(await tick.count(), 1, "the tick is under Advanced");
        assert.equal(await tick.isChecked(), false);
        await click(form.locator("button.primary"));
        assert.ok(!("providerSettingsHere" in await waitPost(1)), "an old setup posted providerSettingsHere");
        await form.waitFor({ state: "detached" });

        // ticked: posted
        form = await edit();
        await click(adv.locator("summary"));
        await click(tick);
        await noneWider(page);
        await click(form.locator("button.primary"));
        assert.equal((await waitPost(2)).providerSettingsHere, true);
        await form.waitFor({ state: "detached" });

        // opened again: Advanced open, ticked; Providers unticked hides it
        form = await edit();
        assert.equal(await adv.evaluate((d) => d.open), true, "Advanced folded with the provider settings kept here");
        assert.equal(await tick.isChecked(), true);
        const prov = form.locator("label.tick", { hasText: exact(w("Providers")) }).locator("input");
        await click(prov);
        await tickL.waitFor({ state: "hidden" });
        await click(prov);
        await tickL.waitFor({ state: "visible" });
        await click(tick);
        await noneWider(page);
        await click(form.locator("button.primary"));
        assert.equal((await waitPost(3)).providerSettingsHere, false, "unticked posts providerSettingsHere: false");
        await form.waitFor({ state: "detached" });
        assert.deepEqual(errors, []);
      });
    }
  }
}
