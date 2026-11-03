// Run with Node's test runner and Playwright on the module path; see README.md.
// #2094 (wangjianjq): Command Code's list serves its Claude models on
// /messages alone and the rest on chat and Responses. At a provider given
// only the chat and Responses URLs, Select all ticked the Claude models too,
// and agents were offered models no request could reach. A model listed on
// none of the APIs the provider has a URL for (noAPI) is drawn dimmed and
// says why; Select all and Free only leave it; the vendor's list (dashed)
// is the first 24 of the others; a model whose list says no API is picked
// as before. A pick of such a model made before stays picked and is saved
// again, to be offered once the URL is added. Nothing scrolls, no stripe,
// nothing runs off the side; every language, Chromium and WebKit, 1100 and
// 420 wide.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const { I18N } = strings();
const fetched = new Date(Date.now() - 3600e3).toISOString();
const relay = (id, name, models, chosen, extra = {}) => ({
  id, name, icon: "generic", host: id + ".example.com", chat: `https://${id}.example.com/v1`, responses: `https://${id}.example.com/v1`, anthropic: "", catalog: "",
  speaks: ["chat", "responses"], models, chosen, fetched, agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "", ...extra,
});
// as /api/providers has them: m.on is what agents are offered
const claude = (id) => ({ id, name: id, on: false, auto: ["anthropic"], noAPI: true });
const gpt = (id, on, extra = {}) => ({ id, name: id, on, auto: ["chat", "responses"], ...extra });
const CLAUDE = ["claude-sonnet-5-5", "claude-opus-5-5", "claude-haiku-4-5"];
const CHAT = Array.from({ length: 26 }, (_, i) => `deepseek-v4.${i}`);
// a list's order, Claude first as Command Code's is; one model with no API
// listed, one free model on chat and one free Claude
const listed = [...CLAUDE.map(claude), ...CHAT.map((id, i) => gpt(id, i < 24)), { id: "plain-model", name: "plain-model", on: false },
  gpt("ling-3.1-flash:free", false, { free: true }), { ...claude("claude-free"), free: true }];
const cc = relay("cc-chat", "Command Code chat", listed, []);
// picked before: a Claude model and a chat one; agents get the chat one
const picked = relay("cc-picked", "Command Code picked", [claude("claude-sonnet-5-5"), gpt("deepseek-v4.1", true), gpt("deepseek-v4.2", false)], ["claude-sonnet-5-5", "deepseek-v4.1"]);

function serve(lang, posts) {
  const providers = { providers: [cc, picked], presets: [], excluded: [], gateway: { running: true, window: true } };
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (req.method() === "POST" && url.pathname.startsWith("/api/")) posts.push({ path: url.pathname, body: req.postDataJSON() || {} });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const LANGS = ["en", "zh", "zh-TW", "ja", "de"];
const TIP = "{p} has no URL for the API its list serves it on ({apis}), so agents aren't offered it. Add that URL, or set the API it is asked on in Names, prices & levels.";
const ALL_TIP = "Pick every model listed that this provider can serve (those the filter shows)";

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    for (const width of [1100, 420]) {
      const tr = (k) => (lang === "en" ? k : I18N[lang][k]);
      test(`${engine} ${lang} ${width}: Select all leaves the models the provider has no URL for`, async (t) => {
        for (const k of [TIP, ALL_TIP, "Select all", "Free only", "Save"]) assert(tr(k), `${lang} lacks ${k}`);
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-select-all-api.png`) });
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

        const editor = page.locator("#modal:not([hidden]) .editor");
        const open = async (name) => {
          await page.locator(".row.provider", { hasText: name }).click();
          await editor.locator(".ehead b", { hasText: name }).waitFor();
          await editor.locator(".mchips .mchip").first().waitFor();
        };
        const bulk = (name) => editor.locator(".mbulk").getByRole("button", { name, exact: true });
        const chipsOf = (cls) => editor.locator(`.mchips .mchip${cls}`).evaluateAll((cs) => cs.map((c) => c.querySelector("span").textContent).sort());
        const scrolled = () => page.evaluate(() => [window.scrollX, window.scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop > 0).map((e) => `${e.className}:${e.scrollTop}`)].join(" "));
        const save = async () => {
          await editor.locator(".bar").getByRole("button", { name: tr("Save"), exact: true }).click();
          for (let i = 0; i < 100 && !posts.some((p) => p.path === "/api/provider/save"); i++) await page.waitForTimeout(30);
          const saved = posts.find((p) => p.path === "/api/provider/save");
          assert(saved, "nothing saved");
          posts.length = 0;
          return [...saved.body.models].sort();
        };

        await open("Command Code chat");
        // the Claude models dimmed, saying why; the vendor's list (dashed)
        // is the first 24 of the chat ones
        assert.deepEqual(await chipsOf(".noapi"), [...CLAUDE, "claude-free"].sort());
        const tip = await editor.locator(".mchips .mchip.noapi", { hasText: "claude-opus-5-5" }).getAttribute("title");
        assert(tip.includes(tr(TIP).replace("{p}", "Command Code chat").split("{apis}")[0]), `tooltip: ${tip}`);
        assert.deepEqual(await chipsOf(".auto"), CHAT.slice(0, 24).sort());
        assert.equal(await editor.locator(".mchips .mchip.noapi.auto").count(), 0);
        assert(await editor.locator(".mchips .mchip.noapi").first().evaluate((e) => parseFloat(getComputedStyle(e).opacity) < 1), "not dimmed");
        assert.equal(await bulk(tr("Select all")).getAttribute("title"), tr(ALL_TIP));

        // Free only: the free chat model, not the free Claude
        await bulk(tr("Free only")).click();
        assert.deepEqual(await chipsOf(".on"), ["ling-3.1-flash:free"]);

        // Select all, the button scrolled into the editor's view first: the
        // click moves nothing, and the Claude models are left
        await editor.locator(".ebody").evaluate((b) => { b.scrollTop += b.querySelector(".mbulk").getBoundingClientRect().top - b.getBoundingClientRect().top - 8; });
        const before = await scrolled();
        await bulk(tr("Select all")).click();
        assert.equal(await scrolled(), before, "the click scrolled");
        const want = [...CHAT, "plain-model", "ling-3.1-flash:free"].sort();
        assert.deepEqual(await chipsOf(".on"), want);
        assert.deepEqual(await save(), want);

        // nothing runs off the side, and no stripe
        const over = await page.evaluate(() => [document.documentElement.scrollWidth - innerWidth, ...[...document.querySelectorAll("#modal .editor *")].filter((e) => getComputedStyle(e).borderLeftWidth !== getComputedStyle(e).borderRightWidth && parseFloat(getComputedStyle(e).borderLeftWidth) > 1).map((e) => e.className)]);
        assert.deepEqual(over, [0], "overflow or stripe");
        // the fake save answers nothing, so the page is opened again
        await page.goto("http://magpie.test/?view=providers");

        // a Claude pick made before stays picked (dimmed), and is saved again
        await open("Command Code picked");
        assert.deepEqual(await chipsOf(".on"), ["claude-sonnet-5-5", "deepseek-v4.1"]);
        assert.deepEqual(await chipsOf(".on.noapi"), ["claude-sonnet-5-5"]);
        assert.deepEqual(await save(), ["claude-sonnet-5-5", "deepseek-v4.1"]);
        assert.deepEqual(errors, []);
      });
    }
  }
}
