// Run with Node's test runner and Playwright on the module path; see README.md.
// A Copilot account on GitHub Enterprise Cloud with data residency (#723):
// the device code's panel offers "Account on GHE.com?", which asks the
// enterprise's <name>.ghe.com and posts it as the sign-in's site; a host
// that isn't one can't be submitted, and github.com is one click back.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const playwright = require("playwright");

const engines = (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"]);
const assets = path.resolve(__dirname, "../assets");

for (const engine of engines) for (const lang of ["en", "zh"]) {
  test(`copilot GHE.com sign-in (${engine}, ${lang})`, async (t) => {
    const browser = await playwright[engine].launch();
    t.after(() => browser.close());
    const page = await browser.newPage({ viewport: { width: 420, height: 640 } });
    page.setDefaultTimeout(5000);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    const posted = [];
    const canceled = [];
    let n = 0;
    await page.route("**/*", async (route) => {
      const url = new URL(route.request().url());
      const json = (data) => route.fulfill({ json: data });
      if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"dark",web:true};` });
      if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
      if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "dark" } });
      if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: {} });
      if (url.pathname === "/api/groups") return json({ groups: [] });
      if (url.pathname === "/api/signin") {
        const body = route.request().postDataJSON();
        posted.push(body);
        const host = body.site || "github.com";
        return json({ id: "c" + ++n, agent: "copilot", state: "waiting", code: "ABCD-1234", url: `https://${host}/login/device` });
      }
      if (/^\/api\/signin\/c\d+\/cancel$/.test(url.pathname)) { canceled.push(url.pathname.split("/")[3]); return json({}); }
      if (/^\/api\/signin\/c\d+$/.test(url.pathname)) return new Promise(() => {}); // stays waiting
      if (url.pathname.startsWith("/api/")) return json({});
      const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
      const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
      return route.fulfill({ body: await fs.readFile(file), contentType });
    });
    await page.goto("http://magpie.test/");
    await page.waitForFunction((l) => state.settings.lang === l, lang);
    await page.evaluate(() => {
      providers = providers || { providers: [], presets: [], plugins: [] };
      window.renderProviders = () => {
        const host = document.getElementById("csign") || Object.assign(document.createElement("div"), { id: "csign" });
        host.style.cssText = "position:fixed;inset:0 auto auto 0;width:100%;z-index:1000;";
        host.replaceChildren(signing ? renderSigning(subOf("copilot")) : "");
        document.body.append(host);
      };
      startSignIn("copilot", true);
    });
    const box = page.locator("#csign");
    const noAccents = async () => {
      const bad = await box.evaluate((root) => [...root.querySelectorAll("*")].filter((e) => {
        const s = getComputedStyle(e);
        return parseFloat(s.borderLeftWidth) > 0 && s.borderLeftWidth !== s.borderRightWidth;
      }).map((e) => e.outerHTML.slice(0, 80)));
      assert.deepEqual(bad, []);
      assert.equal(await box.locator("select").count(), 0);
    };

    // github.com's sign-in, as before, with the way to an enterprise's
    await page.waitForFunction(() => signing?.state === "waiting");
    assert.deepEqual(posted, [{ agent: "copilot" }]);
    const toGHE = box.locator('button[data-ghe="ghe"]');
    assert.equal(await toGHE.textContent(), lang === "zh" ? "账号在 GHE.com 上？" : "Account on GHE.com?");
    assert.ok((await toGHE.getAttribute("title")).length > 0);
    await noAccents();

    // it asks the host, ending the github.com sign-in
    await toGHE.click();
    await page.waitForFunction(() => signing?.state === "ghe");
    await page.waitForFunction(() => document.querySelector("#csign .copilot-ghe input"));
    assert.deepEqual(canceled, ["c1"]);
    const text = await box.innerText();
    assert.match(text, lang === "zh" ? /你的企业在 GHE\.com 上的地址/ : /Your enterprise on GHE\.com/);
    assert.match(text, lang === "zh" ? /github\.com 上的账号不用填/ : /An account on github\.com doesn't need it/);
    const field = box.locator(".copilot-ghe input");
    const go = box.locator('.copilot-ghe button[type="submit"]');
    assert.equal(await field.getAttribute("placeholder"), "acme.ghe.com");
    assert.equal(await go.textContent(), lang === "zh" ? "登录" : "Sign in");
    assert.equal(await go.isDisabled(), true);
    for (const bad of ["evil.com", "acme.ghe.com.evil.com", "a.b.ghe.com", "acme.ghe.com:8443", "github.example.com"]) {
      await field.fill(bad);
      assert.equal(await go.isDisabled(), true, bad);
    }
    await noAccents();
    await field.fill("https://Acme.ghe.com/");
    assert.equal(await go.isDisabled(), false);
    await field.press("Enter");
    await page.waitForFunction(() => signing?.state === "waiting" && signing.site);
    assert.deepEqual(posted[1], { agent: "copilot", site: "acme.ghe.com" });
    assert.equal(await page.evaluate(() => signing.site), "acme.ghe.com");
    assert.match(await box.innerText(), /https:\/\/acme\.ghe\.com\/login\/device/);

    // and back to github.com
    const back = box.locator('button[data-ghe="github"]');
    assert.equal(await back.textContent(), lang === "zh" ? "改为在 github.com 登录" : "Sign in on github.com instead");
    assert.equal(await back.getAttribute("title"), lang === "zh" ? "正在 acme.ghe.com 登录" : "Signing in on acme.ghe.com");
    await back.click();
    await page.waitForFunction(() => signing?.state === "waiting" && !signing.site && signing.id === "c3");
    assert.deepEqual(posted[2], { agent: "copilot" });
    assert.deepEqual(canceled, ["c1", "c2"]);
    assert.deepEqual(errors, []);
  });
}
