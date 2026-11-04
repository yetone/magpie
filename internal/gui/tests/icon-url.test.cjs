// Run with Node's test runner and Playwright on the module path; see README.md.
// usrname1833 on Discord: many sites have no /favicon.ico, so a custom
// provider's icon came out wrong or missing. The editor's Get icon opens the
// app's own menu (never a native select) with Automatic (the site's icon, as
// before), Image URL… and Letter icon. A typed address is sent with Enter; one
// the backend couldn't use comes back as the letter tile with a note saying
// why, and the box stays for another try, as does one that worked (hoobnn).
// Escape puts the box away. What is
// picked is what Save sends. No click scrolls the page. In Chromium and
// WebKit, at 420 and 1100 wide, in every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const relay = {
  id: "relay", name: "My Relay", icon: "generic", host: "relay.example.com", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "gpt-5", name: "gpt-5", on: true }], chosen: ["gpt-5"],
  fetched: new Date(Date.now() - 3600e3).toISOString(), agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
};
const ICON = { favicon: "file:00000000000000aa.png", url: "file:00000000000000bb.png", letter: "file:00000000000000cc.svg" };
const FAILED = "the icon URL answered 404 Not Found";

function serve(lang, posts) {
  const providers = { providers: [relay], presets: [], excluded: [], gateway: { running: true, window: true } };
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
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/icons/favicon") return json({ icon: ICON.favicon });
    if (url.pathname === "/api/icons/url") {
      if (!body.url) return json({ icon: ICON.letter, failed: "" });
      return json(body.url.endsWith("/gone.png") ? { icon: ICON.letter, failed: FAILED } : { icon: ICON.url, failed: "" });
    }
    if (url.pathname.startsWith("/api/icons/")) return route.fulfill({ contentType: "image/svg+xml", body: '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 8"><rect width="8" height="8" fill="#3875b3"/></svg>' });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const KEYS = ["Get icon", "Automatic", "The site's own icon, looked for from the base URL", "Image URL…",
  "A PNG, JPEG, GIF, WebP, ICO or SVG at a public https address", "Letter icon",
  "The name's first letters on a coloured tile", "Image URL", "That picture couldn't be used, so the icon is the name's letters: {why}", "Icon"];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [420, 1100]) {
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      test(`${engine} ${width} ${lang}: a custom provider's icon from an image URL or its letters`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${width}-${lang}-icon-url.png`) });
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

        // every new word is said in every language, and the test reads its labels from there
        const w = await page.evaluate(([lang, keys]) => Object.fromEntries(keys.map((k) => [k, lang === "en" ? k : I18N[lang]?.[k]])), [lang, KEYS]);
        assert.deepEqual(KEYS.filter((k) => !w[k]), [], `${lang} lacks these`);

        const editor = page.locator("#modal:not([hidden]) .editor");
        await page.locator(".row.provider", { hasText: "My Relay" }).click();
        const pick = editor.locator(".icon-pick");
        await pick.waitFor();
        const scrolls = () => page.evaluate(() => [scrollY, ...[...document.querySelectorAll("#modal, #modal .editor, #modal .ebody")].map((e) => e.scrollTop)]);
        // the row and the address box under it in view, so no step needs Playwright to scroll
        await pick.evaluate((p) => p.scrollIntoView({ block: "start" }));
        const before = await scrolls();

        const get = pick.getByRole("button", { name: w["Get icon"] });
        assert.equal(await get.getAttribute("aria-haspopup"), "menu");
        const menu = page.locator(".proto-menu");
        // past the menu's fade, for a person looking at the pictures only
        const shot = async (what) => process.env.ARTIFACT_DIR && (await page.waitForTimeout(250), page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${width}-${lang}-icon-url-${what}.png`) }));
        const choose = async (name) => {
          await pick.getByRole("button", { name: w["Get icon"] }).click();
          await menu.waitFor();
          assert.equal(await menu.locator(".pm-head").textContent(), w.Icon);
          assert.deepEqual(await menu.locator(".pm-name").allTextContents(), [w.Automatic, w["Image URL…"], w["Letter icon"]]);
          await shot("menu");
          await menu.getByRole("menuitemradio", { name: new RegExp("^" + name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")) }).click();
          await menu.waitFor({ state: "detached" });
        };
        const now = () => pick.locator(".icon-now img").getAttribute("src");
        const posted = async (p) => {
          for (let i = 0; i < 100 && !posts.some((x) => x.path === p); i++) await page.waitForTimeout(30);
          const at = posts.findIndex((x) => x.path === p);
          assert(at >= 0, `nothing posted to ${p}`);
          return posts.splice(at, 1)[0].body;
        };
        assert.equal(await editor.locator("select").count(), 0, "a native select");

        // Automatic: the site's icon from the base URL, as before
        await choose(w.Automatic);
        assert.deepEqual(await posted("/api/icons/favicon"), { name: "My Relay", url: "https://relay.example.com/v1" });
        await page.waitForFunction((src) => document.querySelector(".editor .icon-now img")?.getAttribute("src")?.endsWith(src), ICON.favicon.slice(5));

        // Image URL…: a box for the address, focused; Enter sends it
        await choose(w["Image URL…"]);
        const box = pick.getByRole("textbox", { name: w["Image URL"] });
        await box.waitFor();
        assert(await box.evaluate((b) => b === document.activeElement), "the address box isn't focused");
        assert.equal(await box.getAttribute("type"), "url");
        await box.fill("https://cdn.example.com/gone.png");
        await box.press("Enter");
        assert.deepEqual(await posted("/api/icons/url"), { name: "My Relay", url: "https://cdn.example.com/gone.png", base: "https://relay.example.com/v1" });
        // it couldn't be used: the letters, a note saying why, and the box kept
        const note = editor.locator(".editor-error");
        await note.waitFor();
        assert.match(await note.getAttribute("class"), /\bwarn\b/);
        assert.equal(await note.textContent(), w["That picture couldn't be used, so the icon is the name's letters: {why}"].replace("{why}", FAILED));
        assert((await now()).endsWith(ICON.letter.slice(5)));
        assert.equal(await box.inputValue(), "https://cdn.example.com/gone.png");
        await shot("note");
        await box.fill("https://cdn.example.com/logo.png");
        await box.press("Enter");
        assert.equal((await posted("/api/icons/url")).url, "https://cdn.example.com/logo.png");
        // the address it came from stays in its box (hoobnn)
        await page.waitForFunction((src) => document.querySelector(".editor .icon-now img")?.getAttribute("src")?.endsWith(src), ICON.url.slice(5));
        assert.equal(await box.inputValue(), "https://cdn.example.com/logo.png");
        assert.equal(await note.count(), 0, "the note outlived a picture that worked");
        assert((await now()).endsWith(ICON.url.slice(5)));

        // Escape puts the box away and leaves the dialog open
        await choose(w["Image URL…"]);
        await box.press("Escape");
        await box.waitFor({ state: "detached" });
        assert(await editor.isVisible(), "Escape closed the editor");
        assert(await pick.getByRole("button", { name: w["Get icon"] }).evaluate((b) => b === document.activeElement), "focus didn't go back to Get icon");

        // Letter icon: the name's letters, asked with no address
        await choose(w["Letter icon"]);
        assert.deepEqual(await posted("/api/icons/url"), { name: "My Relay", base: "https://relay.example.com/v1" });
        await page.waitForFunction((src) => document.querySelector(".editor .icon-now img")?.getAttribute("src")?.endsWith(src), ICON.letter.slice(5));
        assert.deepEqual(await scrolls(), before, "a click scrolled");

        // at 420 the row stays inside the dialog
        const over = await pick.evaluate((p) => [...p.children].filter((c) => c.getBoundingClientRect().right > p.getBoundingClientRect().right + 1).map((c) => c.className || c.textContent));
        assert.deepEqual(over, [], "the icon row spills out");

        await editor.locator(".bar").getByRole("button", { name: lang === "en" ? "Save" : await page.evaluate((l) => I18N[l].Save, lang), exact: true }).click();
        assert.equal((await posted("/api/provider/save")).icon, ICON.letter);
        assert.deepEqual(errors, []);
      });
    }
  }
}
