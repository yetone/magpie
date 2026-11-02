// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › Privacy's "Keep a key's form" (Charles Cao on X): a row right
// under Mask secrets while secrets are masked, none while they aren't, Off
// unless the user turned it on, so a key goes as a placeholder as before.
// A click on On saves redactShape with the rest of the settings as they
// were, and moves nothing; a later save from the page keeps it. In every
// language, Chromium and WebKit, in a wide window and a narrow one.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { reach, watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const words = {
  en: { name: "Keep a key's form", on: "On", off: "Off" },
  zh: { name: "保留密钥格式", on: "开启", off: "关闭" },
  "zh-TW": { name: "保留金鑰格式", on: "開啟", off: "關閉" },
  ja: { name: "キーの形式を保つ", on: "オン", off: "オフ" },
  de: { name: "Schlüsselformat beibehalten", on: "An", off: "Aus" },
};

function serve(lang, saved, redact = true) {
  let settings = { lang, theme: "light", redact, redactPersonal: false, redactWords: ["Nightjar"] };
  const state = { agents: [], profiles: [], settings };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/settings") {
      if (r.request().method() === "POST") {
        settings = JSON.parse(r.request().postData());
        saved.push(settings);
      }
      return json(settings);
    }
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

async function open(engine, lang, width, saved, redact) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  const page = await (await browser.newContext({ viewport: { width, height: 1400 } })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, saved, redact));
  await watchSettingsReads(page);
  await page.goto("http://magpie.test/?view=settings&tab=privacy");
  await page.locator("#redactList .row").first().waitFor();
  await settingsDrawn(page);
  return { browser, page, errors };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [lang, w] of Object.entries(words)) {
    for (const width of [1100, 640]) {
      test(`${engine} ${lang} ${width}px: a key keeps its form only when turned on`, async (t) => {
        const saved = [];
        const { browser, page, errors } = await open(engine, lang, width, saved, true);
        t.after(() => browser.close());
        const row = page.locator("#redactShapeRow");
        await reach(page, row);
        const m = await row.evaluate((r) => {
          const sub = r.querySelector(".sub"), list = document.querySelector("#redactList").getBoundingClientRect(), b = r.getBoundingClientRect();
          return {
            name: r.querySelector(".name").textContent, sub: sub.textContent,
            on: r.querySelector(".segs .opt.on")?.textContent,
            first: r.previousElementSibling?.querySelector(".name")?.textContent === t("Mask secrets"),
            hint: getComputedStyle(sub).getPropertyValue("--hint").trim() === "1",
            inside: b.left >= list.left - 1 && b.right <= list.right + 1,
            wide: document.documentElement.scrollWidth > document.documentElement.clientWidth,
          };
        });
        assert.equal(m.name, w.name);
        assert.equal(m.on, w.off, "off unless the user turned it on");
        assert(m.first, "right under Mask secrets");
        assert(m.hint && m.inside && !m.wide, "its explanation rests out of sight, for the hover card, inside the page");
        if (lang !== "en") assert(!m.sub.includes("made-up"), "its words translated");

        const before = await page.evaluate(() => [scrollX, scrollY, document.querySelector("#view-settings").scrollTop]);
        const b = await row.locator(".segs .opt", { hasText: w.on }).boundingBox();
        await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
        await page.waitForFunction(() => document.querySelector("#redactShapeRow .segs .opt.on")?.textContent === t("On"));
        await new Promise((r) => setTimeout(r, 300));
        assert.equal(saved.length, 1, "one save");
        assert.equal(saved[0].redactShape, true);
        assert.equal(saved[0].redact, true);
        assert.deepEqual(saved[0].redactWords, ["Nightjar"]);
        assert.deepEqual(await page.evaluate(() => [scrollX, scrollY, document.querySelector("#view-settings").scrollTop]), before, "the click moved nothing");

        // another choice on the page keeps it: Mask personal data
        const p = page.locator("#redactShapeRow + .row").locator(".segs .opt", { hasText: w.on });
        await reach(page, p);
        await p.click();
        await page.waitForFunction(() => document.querySelector("#redactShapeRow + .row .segs .opt.on")?.textContent === t("On"));
        await new Promise((r) => setTimeout(r, 300));
        assert.equal(saved.length, 2);
        assert.equal(saved[1].redactPersonal, true);
        assert.equal(saved[1].redactShape, true, "a later save keeps it");
        assert.deepEqual(errors, []);
      });
    }
  }
  test(`${engine}: no key's form while secrets aren't masked`, async (t) => {
    const { browser, page, errors } = await open(engine, "en", 1100, [], false);
    t.after(() => browser.close());
    assert.equal(await page.locator("#redactShapeRow").count(), 0);
    assert.deepEqual(errors, []);
  });
}
