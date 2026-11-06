// Run with Node's test runner and Playwright on the module path; see README.md.
// An update's download never sits on a still percent (esir_tech on Discord:
// "17%" for minutes). The header's Update pill and Settings' Version row say
// how fast the bytes come, "Reconnecting…" while a broken download waits to
// try again, and then each step after the bytes (verifying, unpacking,
// checking the signature) instead of a stale 100%. Every language, a wide
// and a narrow window: the pill's text fits, or its tooltip carries it where
// the tight header hides the word. No NaN, no scroll, no left border; no
// backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const words = {
  en: { half: "Downloading… 50%", retry: "Reconnecting…", verify: "Verifying…", unpack: "Unpacking…", signature: "Checking signature…", ready: "0.1.401 is downloaded" },
  zh: { half: "正在下载… 50%", retry: "正在重新连接…", verify: "正在校验…", unpack: "正在解压…", signature: "正在检查签名…", ready: "0.1.401 已下载" },
  "zh-TW": { half: "正在下載… 50%", retry: "正在重新連線…", verify: "正在驗證…", unpack: "正在解壓縮…", signature: "正在檢查簽章…", ready: "0.1.401 已下載" },
  ja: { half: "ダウンロード中… 50%", retry: "再接続中…", verify: "検証中…", unpack: "展開中…", signature: "署名を確認中…", ready: "0.1.401 をダウンロードしました" },
  de: { half: "Wird heruntergeladen… 50 %", retry: "Verbindung wird neu aufgebaut…", verify: "Wird geprüft…", unpack: "Wird entpackt…", signature: "Signatur wird geprüft…", ready: "0.1.401 ist heruntergeladen" },
};

function settingsPayload(lang) {
  return {
    theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd",
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
  };
}

function server(lang, ctl) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: settingsPayload(lang), fx: settingsPayload(lang).fx });
    if (url.pathname === "/api/settings") return json(settingsPayload(lang));
    if (url.pathname === "/api/update/install" || url.pathname === "/api/update") return json({ ...ctl.update });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    if (!/^\/[\w./-]*$/.test(url.pathname) || url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const at = (extra) => ({ state: "downloading", current: "0.1.400", latest: "0.1.401", total: 30_000_000, ...extra });
const scrolls = (page) => page.evaluate(() => [window.scrollY, document.scrollingElement.scrollTop, ...[...document.querySelectorAll(".view")].map((v) => v.scrollTop)].join(","));

async function open(browser, lang, ctl, width, url) {
  const errors = [];
  const page = await (await browser.newContext({ viewport: { width, height: 640 }, reducedMotion: "reduce" })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  page.on("pageerror", (e) => { if (!/^ResizeObserver loop completed with undelivered notifications/.test(e.message)) errors.push(e.message); });
  await page.route("**/*", server(lang, ctl));
  await page.goto(url);
  return { page, errors };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": an update's download says its speed, then each step after it", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(words)) {
      const w = words[lang];
      for (const width of [900, 420]) {
        await t.test(`${lang} at ${width}px: the pill`, async () => {
          const ctl = { update: { state: "error", retry: true, current: "0.1.400", latest: "0.1.401", error: "timed out" } };
          const { page, errors } = await open(browser, lang, ctl, width, "http://magpie.test/");
          const pill = page.locator("#update"), label = pill.locator("span").first();
          await pill.waitFor({ state: "visible" });
          const seen = [];
          await page.exposeFunction("__seen", (s) => seen.push(s));
          await label.evaluate((l) => new MutationObserver(() => window.__seen(l.textContent)).observe(l, { childList: true, characterData: true, subtree: true }));

          ctl.update = at({ done: 15_000_000, step: "download", speed: 1_500_000 });
          const before = await scrolls(page);
          await pill.click();
          assert.equal(await scrolls(page), before, "the click must not scroll the page");
          const says = async (want) => {
            await page.waitForFunction((s) => document.querySelector("#update span").textContent.trim() === s, want);
            // the word fits the pill, or the tight header hides it and the tooltip carries it
            const fit = await pill.evaluate((b) => {
              const s = b.querySelector("span"), shown = getComputedStyle(s).display !== "none";
              return { shown, fits: s.scrollWidth <= s.clientWidth + 1 && b.scrollWidth <= b.clientWidth + 1, title: b.title };
            });
            assert.ok(fit.shown ? fit.fits : fit.title === want, `${want}: ${JSON.stringify(fit)}`);
          };
          await says(w.half + " · 1.5 MB/s");
          ctl.update = at({ done: 15_000_000, step: "download", speed: 42_000 });
          await says(w.half + " · 42 KB/s");
          ctl.update = at({ done: 15_000_000, step: "retry" });
          await says(w.retry);
          for (const step of ["verify", "unpack", "signature"]) {
            ctl.update = at({ done: 30_000_000, step });
            await says(w[step]);
          }
          assert.deepEqual(seen.filter((s) => /NaN|undefined|Infinity/.test(s)), [], "the pill said " + seen.join(" | "));
          assert.equal(await pill.evaluate((e) => getComputedStyle(e).borderLeftWidth === "0px" || getComputedStyle(e).borderLeftColor === getComputedStyle(e).borderTopColor), true);
          assert.deepEqual(errors, []);
          await page.context().close();
        });

        await t.test(`${lang} at ${width}px: Settings' Version row`, async () => {
          const ctl = { update: at({ done: 15_000_000, step: "download", speed: 1_500_000 }) };
          const { page, errors } = await open(browser, lang, ctl, width, "http://magpie.test/?view=settings&tab=about");
          const sub = page.locator("#about .row.pref .sub", { hasText: "0.1.401" }).first();
          await sub.waitFor();
          await page.waitForFunction(() => /50%|50 %/.test([...document.querySelectorAll("#about .row.pref .sub")].map((s) => s.textContent).join("|")));
          assert.match(await sub.textContent(), /15\.0.*30\.0.* · 1\.5 MB\/s$/);
          ctl.update = at({ done: 15_000_000, step: "retry" });
          await sub.filter({ hasText: w.retry }).waitFor();
          assert.ok((await sub.textContent()).endsWith(" · " + w.retry));
          for (const step of ["verify", "unpack", "signature"]) {
            ctl.update = at({ done: 30_000_000, step });
            await sub.filter({ hasText: w[step] }).waitFor();
            const text = await sub.textContent();
            assert.ok(text.endsWith(" · " + w[step]), text);
            assert.ok(text.startsWith(w.ready), text);
            assert.doesNotMatch(text, /NaN|undefined|Infinity|100%/);
          }
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, "the page scrolls sideways");
          assert.deepEqual(errors, []);
          await page.context().close();
        });
      }
    }
  });
}
