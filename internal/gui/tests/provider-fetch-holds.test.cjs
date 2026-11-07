// Run with Node's test runner and Playwright on the module path; see README.md.
// Fetch models again leaves the provider's dialog where it was (indexyz on
// Discord: 打开供应商列表点重新获取模型整个 Modal 会往下移动). The redraw
// that brings the list back built the models' "Expand the list" fold shown
// and hid it a frame later, once measured; the dialog, holding the pressed
// button's place, took the frame with the fold as that place and moved down
// by its height when it went. Every frame from the press until the list is
// back is sampled: the dialog and the button stay put, and the fold is never
// shown or hidden for a frame, for a list short enough to need no fold and
// one long enough to need it. Chinese and English, Chromium and WebKit, wide
// and narrow, with motion on as the reporter has it.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.resolve(__dirname, "../assets"));
const fetched = new Date(Date.now() - 3600e3).toISOString();
const ids = (n) => Array.from({ length: n }, (_, i) => `relay-model-${i}`);
const relay = (models, on) => ({
  id: "relay", name: "Some Relay", icon: "generic", host: "relay.example.com", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: models.map((m) => ({ id: m, name: m, on: on.includes(m) })), chosen: on, fetched, agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…aaa" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
});

function serve(lang, p, calls) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/providers") return json({ providers: [p], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/provider/models") {
      calls.push(req.postDataJSON());
      // the vendor takes a moment, as a real one does; the same list comes back
      await new Promise((r) => setTimeout(r, 300));
      return json({ count: p.models.length, provider: p });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

// a few picks: the chips fit, no fold; none picked out of 150: every chip, folded
const lists = { picked: relay(ids(6), ["relay-model-0", "relay-model-1"]), long: relay(ids(150), []) };
const sizes = { zh: [[900, 820], [420, 820]], en: [[1300, 900]] };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [lang, views] of Object.entries(sizes)) {
    for (const [width, height] of views) {
      for (const [kind, p] of Object.entries(lists)) {
        test(`${engine} ${lang} ${width}px ${kind}: Fetch models again leaves the dialog where it was`, async (t) => {
          const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
          const page = await (await browser.newContext({ viewport: { width, height } })).newPage();
          t.after(async () => {
            if (process.env.ARTIFACT_DIR) {
              await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
              await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-${kind}-provider-fetch-holds.png`) });
            }
            await browser.close();
          });
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          const calls = [];
          await page.route("**/*", serve(lang, p, calls));
          await page.goto("http://magpie.test/?view=providers");
          const again = await page.evaluate(([l, s]) => (l === "en" ? s : I18N[l][s]), [lang, "Fetch models again"]);
          assert.ok(again, `Fetch models again has its ${lang}`);

          await page.locator(".row.provider", { hasText: "Some Relay" }).click();
          const editor = page.locator("#modal:not([hidden]) .editor");
          await editor.locator(".mchips .mchip").first().waitFor();
          const button = editor.locator(".mfoot").getByRole("button", { name: again, exact: true });
          await button.scrollIntoViewIfNeeded();
          // opened and settled: no animation left on the dialog
          await page.waitForFunction(() => {
            const d = document.querySelector("#modal").firstElementChild;
            return !d.getAnimations({ subtree: true }).some((a) => a.playState === "running");
          });
          await page.waitForTimeout(300);
          const fold = editor.locator(".mchips-fold");
          assert.equal(await fold.isVisible(), kind === "long", "the fold is there only for a list that needs it");

          // every frame from the press until a while after the list is back
          await page.evaluate(() => {
            const d = document.querySelector("#modal").firstElementChild;
            window.frames_ = [];
            const go = () => {
              const b = d.querySelector(".mfoot .fetch-models"), f = d.querySelector(".mchips-fold");
              window.frames_.push({ top: d.getBoundingClientRect().top, button: b?.getBoundingClientRect().top, fold: f ? !f.hidden : null });
              if (!window.framesStop) requestAnimationFrame(go);
            };
            go();
            d.querySelector(".mfoot .fetch-models").dataset.old = "";
          });
          const before = await page.evaluate(() => window.frames_[0]);
          await button.click();
          for (let i = 0; i < 100 && !calls.length; i++) await page.waitForTimeout(20);
          assert.equal(calls.length, 1, "the list is asked for");
          // the list is back: the editor drawn again, its button a new one
          await editor.locator(".mfoot .fetch-models:not([data-old])").waitFor();
          await page.waitForTimeout(800);
          const frames = await page.evaluate(() => { window.framesStop = true; return window.frames_; });
          assert.ok(frames.length > 10, "frames were sampled");
          assert.ok(await button.isVisible(), "the button is still there");
          // the redraw's own soft swap moves it 2px at most
          const moved = (k) => Math.max(...frames.map((f) => Math.abs(f[k] - before[k])));
          assert.ok(moved("top") <= 3, `the dialog moved ${moved("top").toFixed(1)}px (${frames.map((f) => Math.round(f.top)).join(",")})`);
          assert.ok(moved("button") <= 3, `the button moved ${moved("button").toFixed(1)}px`);
          assert.deepEqual([...new Set(frames.map((f) => f.fold))], [before.fold], "the fold never came or went for a frame");
          assert.deepEqual(errors, []);
        });
      }
    }
  }
}
