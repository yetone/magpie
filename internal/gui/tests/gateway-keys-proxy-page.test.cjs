// Run with Node's test runner and Playwright on the module path; see README.md.
// The Gateway page's keys list, read through a proxy that answers with its
// own web page (Cloudflare's 524 when the read outlasted its 100 seconds):
// the section showed "<!DOCTYPE html> <!--[if lt IE 7]> <html class=…" as
// text (aiandcloud on X). It says a proxy answered, by the page's title,
// in every language, in Chromium and WebKit; a 200 login page is the same.
const assert = require("node:assert/strict");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture } = require("./fixtures/caller-keys.cjs");
const { strings } = require("./strings.cjs");

const { I18N } = strings();
const tr = (lang, s, v = {}) => (lang === "en" ? s : I18N[lang]?.[s] ?? s).replace(/\{(\w+)\}/g, (_, k) => v[k]);

const page524 = (title) => `<!DOCTYPE html>
<!--[if lt IE 7]> <html class="no-js ie6 oldie" lang="en-US"> <![endif]-->
<!--[if gt IE 8]><!--> <html class="no-js" lang="en-US"> <!--<![endif]-->
<head><title>${title}</title><meta charset="UTF-8" /></head>
<body><div id="cf-wrapper"><h1>A timeout occurred</h1></div></body></html>`;

const CASES = [
  { status: 524, title: "magpie.example.com | 524: A timeout occurred", key: "A proxy in front of magpie stopped waiting; magpie may still be finishing, check again in a minute ({what})" },
  { status: 502, title: "magpie.example.com | 502: Bad gateway", key: "A proxy in front of magpie answered, not magpie ({what})" },
  { status: 200, title: "Sign in", key: "A proxy in front of magpie answered, not magpie ({what})" },
];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: the keys list read through a proxy's error page says so, in words`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      for (const c of CASES) {
        const page = await (await browser.newContext({ viewport: { width: 420, height: 800 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(6000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        const base = fixture(lang, "light", [], { lan: true, web: true });
        await page.route("**/*", (route) => {
          if (new URL(route.request().url()).pathname === "/api/caller-keys") {
            return route.fulfill({ status: c.status, contentType: "text/html; charset=UTF-8", body: page524(c.title) });
          }
          return base(route);
        });
        await page.goto("http://magpie.test/?view=gateway");
        const none = page.locator("#gatewayKeys .gw-keys .none");
        await none.waitFor();
        const said = (await none.textContent()).trim();
        assert.equal(said, tr(lang, c.key, { what: c.title }), `${c.status}`);
        assert.doesNotMatch(await page.locator("#gatewayKeys").textContent(), /<!DOCTYPE|<html|<head/i);
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1), "fits");
        assert.deepEqual(errors, []);
        await page.context().close();
      }
    });
  }
}

// the same page inside magpie's own JSON error, as the server says it when
// a request it made to a remote magpie or a vendor came back as a proxy's
// page: the toast of a key action says the title, not the markup
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: a web page inside magpie's error is said by its title`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 420, height: 800 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(6000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      const base = fixture(lang, "light", [], { lan: true, web: true });
      const title = "magpie.example.com | 524: A timeout occurred";
      await page.route("**/*", (route) => {
        if (new URL(route.request().url()).pathname === "/api/caller-keys" && route.request().method() === "GET") {
          return route.fulfill({ status: 400, json: { error: "remote magpie answered 524: " + title, said: "A proxy in front of magpie answered, not magpie ({what})", what: "remote magpie answered 524: " + title } });
        }
        return base(route);
      });
      await page.goto("http://magpie.test/?view=gateway");
      const none = page.locator("#gatewayKeys .gw-keys .none");
      await none.waitFor();
      const said = (await none.textContent()).trim();
      assert.equal(said, tr(lang, "A proxy in front of magpie answered, not magpie ({what})", { what: "remote magpie answered 524: " + title }));
      assert.doesNotMatch(said, /<!DOCTYPE|<html|<head/i);
      assert.deepEqual(errors, []);
    });
  }
}
