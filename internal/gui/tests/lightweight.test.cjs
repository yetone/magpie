// Run with Node's test runner and Playwright on the module path; see README.md.
// somls on #580: GUI版可否增加轻量模式，关闭面板退出webview，减少资源占用.
// Settings → General has a Lightweight mode row, off by default: On and Off
// are saved as lightweight true and false, the click moving nothing; the
// page saved again elsewhere keeps it; a browser's page (web) has no window
// to let go and shows no such row. In English and Chinese, with the API faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const words = {
  en: { name: "Lightweight mode", sub: "A closed panel or window frees its page after a minute, about 100 MB each, and loads it anew when opened", on: "On", off: "Off" },
  zh: { name: "轻量模式", sub: "面板或窗口关闭约一分钟后释放其网页（每个约 100 MB），再次打开时重新加载", on: "开启", off: "关闭" },
};

function serve(lang, web, posted, st) {
  const settings = () => ({ lang, theme: "light", searchVendors: [], searchAPIs: [], ...st });
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data, status = 200) => r.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: settings() });
    if (url.pathname === "/api/settings") {
      if (r.request().method() === "POST") {
        const b = JSON.parse(r.request().postData());
        posted.push(b);
        Object.assign(st, b);
      }
      return json(settings());
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

async function open(engine, lang, web, posted, st, t) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  t.after(() => browser.close());
  // tall enough that Lightweight mode is over the window's foot: rows were added above it
  const page = await (await browser.newContext({ viewport: { width: 900, height: 1000 } })).newPage();
  page.setDefaultTimeout(5000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, web, posted, st));
  await page.goto("http://magpie.test/?view=settings&tab=general");
  await page.locator("#setPage-general .row.pref").first().waitFor();
  return { page, errors };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: lightweight mode is turned on and off in Settings → General`, async (t) => {
      const posted = [], st = {};
      const { page, errors } = await open(engine, lang, false, posted, st, t);
      const row = page.locator("#lightweightRow");
      await row.waitFor();
      assert.equal(await row.locator(".name").innerText(), w.name);
      assert.equal(await row.locator(".sub").innerText(), w.sub);
      const opt = (name) => row.locator("#lightweightSegs .opt", { hasText: name });
      assert.equal(await row.locator("#lightweightSegs .opt.on").innerText(), w.off, "off by default");
      const where = () => page.evaluate(() => [scrollX, scrollY, document.scrollingElement.scrollTop, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => e.scrollTop)]);
      const before = await where();
      const click = async (loc) => {
        // the last save's answer draws Settings again, every option replaced:
        // one measured before that is gone (null) by the time it is clicked
        await page.waitForFunction(() => prefsBusy === 0);
        const b = await loc.boundingBox();
        await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
      };
      await click(opt(w.on));
      for (let i = 0; i < 50 && !posted.length; i++) await page.waitForTimeout(50);
      assert.equal(posted.at(-1)?.lightweight, true, `posted ${JSON.stringify(posted)}`);
      assert.equal(st.lightweight, true);
      assert.equal(await row.locator("#lightweightSegs .opt.on").innerText(), w.on);
      // another setting saved keeps it on
      const n = posted.length;
      await click(page.locator("#dockSegs .opt").first()).catch(() => {});
      for (let i = 0; i < 20 && posted.length === n; i++) await page.waitForTimeout(50);
      if (posted.length > n) assert.equal(posted.at(-1).lightweight, true, "another setting's save turned it off");
      await click(opt(w.off));
      for (let i = 0; i < 50 && posted.at(-1)?.lightweight !== false; i++) await page.waitForTimeout(50);
      assert.equal(posted.at(-1).lightweight, false);
      assert.deepEqual(await where(), before, "the clicks moved the page");
      // the row has no coloured stripe down its left
      const left = await row.evaluate((e) => getComputedStyle(e).borderLeftWidth);
      assert(parseFloat(left) <= 1, `left border ${left}`);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: a browser's page has no lightweight mode`, async (t) => {
      const { page, errors } = await open(engine, lang, true, [], {}, t);
      assert.equal(await page.locator("#lightweightRow").isHidden(), true);
      assert.deepEqual(errors, []);
    });
  }
}
