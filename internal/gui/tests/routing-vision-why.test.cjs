// Run with Node's test runner and Playwright on the module path; see README.md.
// A DeepSeek chat in Codex had its image described by Codex's GPT, and
// the row said only "Image description" (#1287: codex的图片识别还是调用默认的
// 模型): the row's story says why the model was counted as unable to see —
// nothing magpie knows says it sees, or its list says text only — and
// where that and the describer are changed. An Image recognition model
// picked in Settings that magpie can't find any more is named, with the one
// that described in its place, not replaced in silence.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const day = [now.getFullYear(), now.getMonth() + 1, now.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const at = (i) => new Date(now.getTime() - (i + 1) * 60e3).toISOString();
const seat = (model, provider) => ({ id: provider, provider, name: provider === "codex" ? "Codex" : "DeepSeek", who: "a@b.c", kind: provider === "codex" ? "account" : "key", model });
const call = (id, i, agent, model, provider, extra) => ({
  id, seq: id, time: at(i), agent, model: provider + "/" + model, provider, ...extra,
  order: [seat(model, provider)], tries: [{ id: provider, model, start: at(i), done: true, status: 200, ms: 900 }],
  done: true, status: 200, ms: 900, tokens: 1200,
});
const routes = [
  call(202, 0, "magpie", "gpt-6.1-sol", "codex", { kind: "vision", for: { agent: "codex", model: "deepseek/deepseek-v4.1-flash", unknown: true } }),
  call(201, 1, "magpie", "gpt-6.1-sol", "codex", { kind: "vision", for: { agent: "codex", model: "deepseek/deepseek-v4-pro" } }),
  // the Image recognition model picked in Settings is gone: the one magpie
  // picks described in its place, and the row names the one picked
  call(200, 2, "magpie", "gpt-6.1-sol", "codex", { kind: "vision", for: { agent: "codex", model: "deepseek/deepseek-v4-pro", missing: "gone/qwen-vl-max" } }),
];

function serve(lang) {
  const state = {
    agents: [{ id: "codex", name: "Codex", path: "/test/config.toml", fields: [] }],
    clients: [{ id: "codex", name: "Codex", icon: "codex-color" }, { id: "magpie", name: "magpie", icon: "magpie" }],
    profiles: [], settings: { lang, theme: "light" },
  };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") {
      const d = url.searchParams.get("day");
      return json({ cut: false, days: [{ day, requests: routes.length }], routes: d ? routes : [] });
    }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    const body = await fs.readFile(file).catch(() => null);
    await (body ? route.fulfill({ body, contentType }) : route.fulfill({ status: 404, body: "" }));
  };
}

const want = {
  en: { tag: "Image description", unknown: [/nothing magpie knows says deepseek\/deepseek-v4\.1-flash can see images/, /tick “Accepts images” for it in its provider's models/, /Settings › Models › Image recognition picks the model that describes/, /codex\/gpt-6\.1-sol describe an image for Codex's/],
    listed: [/which its provider's list or its own setting says takes text only/, /Settings › Models › Image recognition/],
    missing: [/gone\/qwen-vl-max, the Image recognition model picked in Settings, isn't set up any more/, /codex\/gpt-6\.1-sol, its automatic choice/, /Pick another in Settings › Models › Image recognition/] },
  zh: { tag: "图片描述", unknown: [/magpie 所知的信息都没有说 deepseek\/deepseek-v4\.1-flash 能看图/, /勾选“支持图片输入”/, /设置 › 模型 › 识图模型/, /让 codex\/gpt-6\.1-sol 为 Codex/],
    listed: [/服务商的模型列表或它自己的设置说它只收文本/, /设置 › 模型 › 识图模型/],
    missing: [/设置里选的识图模型 gone\/qwen-vl-max 已不可用/, /自动选择的 codex\/gpt-6\.1-sol/, /设置 › 模型 › 识图模型 里另选一个/] },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: an image's description says why the model can't see, and where to change it`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const context = await browser.newContext({ viewport: { width: 1100, height: 760 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang));
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-vision-why.png`), fullPage: true });
        }
        await browser.close();
      });
      await page.goto("http://magpie.test/?view=routing");
      await page.locator(".rt-days .rt-day").nth(1).click();
      await page.locator(".rt-req").nth(routes.length - 1).waitFor();
      const story = async (i) => {
        await page.locator(".rt-req").nth(i).click();
        await page.waitForTimeout(400);
        return page.locator(".rt-steps li.kind");
      };
      let s = await story(0);
      assert.equal(await s.locator(".kind").textContent(), want[lang].tag);
      for (const re of want[lang].unknown) assert.match(await s.textContent(), re);
      s = await story(1);
      for (const re of want[lang].listed) assert.match(await s.textContent(), re);
      assert.doesNotMatch(await s.textContent(), /nothing magpie knows|所知的信息/);
      s = await story(2);
      assert.equal(await s.locator(".kind").textContent(), want[lang].tag);
      for (const re of want[lang].missing) assert.match(await s.textContent(), re);
      assert.deepEqual(errors, []);
    });
  }
}
