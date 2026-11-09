// Run with Node's test runner and Playwright on the module path; see README.md.
// A reset's time is said the same as it was before its date formatters were
// kept (#5310). resetClock and resetText called toLocale*String, which makes
// a formatter each call; with hundreds of accounts a render made thousands.
// dateFmt keeps one per language and options now. This holds every reset
// time a week either side, for every language, to what toLocale*String
// says, in both engines, and to the language picked after the first call.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

function serve(lang) {
  const settings = { lang, theme: "light", currency: "usd" };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await r.fulfill({ body: await fs.readFile(file), contentType }); } catch { await r.fulfill({ status: 404, body: "" }); }
  };
}

for (const engine of ["chromium", "webkit"]) {
  test(`${engine}: a reset's time reads as toLocale*String says it, in every language`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const context = await browser.newContext({ locale: "en-GB", timezoneId: "Asia/Tokyo" });
    const page = await context.newPage();
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.route("**/*", serve("en"));
    await page.goto("http://magpie.test/");
    await page.waitForFunction(() => typeof resetClock === "function" && typeof dateFmt === "function");
    const wrong = await page.evaluate(() => {
      // what resetClock and resetText said before dateFmt, word for word
      const was = (at, now) => {
        const lang = intlLang();
        const time = at.toLocaleTimeString(lang, { hour: "2-digit", minute: "2-digit", hour12: false });
        const day = (d) => new Date(d.getFullYear(), d.getMonth(), d.getDate());
        const days = Math.round((day(at) - day(now)) / 864e5);
        if (days <= 0) return time;
        if (days === 1) return t("tomorrow {time}", { time });
        if (days <= 6 - ((now.getDay() + 6) % 7)) return at.toLocaleDateString(lang, { weekday: "short" }) + " " + time;
        return at.toLocaleDateString(lang, { month: "short", day: "numeric" }) + " " + time;
      };
      const out = [];
      let seen = 0;
      // "system" last: the system's English after a language was kept
      for (const lang of ["en", "zh", "zh-TW", "ja", "de", "system"]) {
        setLocale(lang);
        const now = new Date();
        for (let h = -24 * 7; h <= 24 * 8; h += 5) {
          const at = new Date(now.getTime() + h * 36e5 + 17 * 6e4);
          const pairs = [[resetClock(at, now), was(at, now)]];
          if (at <= Date.now()) pairs.push([resetText(at), t("Reset time passed {when}", { when: at.toLocaleString(intlLang()) })]);
          for (const [got, want] of pairs) {
            seen++;
            if (got !== want) out.push(`${lang} ${at.toISOString()}: ${got} ≠ ${want}`);
          }
        }
      }
      return { out, seen };
    });
    await context.close();
    assert.deepEqual(errors, []);
    assert.ok(wrong.seen > 400, `times compared: ${wrong.seen}`);
    assert.deepEqual(wrong.out, []);
  });
}
