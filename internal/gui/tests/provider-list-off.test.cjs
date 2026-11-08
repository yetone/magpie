// Run with Node's test runner and Playwright on the module path; see README.md.
// A key's provider can have its model list never asked for (axelburks on
// Discord: Volcengine Ark's list names models by ids that can't be asked
// for, so each endpoint id is typed by hand). The editor's Fetch the model
// list, ticked by default: unticked, Fetch models and Forget go and the Save
// sends listOff; one saved so opens unticked, with no vendor-list hint. A
// signed-in account has no such tick. Every language, Chromium and WebKit,
// wide and narrow.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.resolve(__dirname, "../assets"));
const fetched = new Date(Date.now() - 3600e3).toISOString();
const ark = (listOff) => ({
  id: "volcengine", name: "Volcengine Ark", icon: "generic", preset: "volcengine", host: "ark.cn-beijing.volces.com", chat: "https://ark.cn-beijing.volces.com/api/v3", responses: "", anthropic: "", catalog: "",
  models: listOff ? [{ id: "ep-20261108-abcde", name: "ep-20261108-abcde", on: true }] : [{ id: "doubao-seed-2-0", name: "doubao-seed-2-0", on: true }, { id: "ep-20261108-abcde", name: "ep-20261108-abcde", on: true }],
  chosen: listOff ? ["ep-20261108-abcde"] : ["doubao-seed-2-0", "ep-20261108-abcde"], ...(listOff ? {} : { fetched }), agents: [], fallback: [], headers: {},
  key: { set: true, masked: "ark-…aaa" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", takesListOff: true, listOff,
});
const account = { id: "codex", name: "Codex", icon: "openai", account: { kind: "codex" }, models: [{ id: "gpt-5.5", name: "GPT-5.5", on: true }], chosen: ["gpt-5.5"], fetched, agents: [], fallback: [], headers: {}, keyList: [] };

function serve(lang, list, saves) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/providers") return json({ providers: list, presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/provider/save") {
      saves.push(req.postDataJSON());
      return json({ providers: list, presets: [], excluded: [], gateway: { running: true, window: true } });
    }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const say = (page, lang, s) => page.evaluate(([l, s]) => (l === "en" ? s : I18N[l][s]), [lang, s]);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1200, 420]) {
      test(`${engine} ${lang} ${width}px: Fetch the model list turns the vendor's list off`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        for (const [kind, p] of [["on", ark(false)], ["off", ark(true)]]) {
          const page = await (await browser.newContext({ viewport: { width, height: 900 } })).newPage();
          page.setDefaultTimeout(5000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          const saves = [];
          await page.route("**/*", serve(lang, [p, account], saves));
          await page.goto("http://magpie.test/?view=providers");
          const label = await say(page, lang, "Fetch the model list");
          assert.ok(label, `Fetch the model list has its ${lang}`);
          await page.locator(".row.provider", { hasText: "Volcengine Ark" }).click();
          const editor = page.locator("#modal:not([hidden]) .editor");
          await editor.locator(".mchips .mchip").first().waitFor();
          const tick = editor.locator(".list-on input[type=checkbox]");
          assert.equal(await tick.count(), 1, `${kind}: one tick`);
          assert.equal(await editor.locator(".list-on").textContent(), label);
          const fetchBtn = editor.locator(".mfoot .fetch-models");
          if (kind === "on") {
            assert.equal(await tick.isChecked(), true, "ticked by default");
            assert.equal(await fetchBtn.isVisible(), true);
            await tick.uncheck();
            assert.equal(await fetchBtn.isVisible(), false, "unticked: Fetch models goes");
            assert.equal(await editor.locator(".mfoot button", { hasText: await say(page, lang, "Forget") }).isVisible(), false, "and Forget");
          } else {
            assert.equal(await tick.isChecked(), false, "saved off: opens unticked");
            assert.equal(await fetchBtn.isVisible(), false);
            assert.equal(await editor.locator(".mfoot .hint").count(), 0, "no vendor-list hint");
            await tick.check();
            assert.equal(await fetchBtn.isVisible(), true, "ticked again: Fetch models is back");
          }
          await editor.locator(".bar button.primary").click();
          for (let i = 0; i < 100 && !saves.length; i++) await page.waitForTimeout(20);
          assert.equal(saves.length, 1);
          assert.equal(saves[0].listOff, kind === "on", "the Save sends the tick");

          // a signed-in account lists its models as its sign-in does
          await page.locator(".row.provider", { hasText: "Codex" }).click();
          await editor.locator(".mchips .mchip").first().waitFor();
          assert.equal(await editor.locator(".list-on").count(), 0, "no tick on an account");
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-${kind}-provider-list-off.png`) });
          }
          assert.deepEqual(errors, []);
          await page.context().close();
        }
      });
    }
  }
}
