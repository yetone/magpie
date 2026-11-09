// Run with Node's test runner and Playwright on the module path; see README.md.
// #957 (Evan26Ma): two Plus accounts started by Daily warm-up at one time
// run out together; each can have its own time, so one takes over as the
// other's window ends. Under Daily warm-up each ChatGPT account has a row:
// Same (the time above), Own (a time field) or Off, each set on its own
// (POST /api/settings/codex-warm-at {user, at}: "" for Same). The rows show
// with two accounts or more, or while one has its own time, in Codex's
// provider editor (they were in Settings › Usage until the owner moved them
// on 11-07); a click never moves the page; nothing runs off the side at
// 440px. en, zh, ja, de; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { provPrefsProviders, openProvPrefs, inEditor } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

function serve(lang, asked, users) {
  const settings = { theme: "light", lang, currency: "usd", codexWarmup: "", codexWarmAt: "06:00", codexUsers: users };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/settings/codex-warm-at") {
      const { user, at } = route.request().postDataJSON();
      asked.push([user, at]);
      const of = { ...(settings.codexWarmAtOf || {}) };
      delete of[user.toLowerCase()];
      if (at) of[user.toLowerCase()] = at;
      settings.codexWarmAtOf = Object.keys(of).length ? of : undefined;
      return json(settings);
    }
    if (url.pathname === "/api/providers") return json(provPrefsProviders(["codex"], { codex: "Codex" }));
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const W = {
  en: { same: "Same as above, 06:00", own: "Its own time each day", off: "Not started at a time of day", segs: ["Same", "Own", "Off"] },
  zh: { same: "与上面相同，06:00", own: "每天按它自己的时间", off: "不按时间定时预热", segs: ["相同", "单独", "关闭"] },
  ja: { same: "上と同じ、06:00", own: "毎日このアカウント独自の時刻", off: "時刻指定では開始しません", segs: ["同じ", "個別", "オフ"] },
  de: { same: "Wie oben, 06:00", own: "Täglich zu seiner eigenen Uhrzeit", off: "Nicht zu einer Uhrzeit gestartet", segs: ["Gleich", "Eigene", "Aus"] },
};

