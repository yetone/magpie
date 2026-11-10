// Run with Node's test runner and Playwright on the module path; see README.md.
// Bark (EV_John_Liu on X): Settings › Usage has a Bark row, where a server
// and a device key are saved, and a daily summary beside it once a key is
// set. The page never has the key back: after it is saved only dots show,
// and the key is in no text of the page. Test pushes, Remove takes the key
// and the summary away. Every language, wide and narrow, both engines; the
// API is faked here, and nothing reaches the network.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn, reach } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const KEY = "AbCdEf123456secretkey";

function server(lang, posts) {
  const fixed = {
    theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd", dock: false, dockWindow: false, proxy: "",
    redact: true, redactPersonal: false, redactWords: [], codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "",
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "", version: "0.1.900", dir: "~/.config/magpie",
    gateway: "http://127.0.0.1:3425", proxyNow: "none", proxySource: "none", visionModels: [], imageGenModels: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false }, turnDoneAlert: 0, featuresOff: [],
  };
  let cur = fixed;
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false,featuresOff:[]};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: fixed.fx });
    if (url.pathname === "/api/settings/bark") {
      const body = req.postDataJSON();
      posts.push({ path: "bark", ...body });
      const next = { ...cur };
      if ("server" in body) next.barkServer = body.server;
      if ("key" in body) next.barkKeySet = !!body.key;
      if ("daily" in body) next.barkDaily = body.daily;
      if (!next.barkKeySet) { delete next.barkKeySet; delete next.barkDaily; }
      cur = next;
      return json(cur); // never the key
    }
    if (url.pathname === "/api/bark/test") { posts.push({ path: "test" }); return json({ ok: true }); }
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") { posts.push({ path: "settings", ...req.postDataJSON() }); cur = { ...cur, ...req.postDataJSON() }; }
      return json(cur);
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { name: "Bark", daily: "Bark daily summary", key: "Device key", test: "Test", save: "Save", remove: "Remove", off: "Off", on: "On", sent: "Sent to Bark" },
  zh: { name: "Bark", daily: "Bark 每日汇总", key: "设备密钥", test: "测试", save: "保存", remove: "移除", off: "关闭", on: "开启", sent: "已发送到 Bark" },
  "zh-TW": { name: "Bark", daily: "Bark 每日彙總", key: "裝置金鑰", test: "測試", save: "儲存", remove: "移除", off: "關閉", on: "開啟", sent: "已傳送到 Bark" },
  ja: { name: "Bark", daily: "Bark の毎日のまとめ", key: "デバイスキー", test: "テスト", save: "保存", remove: "削除", off: "オフ", on: "オン", sent: "Bark に送信しました" },
  de: { name: "Bark", daily: "Bark-Tagesübersicht", key: "Geräteschlüssel", test: "Testen", save: "Speichern", remove: "Entfernen", off: "Aus", on: "An", sent: "An Bark gesendet" },
};

