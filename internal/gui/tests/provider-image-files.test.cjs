// Run with Node's test runner and Playwright on the module path; see README.md.
// matrix0487 (Discord #feedback, 11-05): image-files (#5407), DeepSeek's
// own API sent an agent's images once by file id, was only
// `magpie provider set <id> image-files=off`. Now the editor of a
// provider on api.deepseek.com, the built-in DeepSeek or a custom one
// there (the server's takesImageFiles), has under Advanced "Upload images
// once and send them by file id", ticked by default. Unticked and saved,
// it posts imageFilesOff: true; one set off from the CLI opens unticked
// with Advanced open, and ticked again posts false. Any other provider
// has no such row and its Save sends none. A checkbox, never a native
// select. Every language, wide and narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const base = {
  icon: "generic", chat: "", responses: "", anthropic: "", catalog: "",
  models: [{ id: "deepseek-flash", name: "deepseek-flash", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…ds1" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", searches: false,
};
const ds = {
  ...base, id: "deepseek", name: "DeepSeek", preset: "deepseek", icon: "deepseek-color", catalog: "deepseek",
  chat: "https://api.deepseek.com/v1", responses: "https://api.deepseek.com/v1", anthropic: "https://api.deepseek.com/anthropic",
  takesImageFiles: true, imageFilesOff: false,
};
const cliOff = { ...base, id: "mine", name: "Own Endpoint", anthropic: "https://api.deepseek.com/anthropic", takesImageFiles: true, imageFilesOff: true };
const relay = { ...base, id: "relay", name: "Other Relay", chat: "https://relay.example.com/deepseek/v1", imageFilesOff: false };

function serve(lang, posts) {
  const providers = {
    providers: [ds, cliOff, relay],
    presets: [{ id: "deepseek", name: "DeepSeek", kind: "vendor", icon: "deepseek-color", chat: "https://api.deepseek.com/v1", added: true }],
    excluded: [], gateway: { running: true, window: true },
  };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/provider/") && route.request().method() === "POST") {
      posts.push({ action: url.pathname.slice("/api/provider/".length), body: route.request().postDataJSON() });
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const strings = ["Images", "Upload images once and send them by file id", "Off: an agent's images are sent inline on every turn."];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      const open = async (t, name) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        let page;
        t.after(async () => {
          if (process.env.ARTIFACT_DIR && page) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-${name.replace(/\s+/g, "-")}-image-files.png`), fullPage: true });
          }
          await browser.close();
        });
        page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const posts = [];
        await page.route("**/*", serve(lang, posts));
        await page.goto("http://magpie.test/?view=providers");
        const w = await page.evaluate(([lang, ks]) => Object.fromEntries(ks.map((k) => [k, (I18N[lang] || {})[k] || k])), [lang, [...strings, "Save"]]);
        await page.locator(".row.provider", { hasText: name }).click();
        await page.locator(".editor .bar").waitFor();
        return { page, errors, posts, w };
      };
      const adv = (page) => page.locator(".editor details.advanced");
      const unfold = async (page) => {
        await adv(page).waitFor({ state: "attached" });
        if (!(await adv(page).evaluate((d) => d.open))) await adv(page).locator(":scope > summary").click();
      };
      const row = (page) => page.locator(".editor label.tick.image-files");
      const box = (page) => row(page).locator("input[type=checkbox]");
      const save = async (page, posts, w) => {
        const n = posts.length;
        await page.locator(".editor .bar").getByRole("button", { name: w.Save, exact: true }).click();
        for (let i = 0; i < 50 && posts.length === n; i++) await page.waitForTimeout(50);
        return posts.at(-1);
      };

      test(`${engine} ${lang} ${width}: the built-in DeepSeek uploads images by default, and is saved not to`, async (t) => {
        const { page, errors, posts, w } = await open(t, "DeepSeek");
        if (lang !== "en") {
          const missing = await page.evaluate(([lang, ks]) => ks.filter((k) => !(I18N[lang] || {})[k]), [lang, strings]);
          assert.deepEqual(missing, [], `every string has its ${lang}`);
        }
        assert.equal(await adv(page).evaluate((d) => d.open), false, "Advanced stays folded while it is as by default");
        await unfold(page);
        assert.equal(await row(page).isVisible(), true);
        assert.equal(await row(page).innerText().then((s) => s.trim()), w["Upload images once and send them by file id"]);
        assert.equal(await box(page).isChecked(), true, "ticked by default");
        assert.equal(await page.locator(".editor select").count(), 0, "no native select");
        await box(page).scrollIntoViewIfNeeded();
        const r = await row(page).boundingBox();
        assert.ok(r.width > 60 && r.x >= 0 && r.x + r.width <= width, `the row fits: ${JSON.stringify(r)}`);
        assert.ok(await page.locator(".editor", { hasText: w[strings[2]] }).count() > 0, "the note is shown");
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, "no sideways scroll");
        await box(page).uncheck();
        const saved = await save(page, posts, w);
        assert.equal(saved.action, "save");
        assert.equal(saved.body.id, "deepseek");
        assert.equal(saved.body.imageFilesOff, true);
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}: one set off from the CLI opens unticked with Advanced open, and ticked is saved on`, async (t) => {
        const { page, errors, posts, w } = await open(t, "Own Endpoint");
        assert.equal(await adv(page).evaluate((d) => d.open), true, "Advanced opens with it set");
        assert.equal(await box(page).isChecked(), false, "the CLI's off shows as off");
        await box(page).scrollIntoViewIfNeeded();
        await box(page).check();
        const saved = await save(page, posts, w);
        assert.equal(saved.body.id, "mine");
        assert.equal(saved.body.imageFilesOff, false);
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${lang} ${width}: a provider elsewhere has no row, and its Save sends none`, async (t) => {
        const { page, errors, posts, w } = await open(t, "Other Relay");
        await unfold(page);
        assert.equal(await row(page).count(), 0);
        assert.equal(await page.locator(".editor", { hasText: w["Upload images once and send them by file id"] }).count(), 0);
        const saved = await save(page, posts, w);
        assert.equal(saved.action, "save");
        assert.equal("imageFilesOff" in saved.body, false, `its Save sends none: ${JSON.stringify(saved.body)}`);
        assert.deepEqual(errors, []);
      });
    }
  }
}
