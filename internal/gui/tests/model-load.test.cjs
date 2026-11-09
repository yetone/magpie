// Run with Node's test runner and Playwright on the module path; see README.md.
// A model's load, as its plugin reports it (magpie.load, axelburks on
// Discord: our vendor's busy models make requests queue, and the load put
// in the model's name only changed when the list did): a small grey
// number by the model on the provider editor's chips, its title saying
// what it is; a model with no load reported shows none. And Settings ›
// Network and sharing › Busy models last: Off by default, a threshold
// posted on its own as settings/busy-load, one set in settings.json to
// another number shown as picked, no click scrolling the page; Custom…
// opens a field for 1 to 9999 that says what the number means, refuses
// anything else without posting, and the number saved shows as its own
// choice. Every
// language, Chromium and WebKit, at a narrow and a wide window; no
// backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn, wheelTo } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const fetched = new Date(Date.now() - 3600e3).toISOString();
const inhouse = {
  id: "inhouse", name: "In-house", icon: "generic", host: "llm.example.com", chat: "https://llm.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [
    { id: "big", name: "Big", on: false, load: 87.4 },
    { id: "small", name: "Small", on: false, load: 0 },
    { id: "quiet", name: "Quiet", on: false },
    { id: "queued", name: "Queued", on: false, load: 180.2 },
  ],
  chosen: [], fetched, agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
};

function settingsPayload(over) {
  return {
    theme: "light", lang: "en", tray: "panel", quotaLeft: false, currency: "usd",
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.1220", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [], lan: false, lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    ...over,
  };
}

function server(lang, posted, over = {}) {
  let cur = settingsPayload({ lang, ...over });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: cur.fx });
    if (url.pathname === "/api/providers") return json({ providers: [inhouse], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/settings/busy-load") {
      const body = req.postDataJSON();
      posted.push(["busy-load", body]);
      cur = { ...cur, busyLoad: body.at };
      return json(cur);
    }
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") posted.push(["settings", req.postDataJSON()]);
      return json(cur);
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/agents/cli") return json({ agents: {}, pending: false });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const want = {
  en: { busy: "87% busy now, as its plugin says", queued: "180% busy now, as its plugin says", idle: "0% busy now, as its plugin says", name: "Busy models last", off: "Off", over: (n) => `Over ${n}%`, sub: "last 5 minutes", custom: "Custom…", explain: "200 is twice", refused: "whole number from 1 to 9999" },
  zh: { busy: "插件报告当前负载 87%", queued: "插件报告当前负载 180%", idle: "插件报告当前负载 0%", name: "繁忙的模型靠后", off: "关闭", over: (n) => `高于 ${n}%`, sub: "5 分钟", custom: "自定义…", explain: "200 即两倍", refused: "1 到 9999 之间的整数" },
  "zh-TW": { busy: "外掛回報目前負載 87%", queued: "外掛回報目前負載 180%", idle: "外掛回報目前負載 0%", name: "繁忙的模型靠後", off: "關閉", over: (n) => `高於 ${n}%`, sub: "5 分鐘", custom: "自訂…", explain: "200 即兩倍", refused: "1 到 9999 之間的整數" },
  ja: { busy: "プラグインの報告では現在 87% の負荷", queued: "プラグインの報告では現在 180% の負荷", idle: "プラグインの報告では現在 0% の負荷", name: "混雑しているモデルを後回しに", off: "オフ", over: (n) => `${n}% 超`, sub: "5 分間", custom: "カスタム…", explain: "200 はその 2 倍", refused: "1〜9999 の整数" },
  de: { busy: "laut seinem Plugin gerade zu 87 % ausgelastet", queued: "laut seinem Plugin gerade zu 180 % ausgelastet", idle: "laut seinem Plugin gerade zu 0 % ausgelastet", name: "Ausgelastete Modelle zuletzt", off: "Aus", over: (n) => `Über ${n} %`, sub: "5 Minuten", custom: "Eigener…", explain: "200 ist das Doppelte", refused: "ganze Zahl von 1 bis 9999" },
};
const view = (page) => page.locator("#view-settings").evaluate((v) => v.scrollTop);

