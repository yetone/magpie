// Run with Node's test runner and Playwright on the module path; see README.md.
// Environment lines from a vendor's page, pasted into the Add sheet's search
// box, open a custom provider with the name from the URL's site, its API from
// the variable names (ANTHROPIC_* Anthropic's, OPENAI_* OpenAI's) and the key
// (WJaunita45604 on X). Pasted into a custom provider's key or Base URL they
// fill the form. A key that only stands for one is left out. The site's icon
// is asked for with the URL and name only: the key goes nowhere but the draft.
// English and Chinese, Chromium and WebKit, at a narrow window too.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const own = "sk-ant-api03-Qx7vT2mK9pLw4RzN8bYc";

const forms = {
  export: `export ANTHROPIC_BASE_URL="https://api.moonshot.cn/anthropic"\nexport ANTHROPIC_AUTH_TOKEN="${own}"\n`,
  tweet: `export ANTHROPIC_BASE_URL="example-xxx"\nexport ANTHROPIC_AUTH_TOKEN="sk-xxxxxxxxxxxxxxx"`,
  cmd: `set ANTHROPIC_BASE_URL=https://api.moonshot.cn/anthropic\r\nset ANTHROPIC_AUTH_TOKEN=${own}`,
  ps: `$env:ANTHROPIC_BASE_URL = "https://api.moonshot.cn/anthropic"\n$env:ANTHROPIC_AUTH_TOKEN = '${own}'`,
  json: JSON.stringify({ env: { ANTHROPIC_BASE_URL: "https://api.moonshot.cn/anthropic", ANTHROPIC_AUTH_TOKEN: own, ANTHROPIC_MODEL: "kimi-k3" } }),
  dotenv: `# my relay\nANTHROPIC_BASE_URL=https://api.moonshot.cn/anthropic # note\nANTHROPIC_API_KEY=${own}\n`,
};

function serve(lang, posts) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
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
    if (url.pathname.startsWith("/api/icons/favicon")) { posts.push({ path: url.pathname, body: route.request().postDataJSON() }); return json({ icon: "generic" }); }
    if (url.pathname.startsWith("/api/provider/")) { posts.push({ path: url.pathname, body: route.request().postDataJSON() }); return json(providers); }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    await route.fulfill({ body: await fs.readFile(file), contentType: { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)] });
  };
}

const words = {
  en: { add: "Add", note: "Filled in from the pasted text: Base URL (Anthropic compatible) · API key." },
  zh: { add: "添加", note: "已从粘贴的文本填入：API 地址 (Anthropic 兼容) · API 密钥。" },
};
const launch = (engine) => engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" });

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: envOf reads each form`, async (t) => {
    const browser = await launch(engine);
    t.after(() => browser.close());
    const page = await (await browser.newContext()).newPage();
    await page.route("**/*", serve("en", []));
    await page.goto("http://magpie.test/?view=providers");
    await page.waitForFunction(() => typeof envOf === "function");
    for (const [name, text] of Object.entries(forms)) {
      const c = await page.evaluate((s) => envOf(s), text);
      if (name === "tweet") {
        // the page's own stand-in, "example-xxx", is no address
        assert.deepEqual([c.bases, c.key, c.placeholder], [[], "", "sk-xxxxxxxxxxxxxxx"], name);
        continue;
      }
      assert.deepEqual(c.bases, [["anthropic", "https://api.moonshot.cn"]].map(([a]) => [a, "https://api.moonshot.cn/anthropic"]), name);
      assert.equal(c.key, own, name);
      assert.equal(c.name, "Moonshot", name);
      assert.equal(c.api, "anthropic", name);
      if (name === "json") assert.equal(c.model, "kimi-k3");
    }
    const both = await page.evaluate((s) => envOf(s), `export OPENAI_BASE_URL=https://openrouter.ai/api/v1\nexport OPENAI_API_KEY=sk-or-v1-abcdef0123\nexport ANTHROPIC_BASE_URL=https://openrouter.ai/api`);
    assert.deepEqual(both.bases, [["openai", "https://openrouter.ai/api/v1"], ["anthropic", "https://openrouter.ai/api"]]);
    assert.equal(both.api, "openai");
    assert.equal(both.name, "Openrouter");
    for (const s of ["", "sk-a\nsk-b", "https://api.moonshot.cn/v1", "PATH=/usr/bin\nHOME=/root", "export FOO=bar"]) {
      assert.equal(await page.evaluate((x) => envOf(x), s), null, JSON.stringify(s));
    }
    assert.deepEqual(await page.evaluate(() => ["https://open.bigmodel.cn/api/paas/v4", "https://api.example.co.uk/v1", "http://127.0.0.1:8888/v1"].map(siteName)), ["Bigmodel", "Example", "127.0.0.1"]);
  });

  for (const [lang, width] of [["en", 900], ["zh", 900], ["en", 440], ["zh", 440]]) {
    test(`${engine} ${lang} ${width}px: env lines pasted into the Add sheet open a provider`, async (t) => {
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
      const key = ed.locator(".pair input[type=password]").first();
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
      // text that isn't env lines is typed as before
      assert.equal(await paste(page.locator("#addSheet .find"), "moonshot"), false);
      assert.equal(await paste(page.locator("#addSheet .find"), forms.export), true);
      await ed.waitFor();
      assert.equal(await ed.locator(".base-url").inputValue(), "https://api.moonshot.cn/anthropic");
      assert.equal(await ed.locator(".base-apis .opt.on").getAttribute("data-api"), "anthropic");
      assert.equal(await key.inputValue(), own);
      assert.equal(await ed.locator("input[placeholder]").first().inputValue(), "Moonshot");
      for (let i = 0; i < 40 && !posts.some((p) => p.path === "/api/icons/favicon"); i++) await page.waitForTimeout(50);
      const ask = posts.find((p) => p.path === "/api/icons/favicon");
      assert.deepEqual(ask.body, { url: "https://api.moonshot.cn/anthropic", name: "Moonshot" });
      assert.ok(!JSON.stringify(posts).includes(own), "the key is sent only when saved");
      assert.deepEqual(await page.evaluate(() => ({ page: document.documentElement.scrollWidth <= innerWidth })), { page: true });
      await ed.locator(":scope > .bar").getByRole("button", { name: w.add, exact: true }).click();
      for (let i = 0; i < 60 && !posts.some((p) => p.path === "/api/provider/save"); i++) await page.waitForTimeout(50);
      const body = posts.find((p) => p.path === "/api/provider/save")?.body;
      assert.ok(body, "saved");
      assert.equal(body.anthropic, "https://api.moonshot.cn/anthropic");
      assert.equal(body.key, own);
      assert.equal(body.name, "Moonshot");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang} ${width}px: env lines pasted into a custom provider's key fill it`, async (t) => {
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
      const key = ed.locator(".pair input[type=password]").first();
      const note = ed.locator(".editor-error.note");
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
      await ed.locator(".base-url").waitFor();
      assert.equal(await paste(key, forms.export), true);
      assert.equal(await ed.locator(".base-url").inputValue(), "https://api.moonshot.cn/anthropic");
      assert.equal(await key.inputValue(), own);
      assert.equal((await note.textContent()).trim(), w.note);
      // a placeholder key is no key
      await key.fill("");
      assert.equal(await paste(key, forms.tweet), true);
      assert.equal(await key.inputValue(), "");
      assert.match(await note.textContent(), /sk-xxxxxxxxxxxxxxx/);
      assert.deepEqual(errors, []);
    });
  }
}
