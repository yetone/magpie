// Run with Node's test runner and Playwright on the module path; see README.md.
// A key's provider with a System One URL beside its chat URL (axelburks on
// Discord: a relay's own decision model, named as the relay names it): a
// model can be set to be asked on System One from Model settings' API row,
// and the Save sends it as modelPrefs. With one conversation URL the row
// offers Auto and System One alone. One saved so is tagged System One on
// its chip, and its chip's right-click can set it back. A provider with no
// System One URL, and a signed-in account, offer none. Every language,
// Chromium and WebKit, wide and at a phone's width.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.resolve(__dirname, "../assets"));
const base = { icon: "generic", catalog: "", agents: [], fallback: [], headers: {}, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", ready: true, key: { set: true, masked: "sk-…one" } };
const relay = (saved) => ({
  ...base, id: "ark", name: "Ark", chat: "https://ark.example.com/api/v3", responses: "", anthropic: "", gemini: "", decide: "https://ark.example.com/api/v3", decideAPIKind: true, takesModelHeaders: true,
  models: [{ id: "ep-chat", name: "ep-chat", on: true }, { id: "ep-decide", name: "ep-decide", on: true, ...(saved ? { api: "decide" } : {}) }],
  chosen: ["ep-chat", "ep-decide"],
});
const plain = { ...base, id: "plain", name: "Plain", chat: "https://plain.example.com/v1", responses: "", anthropic: "", gemini: "", decideAPIKind: true, models: [{ id: "m1", name: "m1", on: true }], chosen: ["m1"] };
const account = { ...base, id: "codex", name: "Codex", icon: "openai", account: { kind: "codex" }, decide: "https://x.example.com/v1", models: [{ id: "gpt-5.5", name: "GPT-5.5", on: true }], chosen: ["gpt-5.5"] };

function serve(lang, posts, saved) {
  const providers = { providers: [relay(saved), plain, account], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/provider/save") { posts.push(route.request().postDataJSON()); return json(providers); }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const say = (page, lang, s, vars) => page.evaluate(([l, s, v]) => {
  let out = l === "en" ? s : I18N[l][s];
  for (const [k, x] of Object.entries(v || {})) out = out?.replace(`{${k}}`, x);
  return out;
}, [lang, s, vars]);

async function rclick(loc) {
  await loc.click({ button: "right", trial: true });
  await loc.page().evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
  await loc.click({ button: "right" });
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1200, 420]) {
      test(`${engine} ${lang} ${width}px: a model set to be asked on System One`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const open = async (saved) => {
          const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
          page.setDefaultTimeout(5000);
          const errors = [], posts = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", serve(lang, posts, saved));
          await page.goto("http://magpie.test/?view=providers");
          return { page, errors, posts };
        };
        const settingsOf = async (page, name) => {
          await page.locator(".row.provider", { hasText: name }).first().click();
          const editor = page.locator("#modal:not([hidden]) .editor");
          if (!(await editor.locator(".mnames:not([hidden])").count())) await editor.getByRole("button", { name: await say(page, lang, "Model settings"), exact: true }).click();
          await editor.locator(".mnames:not([hidden]) .mname").first().waitFor();
          return editor;
        };

        // staged in Model settings, made with the Save
        let { page, errors, posts } = await open(false);
        let editor = await settingsOf(page, "Ark");
        const row = (id) => editor.locator(".mnames:not([hidden]) .mname", { has: page.locator("code", { hasText: new RegExp(`^${id}$`) }) });
        assert.deepEqual(await row("ep-decide").locator(".mapi .opt").evaluateAll((os) => os.map((o) => o.dataset.api)), ["", "decide"], "Auto and System One: one conversation URL is no choice");
        await row("ep-decide").locator('.mapi .opt[data-api="decide"]').click();
        assert.equal(await row("ep-decide").locator(".mapi .opt.on").getAttribute("data-api"), "decide");
        const over = await page.evaluate(() => {
          const ed = document.querySelector("#modal:not([hidden]) .editor").getBoundingClientRect();
          return [...document.querySelectorAll(".editor .mnames .mapi")].map((m) => m.getBoundingClientRect()).filter((r) => r.width && (r.right > ed.right + 1 || r.left < ed.left - 1)).length
            + (document.documentElement.scrollWidth > innerWidth + 1 ? 100 : 0);
        });
        assert.equal(over, 0, "the API choice stays inside the editor");
        assert.equal(posts.length, 0, "staged, not saved");
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-model-api-decide.png`) });
        }
        await editor.locator(".bar button.primary").click();
        for (let i = 0; i < 100 && !posts.length; i++) await page.waitForTimeout(20);
        assert.deepEqual(posts[0]?.modelPrefs, { "ep-decide": { api: "decide" } });

        // no System One URL, or an account: nothing to set
        editor = await settingsOf(page, "Plain");
        assert.equal(await editor.locator(".mnames:not([hidden]) .mapi").count(), 0, "no System One URL: no API row");
        assert.deepEqual(errors, []);
        ({ page, errors } = await open(false));
        editor = await settingsOf(page, "Codex");
        assert.equal(await editor.locator(".mnames:not([hidden]) .mapi").count(), 0, "an account: no API row");
        assert.deepEqual(errors, []);

        // saved: tagged on its chip, and set back to Auto from its right-click
        ({ page, errors, posts } = await open(true));
        await page.locator(".row.provider", { hasText: "Ark" }).first().click();
        const chip = page.locator(".editor .mchips .mchip", { hasText: "ep-decide" });
        await chip.waitFor();
        assert.equal(await chip.locator(".mapi-tag").textContent(), "System One");
        await rclick(chip);
        await page.locator(".pop.row-menu").getByRole("menuitem", { name: await say(page, lang, "Asked on {api}…", { api: "System One" }) }).click();
        const apiMenu = page.locator(".pop.proto-menu.model-api-menu");
        await apiMenu.waitFor();
        assert.deepEqual((await apiMenu.locator(".pm-name").allTextContents()).slice(1), ["System One"]);
        await apiMenu.locator(".pm-item").first().click();
        await apiMenu.waitFor({ state: "detached" });
        await page.locator("#modal:not([hidden]) .editor .bar button.primary").click();
        for (let i = 0; i < 100 && !posts.length; i++) await page.waitForTimeout(20);
        assert.deepEqual(posts[0]?.modelPrefs, { "ep-decide": { api: "" } });
        assert.deepEqual(errors, []);
      });
    }
  }
}
