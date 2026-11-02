// Run with Node's test runner and Playwright on the module path; see README.md.
// Qoder CN (#349; 中国版 in Chinese, 小武 on Discord): accounts made with an Alibaba Cloud account or a phone
// number live on qoder.cn and can't sign in on qoder.com, so the Add sheet has
// a Qoder CN tile beside Qoder's, and Qoder's says it is the international
// site's (qoder.com). Each tile's title says which accounts it is for; Qoder
// CN's sign-in warns as Qoder's does, saying which accounts it takes, asks
// nothing of the backend until "Sign in anyway", then asks for qoder-cn. The
// click moves nothing. English and Chinese, Chromium and WebKit; the API is
// faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function serve(lang, asked) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [{ id: "openai", name: "OpenAI", icon: "openai", preset: "openai", models: [], agents: [], key: { set: true, masked: "sk-…ab12" } }], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/signin") {
      asked.push(route.request().postDataJSON());
      return json({ id: "s1", agent: "qoder-cn", state: "waiting", url: "https://qoder.cn/device/selectAccounts" });
    }
    if (url.pathname.startsWith("/api/signin/")) return json({ id: "s1", agent: "qoder-cn", state: "waiting" });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: {
    global: "Qoder", cn: "Qoder CN", globalTitle: /^Qoder \(international\) subscription · Pro\nFor accounts on qoder\.com, the international site\.$/,
    cnTitle: /^Qoder CN subscription · Pro\nFor accounts on qoder\.cn: signed in with an Alibaba Cloud account or a phone number\.$/,
    risk: "Qoder CN accounts can be suspended", hint: "For accounts on qoder.cn: signed in with an Alibaba Cloud account or a phone number.",
    note: /^Qoder has no public API for this/, anyway: "Sign in anyway",
  },
  zh: {
    global: "Qoder 国际版", cn: "Qoder 中国版", globalTitle: /^Qoder 国际版 \(qoder\.com\) 订阅 · Pro\n适用于 qoder\.com（国际站）的账号。$/,
    cnTitle: /^Qoder 中国版 \(qoder\.cn\) 订阅 · Pro\n适用于 qoder\.cn 的账号：阿里云账号 \/ 手机号登录。$/,
    risk: "Qoder 中国版 (qoder.cn) 账号可能被封禁", hint: "适用于 qoder.cn 的账号：阿里云账号 / 手机号登录。",
    note: /^Qoder 没有/, anyway: "仍然登录",
  },
};

// where everything that can scroll stands
const scrolls = (page) => page.evaluate(() => [window.scrollX, window.scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop || e.scrollLeft).map((e) => e.scrollTop + "," + e.scrollLeft)].join("|"));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = L[lang];
    test(`${engine} ${lang}: Qoder CN has its own tile, Qoder's is the international one`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 900, height: 800 }, reducedMotion: "reduce" })).newPage();
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-qoder-cn-tile.png`) });
        }
        await browser.close();
      });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], asked = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, asked));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator("#addProvider").click();
      const sheet = page.locator("#addSheet");
      const cn = sheet.locator(`.tile[data-pick="${w.cn}"]`);
      const global = sheet.locator(`.tile[data-pick="${w.global}"]`);
      await cn.waitFor();
      assert.equal(await cn.locator(".n").textContent(), w.cn);
      assert.equal(await global.locator(".n").textContent(), w.global);
      assert.match(await cn.getAttribute("title"), w.cnTitle);
      assert.match(await global.getAttribute("title"), w.globalTitle);

      const before = await scrolls(page);
      await cn.click();
      const box = sheet.locator(".signing");
      await box.waitFor();
      assert.equal(await scrolls(page), before, "the click moved nothing");
      assert.equal(await box.locator(".n").textContent(), w.risk);
      const lines = await box.locator(".s").allTextContents();
      assert.equal(lines[0], w.hint, "the warning says which accounts it takes");
      assert.match(lines[1], w.note);
      const edge = await box.evaluate((b) => { const s = getComputedStyle(b); return [s.borderLeftWidth, s.borderLeftColor, s.borderRightColor]; });
      assert.ok(edge[0] === "0px" || edge[1] === edge[2], "no left-border accent: " + edge);
      assert.equal(asked.length, 0, "nothing asked before it is said");

      await box.locator("button", { hasText: w.anyway }).click();
      for (let i = 0; i < 50 && !asked.length; i++) await page.waitForTimeout(50);
      assert.deepEqual(asked, [{ agent: "qoder-cn" }]);

      const missing = await page.evaluate(() => [
        "Qoder (international)", "Qoder CN", "For accounts on qoder.com, the international site.",
        "For accounts on qoder.cn: signed in with an Alibaba Cloud account or a phone number.",
      ].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, [], "every string has its Chinese");
      assert.deepEqual(errors, []);
    });
  }
}
