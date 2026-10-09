// Run with Node's test runner and Playwright on the module path; see README.md.
// A ChatGPT sign-in finished from its address (Jorben on Discord: magpie in
// Docker on a cloud server, its web UI in his browser; ChatGPT sent the
// browser back to http://localhost:1455/auth/callback?code=…, his own
// machine, and the sign-in never finished). While the sign-in waits, the box
// says what to do with a page that won't load, and a Callback URL field takes
// its address: Finish sign-in posts it to the sign-in's callback route once,
// and the account is signed in. No click moves the page. English and Chinese,
// Chromium and WebKit; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const LINK = "https://auth.openai.com/oauth/authorize?client_id=app&redirect_uri=http%3A%2F%2Flocalhost%3A1455%2Fauth%2Fcallback&state=st1";
const BACK = "http://localhost:1455/auth/callback?code=ac_fake&scope=openid&state=st1";

function server(lang, posted) {
  let signing = { id: "cx1", agent: "codex", state: "waiting", url: LINK, pasteCallback: true };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [{ id: "openai", name: "OpenAI", icon: "openai", preset: "openai", models: [], agents: [], key: { set: true, masked: "sk-…ab12" } }], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/signin/cx1/callback") {
      posted.push(route.request().postDataJSON());
      signing = { ...signing, state: "done", user: "cx@example.com", using: true };
      return route.fulfill({ status: 204 });
    }
    if (url.pathname === "/api/signin" || url.pathname === "/api/signin/cx1") return json(signing);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const W = {
  en: {
    say: "If the page the browser ends on won't load (magpie runs on a server or in Docker), copy its whole address and paste it here.",
    field: "Callback URL", finish: "Finish sign-in",
  },
  zh: {
    say: "若浏览器最终停在打不开的页面（magpie 运行在服务器或 Docker 中），请复制其完整地址粘贴到这里。",
    field: "回调 URL", finish: "完成登录",
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: a ChatGPT sign-in takes its pasted address`, async (t) => {
      const w = W[lang];
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 900, height: 800 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-signin-paste-codex.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      const errors = [], posted = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, posted));
      // the vendor's sign-in page, opened in a new window, gets an empty page:
      // the real one is a page the test can't control (ChatGPT's Cloudflare
      // challenge hung clicks on this page in Chromium, #1307)
      await page.context().route((url) => url.hostname !== "magpie.test", (route) => route.fulfill({ contentType: "text/html", body: "" }));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator("#addProvider").click();
      const sheet = page.locator("#addSheet");
      await sheet.locator('.tile[data-pick="ChatGPT"]').click();
      const box = sheet.locator(".signing");
      await box.getByText(w.say).waitFor();
      const field = box.getByRole("textbox", { name: w.field });
      const finish = box.getByRole("button", { name: w.finish });
      assert(await field.isVisible());
      assert(await finish.isDisabled(), "nothing to finish with yet");

      // the field and its button inside the box, with no left-border accent
      const fit = await box.evaluate((b) => {
        const r = b.getBoundingClientRect(), f = b.querySelector(".callback-form").getBoundingClientRect();
        return f.left >= r.left - 0.5 && f.right <= r.right + 0.5;
      });
      assert(fit, "the callback field fits its box");
      const stripes = await page.evaluate(() => [...document.querySelectorAll(".callback-form, .callback-form *")].filter((e) => {
        const s = getComputedStyle(e);
        return parseFloat(s.borderLeftWidth) > parseFloat(s.borderTopWidth) || (parseFloat(s.borderLeftWidth) && s.borderLeftColor !== s.borderTopColor);
      }).map((e) => e.className));
      assert.deepEqual(stripes, [], "no left-border accent");

      const scrolls = () => page.evaluate(() => [window.scrollY, ...[...document.querySelectorAll("#addSheet, #addSheet *")].filter((e) => e.scrollTop).map((e) => e.scrollTop)]);
      const before = await scrolls();
      await field.click();
      await field.fill(BACK);
      await finish.click();
      for (let i = 0; i < 40 && !posted.length; i++) await page.waitForTimeout(50);
      assert.deepEqual(posted, [{ url: BACK }]);
      // the sign-in over: the box gone, the account named
      await page.waitForFunction(() => document.querySelector("#status").textContent.includes("cx@example.com") && !document.querySelector("#addSheet .callback-form"));
      assert.equal(posted.length, 1, "posted once");
      assert.deepEqual(await scrolls(), before, "nothing scrolled");
      assert.deepEqual(errors, []);
    });
  }
}
