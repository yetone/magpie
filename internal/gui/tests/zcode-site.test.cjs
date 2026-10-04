// Run with Node's test runner and Playwright on the module path; see README.md.
// ZCode's sign-in asks where the account is (Z.ai or BigModel/智谱) and posts
// that site; a GLM team plan's resets are counted by window with no "Use" button.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const playwright = require("playwright");

const engines = (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"]);
const assets = path.resolve(__dirname, "../assets");

for (const engine of engines) for (const lang of ["en", "zh"]) {
  test(`zcode site chooser and team resets (${engine}, ${lang})`, async (t) => {
    const browser = await playwright[engine].launch();
    t.after(() => browser.close());
    const page = await browser.newPage({ viewport: { width: 420, height: 640 } });
    page.setDefaultTimeout(5000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    const posted = [];
    await page.route("**/*", async (route) => {
      const url = new URL(route.request().url());
      const json = (data) => route.fulfill({ json: data });
      if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"dark",web:true};` });
      if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
      if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "dark" } });
      if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: {} });
      if (url.pathname === "/api/groups") return json({ groups: [] });
      if (url.pathname === "/api/signin") {
        posted.push(route.request().postDataJSON());
        return json({ id: "zflow", agent: "zcode", state: "waiting", url: "http://vendor.test/oauth" });
      }
      if (url.pathname === "/api/signin/zflow") return new Promise(() => {}); // stays waiting
      if (url.pathname.startsWith("/api/")) return json({});
      const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
      const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
      return route.fulfill({ body: await fs.readFile(file), contentType });
    });
    await page.goto("http://magpie.test/");
    await page.waitForFunction((l) => state.settings.lang === l, lang);

    // starting ZCode's sign-in asks the site first, posting nothing
    await page.evaluate(() => {
      window.renderProviders = () => {
        const host = document.getElementById("zsite") || Object.assign(document.createElement("div"), { id: "zsite" });
        host.style.cssText = "position:fixed;inset:0 auto auto 0;width:100%;z-index:1000;";
        host.replaceChildren(signing ? renderSigning(subOf("zcode")) : "");
        document.body.append(host);
      };
      startSignIn("zcode", true);
    });
    await page.waitForFunction(() => signing?.state === "site");
    assert.equal(posted.length, 0);
    const title = lang === "zh" ? "你的" : "Where is your";
    assert.match(await page.locator("#zsite").innerText(), new RegExp(title));
    const sites = await page.locator("#zsite button[data-site]").evaluateAll((bs) => bs.map((b) => [b.dataset.site, b.textContent, b.title]));
    assert.deepEqual(sites.map((s) => s[0]), ["zai", "bigmodel"]);
    assert.equal(sites[1][1], lang === "zh" ? "智谱 BigModel" : "BigModel (智谱)");
    assert.equal(sites[1][2], "bigmodel.cn");

    // BigModel posts its site and keeps it for "Try again"
    await page.locator('#zsite button[data-site="bigmodel"]').click();
    await page.waitForFunction(() => signing?.state === "waiting");
    assert.deepEqual(posted, [{ agent: "zcode", site: "bigmodel" }]);
    assert.equal(await page.evaluate(() => signing.site), "bigmodel");

    // a team's resets are counted by window and not spent here
    const words = await page.evaluate(() => {
      const w = resetsWords({ byWindow: true, fiveHour: 2, weekly: 1, count: 3 });
      return [w.querySelector(".resets-n").textContent, w.title];
    });
    assert.equal(words[0], lang === "zh" ? "↺ 2 张 5 小时重置卡 · 1 张每周重置卡" : "↺ 2 five-hour resets · 1 weekly reset");
    assert.ok(words[1].length > 0);
    const one = await page.evaluate(() => resetsWords({ byWindow: true, fiveHour: 1, count: 1 }).querySelector(".resets-n").textContent);
    assert.equal(one, lang === "zh" ? "↺ 1 张 5 小时重置卡" : "↺ 1 five-hour reset");
    assert.deepEqual(errors, []);
  });
}