async function open(engine, width, t, name) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  const page = await (await browser.newContext({ viewport: { width, height: 420 }, reducedMotion: "reduce" })).newPage();
  t.after(async () => {
    if (process.env.ARTIFACT_DIR) {
      await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
      await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${width}-${name}.png`) });
    }
    await browser.close();
  });
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  return page;
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [390, 900]) {
    for (const lang of Object.keys(want)) {
      const w = want[lang];
      test(`${engine} ${width} ${lang}: a model's load shows by it`, async (t) => {
        const page = await open(engine, width, t, `${lang}-model-load`);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, []));
        await page.goto("http://magpie.test/?view=providers");
        const editor = page.locator("#modal:not([hidden]) .editor");
        await page.locator(".row.provider", { hasText: "In-house" }).click();
        await editor.locator(".mchips .mchip").first().waitFor();
        const chips = await editor.locator(".mchips .mchip").evaluateAll((cs) => cs.map((c) => {
          const b = [...c.querySelectorAll(".badge")].find((x) => /%$/.test(x.textContent));
          return [c.querySelector("span").textContent, b ? [b.textContent, b.title] : null];
        }));
        assert.deepEqual(chips, [["Big", ["87%", w.busy]], ["Small", ["0%", w.idle]], ["Quiet", null], ["Queued", ["180%", w.queued]]]);
        // past 100 for a vendor that queues: the badge holds it on one line
        const q = await editor.locator(".mchip .badge", { hasText: "180%" }).evaluate((b) => [b.scrollWidth <= b.clientWidth + 1, b.getClientRects().length]);
        assert.deepEqual(q, [true, 1]);
        // grey and small as the context badge, no colour of its own
        const look = await editor.locator(".mchip .badge", { hasText: "87%" }).evaluate((b) => {
          const s = getComputedStyle(b), ctx = document.createElement("span");
          ctx.className = "badge ctx";
          b.parentNode.append(ctx);
          const c = getComputedStyle(ctx), out = [s.color === c.color, s.backgroundColor === c.backgroundColor, s.fontSize, s.backgroundImage];
          ctx.remove();
          return out;
        });
        assert.deepEqual(look, [true, true, "9px", "none"]);
        const border = await page.evaluate(() => [...document.querySelectorAll(".mchip, .mchip *")].map((e) => getComputedStyle(e).borderLeftWidth).filter((b) => parseFloat(b) > 1));
        assert.deepEqual(border, [], "no border stripes");
        assert(await editor.evaluate((e) => e.scrollWidth <= e.clientWidth + 1), "the editor scrolls sideways");
        assert.deepEqual(errors, []);
      });

      // in the window as well as on the web page, whose narrow rows wrap by
      // themselves (body.web): only the window's squeezed the words
      for (const web of [true, false]) test(`${engine} ${width} ${lang} ${web ? "web page" : "window"}: busy models go last, or not`, async (t) => {
        const page = await open(engine, width, t, `${lang}-${web ? "web" : "window"}-busy-load`);
        const errors = [], posted = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, posted));
        await watchSettingsReads(page);
        if (!web) await page.route("**/boot.js", (r) => r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light"};` }));
        await page.goto("http://magpie.test/?view=settings&tab=models");
        await settingsDrawn(page);
        const segs = page.locator("#busyLoadSegs .opt");
        await segs.first().waitFor();
        const row = page.locator("#busyLoadRow");
        assert(await row.isVisible(), "the row is drawn");
        assert.equal((await row.locator(".name").textContent()).trim(), w.name);
        assert((await row.locator(".sub").textContent()).includes(w.sub), "the sub says how long a load counts");
        assert.deepEqual((await segs.allTextContents()).map((s) => s.trim()), [w.off, w.over(50), w.over(70), w.over(80), w.over(90), w.custom]);
        assert.equal(await segs.nth(0).evaluate((b) => b.classList.contains("on")), true, "off by default");
        assert.equal(await row.locator("select").count(), 0, "no native select");
        assert(await page.locator("#view-settings").evaluate((v) => v.scrollWidth <= v.clientWidth + 1), "the settings scroll sideways");
        const rb = await row.boundingBox(), sb = await page.locator("#busyLoadSegs").boundingBox();
        assert(sb.x + sb.width <= rb.x + rb.width + 1, "the switch runs out of its row");
        // the choices never squeeze the words beside them: at a phone's width
        // they once left the words a word a line, the row 2.3k px tall
        const who = await row.locator(".who").boundingBox();
        assert(who.width >= Math.min(220, rb.width - 30), `the words are squeezed to ${Math.round(who.width)}px`);
        assert(rb.height < 200, `the row is ${Math.round(rb.height)}px tall`);

        await wheelTo(page, segs.nth(3), 200);
        const before = await view(page);
        await segs.nth(3).click();
        await page.locator("#busyLoadSegs .opt.on", { hasText: w.over(80) }).waitFor();
        await page.waitForTimeout(400);
        assert.equal(await view(page), before, "the click scrolled the page");
        assert.deepEqual(posted, [["busy-load", { at: 80 }]]);
        await segs.nth(0).click();
        await page.locator("#busyLoadSegs .opt.on", { hasText: w.off }).waitFor();
        assert.deepEqual(posted, [["busy-load", { at: 80 }], ["busy-load", { at: 0 }]]);
        assert.equal(await view(page), before, "the click scrolled the page");
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${width} ${lang}: a custom threshold is typed in`, async (t) => {
        const page = await open(engine, width, t, `${lang}-busy-custom`);
        const errors = [], posted = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, posted));
        await watchSettingsReads(page);
        await page.goto("http://magpie.test/?view=settings&tab=models");
        await settingsDrawn(page);
        const row = page.locator("#busyLoadRow"), sub = row.locator(".sub"), field = row.locator("input.busy-load-at");
        const custom = row.locator(".opt", { hasText: w.custom }), apply = row.locator("#busyLoadSegs .val button").first();
        // a press on what is in sight moves the page no more than the row
        // grew or shrank by (its sub says more while the field is open):
        // wheeled to first, so Playwright's own scrolling isn't counted
        const still = async (what, target, act, done) => {
          await wheelTo(page, target, 160);
          const h0 = (await row.boundingBox()).height, v0 = await view(page);
          await act();
          await done();
          await page.waitForTimeout(150);
          const h1 = (await row.boundingBox()).height, v1 = await view(page);
          assert(Math.abs(v1 - v0) <= Math.abs(h1 - h0) + 2, `${what} scrolled the page: ${v0} to ${v1}, the row ${h0} to ${h1}`);
        };
        await still("Custom…", custom, () => custom.click(), () => field.waitFor());
        assert(await field.evaluate((f) => document.activeElement === f), "the field takes the keys");
        assert((await sub.textContent()).includes(w.explain), "the sub says what the number means");
        assert.equal(await row.locator("select").count(), 0, "no native select");
        const fits = async (what) => {
          const rb = await row.boundingBox(), sb = await page.locator("#busyLoadSegs").boundingBox();
          assert(sb.x + sb.width <= rb.x + rb.width + 1 && sb.x >= rb.x - 1, `${what} runs out of its row`);
          assert(await page.locator("#view-settings").evaluate((v) => v.scrollWidth <= v.clientWidth + 1), "the settings scroll sideways");
        };
        await fits("the field");
        // wide enough for the four digits it takes
        assert((await field.boundingBox()).width >= 60, "the field is squeezed");
        for (const bad of ["abc", "0", "10000", "1.5", "-3"]) {
          await wheelTo(page, field, 160);
          await field.fill(bad);
          await field.press("Enter");
          await sub.filter({ hasText: w.refused }).waitFor();
          assert(await sub.evaluate((s) => s.classList.contains("err")), `${bad}: said as an error`);
          assert.equal(await field.inputValue(), bad, `${bad}: kept to correct`);
        }
        await page.waitForTimeout(200);
        assert.deepEqual(posted, [], "a refused number is posted");
        // Escape closes it untouched, the choices back as they were
        await field.press("Escape");
        await row.locator(".opt.on", { hasText: w.off }).waitFor();
        assert.deepEqual(posted, []);
        await still("Custom…", custom, () => custom.click(), () => field.waitFor());
        await field.fill("150");
        await still("Apply", apply, () => apply.click(), () => row.locator(".opt.on", { hasText: w.over(150) }).waitFor());
        assert.deepEqual(posted, [["busy-load", { at: 150 }]]);
        assert.deepEqual((await row.locator(".opt").allTextContents()).map((s) => s.trim()), [w.off, w.over(50), w.over(70), w.over(80), w.over(90), w.over(150), w.custom]);
        assert((await sub.textContent()).includes(w.sub), "the sub is back");
        assert(!(await sub.evaluate((s) => s.classList.contains("err"))), "no error left");
        await fits("the switch");
        // the custom value opens again in the field to change it
        await still("Custom…", custom, () => custom.click(), () => field.waitFor());
        assert.equal(await field.inputValue(), "150");
        await field.fill("2000");
        await field.press("Enter");
        await row.locator(".opt.on", { hasText: w.over(2000) }).waitFor();
        assert.deepEqual(posted, [["busy-load", { at: 150 }], ["busy-load", { at: 2000 }]]);
        assert.deepEqual(errors, []);
      });
    }
  }
}

test("a threshold past 100 set by hand is shown as picked", async (t) => {
  const page = await open(process.env.BROWSER || "chromium", 420, t, "busy-load-150");
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", server("en", [], { busyLoad: 150 }));
  await page.goto("http://magpie.test/?view=settings&tab=models");
  const segs = page.locator("#busyLoadSegs .opt");
  await segs.first().waitFor();
  assert.deepEqual((await segs.allTextContents()).map((s) => s.trim()), ["Off", "Over 50%", "Over 70%", "Over 80%", "Over 90%", "Over 150%", "Custom…"]);
  assert.equal((await page.locator("#busyLoadSegs .opt.on").textContent()).trim(), "Over 150%");
  const rb = await page.locator("#busyLoadRow").boundingBox(), sb = await page.locator("#busyLoadSegs").boundingBox();
  assert(sb.x + sb.width <= rb.x + rb.width + 1, "the switch runs out of its row");
  assert.deepEqual(errors, []);
});

test("a threshold set by hand is shown as picked", async (t) => {
  const page = await open(process.env.BROWSER || "chromium", 900, t, "busy-load-own");
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", server("en", [], { busyLoad: 75 }));
  await page.goto("http://magpie.test/?view=settings&tab=models");
  const segs = page.locator("#busyLoadSegs .opt");
  await segs.first().waitFor();
  assert.deepEqual((await segs.allTextContents()).map((s) => s.trim()), ["Off", "Over 50%", "Over 70%", "Over 75%", "Over 80%", "Over 90%", "Custom…"]);
  assert.equal((await page.locator("#busyLoadSegs .opt.on").textContent()).trim(), "Over 75%");
  assert.deepEqual(errors, []);
});
