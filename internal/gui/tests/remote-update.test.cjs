// Run with Node's test runner and Playwright on the module path; see README.md.
// nnlrl_75403 on Discord: once magpie updates, the Remote magpies it uses
// are asked to update too. A Remote magpie's row says what came of it:
// updating to the version, or, where it can't, why, with what to run
// there over it (a container's image, a package manager's command). A row
// with nothing to say says nothing. In English, Chinese, Japanese and
// German, Chromium and WebKit, at 1100 and 420 wide.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const remote = (id, name, more = {}) => ({
  id, name, preset: "remote-magpie", icon: "magpie", host: "192.168.1.20:3425", chat: "http://192.168.1.20:3425/v1", responses: "", anthropic: "", catalog: "", routing: "",
  models: [{ id: "own/m1", name: "m1", on: true }], agents: [], fallback: [], headers: {}, keyList: [], key: { set: true, masked: "mg-…a1b2" }, ready: true, ...more,
});

function serve(lang, list) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json({ providers: list, presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const said = {
  en: { updating: "updating to v0.1.200", cant: "can't update itself", off: "automatic updates off", there: "update it there" },
  zh: { updating: "正在更新到 v0.1.200", cant: "无法自行更新", off: "已关闭自动更新", there: "请在那台电脑上更新" },
  ja: { updating: "v0.1.200 に更新中", cant: "自動で更新できません", off: "自動更新はオフ", there: "あちらで更新してください" },
  de: { updating: "wird auf v0.1.200 aktualisiert", cant: "kann sich nicht selbst aktualisieren", off: "automatische Updates aus", there: "dort aktualisieren" },
};
// the keys internal/gui/update_remote.go sends
const strings = [
  "can't update itself",
  "updating to {v}",
  "This magpie updated, so that one was asked to update to {v} too. It restarts into it once nothing is in flight.",
  "automatic updates off",
  "Automatic updates are turned off on that computer, so it stays on its version.",
  "update it there",
  "That magpie is too old to be updated from here: update it on that computer once.",
  "Updating it from here takes a gateway key with full access there; this provider's key is read-only or held.",
  "It runs in a container: pull the new image (docker pull ghcr.io/yetone/magpie:latest) and recreate the container.",
  "{manager} installed it there: update it with {manager}.",
  "{manager} installed it there: update it with {cmd}",
  "It was built from source: update it the way it was built.",
  "magpie can't replace itself where it runs on that computer: update it there.",
];

const rows = [
  remote("home", "Home", { remoteUpdate: { say: "updating to {v}", tip: strings[2], v: "v0.1.200" } }),
  remote("nas", "NAS", { remoteUpdate: { say: "can't update itself", tip: strings[8] } }),
  remote("arch", "Arch", { remoteUpdate: { say: "can't update itself", tip: strings[10], manager: "pacman", cmd: "yay -S magpie-bin" } }),
  remote("lab", "Lab", { remoteUpdate: { say: "automatic updates off", tip: strings[4] } }),
  remote("old", "Old", { remoteUpdate: { say: "update it there", tip: strings[6] } }),
  remote("office", "Office"),
];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 420]) {
    for (const lang of Object.keys(said)) {
      test(`${engine} ${width} ${lang}: a Remote magpie's row says what came of asking it to update`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, rows));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider").first().waitFor();
        const rup = (name) => page.locator(".row.provider", { hasText: name }).first().locator(".sub .rup");
        const s = said[lang];
        for (const [name, text] of [["Home", s.updating], ["NAS", s.cant], ["Arch", s.cant], ["Lab", s.off], ["Old", s.there]]) {
          assert.equal(await rup(name).count(), 1, `${name} says something`);
          assert.equal((await rup(name).textContent()).trim(), text, name);
          const tip = await rup(name).getAttribute("title");
          assert.ok(tip.length > 20, `${name}'s tip says why: ${tip}`);
        }
        assert.match(await rup("NAS").getAttribute("title"), /docker pull ghcr\.io\/yetone\/magpie:latest/);
        assert.match(await rup("Arch").getAttribute("title"), /yay -S magpie-bin/);
        assert.match(await rup("Arch").getAttribute("title"), /pacman/);
        assert.match(await rup("Home").getAttribute("title"), /v0\.1\.200/);
        assert.doesNotMatch(await rup("Arch").getAttribute("title"), /\{(manager|cmd)\}/);
        assert.equal(await rup("Office").count(), 0, "a row with nothing to say says nothing");
        // the line keeps within the row
        const row = await page.locator(".row.provider", { hasText: "NAS" }).first().boundingBox();
        const sub = await page.locator(".row.provider", { hasText: "NAS" }).first().locator(".sub").boundingBox();
        assert.ok(sub.x >= row.x && sub.x + sub.width <= row.x + row.width + 1, `the line fits the row at ${width}`);
        if (lang === "en") {
          const missing = await page.evaluate((ks) => ["zh", "zh-TW", "ja", "de"].flatMap((l) => ks.filter((k) => !I18N[l][k]).map((k) => `${l}: ${k}`)), strings);
          assert.deepEqual(missing, [], "every string has its translations");
        }
        assert.deepEqual(errors, []);
      });
    }
  }
}
