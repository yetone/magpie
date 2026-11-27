// Run with Node's test runner and Playwright on the module path; see README.md.
// Share on local network while MAGPIE_ADDR puts the gateway on the network
// (rael_tao on X: the switch said Off and the gateway still listened on
// [::] after a restart). Docker's image and a service that carried
// MAGPIE_ADDR listen on every address whatever the switch says, so the row
// says On, can't be switched, and says what keeps the gateway on this
// computer: in a container, publishing the port on 127.0.0.1; elsewhere,
// MAGPIE_ADDR set to 127.0.0.1 or unset. Off the network, the switch is as
// it was. No backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

function settingsPayload(over) {
  return {
    theme: "light", lang: "en", tray: "panel", quotaLeft: false, currency: "usd",
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.400", dir: "/config/magpie", gateway: "http://0.0.0.0:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [],
    lan: false, lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    ...over,
  };
}

function server(lang, over, posts) {
  const s = settingsPayload({ lang, ...over });
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: s.fx });
    if (url.pathname === "/api/settings/lan") { posts.push(route.request().postDataJSON()); return json(s); }
    if (url.pathname === "/api/settings") return json(s);
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
  en: {
    container: "MAGPIE_ADDR=0.0.0.0:3425 listens on every address in this container: publish the port as -p 127.0.0.1:3425:3425 to keep the gateway on this computer",
    service: "MAGPIE_ADDR=0.0.0.0:3999 has the gateway listen on the network whatever is set here: set it to 127.0.0.1:3999, or unset it, and restart magpie to keep the gateway on this computer",
  },
  zh: {
    container: "MAGPIE_ADDR=0.0.0.0:3425 让网关在这个容器的所有地址上监听：发布端口时写 -p 127.0.0.1:3425:3425，网关就只在这台电脑上",
    service: "MAGPIE_ADDR=0.0.0.0:3999 让网关在网络上监听，与这里的设置无关：把它设为 127.0.0.1:3999 或去掉，再重启 magpie，网关就只在这台电脑上",
  },
  "zh-TW": {}, ja: {}, de: {},
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": MAGPIE_ADDR on the network: Share says On and what turns it off", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const open = async (lang, over, width = 900) => {
      const page = await (await browser.newContext({ viewport: { width, height: 600 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, over, posts));
      await watchSettingsReads(page);
      await page.goto("http://127.0.0.1:3430/?view=settings&tab=network");
      await settingsDrawn(page);
      const row = page.locator("#lanList .row.pref").first();
      await row.waitFor();
      return { page, errors, posts, row };
    };
    const english = {};
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      await t.test(lang, async () => {
        for (const [kind, over] of [
          ["container", { addrEnv: "0.0.0.0:3425", onNetwork: true, inContainer: true }],
          ["service", { addrEnv: "0.0.0.0:3999", onNetwork: true, port: 3999, gateway: "http://0.0.0.0:3999" }],
        ]) {
          for (const width of [900, 380]) {
            const { page, errors, posts, row } = await open(lang, over, width);
            const sub = (await row.locator(".sub").textContent()).trim();
            if (lang === "en") english[kind] = sub;
            if (words[lang][kind]) assert.equal(sub, words[lang][kind]);
            else {
              assert(sub.includes("MAGPIE_ADDR=" + over.addrEnv), sub);
              assert.notEqual(sub, english[kind], lang + " left in English");
            }
            const opts = row.locator(".segs .opt");
            assert.deepEqual(await opts.evaluateAll((bs) => bs.map((b) => [b.dataset.seg, b.getAttribute("aria-pressed"), b.disabled])),
              [["off", "false", true], ["on", "true", true]]);
            // a click as the reader's would land: a disabled button takes none
            await opts.first().evaluate((b) => b.click());
            for (let i = 0; i < 10; i++) await page.waitForTimeout(20);
            assert.deepEqual(posts, [], "a switch that can't turn it off posted");
            // the row's text stays inside the page
            const box = await row.boundingBox();
            assert(box && box.x >= 0 && box.x + box.width <= width + 1, `row overflows at ${width}: ${JSON.stringify(box)}`);
            assert.deepEqual(errors, []);
            await page.context().close();
          }
        }
        // MAGPIE_ADDR on this computer, or unset: the switch is as it was
        for (const over of [{}, { addrEnv: "127.0.0.1:3426", gateway: "http://127.0.0.1:3426" }]) {
          const { page, errors, posts, row } = await open(lang, over);
          const opts = row.locator(".segs .opt");
          assert.deepEqual(await opts.evaluateAll((bs) => bs.map((b) => [b.dataset.seg, b.getAttribute("aria-pressed"), b.disabled])),
            [["off", "true", false], ["on", "false", false]]);
          await opts.nth(1).click();
          for (let i = 0; i < 250 && !posts.length; i++) await page.waitForTimeout(20);
          assert.deepEqual(posts, [{ on: true }]);
          assert.deepEqual(errors, []);
          await page.context().close();
        }
      });
    }
  });
}
