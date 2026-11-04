// Run with Node's test runner and Playwright on the module path; see README.md.
// xwoffice_91592 on Discord: in the provider editor (Trae CN, a plugin's
// subscription with a daily check-in), each account's row has its own
// Check in now (立即签到), at the end of its usage line, under the
// account's First or Make first; where the line is full it wraps whole,
// never cut. It shows on an account the check-in reaches that isn't in
// today (none yet, or failed today), as the Usage card's press decides
// (checkinDue), and not on one checked in today or one no check-in
// reaches. A press checks that account in alone (POST
// /api/usage/checkin-account {by, key}, Settings' press), says what came
// of it in the status line as Settings' does, and reads the row again,
// after which the button is gone. A click never scrolls the sheet.
// Chromium and WebKit, 1000, 560 and 390 wide, en, zh, zh-TW, ja, de.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_GUI_ASSETS || path.resolve(__dirname, "../assets"));
const today = new Date(Date.now() + 8 * 3600e3).toISOString().slice(0, 10);
const trae = () => ({
  id: "trae-cn", name: "Trae CN", icon: "", chat: "", responses: "", anthropic: "", catalog: "", routing: "",
  models: [{ id: "doubao-seed-2", name: "", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: {
    agent: "trae-cn", agentName: "Trae CN", user: "ann@example.com", plan: "",
    logins: [
      { user: "ann@example.com", active: true, on: true },
      { user: "bob@example.com", on: true },
      { user: "cat@example.com", on: true },
      { user: "dan@example.com", on: true },
    ],
  },
});
const win = { name: "Credits", used: 25.5, limit: 100 };
const serve = (lang, web, ctl) => async (route) => {
  const url = new URL(route.request().url());
  const json = (data) => route.fulfill({ json: data });
  if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:${web}};` });
  if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
  if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light", traeCheckin: true } });
  if (url.pathname === "/api/providers") return json({ providers: [trae()], presets: [], excluded: [], gateway: { running: true, window: true },
    plugins: [{ id: "trae-cn", pid: "trae-cn", name: "Trae CN", icon: "", spec: "@magpie-community/opencode-trae-auth" }] });
  if (url.pathname === "/api/groups") return json({ groups: [] });
  if (url.pathname === "/api/plugins") return json({ plugins: [] });
  if (url.pathname === "/api/login/usage") {
    ctl.reads++;
    const ci = (key, checkin) => ({ plan: "Credits", windows: [win], checkins: true, checkinBy: "trae", checkinKey: key, ...(checkin ? { checkin } : {}) });
    return json({
      // none yet today: due, unless pressed already
      "ann@example.com": ci("trae-cn|1", ctl.pressed ? { day: today, outcome: "claimed", credit: 100, by: "trae" } : null),
      // in today: no button
      "bob@example.com": ci("trae-cn|2", { day: today, outcome: "claimed", credit: 100, by: "trae" }),
      // failed today: due
      "cat@example.com": ci("trae-cn|3", { day: today, outcome: "failed", msg: "busy", by: "trae" }),
      // no check-in reaches it
      "dan@example.com": { plan: "Credits", windows: [win] },
    });
  }
  if (url.pathname === "/api/usage/checkin-account") {
    ctl.asked.push(route.request().postDataJSON());
    ctl.pressed = true;
    await new Promise((r) => setTimeout(r, 150));
    return json({ day: today, outcome: "claimed", asked: true, credit: 100, by: "trae", user: "ann@example.com" });
  }
  if (url.pathname.startsWith("/api/")) return json({});
  const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
  const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
  try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
};

for (const [engine, browserType] of [["chromium", chromium], ["webkit", webkit]]) {
  if (process.env.BROWSER && process.env.BROWSER !== engine) continue;
  test(engine + ": each account the check-in reaches and isn't in today has its own Check in now", async (t) => {
    const browser = await browserType.launch();
    t.after(() => browser.close());
    for (const [web, width] of [[false, 1000], [false, 560], [true, 390]]) for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      const at = `${lang} ${web ? "web " : ""}${width}`;
      const ctl = { reads: 0, asked: [], pressed: false };
      const context = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, web, ctl));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: "Trae CN" }).first().click();
      const row = (u) => page.locator(`.editor .accts .acc[data-account-id="${u}"]`);
      await row("ann@example.com").locator(".ci-acct").waitFor();
      await page.mouse.move(1, 1);

      const w = await page.evaluate((lang) => {
        const tr = (k) => lang === "en" ? k : I18N[lang]?.[k];
        return { name: tr("Check in now"), tip: tr("Checks one account in once now; the daily check-in's switches stay as they are"), done: tr("Checked in {who}") };
      }, lang);
      assert.ok(w.name && w.tip && w.done, `${at}: a string has no ${lang}`);
      for (const u of ["ann@example.com", "cat@example.com"]) {
        const b = row(u).locator(".aq > .ci-acct");
        assert.equal(await b.count(), 1, `${at}: ${u} has no Check in now on its usage line`);
        assert.equal((await b.textContent()).trim(), w.name);
        assert.equal(await b.getAttribute("title"), w.tip);
        const box = await b.boundingBox(), line = await row(u).locator(".acc-line").boundingBox(), r = await row(u).boundingBox();
        const meter = await row(u).locator(".aq .aq-w").first().boundingBox();
        assert.ok(box && box.width >= 10, `${at}: ${u}'s Check in now is not shown`);
        assert.ok(box.y >= line.y + line.height - 2 && box.y + box.height <= r.y + r.height + 1, `${at}: ${u}'s Check in now is not under its account's line`);
        // after its usage on that line, or wrapped whole under it
        assert.ok(box.y >= meter.y + meter.height - 1 || box.x > meter.x + meter.width, `${at}: ${u}'s Check in now is not after its usage`);
        assert.ok(Math.abs(box.x + box.width - (line.x + line.width)) <= 2, `${at}: ${u}'s Check in now is not at the line's right end`);
        assert.ok(await b.evaluate((x) => x.scrollWidth <= x.clientWidth + 1), `${at}: ${u}'s Check in now is cut`);
      }
      for (const u of ["bob@example.com", "dan@example.com"]) assert.equal(await row(u).locator(".ci-acct").count(), 0, `${at}: ${u} has a Check in now`);
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1), true, `${at}: the page scrolls sideways`);
      await page.screenshot({ path: path.join(process.env.SHOTS || "/tmp", `editor-checkin-account-${engine}-${lang}-${width}.png`) }).catch(() => {});

      // a press checks that account in alone, says so, and reads the row again
      const scrolls = () => page.evaluate(() => [...document.querySelectorAll("*")].filter((x) => x.scrollTop).map((x) => x.className + ":" + x.scrollTop).join(","));
      const before = await scrolls(), reads = ctl.reads;
      await row("ann@example.com").locator(".ci-acct").click();
      await page.waitForFunction((t) => document.querySelector("#status")?.textContent.includes(t), w.done.replace("{who}", "ann@example.com"));
      assert.deepEqual(ctl.asked, [{ by: "trae", key: "trae-cn|1" }], `${at}: what was pressed`);
      await row("ann@example.com").locator(".ci-acct").waitFor({ state: "detached" });
      assert.ok(ctl.reads > reads, `${at}: the row isn't read again`);
      assert.equal(await row("cat@example.com").locator(".ci-acct").count(), 1, `${at}: another account's Check in now went`);
      assert.equal(await scrolls(), before, `${at}: the press scrolled`);
      assert.deepEqual(errors, []);
      await context.close();
    }
  });
}
