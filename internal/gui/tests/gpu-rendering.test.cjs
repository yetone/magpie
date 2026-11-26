// Run with Node's test runner and Playwright on the module path; see README.md.
// ZDegress on X: on CachyOS, niri (Wayland) and Intel+NVIDIA, magpie set
// WEBKIT_DISABLE_DMABUF_RENDERER=1 and the window lagged; asked for a way to
// let the GPU draw. Linux's app has Settings → General → Advanced → GPU
// rendering (webkit_linux.go): it shows magpie's pick for this computer
// until chosen, says it takes effect at the next start, saves a choice
// equal to the pick as none (so the pick goes on applying), is left out of
// other settings' saves, and names the WEBKIT_* variables the user set
// instead of offering a choice they would override. Off Linux (no `gpu` in
// the settings) and in a browser's page there is no Advanced fold at all.
// Every language, both engines, with the API faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn, reach } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const words = {
  en: { name: "GPU rendering", sub: "Takes effect when magpie next starts", env: "Set by WEBKIT_DISABLE_COMPOSITING_MODE=0 in the environment; magpie leaves it as it is", on: "On", off: "Off" },
  zh: { name: "GPU 渲染", sub: "magpie 下次启动时生效", env: "已由环境变量 WEBKIT_DISABLE_COMPOSITING_MODE=0 设置，magpie 不会改动", on: "开启", off: "关闭" },
  "zh-TW": { name: "GPU 算繪", sub: "magpie 下次啟動時生效", env: "已由環境變數 WEBKIT_DISABLE_COMPOSITING_MODE=0 設定，magpie 不會更動", on: "開啟", off: "關閉" },
  ja: { name: "GPU レンダリング", sub: "magpie の次回起動時に反映されます", env: "環境変数 WEBKIT_DISABLE_COMPOSITING_MODE=0 で設定されています。magpie は変更しません", on: "オン", off: "オフ" },
  de: { name: "GPU-Rendering", sub: "Wirkt beim nächsten Start von magpie", env: "Durch WEBKIT_DISABLE_COMPOSITING_MODE=0 in der Umgebung gesetzt; magpie lässt es so", on: "An", off: "Aus" },
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
        // what settingsapi.Merge does: a save without the field keeps it
        Object.assign(st, b);
        if ("gpuRendering" in b && b.gpuRendering === null) delete st.gpuRendering;
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
  const page = await (await browser.newContext({ viewport: { width: 900, height: 900 } })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, web, posted, st));
  await watchSettingsReads(page);
  await page.goto("http://magpie.test/?view=settings&tab=general");
  await settingsDrawn(page);
  await page.locator("#setPage-general .row.pref").first().waitFor();
  return { page, errors };
}

// a click once the last save's redraw is done, the option in sight
async function press(page, loc) {
  await page.waitForFunction(() => prefsBusy === 0);
  await reach(page, loc);
  await loc.click();
}

async function saved(page, posted, n, want) {
  for (let i = 0; i < 60 && posted.length === n; i++) await page.waitForTimeout(50);
  assert.ok(posted.length > n, "nothing was saved");
  assert.ok("gpuRendering" in posted.at(-1), `saved without gpuRendering: ${JSON.stringify(posted.at(-1))}`);
  assert.equal(posted.at(-1).gpuRendering, want, `posted ${JSON.stringify(posted.at(-1))}`);
}

async function openMore(page) {
  const more = page.locator("#generalMore");
  await more.waitFor();
  await press(page, more.locator("summary"));
  await page.locator("#gpuRow").waitFor();
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    test(`${engine} ${lang}: GPU rendering follows magpie's pick until chosen, and takes effect at the next start`, async (t) => {
      // Wayland on AMD: magpie's pick is off
      const posted = [], st = { gpu: { auto: false, now: false } };
      const { page, errors } = await open(engine, lang, false, posted, st, t);
      await openMore(page);
      const row = page.locator("#gpuRow");
      assert.equal(await row.locator(".name").innerText(), w.name);
      assert.equal(await row.locator(".sub").innerText(), w.sub);
      const opt = (name) => row.locator("#gpuSegs .opt", { hasText: name });
      assert.equal(await row.locator("#gpuSegs .opt.on").innerText(), w.off, "magpie's pick while unchosen");
      // another setting's save leaves it alone (settingsapi keeps it)
      let n = posted.length;
      await press(page, page.locator("#lightweightSegs .opt").last()).catch(() => {});
      for (let i = 0; i < 20 && posted.length === n; i++) await page.waitForTimeout(50);
      if (posted.length > n) assert.equal("gpuRendering" in posted.at(-1), false, `another setting's save chose it: ${JSON.stringify(posted.at(-1))}`);
      n = posted.length;
      await press(page, opt(w.on));
      await saved(page, posted, n, true);
      assert.equal(await row.locator("#gpuSegs .opt.on").innerText(), w.on);
      assert.equal(await page.locator("#generalMore").evaluate((d) => d.open), true, "the fold closed on the redraw");
      // back to magpie's pick: saved as none, so a later pick still applies
      n = posted.length;
      await press(page, opt(w.off));
      await saved(page, posted, n, null);
      assert.equal(await row.locator("#gpuSegs .opt.on").innerText(), w.off);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: GPU rendering where magpie's pick is on`, async (t) => {
      // Wayland with the NVIDIA driver loaded, turned off by the user
      const posted = [], st = { gpu: { auto: true, now: false }, gpuRendering: false };
      const { page, errors } = await open(engine, lang, false, posted, st, t);
      await openMore(page);
      const row = page.locator("#gpuRow");
      assert.equal(await row.locator("#gpuSegs .opt.on").innerText(), w.off, "the user's choice over magpie's pick");
      const n = posted.length;
      await press(page, row.locator("#gpuSegs .opt", { hasText: w.on }));
      await saved(page, posted, n, null);
      assert.equal(await row.locator("#gpuSegs .opt.on").innerText(), w.on);
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: WEBKIT_* variables the user set are named, not overridden`, async (t) => {
      const posted = [], st = { gpu: { auto: false, now: true, env: ["WEBKIT_DISABLE_COMPOSITING_MODE=0"] } };
      const { page, errors } = await open(engine, lang, false, posted, st, t);
      await openMore(page);
      const row = page.locator("#gpuRow");
      assert.equal(await row.locator(".sub").innerText(), w.env);
      assert.equal(await row.locator("#gpuSegs .opt").count(), 0, "a choice the user's variable would override");
      assert.deepEqual(errors, []);
    });

    test(`${engine} ${lang}: no GPU rendering off Linux's app`, async (t) => {
      for (const web of [false, true]) {
        const st = web ? { gpu: { auto: false, now: false } } : {};
        const { page, errors } = await open(engine, lang, web, [], st, t);
        assert.equal(await page.locator("#generalMore").isHidden(), true, web ? "in a browser's page" : "without gpu");
        assert.equal(await page.locator("#gpuRow").isVisible(), false);
        assert.deepEqual(errors, []);
      }
    });
  }
}
