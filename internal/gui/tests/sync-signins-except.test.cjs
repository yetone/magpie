// Run with Node's test runner and Playwright on the module path; see README.md.
// Subscriptions left out of syncing sign-ins (Ethereal499 on X: Claude
// kept out, since it is wary of where its accounts are used). Under the
// Sign-ins tick in Sync's form, Not synced shows only while the tick is
// on, None until picked:
// - a setup from before the choice posts no signInsExcept at all;
// - its field (the provider combobox) lists the subscriptions with
//   accounts, not a key provider, each with its logo; picking Claude
//   shows its chip there and Save posts ["claude"];
// - opened again, Advanced is open with Claude shown, and unticking the
//   sign-ins hides the row.
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

const base = { on: true, kind: "webdav", url: "https://dav.example.invalid/dav/", user: "me", passwordSet: true, passphraseSet: true, keys: true, agents: true, library: true, signIns: true, last: new Date(Date.now() - 60000).toISOString() };
const subs = [
  { id: "claude", name: "Claude", account: { logins: [{ user: "me@x.com", active: true }] } },
  { id: "codex", name: "Codex", account: { logins: [{ user: "me@x.com", active: true }] } },
  { id: "deepseek", name: "DeepSeek" },
];

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
    if (pathname === "/api/providers") return json({ providers: subs, presets: [] });
    if (pathname === "/api/davsync") return json(sync);
    if (pathname === "/api/davsync/save") {
      const b = request.postDataJSON();
      posts.push(b);
      sync = { ...sync, signIns: !!b.signIns, signInsExcept: b.signInsExcept };
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
    for (const r of document.querySelectorAll("#syncList .sync-form, #syncList .sync-except, #syncList .sync-except-pick, .pc-pop")) {
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
      test(`${engine} ${width}px ${lang}: a subscription's sign-ins can be left out of sync`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, posts));
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${width}-${lang}-sync-signins-except.png`), fullPage: true });
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

        // a setup from before the choice: shown with None, posted without it
        let form = await edit();
        assert.equal(await form.locator("details.sync-advanced").evaluate((d) => d.open), true, "Advanced folded with sign-ins on");
        const row = form.locator(".sync-except");
        const pick = row.locator(".sync-except-pick");
        const chips = () => page.locator(".sync-except .pc-chip").allTextContents();
        await row.waitFor({ state: "visible" });
        assert.equal(await row.locator("> span").textContent(), w("Not synced"));
        assert.deepEqual(await chips(), []);
        assert.equal(await pick.locator("input").getAttribute("placeholder"), w("None"));
        await noneWider(page);
        await click(form.locator("button.primary"));
        assert.ok(!("signInsExcept" in await waitPost(1)), "an old setup posted signInsExcept");
        await form.waitFor({ state: "detached" });

        // Claude left out
        form = await edit();
        await click(pick);
        const menu = page.locator(".pc-pop");
        await menu.waitFor();
        assert.equal(await menu.locator("[role=listbox]").getAttribute("aria-label"), w("Not synced"));
        assert.deepEqual(await menu.locator("[role=option] .v").allTextContents(), ["Claude", "Codex"], "the list has the subscriptions with accounts");
        assert.equal(await menu.locator("[role=option] .ic").count(), 2, "a logo on each");
        await noneWider(page);
        await click(menu.locator("[role=option]", { hasText: "Claude" }));
        await page.waitForFunction(() => [...document.querySelectorAll(".sync-except .pc-chip")].map((c) => c.textContent).join() === "Claude");
        await page.keyboard.press("Escape");
        await menu.waitFor({ state: "detached" });
        await click(form.locator("button.primary"));
        const saved = await waitPost(2);
        assert.deepEqual(saved.signInsExcept, ["claude"]);
        assert.equal(saved.signIns, true);
        await form.waitFor({ state: "detached" });

        // opened again: shown; the sign-ins unticked hide it
        form = await edit();
        await page.waitForFunction(() => [...document.querySelectorAll(".sync-except .pc-chip")].map((c) => c.textContent).join() === "Claude");
        assert.equal(await page.locator(".sync-except .pc-chip .ic").count(), 1, "the chip has its logo");
        const siTick = form.locator("label.tick", { hasText: w("Sign-ins: subscription accounts and plugin sign-ins") }).locator("input");
        await click(siTick);
        await row.waitFor({ state: "hidden" });
        await click(siTick);
        await row.waitFor({ state: "visible" });
        await noneWider(page);
        await click(first.locator("button", { hasText: exact(w("Close")) }));
        await form.waitFor({ state: "detached" });
        assert.deepEqual(errors, []);
      });
    }
  }
}
