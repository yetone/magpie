// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings' kinds of personal data (lc on Discord): under Mask personal data,
// while it is on, a row for each kind with Off/On, showing the user's choice
// or the kind's default; a click saves that kind alone with the rest of the
// settings as they were, and moves nothing. Their words are shown in full,
// or, where they only explain the kind, rest out of sight for the hover
// card (settingshints.js), and nothing runs off the page. In every language, in a wide window and a
// narrow one.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const ids = ["ssn", "passport", "iban", "birthday", "mac", "serial", "home", "userhost", "ip", "bucket"];
// the user turned SSNs off and IP addresses on; the rest are as by default
const want = { ssn: "off", passport: "on", iban: "on", birthday: "on", mac: "on", serial: "on", home: "off", userhost: "off", ip: "on", bucket: "off" };
// the names and words in English, read from app.js, to see each is translated
const kinds = (() => {
  const src = require("node:fs").readFileSync(path.join(assets, "app.js"), "utf8");
  const list = src.slice(src.indexOf("\nconst REDACT_KINDS = ["), src.indexOf("\n];", src.indexOf("\nconst REDACT_KINDS = [")));
  return [...list.matchAll(/^  \["(\w+)", "((?:[^"\\]|\\.)*)", "((?:[^"\\]|\\.)*)"/gm)].map((m) => [m[1], JSON.parse(`"${m[2]}"`), JSON.parse(`"${m[3]}"`)]);
})();
const english = Object.fromEntries(kinds.map(([id, name]) => [id, name]));
const englishSub = Object.fromEntries(kinds.map(([id, , sub]) => [id, sub]));
const words = {
  en: { ip: "Public IP addresses", on: "On" },
  zh: { ip: "公网 IP 地址", on: "开启" },
  "zh-TW": { ip: "公網 IP 位址", on: "開啟" },
  ja: { ip: "パブリック IP アドレス", on: "オン" },
  de: { ip: "Öffentliche IP-Adressen", on: "An" },
};

function serve(lang, personal, saved) {
  const settings = () => ({ lang, theme: "light", redact: true, redactPersonal: personal, redactKinds: { ssn: false, ip: true }, redactWords: ["Nightjar"] });
  const state = { agents: [], profiles: [], settings: settings() };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/settings") {
      if (r.request().method() === "POST") {
        const body = JSON.parse(r.request().postData());
        saved.push(body);
        return json(body);
      }
      return json(settings());
    }
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body: await fs.readFile(file), contentType });
  };
}

async function open(engine, lang, width, personal, saved) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  const page = await (await browser.newContext({ viewport: { width, height: 1400 } })).newPage();
  page.setDefaultTimeout(5000);
  page.setDefaultNavigationTimeout(60000);
  const errors = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.route("**/*", serve(lang, personal, saved));
  await watchSettingsReads(page);
  await page.goto("http://magpie.test/?view=settings&tab=privacy");
  await page.locator("#redactList .row").first().waitFor();
  await settingsDrawn(page); // the rows found are the ones that stay (#1653)
  return { browser, page, errors };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    for (const width of [1100, 640]) {
      const w = words[lang];
      test(`${engine} ${lang} ${width}px: a row for each kind of personal data`, async (t) => {
        const saved = [];
        const { browser, page, errors } = await open(engine, lang, width, true, saved);
        t.after(() => browser.close());
        const rows = page.locator("#redactList .row.redact-kind");
        await rows.first().waitFor();
        const m = await page.evaluate(() => {
          const list = document.querySelector("#redactList").getBoundingClientRect();
          return {
            rows: [...document.querySelectorAll("#redactList .row.redact-kind")].map((r) => {
              const sub = r.querySelector(".sub"), b = r.getBoundingClientRect(), v = r.querySelector(".val").getBoundingClientRect();
              return {
                id: r.dataset.redact, name: r.querySelector(".name").textContent, sub: sub.textContent,
                on: r.querySelector(".segs .opt.on")?.textContent,
                hint: getComputedStyle(sub).getPropertyValue("--hint").trim() === "1",
                cut: sub.scrollWidth > sub.clientWidth + 1 || sub.scrollHeight > sub.clientHeight + 1,
                inside: b.left >= list.left - 1 && b.right <= list.right + 1 && v.right <= b.right + 1,
              };
            }),
            // right under Mask personal data
            after: document.querySelector("#redactList .row.redact-kind").previousElementSibling.querySelector(".segs .opt.on")?.textContent,
            wide: document.documentElement.scrollWidth > document.documentElement.clientWidth,
          };
        });
        assert.deepEqual(m.rows.map((r) => r.id), ids);
        const onWord = await page.evaluate(() => t("On"));
        assert.equal(onWord, w.on);
        assert.deepEqual(Object.fromEntries(m.rows.map((r) => [r.id, r.on === onWord ? "on" : "off"])), want);
        assert.equal(m.rows.find((r) => r.id === "ip").name, w.ip);
        for (const r of m.rows) {
          assert(r.sub.length > 4 && (r.hint || !r.cut), `${r.id}'s words shown in full, or kept for the hover card: ${r.sub}`);
          assert(r.inside, `${r.id} inside the list`);
          if (lang !== "en" && r.id !== "iban") assert.notEqual(r.name, english[r.id], `${r.id} translated`);
          if (lang !== "en") assert.notEqual(r.sub, englishSub[r.id], `${r.id}'s words translated`);
        }
        assert.equal(m.after, onWord, "under Mask personal data, which is on");
        assert(!m.wide, "nothing runs off the page");

        // Buckets turned on: that kind is saved, the user's other choices
        // and the rest of the settings as they were; nothing moves
        const row = page.locator('#redactList .row.redact-kind[data-redact="bucket"]');
        // the way a reader does: a phone-width page doesn't let a script scroll it
        await page.mouse.move(width / 2, 300);
        for (let i = 0; i < 20 && (await row.boundingBox()).y > 900; i++) {
          await page.mouse.wheel(0, 200);
          await page.waitForTimeout(80);
        }
        await page.waitForTimeout(400);
        const before = await page.evaluate(() => [scrollX, scrollY, document.querySelector("#view-settings").scrollTop]);
        const b = await row.locator(".segs .opt", { hasText: w.on }).boundingBox();
        await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
        await page.waitForFunction(() => document.querySelector('#redactList .row.redact-kind[data-redact="bucket"] .segs .opt.on')?.textContent === t("On"));
        await new Promise((r) => setTimeout(r, 300));
        assert.equal(saved.length, 1, "one save");
        assert.deepEqual(saved[0].redactKinds, { ssn: false, ip: true, bucket: true });
        assert.equal(saved[0].redactPersonal, true);
        assert.equal(saved[0].redact, true);
        assert.deepEqual(saved[0].redactWords, ["Nightjar"]);
        assert.deepEqual(await page.evaluate(() => [scrollX, scrollY, document.querySelector("#view-settings").scrollTop]), before, "the click moved nothing");
        assert.deepEqual(errors, []);
      });
    }
  }
  test(`${engine}: no kinds while personal data isn't masked`, async (t) => {
    const { browser, page, errors } = await open(engine, "en", 1100, false, []);
    t.after(() => browser.close());
    await page.locator("#redactList .row", { hasText: "Mask personal data" }).waitFor();
    assert.equal(await page.locator("#redactList .row.redact-kind").count(), 0);
    assert.deepEqual(errors, []);
  });
}
