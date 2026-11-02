// Run with Node's test runner and Playwright on the module path; see README.md.
// A GLM Coding Plan key Zhipu or Z.ai refuses says so on its card, in every
// language, naming the vendor and what to do, rather than "Allowance
// unavailable" (@xanahopper on X; zhipuRefused, planquota.go).
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function server(lang) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: "light", web: true })};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// the errors planWindows gives for a refused key, as built from Zhipu's
// real reply ({"code":1000,"msg":"身份验证失败。","success":false})
const errs = [
  ["Zhipu", "Zhipu didn't take this key (身份验证失败。): it has expired, was deleted, or is mistyped; paste a current one from https://open.bigmodel.cn/usercenter/proj-mgmt/apikeys in the provider's settings"],
  ["Z.ai", "Z.ai didn't take this key (401 Unauthorized: 令牌已过期或验证不正确): it has expired, was deleted, or is mistyped; paste a current one from https://z.ai/manage-apikey/apikey-list in the provider's settings"],
];
const key = "{vendor} didn't take this key — it expired, was deleted or is mistyped; paste a current one in the provider's settings";

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine}: a refused GLM key's card says the key was refused (${lang})`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 440, height: 760 } })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang));
      await page.goto("http://magpie.test/?view=providers");
      await page.waitForFunction(() => typeof quotaError === "function");
      const generic = await page.evaluate(() => t("Allowance unavailable — hover for why"));
      for (const [vendor, err] of errs) {
        const said = await page.evaluate((e) => quotaError(e), err);
        const want = await page.evaluate(([k, v]) => t(k, { vendor: v }), [key, vendor]);
        assert.equal(said, want, err);
        assert.notEqual(said, generic);
        assert.ok(said.startsWith(vendor), `${said} names ${vendor}`);
        if (lang !== "en") assert.notEqual(said, key.replace("{vendor}", vendor), `${lang} has its own words`);
      }
      // the vendor's words alone, from before, are still the generic line
      assert.equal(await page.evaluate(() => quotaError("身份验证失败。")), generic);
      assert.deepEqual(errors, []);
    });
  }
}
