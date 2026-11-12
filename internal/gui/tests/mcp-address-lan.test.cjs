// Run with Node's test runner and Playwright on the module path; see README.md.
// xzhih on X: magpie as a server on the local network, and Settings › Models'
// MCP server addresses (Image recognition, Image generation, Web search)
// still said 127.0.0.1. While magpie is shared, each now has the gateway's
// This computer / Local network switch beside it; the pick is kept, a page
// other machines opened starts at the network address, and with sharing off
// there is no switch. Chromium and WebKit, every language; no backend.
const assert = require("node:assert/strict");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture } = require("./fixtures/caller-keys.cjs");

const LAN = "http://192.168.1.10:3999";
const ROWS = { visionMcpRow: "/mcp/magpie/vision", imageMcpRow: "/mcp/magpie/image", searchMcpRow: "/mcp/magpie/web-search" };
const LANGS = ["en", "zh", "zh-TW", "ja", "de"];

async function setup(t, engine, lang, width, options) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" })).newPage();
  page.setDefaultTimeout(6000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", fixture(lang, "light", [], options));
  const open = () => page.goto("http://magpie.test/?view=settings&tab=models");
  const code = (id) => page.locator(`#${id} code`);
  return { page, errors, open, code };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}: MCP server addresses switch to the network address`, async (t) => {
        const { page, errors, open, code } = await setup(t, engine, lang, width, { lan: true });
        await open();
        for (const [id, path] of Object.entries(ROWS)) {
          await page.locator(`#${id} .segs`).waitFor();
          assert.match(await code(id).textContent(), new RegExp("^http://127\\.0\\.0\\.1:\\d+" + path + "$"), id + " starts at this computer");
          assert.equal(await page.locator(`#${id} .segs [data-seg]`).count(), 2);
        }
        await page.locator("#visionMcpRow .segs [data-seg=lan]").click();
        for (const [id, path] of Object.entries(ROWS)) {
          await page.waitForFunction(([i, a]) => document.querySelector(`#${i} code`)?.textContent === a, [id, LAN + path]);
        }
        assert(await page.evaluate(() => document.scrollingElement.scrollWidth <= innerWidth + 1), "no sideways scroll");
        // kept across a reload
        await open();
        await page.locator("#searchMcpRow .segs").waitFor();
        assert.equal(await code("searchMcpRow").textContent(), LAN + ROWS.searchMcpRow);
        await page.locator("#searchMcpRow .segs [data-seg=this]").click();
        await page.waitForFunction(() => /127\.0\.0\.1/.test(document.querySelector("#searchMcpRow code")?.textContent));
        assert.deepEqual(errors, []);
      });
    }
  }
  test(`${engine}: a page other machines opened starts at the network address; sharing off has no switch`, async (t) => {
    let h = await setup(t, engine, "en", 1100, { lan: true, web: true });
    await h.open();
    await h.page.waitForFunction((a) => document.querySelector("#imageMcpRow code")?.textContent === a, LAN + ROWS.imageMcpRow);
    assert.equal(await h.page.locator("#imageMcpRow .segs").count(), 1);
    assert.deepEqual(h.errors, []);
    h = await setup(t, engine, "en", 1100, { lan: false });
    await h.open();
    await h.page.locator("#visionMcpRow code").waitFor();
    assert.match(await h.code("visionMcpRow").textContent(), /127\.0\.0\.1/);
    assert.equal(await h.page.locator("#visionMcpRow .segs, #searchMcpRow .segs").count(), 0);
  });
}
