// Run with Node's test runner and Playwright on the module path; see README.md.
// A subscription signed in from the Add sheet (#509, Sun1090: 登录这俩个之后，
// 供应商显示空白 — WorkBuddy AI and Qoder CN): the sign-in done opens the
// account's editor and puts the sheet away at once, not rolled up by
// closeAddSheet. A sign-in that finishes within moments of the click in the
// sheet (Qoder CN's "Sign in anyway", an app already signed in) left the
// page holding that click where the sheet had been, with the room made for it
// kept under a list that now ended far above: closed, the editor uncovered
// an empty page. The sheet put away by a redraw now takes its hold and its
// room with it, as Close does. No backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const keyed = (i) => ({
  id: "k" + i, name: "Key " + i, icon: "generic", host: "k" + i + ".example.com", chat: "https://k" + i + ".example.com/v1", responses: "", anthropic: "",
  models: [{ id: "m1", name: "", on: true }], agents: [], fallback: [], headers: {}, keyList: [], key: { set: true, masked: "sk-…k" + i },
});
const qoder = {
  id: "qoder-cn", name: "Qoder CN", icon: "qoder", chat: "", responses: "", anthropic: "",
  models: [{ id: "auto", name: "Auto", on: true }], agents: [], fallback: [], headers: {}, keyList: [], key: { set: false, masked: "" },
  account: { agent: "qoder-cn", agentName: "Qoder CN", agentIcon: "qoder", user: "Bob", logins: [{ user: "Bob", active: true, on: true }] },
};

function serve(lang) {
  const list = Array.from({ length: 10 }, (_, i) => keyed(i));
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"dark",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "dark" } });
    if (url.pathname === "/api/providers") return json({ providers: list, presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/signin") return json({ id: "s1", agent: "qoder-cn", state: "waiting", url: "https://example.com/login" });
    // done at the first look: the sign-in comes in moments after the click
    if (url.pathname === "/api/signin/s1") {
      if (!list.includes(qoder)) list.push(qoder);
      return json({ id: "s1", agent: "qoder-cn", state: "done", user: "Bob" });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a sign-in done from the Add sheet leaves no blank page", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async () => {
        const page = await (await browser.newContext({ viewport: { width: 1180, height: 728 } })).newPage();
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang));
        // the vendor's sign-in page, opened in a new window, gets an empty page:
        // the real one is a page the test can't control (ChatGPT's Cloudflare
        // challenge hung clicks on this page in Chromium, #1307)
        await page.context().route((url) => url.hostname !== "magpie.test", (route) => route.fulfill({ contentType: "text/html", body: "" }));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator("#providers .row").nth(9).waitFor();
        await page.locator("#addProvider").click();
        await page.waitForTimeout(900);
        await page.locator("#addSheet .tiles button", { hasText: "Qoder CN" }).first().click();
        // its risk said, the sign-in asked for
        await page.locator("#addSheet .signing button.primary").first().click();
        // done: the account's editor opens over the list, the sheet put away
        await page.locator("#modal:not([hidden])").waitFor();
        await page.waitForTimeout(600);
        assert(await page.locator("#addSheet").evaluate((s) => s.hidden), "the sheet must be put away");
        await page.keyboard.press("Escape");
        await page.locator("#modal").waitFor({ state: "hidden" });
        await page.waitForTimeout(600);

        const view = page.locator("#view-providers");
        assert.equal(await page.locator("#view-providers > .view-room").count(), 0, "no room may be left under the list");
        // the list, its new account and Add provider are on the page, which
        // ends where they do: no empty page under them
        const at = await view.evaluate((v) => {
          const b = v.getBoundingClientRect(), add = v.querySelector("#addProvider").getBoundingClientRect();
          const row = v.querySelector('#providers .row[data-id="qoder-cn"]').getBoundingClientRect();
          return { gap: b.bottom - add.bottom, row: row.top - b.top, rowBottom: row.bottom - b.top, height: b.height };
        });
        assert(at.row >= 0 && at.rowBottom <= at.height, `the new account's row must be in sight (${Math.round(at.row)}..${Math.round(at.rowBottom)} of ${Math.round(at.height)})`);
        assert(at.gap < 150, `the page left ${Math.round(at.gap)}px empty under Add provider`);
        await page.context().close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
