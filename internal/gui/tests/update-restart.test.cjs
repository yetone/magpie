// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings → Restart automatically (#2096): off by default; on, a downloaded
// update is restarted into without cutting off an agent request. The switch
// is saved as autoRestart and kept across a reload, with Automatic updates
// and Update check interval left as they were. With automatic updates off it is kept,
// dimmed, and says it works while they are on. No click moves the page, and
// the row fits a narrow window. Every language, both engines, at 1100 and
// 420 wide; no backend, the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn, openPart } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

const words = {
  en: { auto: "Automatic updates", restart: "Restart automatically", whileOn: "While automatic updates are on", what: "Once an update is downloaded, restarts into it without cutting off an agent's request" },
  zh: { auto: "自动更新", restart: "自动重启更新", whileOn: "自动更新开启时生效", what: "新版本下载好后自动重启到新版本，不会中断 Agent 的请求" },
  "zh-TW": { auto: "自動更新", restart: "自動重新啟動更新", whileOn: "自動更新開啟時生效", what: "新版本下載完成後自動重新啟動到新版本，不會中斷 Agent 的請求" },
  ja: { auto: "自動アップデート", restart: "自動で再起動", whileOn: "自動アップデートがオンのとき", what: "アップデートのダウンロード後、エージェントのリクエストを中断せずに再起動して適用します" },
  de: { auto: "Automatische Updates", restart: "Automatisch neu starten", whileOn: "Solange automatische Updates aktiv sind", what: "Startet nach dem Download eines Updates neu, ohne eine Agent-Anfrage abzubrechen" },
};

function settingsPayload(lang, ctl) {
  const s = {
    theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd",
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.400", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
  };
  // as the backend sends them: left out while they are their defaults
  if (ctl.off) s.noAutoUpdate = true;
  if (ctl.every) s.updateEvery = ctl.every;
  if (ctl.restart) s.autoRestart = true;
  return s;
}

function server(lang, ctl) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: settingsPayload(lang, ctl), fx: settingsPayload(lang, ctl).fx });
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") {
        const body = req.postDataJSON();
        ctl.off = !!body.noAutoUpdate;
        ctl.every = body.updateEvery === 360 ? 0 : body.updateEvery;
        ctl.restart = !!body.autoRestart;
        ctl.saves.push(body);
      }
      return json(settingsPayload(lang, ctl));
    }
    if (url.pathname === "/api/update/check") {
      ctl.checks++;
      ctl.state = "latest";
      return json({ state: "latest", current: "0.1.400", latest: "0.1.400" });
    }
    if (url.pathname === "/api/update") return json({ state: ctl.state, current: "0.1.400" });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// wheel a row into sight, as a user would; a click must then not move the page
