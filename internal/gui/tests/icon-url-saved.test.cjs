// Run with Node's test runner and Playwright on the module path; see README.md.
// hoobnn on Discord: a custom provider's icon given by an image URL wasn't
// fetched or applied, and on editing it again the URL was gone. The address
// was sent with Enter only, so one typed and saved was dropped, and nothing
// kept it. Now an address typed in the box is fetched by Save (or by Use, or
// by leaving the box), the picture and its address are saved, and the editor
// opened again shows both. One that can't be used keeps the editor open with
// the note why, and nothing is saved. In Chromium and WebKit, at 420 and
// 1100 wide, in every language.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const ICON = { url: "file:00000000000000bb.png", other: "file:00000000000000dd.png", letter: "file:00000000000000cc.svg" };
const GOOD = "https://cdn.example.com/logo.png", OTHER = "https://cdn.example.com/other.png", GONE = "https://cdn.example.com/gone.png";
const FAILED = "the icon URL answered 404 Not Found";

function serve(lang, posts) {
  const relay = {
    id: "relay", name: "My Relay", icon: "generic", host: "relay.example.com", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
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
    if (url.pathname === "/api/icons/url") {
      if (body.url === GONE) return json({ icon: ICON.letter, failed: FAILED });
      return json({ icon: body.url === OTHER ? ICON.other : body.url ? ICON.url : ICON.letter, failed: "" });
    }
    // the save is kept as the backend keeps it, for the editor's next opening
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

const KEYS = ["Get icon", "Image URL…", "Image URL", "Use", "Save", "That picture couldn't be used, so the icon is the name's letters: {why}"];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [420, 1100]) {
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      test(`${engine} ${width} ${lang}: an icon's image URL typed and saved is fetched, kept and shown again`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${width}-${lang}-icon-url-saved.png`) });
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
        const srcEnds = (sel, icon) => page.waitForFunction(([sel, src]) => document.querySelector(sel)?.getAttribute("src")?.endsWith(src), [sel, icon.slice(5)]);
        const choose = async () => {
          await pick.getByRole("button", { name: w["Get icon"] }).click();
          await page.locator(".proto-menu").getByRole("menuitemradio", { name: new RegExp("^" + w["Image URL…"].replace(/[.*+?^${}()|[\]\\]/g, "\\$&")) }).click();
          await box.waitFor();
        };

        // an address typed, never Entered, then Save: fetched, then saved with it
        await open();
        await choose();
        await box.fill(GOOD);
        await saveBtn.click();
        await editor.waitFor({ state: "detached" });
        assert.deepEqual(posted("/api/icons/url").map((b) => b.url), [GOOD], "Save didn't fetch the typed address");
        const saves = posted("/api/provider/save");
        assert.equal(saves.length, 1);
        assert.equal(saves[0].icon, ICON.url, "Save didn't send the fetched picture");
        assert.equal(saves[0].iconFrom, GOOD, "Save didn't send the address");
        await srcEnds(".row.provider img", ICON.url);

        // opened again: the picture, and its address in its box
        await open();
        await srcEnds(".editor .icon-now img", ICON.url);
        await box.waitFor();
        assert.equal(await box.inputValue(), GOOD, "the address wasn't shown again");
        const over = await pick.evaluate((p) => [...p.children].filter((c) => c.getBoundingClientRect().right > p.getBoundingClientRect().right + 1).map((c) => c.className || c.textContent));
        assert.deepEqual(over, [], "the icon row spills out");
        // nothing changed: Save fetches nothing again
        await saveBtn.click();
        await editor.waitFor({ state: "detached" });
        assert.equal(posted("/api/icons/url").length, 1, "an unchanged address was fetched again");
        assert.equal(posted("/api/provider/save")[1].iconFrom, GOOD);

        // another address with Use, then Save
        await open();
        await box.waitFor();
        await box.fill(OTHER);
        await pick.getByRole("button", { name: w.Use, exact: true }).click();
        await srcEnds(".editor .icon-now img", ICON.other);
        await saveBtn.click();
        await editor.waitFor({ state: "detached" });
        assert.deepEqual(posted("/api/icons/url").map((b) => b.url), [GOOD, OTHER], "Use and Save fetched it twice, or not at all");
        assert.deepEqual([posted("/api/provider/save")[2].icon, posted("/api/provider/save")[2].iconFrom], [ICON.other, OTHER]);

        // one that can't be used: the note, the editor open, nothing saved
        await open();
        await box.waitFor();
        await box.fill(GONE);
        await saveBtn.click();
        const note = editor.locator(".editor-error");
        await note.waitFor();
        assert.equal(await note.textContent(), w["That picture couldn't be used, so the icon is the name's letters: {why}"].replace("{why}", FAILED));
        assert(await editor.isVisible(), "the editor closed on a picture that couldn't be used");
        assert.equal(posted("/api/provider/save").length, 3, "a picture that couldn't be used was saved");
        assert.equal(await box.inputValue(), GONE);
        assert.deepEqual(errors, []);
      });
    }
  }
}
