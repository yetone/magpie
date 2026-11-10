// Run with Node's test runner and Playwright on the module path; see README.md.
// A curl command from a vendor's docs, pasted into a custom provider's Base
// URL or API key, fills the form in (@mark_li21 on X: 能支持通过curl例子直接
// 导入provider不？): the base URL from the request's URL with its endpoint cut
// off, spelled as the API it names wants it, that API picked, the key from
// its auth header unless it only stands for one ($OPENAI_API_KEY, %KEY%,
// <token>, YOUR_API_KEY: then the key's box is left empty and focused), and
// the model of its JSON body added to the models. A note says what was
// taken. Text that isn't curl pastes as before.
//
// fixtures/curl holds the examples as the vendors' docs show them, copied
// from their pages on 2026-10-16: OpenAI's API reference and quickstart,
// Anthropic's Messages examples, DeepSeek's, Zhipu's and Moonshot's quick
// starts, OpenRouter's, Alibaba Bailian's Windows cmd one (^), SAP AI Core's
// PowerShell one (curl.exe, \" in single quotes) and Unsloth Studio's
// PowerShell one (` continuations, as its usage panel renders it).
// curlOf is checked on each; the editor in English and Chinese, Chromium
// and WebKit, at a narrow window too.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const fixtures = path.resolve(__dirname, "fixtures/curl");

// what curlOf reads from each: api, base, urlHole, key, placeholder, model
const want = {
  "openai-chat.sh": { api: "openai", base: "https://api.openai.com/v1", urlHole: "", key: "", placeholder: "$OPENAI_API_KEY", model: "" },
  "openai-responses.sh": { api: "responses", base: "https://api.openai.com/v1", urlHole: "", key: "", placeholder: "$OPENAI_API_KEY", model: "gpt-6-astra" },
  "anthropic.sh": { api: "anthropic", base: "https://api.anthropic.com", urlHole: "", key: "", placeholder: "$ANTHROPIC_API_KEY", model: "claude-opus-5-5" },
  "deepseek.sh": { api: "openai", base: "https://api.deepseek.com/v1", urlHole: "", key: "", placeholder: "${DEEPSEEK_API_KEY}", model: "deepseek-flash" },
  "zhipu.sh": { api: "openai", base: "https://open.bigmodel.cn/api/paas/v4", urlHole: "", key: "", placeholder: "YOUR_API_KEY", model: "glm-5.3" },
  "moonshot.sh": { api: "openai", base: "https://api.moonshot.cn/v1", urlHole: "", key: "", placeholder: "$MOONSHOT_API_KEY", model: "kimi-k3" },
  "openrouter.sh": { api: "openai", base: "https://openrouter.ai/api/v1", urlHole: "", key: "", placeholder: "$OPENROUTER_API_KEY", model: "~openai/gpt-sol-latest" },
  "bailian-cmd.bat": { api: "openai", base: "https://{WorkspaceId}.cn-beijing.maas.aliyuncs.com/compatible-mode/v1", urlHole: "{WorkspaceId}", key: "", placeholder: "%DASHSCOPE_API_KEY%", model: "qwen3.8-max" },
  "sap-ai-core.ps1": { api: "", base: "", urlHole: "", key: "", placeholder: "<token>", model: "meta--llama3.1-70b-instruct" },
  "unsloth-studio.ps1": { api: "openai", base: "http://127.0.0.1:8888/v1", urlHole: "", key: "", placeholder: "sk-unsloth-YOUR_KEY", model: "" },
};
// a user's own key typed into Anthropic's example
const ownKey = "sk-ant-api03-Qx7vT2mK9pLw4RzN8bYc";
const withKey = async () => (await fs.readFile(path.join(fixtures, "anthropic.sh"), "utf8")).replace("$ANTHROPIC_API_KEY", ownKey);

function serve(lang, posts) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  // one saved provider: with none the page shows its first-run card instead
  const plain = { id: "plain", name: "Plain", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", models: [{ id: "m", name: "m", on: true }], agents: [], fallback: [], headers: {}, key: { set: true, masked: "sk-…1234" }, keyList: [], ready: true };
  const providers = { providers: [plain], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/provider/")) {
      posts.push({ path: url.pathname, body: route.request().postDataJSON() });
      return json(providers);
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    await route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)] });
  };
}

const words = {
  en: {
    add: "Add",
    kimi: "Filled in from the curl command: Base URL (OpenAI compatible) · model kimi-k3. Its key, $MOONSHOT_API_KEY, only stands for one: paste your own API key.",
    claude: "Filled in from the curl command: Base URL (Anthropic compatible) · API key · model claude-opus-5-5.",
  },
  zh: {
    add: "添加",
    kimi: "已从 curl 命令填入：API 地址 (OpenAI 兼容) · 模型 kimi-k3。 其中的密钥 $MOONSHOT_API_KEY 只是占位符：请粘贴你自己的 API 密钥。",
    claude: "已从 curl 命令填入：API 地址 (Anthropic 兼容) · API 密钥 · 模型 claude-opus-5-5。",
  },
};

