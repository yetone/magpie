// Run with Node's test runner and Playwright on the module path; see README.md.
// Command Code signed in to from a browser that can't reach magpie (xugui on
// Discord: magpie in Docker couldn't sign in to Command Code). Its Studio
// page posts the key to magpie's own 127.0.0.1 port, unseen, so there is no
// address to paste: while the sign-in waits, the box says to make an API key
// on Command Code's keys page, links that page, and takes the key in a
// hidden field that posts it once to the sign-in's callback route. A
// plugin's browser sign-in that can't come back (the community Command Code
// plugin's) offers the plugin's own API key way instead: the waiting sign-in
// is canceled and the key field asked. No click moves the page. English and
// Chinese, Chromium and WebKit; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const KEYS = "https://commandcode.ai/settings/keys";

const plugin = { id: "cmdplug", pid: "cmdplug", name: "CmdPlug", icon: "generic", spec: "opencode-cmdplug-auth", signedIn: false, models: 2,
  methods: [{ type: "oauth", label: "CmdPlug (browser)" }, { type: "api", label: "API key (cmdplug.test/keys)" }] };

function server(lang, asked) {
  let signing = { id: "s1", agent: "commandcode-plan", state: "waiting", url: "https://commandcode.ai/studio/auth/cli?state=x", pasteKey: true, keysURL: KEYS };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    const body = () => route.request().postDataJSON();
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [{ id: "openai", name: "OpenAI", icon: "openai", preset: "openai", models: [], agents: [], key: { set: true, masked: "sk-…ab12" } }], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [plugin] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/signin") { asked.push(["signin", body()]); return json(signing); }
    if (url.pathname === "/api/signin/s1/callback") {
      asked.push(["key", body()]);
      signing = { ...signing, state: "done", user: "pasted", using: true };
      return route.fulfill({ status: 204 });
    }
    if (url.pathname === "/api/signin/s1") return json(signing);
    if (url.pathname === "/api/plugin-signin/prompt") return json({ prompt: null, inputs: {} });
    if (url.pathname === "/api/plugin-signin") {
      const b = body();
      asked.push(["plugin-signin", b]);
      return json({ id: "p1", agent: "cmdplug", state: "waiting", url: "https://cmdplug.test/studio" });
    }
    if (url.pathname === "/api/signin/p1/cancel") { asked.push(["cancel", "p1"]); return json({}); }
    if (url.pathname === "/api/signin/p1") return json({ id: "p1", agent: "cmdplug", state: "waiting", url: "https://cmdplug.test/studio" });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const W = {
  en: {
    anyway: "Sign in anyway",
    say: "If the page can't reach magpie (it runs on a server or in Docker), make an API key on Command Code's keys page and paste it here.",
    keys: "Open the keys page", field: "API key", finish: "Finish sign-in",
    instead: "Use an API key instead", pluginKey: "API key (cmdplug.test/keys)",
  },
  zh: {
    anyway: "仍然登录",
    say: "如果页面连不上 magpie（magpie 运行在服务器或 Docker 中），请在 Command Code 的密钥页面创建一个 API 密钥，粘贴到这里。",
    keys: "打开密钥页面", field: "API 密钥", finish: "完成登录",
    instead: "改用 API 密钥", pluginKey: "API key (cmdplug.test/keys)",
  },
};

const scrolls = (page) => page.evaluate(() => [window.scrollX, window.scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop || e.scrollLeft).map((e) => e.scrollTop + "," + e.scrollLeft)].join("|"));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: Command Code takes a pasted API key`, async (t) => {
      const w = W[lang];
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 900, height: 800 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-signin-paste-key.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      const errors = [], asked = [];
      page.on("pageerror", (e) => errors.push(e.message));
      // web: a page opens in the browser, not through magpie
      await page.addInitScript(() => { window.opened = []; window.open = (u) => { window.opened.push(u); return null; }; });
      await page.route("**/*", server(lang, asked));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator("#addProvider").click();
      const sheet = page.locator("#addSheet");

      // the built-in: the key field, hidden, beside a link to the keys page
      await sheet.locator('.tile[data-pick="Command Code"]').click();
      let box = sheet.locator(".signing");
      await box.locator("button", { hasText: w.anyway }).click();
      await box.getByText(w.say).waitFor();
      const field = box.getByRole("textbox", { name: w.field }).or(box.getByLabel(w.field, { exact: true }));
      assert.equal(await field.getAttribute("type"), "password");
      const finish = box.getByRole("button", { name: w.finish });
      assert(await finish.isDisabled(), "nothing to finish with yet");
      const fit = await box.evaluate((b) => {
        const r = b.getBoundingClientRect(), f = b.querySelector(".callback-form").getBoundingClientRect();
        return f.left >= r.left - 0.5 && f.right <= r.right + 0.5;
      });
      assert(fit, "the key field fits its box");
      const stripes = await page.evaluate(() => [...document.querySelectorAll(".callback-form, .callback-form *")].filter((e) => {
        const s = getComputedStyle(e);
        return parseFloat(s.borderLeftWidth) > parseFloat(s.borderTopWidth) || (parseFloat(s.borderLeftWidth) && s.borderLeftColor !== s.borderTopColor);
      }).map((e) => e.className));
      assert.deepEqual(stripes, [], "no left-border accent");

      let before = await scrolls(page);
      await box.locator("button", { hasText: w.keys }).click();
      assert((await page.evaluate(() => window.opened)).includes(KEYS), "the keys page opened");
      await field.click();
      await field.fill("made-key");
      await finish.click();
      for (let i = 0; i < 40 && !asked.some(([k]) => k === "key"); i++) await page.waitForTimeout(50);
      assert.deepEqual(asked.filter(([k]) => k === "key"), [["key", { url: "made-key" }]], "posted once");
      await page.waitForFunction(() => !document.querySelector("#addSheet .callback-form"));
      assert.equal(await scrolls(page), before, "nothing scrolled");

      // a plugin's browser way: its key way offered while it waits
      if (!(await sheet.isVisible())) await page.locator("#addProvider").click();
      await sheet.locator('.tile[data-pick="CmdPlug"]').click();
      box = sheet.locator(".signing");
      await box.locator("button", { hasText: "CmdPlug (browser)" }).click();
      const instead = box.locator("button", { hasText: w.instead });
      await instead.waitFor();
      assert.deepEqual(asked.find(([k]) => k === "plugin-signin")[1], { provider: "cmdplug", method: 0, inputs: {} });
      before = await scrolls(page);
      await instead.click();
      const key = box.getByLabel(w.pluginKey);
      await key.waitFor();
      assert.equal(await key.getAttribute("type"), "password");
      assert(asked.some(([k]) => k === "cancel"), "the browser way canceled");
      assert.equal(await scrolls(page), before, "nothing scrolled");
      assert.deepEqual(errors, []);
    });
  }
}
