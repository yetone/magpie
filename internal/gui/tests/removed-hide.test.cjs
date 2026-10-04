// Run with Node's test runner and Playwright on the module path; see README.md.
// #116 (marsxxl, v0.1.768) gave a removed row of Add a provider's "Removed
// from magpie — still signed in" a "Don't show here" in its menu, behind a
// "Show N hidden" link. #694 (mintonight) found that pointless (掩耳盗铃):
// hidden, the account was still signed in; what puts it away is Sign out.
// The row now has just Add it back and Sign out… side by side, no menu and
// no hiding: one hidden before is listed with the rest, with the same two
// buttons, and no "Show N hidden" link is left. The reminder line keeps
// its "Don't remind me". English and Chinese; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const codex = { id: "codex", name: "Codex", icon: "codex", models: [], agents: [], key: {}, account: { agent: "codex", agentName: "Codex", agentIcon: "codex", user: "a@b", logins: [{ user: "a@b", active: true, on: true }] } };

function server(lang, asked, st) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    const state = () => ({
      providers: [codex], presets: [], plugins: [], gateway: { running: true, window: true },
      excluded: [{ agent: "gemini", provider: "gemini", why: "You removed it from magpie.", quiet: st.quiet, tucked: st.tucked, agentName: "Gemini CLI", agentIcon: "gemini" }],
    });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(state());
    if (url.pathname.startsWith("/api/provider/")) {
      const a = url.pathname.slice("/api/provider/".length);
      asked.push([a, route.request().postDataJSON()]);
      if (a === "tuck") st.tucked = st.quiet = true;
      if (a === "untuck") st.tucked = false;
      if (a === "quiet") st.quiet = true;
      return json(state());
    }
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { back: "Add it back", out: "Sign out…", quiet: "Don't remind me", hide: /Don't show here|Show \d+ hidden|Hide the hidden/ },
  zh: { back: "加回来", out: "退出登录…", quiet: "不再提示", hide: /不在这里显示|显示 \d+ 个已隐藏|收起已隐藏/ },
};

const scrolls = (page) => page.evaluate(() => [scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => e.scrollTop)].join(","));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a removed row has two buttons and nothing hides it", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      for (const tucked of [false, true]) {
        await t.test(lang + (tucked ? ": one hidden before is listed with the rest" : ": Add it back, Sign out…, nothing else"), async () => {
          const w = L[lang];
          const page = await (await browser.newContext({ viewport: { width: 900, height: 520 } })).newPage();
          page.setDefaultTimeout(5000);
          const errors = [], asked = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", server(lang, asked, { quiet: true, tucked }));
          await page.goto("http://magpie.test/?view=providers");
          await page.locator("#addProvider").click();
          const sheet = page.locator("#addSheet");
          const row = sheet.locator(`.tile[data-pick="removed:gemini"]`);
          await row.waitFor();
          await sheet.evaluate((el) => Promise.all(el.getAnimations().map((a) => a.finished)));
          assert.equal(await sheet.locator(".kind-more").count(), 0, "no Show N hidden link");
          assert.doesNotMatch(await sheet.innerText(), w.hide, "nothing offers to hide it");
          assert.deepEqual((await row.locator("button").allInnerTexts()).map((s) => s.trim()), [w.back, w.out]);
          assert.equal(await page.locator("select").count(), 0);

          // a click on the row itself opens nothing and sends nothing
          const y = await scrolls(page);
          await row.locator(".n").click();
          await new Promise((r) => setTimeout(r, 150));
          assert.equal(await page.locator(".proto-menu").count(), 0, "a menu opened");
          assert.deepEqual(asked, []);
          assert.equal(await scrolls(page), y, "the click moved the page");
          if (process.env.ARTIFACT_DIR) await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `removed-row-${engine}-${lang}${tucked ? "-tucked" : ""}.png`) });

          // Add it back, in one click
          await row.locator("button", { hasText: new RegExp("^" + w.back + "$") }).click();
          await new Promise((r) => setTimeout(r, 200));
          assert.deepEqual(asked, [["show", { id: "gemini" }]]);
          assert.equal(await page.locator(".proto-menu").count(), 0);
          assert.deepEqual(errors, []);
        });
      }

      await t.test(lang + ": the reminder line keeps Don't remind me", async () => {
        const w = L[lang];
        const page = await (await browser.newContext({ viewport: { width: 900, height: 700 } })).newPage();
        page.setDefaultTimeout(5000);
        const asked = [];
        await page.route("**/*", server(lang, asked, { quiet: false, tucked: false }));
        await page.goto("http://magpie.test/?view=providers");
        const line = page.locator("#excluded .excluded");
        await line.waitFor();
        await line.locator("button.link", { hasText: new RegExp("^" + w.quiet + "$") }).click();
        await page.waitForFunction(() => !document.querySelector("#excluded .excluded"));
        assert.deepEqual(asked, [["quiet", { id: "gemini" }]]);
      });
    }
  });
}
