// Run with Node's test runner and Playwright on the module path; see README.md.
// Refeccd on Discord: a custom provider whose icon came from its base URL
// couldn't be given a built-in icon. Automatic kept the base URL as the
// picture's address, the editor showed it in the Image URL box, and Save
// fetched that address over the built-in icon just picked. Now a built-in
// icon picked is the one saved, with nothing fetched, and Automatic keeps no
// address. In Chromium and WebKit, at 420 and 1100 wide, in every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, process.env.MAGPIE_ICON_ASSETS || "../assets");
const BASE = "https://relay.example.com/v1", SITE = "file:00000000000000ee.png", LETTER = "file:00000000000000cc.svg";

function serve(lang, posts) {
  // saved the way Automatic saved it before: the base URL as the address
  const relay = {
    id: "relay", name: "My Relay", icon: SITE, iconFrom: BASE, host: "relay.example.com", chat: BASE, responses: "", anthropic: "", catalog: "",
    models: [{ id: "gpt-5", name: "gpt-5", on: true }], chosen: ["gpt-5"],
    fetched: new Date(Date.now() - 3600e3).toISOString(), agents: [], fallback: [], headers: {},
    key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
  };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    const body = req.method() === "POST" ? req.postDataJSON() || {} : null;
    if (body && url.pathname.startsWith("/api/")) posts.push({ path: url.pathname, body });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    const list = () => ({ providers: [relay], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/providers") return json(list());
    if (url.pathname === "/api/groups") return json({ groups: [] });
    // a base URL is no picture: the letters, as the backend answers it
    if (url.pathname === "/api/icons/url") return json({ icon: LETTER, failed: body.url ? "the icon URL isn't a picture" : "" });
    if (url.pathname === "/api/icons/favicon") return json({ icon: SITE });
    if (url.pathname === "/api/provider/save") {
      relay.icon = body.icon;
      if (body.iconFrom) relay.iconFrom = body.iconFrom; else delete relay.iconFrom;
      return json(list());
    }
    if (url.pathname.startsWith("/api/icons/")) return route.fulfill({ contentType: "image/svg+xml", body: '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 8"><rect width="8" height="8" fill="#3875b3"/></svg>' });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const KEYS = ["Get icon", "Automatic", "Built-in icons", "Image URL", "Save"];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [420, 1100]) {
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      test(`${engine} ${width} ${lang}: a built-in icon replaces one that came from the base URL`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${width}-${lang}-icon-builtin.png`) });
          }
          await browser.close();
        });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const posts = [];
        await page.route("**/*", serve(lang, posts));
        await page.goto("http://magpie.test/?view=providers");

        const w = await page.evaluate(([lang, keys]) => Object.fromEntries(keys.map((k) => [k, lang === "en" ? k : I18N[lang]?.[k]])), [lang, KEYS]);
        assert.deepEqual(KEYS.filter((k) => !w[k]), [], `${lang} lacks these`);

        const editor = page.locator("#modal:not([hidden]) .editor");
        const row = page.locator(".row.provider", { hasText: "My Relay" });
        const pick = editor.locator(".icon-pick");
        const box = pick.getByRole("textbox", { name: w["Image URL"] });
        const saveBtn = editor.locator(".bar").getByRole("button", { name: w.Save, exact: true });
        const open = async () => {
          await row.click();
          await pick.waitFor();
          await pick.evaluate((p) => p.scrollIntoView({ block: "start" }));
        };
        const posted = (p) => posts.filter((x) => x.path === p).map((x) => x.body);
        const save = async () => {
          await saveBtn.click();
          await editor.waitFor({ state: "detached" });
          return posted("/api/provider/save").at(-1);
        };

        // the reporter's case: a built-in icon picked over the base URL's
        await open();
        await pick.getByRole("button", { name: w["Built-in icons"] }).click();
        await pick.locator('.icon-grid button[title="github"]').click();
        assert.equal(await box.count(), 0, "the base URL is still in the Image URL box");
        let saved = await save();
        assert.deepEqual(posted("/api/icons/url").map((b) => b.url), [], "Save fetched the base URL over the picked icon");
        assert.deepEqual([saved.icon, saved.iconFrom], ["github", ""], "the picked icon wasn't saved");

        // opened again: still the picked one, and no address
        await open();
        await page.waitForFunction(() => document.querySelector(".editor .icon-now [data-icon]")?.dataset.icon === "github");
        assert.equal(await box.count(), 0);

        // Automatic looks for the site's icon from the base URL, but keeps
        // no address: the base URL isn't the picture's
        await pick.getByRole("button", { name: w["Get icon"] }).click();
        await page.locator(".proto-menu").getByRole("menuitemradio", { name: new RegExp("^" + w.Automatic) }).click();
        await page.waitForFunction(() => document.querySelector(".editor .icon-now [data-icon]")?.dataset.icon === "file:00000000000000ee.png");
        assert.deepEqual(posted("/api/icons/favicon").map((b) => b.url), [BASE]);
        assert.equal(await box.count(), 0, "Automatic put the base URL in the Image URL box");
        saved = await save();
        assert.deepEqual([saved.icon, saved.iconFrom], [SITE, ""], "Automatic saved the base URL as the picture's address");

        // and a built-in icon over Automatic's sticks too
        await open();
        await pick.getByRole("button", { name: w["Built-in icons"] }).click();
        await pick.locator('.icon-grid button[title="github"]').click();
        saved = await save();
        assert.equal(saved.icon, "github");
        assert.deepEqual(posted("/api/icons/url"), [], "something was fetched over a picked icon");
        assert.deepEqual(errors, []);
      });
    }
  }
}