async function open(engine, width, lang, posts, t) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  const page = await (await browser.newContext({ viewport: { width, height: 700 }, reducedMotion: "reduce" })).newPage();
  t.after(async () => {
    if (process.env.ARTIFACT_DIR) {
      await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
      await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-bark.png`) });
    }
    await browser.close();
  });
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", server(lang, posts));
  await watchSettingsReads(page);
  await page.goto("http://magpie.test/?view=settings&tab=usage");
  await settingsDrawn(page);
  return { page, errors };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [900, 380]) {
      test(`${engine} ${lang} ${width}px: Bark is set, tried, given a daily summary and removed in Settings › Usage`, async (t) => {
        const posts = [];
        const { page, errors } = await open(engine, width, lang, posts, t);
        const w = words[lang];
        const posted = async (n) => { for (let i = 0; i < 100 && posts.length < n; i++) await page.waitForTimeout(20); assert.equal(posts.length, n, "posts " + JSON.stringify(posts)); return posts[n - 1]; };
        const keyField = page.locator("#barkBox input[type=password]");
        await keyField.waitFor();

        assert.equal((await page.locator("#barkRow .name").textContent()).trim(), w.name);
        assert.equal(await keyField.getAttribute("placeholder"), w.key);
        assert.equal(await page.locator("#barkDailyRow").isHidden(), true, "no summary without a key");
        assert.equal(await page.locator("#barkRow").count(), 1, "one row");

        // no key, nothing saved
        await reach(page, page.locator("#barkBox .bark-save"));
        assert.equal((await page.locator("#barkBox .bark-save").textContent()).trim(), w.save);
        await page.locator("#barkBox .bark-save").click();
        await page.waitForTimeout(150);
        assert.equal(posts.length, 0, "saved with no key");

        const scrolled = () => page.locator("#view-settings").evaluate((v) => v.scrollTop);
        await page.locator("#barkBox .bark-server-input").fill("https://bark.example.com");
        await keyField.fill(KEY);
        const top = await scrolled();
        await page.locator("#barkBox .bark-save").click();
        const first = await posted(1);
        assert.deepEqual(first, { path: "bark", server: "https://bark.example.com", key: KEY });

        // the key is written and never shown: dots, Test and Remove
        await page.locator("#barkBox .bark-key").waitFor();
        assert.equal((await page.locator("#barkBox .bark-key").textContent()).trim(), "••••••••");
        assert.equal(await page.locator("#barkBox input").count(), 0, "no field left");
        const all = await page.evaluate(() => document.documentElement.outerHTML + JSON.stringify([...document.querySelectorAll("input")].map((i) => i.value)));
        assert.ok(!all.includes(KEY), "the key is in the page");
        assert.equal(await scrolled(), top, "the click moved the page");
        assert.equal((await page.locator("#barkBox .bark-server").textContent()).trim(), "bark.example.com");
        assert.deepEqual(await page.locator("#barkBox button").allTextContents(), [w.test, w.remove]);

        // the daily summary appears, Off until turned on
        await page.locator("#barkDailyRow").waitFor({ state: "visible" });
        assert.equal((await page.locator("#barkDailyRow .name").textContent()).trim(), w.daily);
        const opts = page.locator("#barkDailySegs .opt");
        assert.deepEqual(await opts.allTextContents(), [w.off, w.on]);
        assert.equal(await page.locator("#barkDailySegs .opt.on").textContent(), w.off);
        await reach(page, opts.nth(1));
        await opts.nth(1).click();
        assert.deepEqual(await posted(2), { path: "bark", daily: "21:00" });
        await page.locator("#barkDailySegs input.at").waitFor();
        assert.equal(await page.locator("#barkDailySegs input.at").inputValue(), "21:00");
        await page.locator("#barkDailySegs input.at").fill("07:45");
        await page.locator("#barkDailySegs input.at").blur();
        assert.deepEqual(await posted(3), { path: "bark", daily: "07:45" });

        // Test pushes, and says so
        await page.locator("#barkBox .bark-test").click();
        assert.deepEqual(await posted(4), { path: "test" });
        await page.getByText(w.sent).first().waitFor();

        // Settings saved later keep the Bark rows
        const over = await page.evaluate(() => {
          const rows = [...document.querySelectorAll("#barkRow, #barkDailyRow")];
          return rows.flatMap((row) => { const r = row.getBoundingClientRect(); return [...row.querySelectorAll("*")].filter((e) => e.getBoundingClientRect().right > Math.max(r.right, innerWidth) + 1).map((e) => e.className || e.id); });
        });
        assert.deepEqual(over, [], "nothing past the edge");

        await reach(page, page.locator("#barkBox .bark-remove"));
        await page.locator("#barkBox .bark-remove").click();
        assert.deepEqual(await posted(5), { path: "bark", key: "", daily: "" });
        await keyField.waitFor();
        assert.equal(await page.locator("#barkDailyRow").isHidden(), true, "no summary without a key");
        assert.deepEqual(errors, []);
      });
    }
  }
}
