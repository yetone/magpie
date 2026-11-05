// Run with Node's test runner and Playwright on the module path; see README.md.
// hanslin1666 on Discord: B uses A through a Remote magpie, and sync
// carried that provider to A, where it points at A itself. magpie now
// finds it (the list it fetches was listed by this magpie) and leaves it
// out. Its row says "This magpie", its editor says why, and "Use it
// anyway" keeps it in: ticking posts provider/keepself, the redraw keeps
// it ticked, unticking posts it off. Another Remote magpie says nothing.
// The page doesn't move. In English, Chinese, Japanese and German,
// Chromium and WebKit, at 1100 and 420 wide.
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

function serve(lang, first, posts) {
  let list = first;
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  const providers = () => ({ providers: list, presets: [], excluded: [], gateway: { running: true, window: true } });
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers());
    if (url.pathname === "/api/provider/keepself") {
      const body = route.request().postDataJSON();
      posts.push(body);
      list = list.map((p) => p.id === body.id ? { ...p, keepSelf: body.keepSelf } : p);
      return json(providers());
    }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const said = {
  en: ["This magpie", "Use it anyway"],
  zh: ["本机 magpie", "仍然使用"],
  ja: ["この magpie", "それでも使う"],
  de: ["Dieses magpie", "Trotzdem nutzen"],
};
const strings = [
  "This magpie",
  "This is this magpie itself, used anyway: its models are listed twice, and a request through it passes here twice.",
  "This is this magpie itself, brought by sync from a computer that uses it. It is left out, so its models aren't listed twice; a request for one is served here.",
  "Use it anyway",
  "{name} is used anyway",
  "{name} is left out: it is this magpie",
];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 420]) {
    for (const lang of Object.keys(said)) {
      const open = async (t, list) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, list, posts));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider").first().waitFor();
        return { page, errors, posts };
      };

      test(`${engine} ${width} ${lang}: a Remote magpie that is this magpie says so, and Use it anyway keeps it`, async (t) => {
        const { page, errors, posts } = await open(t, [remote("home", "Home", { self: true }), remote("office", "Office")]);
        const row = page.locator(".row.provider", { hasText: "Home" }).first();
        const badge = row.locator(".name .badge", { hasText: said[lang][0] });
        assert.equal(await badge.count(), 1, "the row says it is this magpie");
        assert.ok((await badge.getAttribute("title")).length > 20, "the badge says why");
        assert.match(await badge.getAttribute("class"), /\boff\b/, "left out, it reads as off");
        assert.equal(await page.locator(".row.provider", { hasText: "Office" }).locator(".name .badge").count(), 0, "another Remote magpie says nothing");
        if (lang === "en") {
          const missing = await page.evaluate((ks) => ["zh", "zh-TW", "ja", "de"].flatMap((l) => ks.filter((k) => !I18N[l][k]).map((k) => `${l}: ${k}`)), strings);
          assert.deepEqual(missing, [], "every string has its translations");
        }
        await row.click();
        const note = page.locator(".editor .self-note");
        await note.waitFor();
        const keep = note.locator("label.tick");
        assert.equal((await keep.textContent()).trim(), said[lang][1]);
        assert.equal(await keep.locator("input").isChecked(), false);
        assert.ok((await note.textContent()).length > said[lang][1].length + 20, "the note says why");
        await keep.scrollIntoViewIfNeeded();
        const box = await note.boundingBox();
        assert.ok(box.x >= 0 && box.x + box.width <= width, `the note fits across ${width}`);
        const scrollers = () => page.evaluate(() => [...document.querySelectorAll("*")].filter((e) => e.scrollTop).map((e) => e.scrollTop).concat(document.scrollingElement.scrollTop));
        const before = await scrollers();
        await keep.locator("input").click();
        await page.waitForFunction(() => document.querySelector(".editor .self-note input")?.checked === true);
        assert.deepEqual(posts, [{ id: "home", keepSelf: true }]);
        await page.waitForFunction(() => [...document.querySelectorAll(".row.provider[data-id=home] .name .badge")].some((b) => b.title && !b.classList.contains("off")));
        assert.deepEqual(await scrollers(), before, "the page doesn't move");
        assert.doesNotMatch(await badge.getAttribute("class"), /\boff\b/, "kept, it no longer reads as off");
        await page.locator(".editor .self-note input").click();
        await page.waitForFunction(() => document.querySelector(".editor .self-note input")?.checked === false);
        await page.waitForFunction(() => document.querySelector(".row.provider[data-id=home] .name .badge[title].off"));
        assert.deepEqual(posts, [{ id: "home", keepSelf: true }, { id: "home", keepSelf: false }]);
        await page.keyboard.press("Escape");
        await page.locator(".editor").waitFor({ state: "detached" });
        await page.locator(".row.provider", { hasText: "Office" }).first().click();
        await page.locator(".editor .ehead", { hasText: "Office" }).waitFor();
        assert.equal(await page.locator(".editor .self-note").count(), 0, "another Remote magpie's editor has no note");
        assert.deepEqual(errors, []);
      });
    }
  }
}