const sideways = (page) => page.evaluate(() => {
  const out = [];
  const de = document.documentElement, v = document.querySelector(".editor .ebody") || document.querySelector(".editor");
  if (de.scrollWidth > de.clientWidth) out.push("page");
  if (v.scrollWidth > v.clientWidth) out.push("view");
  for (const r of document.querySelectorAll(".warm-own")) {
    const rr = r.getBoundingClientRect();
    for (const e of r.querySelectorAll(".name, .sub, .segs, input")) {
      const b = e.getBoundingClientRect();
      if (b.right > rr.right + 0.5 || b.left < rr.left - 0.5) out.push(r.dataset.user + " " + e.className);
      if (e.classList.contains("name") && b.width < 40) out.push(r.dataset.user + " name squeezed to " + b.width);
    }
  }
  return out;
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "ja", "de"]) {
    const w = W[lang];
    for (const width of [900, 440]) {
      test(`${engine} ${lang} ${width}px: each Codex account has its own daily warm-up time`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], asked = [];
        page.on("pageerror", (e) => { if (engine === "webkit" && /due to access control checks/.test(e.message)) return; errors.push(e.message); }); // a goto in WebKit can cut off a fetch (#1350)
        await page.route("**/*", serve(lang, asked, ["a@example.com", "B@Example.com"]));
        await openProvPrefs(page, "codex");
        const rows = page.locator(".editor .prov-prefs #codexWarmList .warm-own");
        await rows.first().waitFor();
        assert.deepEqual(await rows.evaluateAll((rs) => rs.map((r) => r.dataset.user)), ["a@example.com", "B@Example.com"]);
        assert.deepEqual((await rows.locator(".sub").allInnerTexts()).map((s) => s.trim()), [w.same, w.same]);
        assert.deepEqual((await rows.first().locator(".segs .opt").allInnerTexts()).map((s) => s.trim()), w.segs);
        assert.equal(await rows.locator("input").count(), 0, "no time field while it follows the one above");
        assert.deepEqual(await sideways(page), []);

        // Own: the time above to start from, then its own
        // both rows brought into sight in the editor first
        await inEditor(page, rows.last().locator(".segs"));
        await inEditor(page, rows.first().locator(".segs"));
        if (process.env.ARTIFACT_DIR) await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `warm-at-own-${engine}-${lang}-${width}.png`) });
        // each pick stays under the pointer: the editor scrolls to hold a
        // row drawn again (modalHold)
        const top = (loc) => loc.evaluate((e) => e.getBoundingClientRect().top);
        const settled = () => page.evaluate(() => new Promise((ok) => requestAnimationFrame(() => requestAnimationFrame(ok))));
        let pressed, y;
        const press = async (loc) => { await inEditor(page, loc); pressed = loc; y = await top(loc); await loc.click(); }; // in sight first, so the click itself scrolls nothing
        const stayed = async () => { await settled(); const now = await top(pressed); assert(Math.abs(now - y) <= 1, `a click moved from ${y} to ${now}`); };
        await press(rows.first().locator(".segs .opt").nth(1));
        await page.locator('.warm-own[data-user="a@example.com"] input').waitFor();
        await stayed();
        assert.deepEqual(asked, [["a@example.com", "06:00"]]);
        const a = page.locator('.warm-own[data-user="a@example.com"]');
        assert.equal((await a.locator(".sub").innerText()).trim(), w.own);
        await a.locator("input").fill("09:00");
        await a.locator("input").press("Enter");
        for (let i = 0; i < 50 && asked.length < 2; i++) await page.waitForTimeout(40);
        assert.deepEqual(asked[1], ["a@example.com", "09:00"]);
        // B off, by its name as it is; then back to the time above
        const b = page.locator('.warm-own[data-user="B@Example.com"]');
        await press(b.locator(".segs .opt").nth(2));
        for (let i = 0; i < 50 && asked.length < 3; i++) await page.waitForTimeout(40);
        assert.deepEqual(asked[2], ["B@Example.com", "off"]);
        await page.waitForFunction((s) => document.querySelector('.warm-own[data-user="B@Example.com"] .sub')?.textContent === s, w.off);
        await stayed();
        await press(b.locator(".segs .opt").nth(0));
        for (let i = 0; i < 50 && asked.length < 4; i++) await page.waitForTimeout(40);
        assert.deepEqual(asked[3], ["B@Example.com", ""]);
        await page.waitForFunction((s) => document.querySelector('.warm-own[data-user="B@Example.com"] .sub')?.textContent === s, w.same);
        assert.equal(await page.locator('.warm-own[data-user="a@example.com"] input').inputValue(), "09:00");
        await stayed();
        assert.equal(await page.evaluate(() => scrollY), 0, "the page itself never scrolls");
        assert.deepEqual(await sideways(page), []);
        assert.equal(await page.locator("select").count(), 0);
        assert.deepEqual(errors, []);
      });
    }
  }

  test(`${engine}: one account has no rows of its own`, async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const page = await (await browser.newContext({ viewport: { width: 900, height: 800 }, reducedMotion: "reduce" })).newPage();
    page.setDefaultTimeout(5000);
    page.setDefaultNavigationTimeout(60000);
    const errors = [];
    page.on("pageerror", (e) => { if (engine === "webkit" && /due to access control checks/.test(e.message)) return; errors.push(e.message); }); // a goto in WebKit can cut off a fetch (#1350)
    await page.route("**/*", serve("en", [], ["a@example.com"]));
    await openProvPrefs(page, "codex");
    await page.locator(".editor .prov-prefs #warmAtSegs .opt").first().waitFor();
    assert.equal(await page.locator(".warm-own").count(), 0);
    assert.deepEqual(errors, []);
  });
}
