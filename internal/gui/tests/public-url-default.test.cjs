const assert = require("node:assert/strict");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture } = require("./fixtures/caller-keys.cjs");
const { openPart } = require("./reader.cjs");

// #1551 (everpcpc): magpie web in Docker behind a reverse proxy, with
// MAGPIE_PUBLIC_URL=https://magpie.example.com. The page is opened from
// other machines, yet Gateway's Connect started at http://127.0.0.1:3425,
// and so did the copied snippets and the gateway's own address. With no
// address picked, Connect now starts at the public URL, labelled as such,
// the snippets with it, and About shows it beside the local address agents
// here use. An address the user picked, this computer's included, still
// wins after a reload. Served to other machines (magpie web), the
// gateway's address and Settings' MCP server show the public URL too; in
// the app they stay this computer's. Without MAGPIE_PUBLIC_URL nothing
// changes: Connect starts at this computer's. The sidebar's gateway line
// and About's Gateway URL follow the same rule (noahhrreion on Discord:
// magpie web in Docker with MAGPIE_PUBLIC_URL, and the line at the foot
// said the container's own address).
const PUBLIC = "https://magpie.example.com";
const LOCAL = "http://127.0.0.1:3999";
const LANGS = ["en", "zh", "zh-TW", "ja", "de"];
const words = {
  en: "Public address", zh: "对外地址", "zh-TW": "對外位址", ja: "公開アドレス", de: "Öffentliche Adresse",
};

async function setup(t, engine, lang, width, options) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
  await page.addInitScript(() => { try { localStorage.setItem("magpie.gwConnectFolded", "0"); } catch {} }); // Connect opened, as a user who reads it
  page.setDefaultTimeout(6000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", fixture(lang, "light", [], { lan: true, ...options }));
  const open = async () => {
    await page.goto("http://magpie.test/?view=gateway");
    await page.locator("#connectAddress").waitFor();
  };
  const shown = (sel) => page.locator(sel).textContent();
  const settingsTab = async (tab) => {
    if (!(await page.locator("#view-settings").isVisible())) await page.locator("#prefs").click();
    await openPart(page, tab);
  };
  const fits = async (where) => assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, where + " wider than the window");
  return { page, errors, open, shown, settingsTab, fits };
}

// the snippets carry the gateway key Connect picked for an address
// elsewhere; its read redraws Connect, which closes a menu open
const keyed = (page, base) => page.waitForFunction((u) => {
  const s = document.querySelector("#connect pre.snip")?.textContent || "";
  return s.includes(u) && s.includes("fixture-laptop");
}, base);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of LANGS) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}: Connect starts at MAGPIE_PUBLIC_URL until an address is picked`, async (t) => {
        const { page, errors, open, shown, settingsTab, fits } = await setup(t, engine, lang, width, { lanURLs: [PUBLIC], publicURL: PUBLIC });

        // nothing picked yet: the public URL, with a gateway key, in the
        // snippet too
        await open();
        assert.equal(await shown("#connectAddress"), PUBLIC + "/v1");
        await keyed(page, PUBLIC + "/v1");
        assert.doesNotMatch(await shown("#connect pre.snip"), /127\.0\.0\.1/);
        // in the app, the gateway's own address stays this computer's
        assert.equal(await shown("#gatewayLan .gw-url:first-child code"), LOCAL);
        // the sidebar's line, shared, names where other machines reach it
        // (coeo91), this computer's in its tooltip
        if (width >= 1100) {
          assert.match(await shown("#sideGw span"), /magpie\.example\.com/);
          assert.match(await page.locator("#sideGw").getAttribute("title"), /127\.0\.0\.1:3999/);
        }
        await page.locator("#connectAddress").click();
        assert.match(await page.locator(".proto-menu .pm-item", { hasText: "magpie.example.com" }).textContent(), new RegExp(words[lang]));
        await fits("Gateway");

        // the user's own pick, this computer, wins over the public URL
        await page.locator(".proto-menu .pm-item", { hasText: "127.0.0.1" }).click();
        await page.locator(".proto-menu").waitFor({ state: "detached" });
        assert.equal(await shown("#connectAddress"), LOCAL + "/v1");
        await open();
        assert.equal(await shown("#connectAddress"), LOCAL + "/v1");

        // About keeps the local address agents use, and shows the public one
        await settingsTab("about");
        await page.locator("#publicURLRow").waitFor();
        assert.match(await shown("#publicURLRow .who"), new RegExp(words[lang]));
        assert.equal(await shown("#publicURLRow code"), PUBLIC);
        assert.match(await shown("#about"), /127\.0\.0\.1:3999/);
        await fits("About");
        await settingsTab("models");
        assert.equal(await shown("#searchMcpRow code"), LOCAL + "/mcp/magpie/web-search");
        assert.deepEqual(errors, []);
      });
    }
  }
  for (const width of [1100, 420]) {
    test(`${engine} ${width}: magpie web shows MAGPIE_PUBLIC_URL as the gateway's address`, async (t) => {
      const { page, errors, open, shown, settingsTab } = await setup(t, engine, "en", width, { lanURLs: [PUBLIC], publicURL: PUBLIC, web: true });
      await open();
      assert.equal(await shown("#connectAddress"), PUBLIC + "/v1");
      await keyed(page, PUBLIC + "/v1");
      assert.equal(await shown("#gatewayLan .gw-url:first-child code"), PUBLIC);
      // the sidebar's line at its foot, there at this width, says it too
      if (width >= 1100) {
        assert.match(await shown("#sideGw span"), /magpie\.example\.com/);
        assert.doesNotMatch(await page.locator("#sideGw").getAttribute("title"), /127\.0\.0\.1/);
      }
      // and About's Gateway URL, with no second row saying the same
      await settingsTab("about");
      await page.locator("#about .row").first().waitFor();
      assert.match(await shown("#about"), /magpie\.example\.com/);
      assert.doesNotMatch(await shown("#about"), /127\.0\.0\.1/);
      assert.equal(await page.locator("#publicURLRow").count(), 0);
      await settingsTab("models");
      assert.equal(await shown("#searchMcpRow code"), PUBLIC + "/mcp/magpie/web-search");
      assert.deepEqual(errors, []);
    });
  }
  test(`${engine}: without MAGPIE_PUBLIC_URL Connect starts at this computer's address`, async (t) => {
    const { page, errors, open, shown, settingsTab } = await setup(t, engine, "en", 1100, { lanURLs: ["http://192.168.1.10:3999"], web: true });
    await open();
    assert.equal(await shown("#connectAddress"), LOCAL + "/v1");
    assert.equal(await shown("#gatewayLan .gw-url:first-child code"), LOCAL);
    await settingsTab("about");
    await page.locator("#about .row").first().waitFor();
    assert.equal(await page.locator("#publicURLRow").count(), 0);
    assert.deepEqual(errors, []);
  });
}
