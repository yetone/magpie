// Run with Node's test runner and Playwright on the module path; see README.md.
// canlee886 on X (0.1.4582, 0.1.4617 downloaded): Settings' Restart to
// update did nothing. The restart asks the feed first, and with the feed
// slow, or a newer version out to download, the app now carries the click's
// restart through itself and says so ("checking", waiting). The row shows
// the check and the download, never Restart to update again as if nothing
// was clicked, and once the app is gone says it is restarting. The Update
// pill follows the same answer. Every language, both engines; no backend,
// the API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn } = require("./reader.cjs");
const { strings } = require("./strings.cjs");

const assets = path.resolve(__dirname, "../assets");
const { I18N } = strings();
const tr = (lang, k, v = {}) => ((lang === "en" ? k : I18N[lang][k]) || k).replace(/\{(\w+)\}/g, (_, n) => v[n]);

function settingsPayload(lang) {
  return {
    theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd",
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.4582", dir: "~/.config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [], lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
  };
}

// ctl.reads: what /api/update answers after the click, one a read, the
// last kept; "gone" is the app restarted (no answer)
function server(lang, ctl) {
  const cur = settingsPayload(lang);
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: cur, fx: cur.fx });
    if (url.pathname === "/api/settings") return json(cur);
    if (url.pathname === "/api/update") {
      if (!ctl.clicked) return json(ctl.update);
      const next = ctl.reads.length > 1 ? ctl.reads.shift() : ctl.reads[0];
      ctl.seen.push(next === "gone" ? "gone" : next.state + (next.waiting ? "+waiting" : ""));
      return next === "gone" ? route.abort("connectionrefused") : json(next);
    }
    if (url.pathname === "/api/update/install") {
      ctl.installs.push(req.postDataJSON() || {});
      ctl.clicked = true;
      return json(ctl.answer);
    }
    if (url.pathname === "/api/update/notes") return json({ notes: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const base = { current: "0.1.4582", url: "https://example.invalid/v0.1.4620" };
const ctlFor = () => {
  const checking = { ...base, state: "checking", latest: "0.1.4617", waiting: true };
  const downloading = { ...base, state: "downloading", latest: "0.1.4620", waiting: true, done: 4e6, total: 4e7 };
  return {
    update: { ...base, state: "ready", latest: "0.1.4617" },
    answer: checking,
    reads: [checking, checking, downloading, downloading, "gone"],
    installs: [], seen: [], clicked: false,
  };
};
const confirmAsk = (page) => page.locator("#modal .update-ask button.primary").click();

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a restart to update that waits on the feed is carried through", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const open = async (lang, ctl, query, width) => {
      const errors = [];
      const page = await (await browser.newContext({ viewport: { width, height: 560 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(8000);
      page.setDefaultNavigationTimeout(60000);
      page.on("pageerror", (e) => { if (!/^ResizeObserver loop completed with undelivered notifications/.test(e.message)) errors.push(e.message); });
      await page.route("**/*", server(lang, ctl));
      await watchSettingsReads(page);
      await page.goto("http://magpie.test/" + query);
      if (query.includes("settings")) await settingsDrawn(page);
      return { page, errors };
    };

    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      await t.test(lang + ": Settings' row checks, downloads and restarts, never offering the click again", async () => {
        const ctl = ctlFor();
        const { page, errors } = await open(lang, ctl, "?view=settings&tab=about", lang === "de" ? 420 : 900);
        const row = page.locator("#about .row.pref", { has: page.locator(".name", { hasText: tr(lang, "Version") }) }).first();
        const sub = row.locator(".sub");
        const restart = tr(lang, "Restart to update");
        await row.locator("button", { hasText: restart }).waitFor();
        assert.equal((await sub.textContent()).trim(), tr(lang, "{v} is downloaded", { v: "0.1.4617" }));

        await row.locator("button", { hasText: restart }).click();
        await confirmAsk(page);
        // the click's answer drawn
        const first = tr(lang, "{v} is downloaded", { v: "0.1.4617" });
        for (let i = 0; i < 100 && (!ctl.installs.length || (await sub.textContent()).trim() === first); i++) await page.waitForTimeout(20);
        // each redraw until the app is gone: no Restart to update on offer
        const shown = new Set();
        const restarting = tr(lang, "Restarting…");
        for (let i = 0; i < 160; i++) {
          const s = (await sub.textContent()).trim();
          shown.add(s);
          const buttons = await row.locator(".val button").allTextContents();
          assert.ok(!buttons.includes(restart), `Restart to update offered again after the click: ${s} (${ctl.seen})`);
          if (s === restarting) break;
          await page.waitForTimeout(50);
        }
        assert.deepEqual(ctl.installs, [{ view: "settings" }], "one click, one install");
        assert.ok(shown.has(tr(lang, "Checking for updates…")), [...shown].join(" | "));
        assert.ok([...shown].some((s) => s.startsWith(tr(lang, "Downloading {v}…", { v: "0.1.4620" }))), [...shown].join(" | "));
        assert.equal((await sub.textContent()).trim(), restarting, `the row after the app went: ${[...shown].join(" | ")} (${ctl.seen})`);
        assert.deepEqual(await row.locator(".val button").allTextContents(), []);
        assert.deepEqual(errors, []);
      });
    }

    for (const lang of ["en", "zh"]) {
      await t.test(lang + ": the Update pill shows the download, then the wait, without asking again", async () => {
        const ctl = ctlFor();
        const ready = { ...base, state: "ready", latest: "0.1.4620", waiting: true };
        ctl.reads = [ctl.answer, ctl.reads[2], ready];
        const { page, errors } = await open(lang, ctl, "?mode=panel", 420);
        const pill = page.locator("#update"), label = pill.locator("span").first();
        await pill.waitFor({ state: "visible" });
        await pill.click(); // the panel asks nothing first
        await page.waitForFunction((t) => document.querySelector("#update span").textContent.trim() === t, tr(lang, "Waiting to update"));
        assert.deepEqual(ctl.installs, [{}], "the pill doesn't ask to restart a second time");
        assert.ok(ctl.seen.includes("downloading+waiting"), String(ctl.seen));
        assert.deepEqual(errors, []);
      });
    }
  });
}
