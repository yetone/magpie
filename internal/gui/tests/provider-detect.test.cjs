// Run with Node's test runner and Playwright on the module path; see README.md.
// Which APIs a relay answers at the URL typed, found at a click
// (01huadalang on Discord: 一键检测支持什么协议，发一个最小请求看是否返回200，
// 还有每个模型自定义协议): the custom provider editor's Detect APIs posts
// provider/detect with the URL and the model typed, shows what each API
// answered, and "Use these" sets the URLs of those that answered. A model
// served on one API only is given it in Names & levels (or from the
// detection), staged and sent with the Save as modelPrefs.api; Auto gives
// it back. No click moves the page, and nothing has a left-border accent.
// In English and Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const ROOT = "https://relay.example.com";

function serve(lang, posts) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  const models = ["m-mixed", "m-plain"].map((id) => ({ id, name: id, on: true }));
  const provider = { id: "relay", name: "Relay", icon: "generic", chat: ROOT + "/v1", responses: "", anthropic: "", models, agents: [], fallback: [], headers: {}, key: { set: true, masked: "sk-…1234" }, keyList: [], ready: true };
  const providers = { providers: [provider], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/provider/detect") {
      const body = route.request().postDataJSON();
      posts.push({ path: url.pathname, body });
      return json({ results: [
        { protocol: "chat", ok: false, status: 503, error: "no available channel", model: body.model, base: ROOT + "/v1" },
        { protocol: "responses", ok: false, status: 404, error: "Invalid URL", model: body.model, base: ROOT + "/v1" },
        { protocol: "anthropic", ok: true, ms: 321, model: body.model, base: ROOT },
      ] });
    }
    if (url.pathname.startsWith("/api/provider/")) {
      posts.push({ path: url.pathname, body: route.request().postDataJSON() });
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    await route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)] });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: Detect APIs, Use these, and a model's API`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-provider-detect.png`), fullPage: true });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      await page.goto("http://magpie.test/?view=providers");
      const zh = lang === "zh";
      const L = {
        detect: zh ? "检测协议" : "Detect APIs", use: zh ? "使用检测结果" : "Use these", taken: zh ? "已填入 · 保存后生效" : "Taken · save to keep",
        only: zh ? "m-mixed 只用 Anthropic 协议" : "Ask m-mixed on Anthropic only", said: zh ? "保存后 m-mixed 将使用 Anthropic 协议" : "m-mixed is asked on Anthropic once saved",
        names: zh ? "名称与推理档位" : "Names & levels", auto: zh ? "自动" : "Auto", unsaved: zh ? "未保存" : "unsaved", save: zh ? "保存" : "Save",
        ms: zh ? "321 毫秒" : "321 ms",
      };
      await page.locator(".row.provider").click();
      const ed = page.locator(".editor");
      await ed.locator(".detect").waitFor();
      const row = (id) => page.locator(".mname", { has: page.locator("code", { hasText: id }) });
      // a click that leaves the page, and the button, where they were
      const still = async (loc, what) => {
        await loc.scrollIntoViewIfNeeded();
        await page.waitForTimeout(150);
        const before = await loc.evaluate((e) => e.getBoundingClientRect().top);
        const y = await page.evaluate(() => [scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => e.scrollTop)].join());
        await loc.click();
        await page.waitForTimeout(250);
        if (await loc.count()) assert.equal(await loc.evaluate((e) => e.getBoundingClientRect().top), before, what + " moved");
        assert.equal(await page.evaluate(() => [scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => e.scrollTop)].join()), y, what + " scrolled the page");
      };

      // one URL: no API to pick for a model yet
      await page.getByRole("button", { name: L.names, exact: true }).click();
      await row("m-mixed").waitFor();
      assert.equal(await page.locator(".mname .mapi").count(), 0, "with one URL there is nothing to pick");

      // Detect, with a model typed
      await ed.locator(".detect-model").fill("m-mixed");
      await still(ed.getByRole("button", { name: L.detect, exact: true }), "Detect");
      await ed.locator(".detect-out .ep").nth(2).waitFor();
      assert.equal(posts.length, 1);
      const sent = posts[0].body;
      assert.equal(sent.id, "relay");
      assert.equal(sent.base, ROOT + "/v1", "the URL typed");
      assert.equal(sent.model, "m-mixed");
      assert.equal(sent.chat, "", "the URL typed is asked as each API takes it");
      const shown = await ed.locator(".detect-out .ep").evaluateAll((es) => es.map((e) => [e.dataset.api, e.querySelector(".res").className, e.querySelector("code").textContent]));
      assert.deepEqual(shown, [["chat", "res bad", ROOT + "/v1"], ["responses", "res bad", ROOT + "/v1"], ["anthropic", "res ok", ROOT]]);
      assert.equal(await ed.locator('.detect-out .ep[data-api="anthropic"] .res').textContent(), L.ms);
      assert((await ed.locator('.detect-out .ep[data-api="chat"] .res').textContent()).includes("503"));

      // Use these: Anthropic's URL set, chat (503, not missing) kept
      await still(ed.getByRole("button", { name: L.use, exact: true }), "Use these");
      assert.equal(await ed.getByRole("button", { name: L.taken, exact: true }).count(), 1);
      const values = await ed.locator("input").evaluateAll((is) => is.map((i) => i.value));
      assert(values.includes(ROOT), "the Anthropic URL is in the form: " + values);
      assert(values.includes(ROOT + "/v1"), "chat's URL is kept");

      // with two URLs, each model can be given one
      await row("m-plain").locator(".mapi").waitFor();
      const plain = row("m-plain").locator(".mapi");
      assert.deepEqual(await plain.locator(".opt").allTextContents(), [L.auto, "OpenAI", "Anthropic"]);
      await still(plain.locator(".opt", { hasText: "Anthropic" }), "a model's API");
      assert(await row("m-plain").getByText(L.unsaved, { exact: true }).isVisible(), "it says unsaved");
      await still(plain.locator(".opt", { hasText: L.auto }), "Auto");
      assert.equal(await row("m-plain").getByText(L.unsaved, { exact: true }).isVisible(), false, "back to Auto stages nothing");

      // from the detection: m-mixed on Anthropic alone
      await still(ed.getByRole("button", { name: L.only, exact: true }), "Ask on one API");
      assert.equal(await page.locator("#status").textContent(), L.said);
      assert.equal(await row("m-mixed").locator(".mapi .opt.on").textContent(), "Anthropic");
      assert(await row("m-mixed").getByText(L.unsaved, { exact: true }).isVisible());

      const border = await page.evaluate(() => [...document.querySelectorAll(".detect, .detect *, .mapi, .mapi *")].map((e) => getComputedStyle(e).borderLeftWidth).filter((b) => parseFloat(b) > 1));
      assert.deepEqual(border, [], "no left-border accent");

      // the Save carries the URL and the API picked
      await page.getByRole("button", { name: L.save, exact: true }).click();
      for (let i = 0; i < 60 && posts.length < 2; i++) await page.waitForTimeout(50);
      const save = posts.find((p) => p.path === "/api/provider/save");
      assert(save, "saved");
      assert.equal(save.body.anthropic, ROOT);
      assert.equal(save.body.chat, ROOT + "/v1");
      assert.deepEqual(save.body.modelPrefs, { "m-mixed": { api: "anthropic" } });

      const missing = await page.evaluate(() => [
        "Detect APIs", "Send the smallest request to each API (OpenAI chat completions, Responses, Anthropic messages, Gemini generateContent) at this URL, to see which answer",
        "model to try · empty picks one from the vendor's list", "Type the base URL first", "Asking each API…", "model {model}",
        "None answered: check the URL and the key, or type a model the vendor serves", "Use these", "Set the URLs of the APIs that answered; one not found there is cleared",
        "Taken · save to keep", "Ask {model} on {api} only", "Staged in Names & levels and made with the Save; Auto there gives it back", "{model} is asked on {api} once saved",
        "no URL to ask", "no model to try: type one the vendor serves", "no key is on for this endpoint",
        "The API {id} is asked on. Auto: as the vendor's list says, else each URL the provider has; pick one when the vendor serves it on that one only",
        "Its own name, every reasoning level it has, whether it sees images, the API it is asked on, the model it is the same as, and its list price",
      ].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      assert.deepEqual(errors, []);
    });
  }
}