const launch = (engine) => engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" });

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: curlOf reads the vendors' examples as their docs show them`, async (t) => {
    const browser = await launch(engine);
    t.after(() => browser.close());
    const page = await (await browser.newContext()).newPage();
    await page.route("**/*", serve("en", []));
    await page.goto("http://magpie.test/?view=providers");
    await page.waitForFunction(() => typeof curlOf === "function");
    for (const [file, w] of Object.entries(want)) {
      const text = await fs.readFile(path.join(fixtures, file), "utf8");
      const got = await page.evaluate((s) => curlOf(s), text);
      const { url, ...rest } = got;
      assert.deepEqual(rest, w, file);
      // and as a Windows clipboard gives it, with CRLF line ends
      const crlf = await page.evaluate((s) => curlOf(s), text.replace(/\n/g, "\r\n"));
      assert.deepEqual({ ...crlf, url: undefined }, { ...w, url: undefined }, file + " (CRLF)");
    }
    const own = await page.evaluate((s) => curlOf(s), await withKey());
    assert.equal(own.key, ownKey, "a key of the user's own is taken");
    assert.equal(own.placeholder, "");
    // what isn't a curl command is no curl command
    for (const s of ["https://api.moonshot.cn/v1", "sk-a\nsk-b", "curly braces", "  ", "echo curl https://api.openai.com/v1/chat/completions"]) {
      assert.equal(await page.evaluate((x) => curlOf(x), s), null, JSON.stringify(s));
    }
  });

  for (const [lang, width] of [["en", 900], ["zh", 900], ["en", 440], ["zh", 440]]) {
    test(`${engine} ${lang} ${width}px: a curl command pasted into the editor fills it in`, async (t) => {
      const browser = await launch(engine);
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, posts));
      const w = words[lang];
      const ed = page.locator(".editor");
      const baseURL = ed.locator(".base-url");
      const key = ed.locator(".pair input[type=password]").first();
      const models = ed.locator(".add-models .detect-row input").first();
      const note = ed.locator(".editor-error.note");
      const on = () => ed.locator(".base-apis .opt.on").getAttribute("data-api");
      // a paste as the browser sends it; true when the editor took it
      const paste = (loc, text) => loc.evaluate((i, s) => {
        i.focus();
        const dt = new DataTransfer();
        dt.setData("text/plain", s);
        const e = new ClipboardEvent("paste", { clipboardData: dt, bubbles: true, cancelable: true });
        i.dispatchEvent(e);
        return e.defaultPrevented;
      }, text);

      await page.goto("http://magpie.test/?view=providers");
      await page.locator("#addProvider").click();
      await page.locator("#addSheet .row-head .custom").click();
      await baseURL.waitFor();
      await ed.locator("input[placeholder]").first().fill("Kimi");

      // Moonshot's example into the Base URL: its key is $MOONSHOT_API_KEY
      assert.equal(await paste(baseURL, await fs.readFile(path.join(fixtures, "moonshot.sh"), "utf8")), true);
      assert.equal(await baseURL.inputValue(), "https://api.moonshot.cn/v1");
      assert.equal(await on(), "openai");
      assert.equal(await key.inputValue(), "", "a placeholder is no key");
      assert.ok(await key.evaluate((i) => document.activeElement === i), "the key's box is where to paste one");
      assert.equal(await models.inputValue(), "kimi-k3");
      assert.equal((await note.textContent()).trim(), w.kimi);

      // Anthropic's, with a key of the user's own, into the key's box
      assert.equal(await paste(key, await withKey()), true);
      assert.equal(await baseURL.inputValue(), "https://api.anthropic.com");
      assert.equal(await on(), "anthropic");
      assert.equal(await key.inputValue(), ownKey);
      assert.equal(await models.inputValue(), "kimi-k3, claude-opus-5-5");
      assert.equal((await note.textContent()).trim(), w.claude);
      // the note fits the window, and nothing scrolls sideways
      const fit = await page.evaluate(() => {
        const r = document.querySelector(".editor .editor-error.note").getBoundingClientRect();
        return { right: r.right <= innerWidth + 0.5, page: document.documentElement.scrollWidth <= innerWidth, selects: document.querySelectorAll("select").length };
      });
      assert.deepEqual(fit, { right: true, page: true, selects: 0 });

      // text that isn't curl is pasted as before
      assert.equal(await paste(baseURL, "https://api.moonshot.cn/v1"), false);
      assert.equal(await paste(key, "sk-a\nsk-b"), true, "several keys are still counted");
      assert.equal(await key.inputValue(), "sk-a, sk-b");

      // put back, the user's key: and saved, both APIs and both models
      await paste(key, await withKey());
      await ed.locator(":scope > .bar").getByRole("button", { name: w.add, exact: true }).click();
      for (let i = 0; i < 60 && !posts.some((p) => p.path === "/api/provider/save"); i++) await page.waitForTimeout(50);
      const body = posts.find((p) => p.path === "/api/provider/save")?.body;
      assert.ok(body, "saved");
      assert.equal(body.chat, "https://api.moonshot.cn/v1");
      assert.equal(body.anthropic, "https://api.anthropic.com");
      assert.equal(body.key, ownKey);
      assert.deepEqual(body.models, ["kimi-k3", "claude-opus-5-5"]);
      assert.deepEqual(errors, []);
    });
  }
}