async function showRow(page, row) {
  await page.locator("#view-settings").hover();
  for (let i = 0; i < 40; i++) {
    const box = await row.boundingBox();
    const view = await page.locator("#view-settings").boundingBox();
    if (box && view && box.y >= view.y + 4 && box.y + box.height <= view.y + view.height - 4) break;
    await page.mouse.wheel(0, 180);
    await page.waitForTimeout(16);
  }
  await page.waitForTimeout(250);
}
const scrolls = (page) => page.evaluate(() => [window.scrollY, document.scrollingElement.scrollTop, ...[...document.querySelectorAll(".view")].map((v) => v.scrollTop)].join(","));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": restart automatically into a downloaded update", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const width of [1100, 420]) for (const lang of Object.keys(words)) {
      const w = words[lang];
      await t.test(`${lang} at ${width}: off by default, saved on, kept, dimmed with automatic updates off`, async () => {
        const ctl = { off: false, every: 0, restart: false, state: "", checks: 0, saves: [] };
        const errors = [];
        const page = await (await browser.newContext({ viewport: { width, height: 640 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => { if (!/^ResizeObserver loop completed with undelivered notifications/.test(e.message)) errors.push(e.message); });
        await page.route("**/*", server(lang, ctl));
        await watchSettingsReads(page);
        await page.goto("http://magpie.test/?view=settings");
        await settingsDrawn(page);
        const aboutTab = async () => {
          await openPart(page, "about");
          await page.locator("#setPage-about").waitFor({ state: "visible" });
        };
        await aboutTab();
        const rowOf = (name) => page.locator("#about .row.pref", { has: page.locator(".name", { hasText: new RegExp("^" + name + "$") }) });
        const auto = rowOf(w.auto), restart = rowOf(w.restart);
        await restart.waitFor();
        await restart.locator(".sub", { hasText: w.what }).waitFor();
        assert.equal(await restart.locator(".opt").count(), 2);
        assert.equal(await restart.locator(".opt").nth(0).evaluate((o) => o.classList.contains("on")), true, "off by default");

        // on: saved, nothing else changed
        await showRow(page, restart);
        let before = await scrolls(page);
        await restart.locator(".opt").nth(1).click();
        await page.waitForFunction(() => document.querySelectorAll(".update-restart-row .opt")[1]?.classList.contains("on"));
        assert.equal(ctl.saves.at(-1).autoRestart, true);
        assert.equal(ctl.saves.at(-1).noAutoUpdate, false);
        assert.equal(ctl.saves.at(-1).updateEvery, 360);
        await page.waitForTimeout(400); // the save's answer draws the page again
        assert.equal(await scrolls(page), before, "the click must not scroll the page");
        // the row fits the window, the switch in sight
        // the save's answer draws Settings again: measure the row as drawn then
        let box, segs;
        for (let i = 0; i < 50 && !(box && segs); i++) {
          await page.waitForTimeout(40);
          box = await restart.boundingBox().catch(() => null);
          segs = await restart.locator(".segs").boundingBox().catch(() => null);
        }
        assert.ok(box && segs, "the row isn't drawn");
        assert.ok(box.x >= 0 && box.x + box.width <= width + 0.5, `row ${box.x}+${box.width} past ${width}`);
        assert.ok(segs.x >= box.x && segs.x + segs.width <= box.x + box.width + 0.5, "switch past its row");
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, "the page scrolls sideways");

        // kept after a reload
        await page.reload();
        await aboutTab();
        await restart.waitFor();
        assert.equal(await restart.locator(".opt").nth(1).evaluate((o) => o.classList.contains("on")), true);

        // automatic updates off: kept, dimmed, says when it works
        // (the version row above then says automatic updates are off, a
        // line that grows it about 12px: what is held is the control
        // clicked, where it is on the screen, unless the view is at its
        // top, which stays at its top and lets the switch move down by
        // less than its height, #2134)
        await showRow(page, auto);
        const settings = page.locator("#view-settings");
        const atTop = await settings.evaluate((v) => v.scrollTop < 1);
        const y0 = (await auto.locator(".opt").nth(0).boundingBox()).y;
        await auto.locator(".opt").nth(0).click();
        await restart.locator(".sub", { hasText: w.whileOn }).waitFor();
        assert.equal(await restart.evaluate((r) => r.classList.contains("off")), true);
        assert.equal(ctl.saves.at(-1).noAutoUpdate, true);
        assert.equal(ctl.saves.at(-1).autoRestart, true);
        await page.waitForTimeout(400);
        const y1 = (await auto.locator(".opt").nth(0).boundingBox()).y;
        // (the Version row's line wraps whole, arculongus on Discord: at a
        // narrow width it can grow the row past the switch's height, and
        // then the switch is held where it was clicked)
        const held = Math.abs(y1 - y0) <= 1;
        if (atTop && !held) {
          assert.equal(await settings.evaluate((v) => v.scrollTop), 0, "a view at its top stays at its top");
          const tall = (await auto.locator(".opt").nth(0).boundingBox()).height;
          assert.ok(y1 - y0 >= -1 && y1 - y0 < tall, `the clicked switch moved ${y1 - y0}px, its height ${tall}px`);
        } else assert.ok(Math.abs(y1 - y0) <= 1, `the clicked switch moved ${y1 - y0}px`);

        // and off again
        await showRow(page, restart);
        before = await scrolls(page);
        await restart.locator(".opt").nth(0).click();
        await page.waitForFunction(() => document.querySelectorAll(".update-restart-row .opt")[0]?.classList.contains("on"));
        assert.equal(ctl.saves.at(-1).autoRestart, false);
        await page.waitForTimeout(400);
        assert.equal(await scrolls(page), before, "the click must not scroll the page");
        assert.deepEqual(errors, []);
        await page.context().close();
      });
    }
  });
}
