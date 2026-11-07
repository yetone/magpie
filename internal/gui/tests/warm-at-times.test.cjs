// Run with Node's test runner and Playwright on the module path; see README.md.
// #1260 (Sinnhu): Daily warm-up has several times a day, each starting the
// 5-hour windows once (09:00, 15:05, 19:10). A settings payload from before
// the list (codexWarmAt alone) shows its one time; + adds one a window after
// the last, × beside a time takes it away while another is left, a field
// changed posts the list with it, and Off posts none. Each post sends the
// whole list (codexWarmAts) and its first as codexWarmAt, which an older
// magpie reads. The app's own controls (no <select>), nothing runs off the
// side at 440px, a click never moves the page. It is in Codex's provider
// editor (it was in Settings › Usage until the owner moved it on 11-07).
// en, zh, ja, de; the API is faked here, keeping the list as Go's
// settings do (sorted, once each).
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn, provPrefsProviders, openProvPrefs, inEditor } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

function serve(lang, posts) {
  let settings = { theme: "light", lang, currency: "usd", codexWarmup: "", claudeWarmup: "", codexWarmAt: "09:00", claudeWarmAt: "", codexUsers: ["a@example.com"] };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") {
      if (req.method() === "POST") {
        const body = req.postDataJSON();
        posts.push(body);
        settings = { ...settings, ...body };
        for (const k of ["codexWarmAts", "claudeWarmAts"]) {
          const list = [...new Set(settings[k] || [])].sort();
          settings[k] = list.length ? list : undefined;
          settings[k.slice(0, -1)] = list[0] || "";
        }
      }
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
  en: { one: "Starts each account's 5-hour window at this time every day", many: "Starts each account's 5-hour window at each of these times every day", add: "Add a time", rm: "Remove 15:05", off: "Off" },
  zh: { one: "每天到这个时间开启各账号的 5 小时窗口", many: "每天到其中每个时间开启各账号的 5 小时窗口", add: "添加一个时间", rm: "删除 15:05", off: "关闭" },
  ja: { one: "毎日この時刻に各アカウントの 5 時間枠を開始します", many: "毎日これらの各時刻に各アカウントの 5 時間枠を開始します", add: "時刻を追加", rm: "15:05 を削除", off: "オフ" },
  de: { one: "Startet das 5-Stunden-Zeitfenster jedes Kontos täglich zu dieser Uhrzeit", many: "Startet das 5-Stunden-Zeitfenster jedes Kontos täglich zu jeder dieser Uhrzeiten", add: "Uhrzeit hinzufügen", rm: "15:05 entfernen", off: "Aus" },
};

// the reader's locale with their language: WebKit draws a time field in
// the locale's clock (12-hour en-US, 24-hour zh, ja, de), not the machine's
const LOCALE = { en: "en-US", zh: "zh-CN", ja: "ja-JP", de: "de-DE" };

// what of the Daily warm-up row runs off its side, or is squeezed
const sideways = (page) => page.evaluate(() => {
  const out = [];
  const de = document.documentElement, v = document.querySelector(".editor .ebody") || document.querySelector(".editor");
  if (de.scrollWidth > de.clientWidth) out.push("page");
  if (v.scrollWidth > v.clientWidth) out.push("view");
  const row = document.querySelector("#warmAtSegs").closest(".row"), rr = row.getBoundingClientRect();
  // the width a time field takes when nothing presses on it: a copy in a
  // .warm-at of its own, out of the flow, in the same row. A 24-hour
  // "09:00" is ~59px in WebKit and a 12-hour "09:00 AM" ~82px, so the
  // floor is the field's own text, not a fixed width (#1353)
  const natural = (i) => {
    const wrap = document.createElement("div");
    wrap.className = "warm-at";
    wrap.style.cssText = "position:absolute; left:0; top:0; width:max-content; visibility:hidden";
    const c = i.cloneNode();
    c.value = i.value;
    wrap.append(c);
    row.append(wrap);
    const w = c.getBoundingClientRect().width;
    wrap.remove();
    return w;
  };
  for (const e of row.querySelectorAll(".name, .sub, .segs, input, button")) {
    const b = e.getBoundingClientRect();
    if (b.right > rr.right + 0.5 || b.left < rr.left - 0.5) out.push(e.className || e.tagName);
    if (e.tagName === "INPUT" && b.width < natural(e) - 0.5) out.push(`time field squeezed to ${b.width} of ${natural(e)}`);
  }
  const name = row.querySelector(".name").getBoundingClientRect();
  if (name.width < 40) out.push("name squeezed to " + name.width);
  return out;
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "ja", "de"]) {
    const w = W[lang];
    for (const width of [900, 440]) {
      test(`${engine} ${lang} ${width}px: Daily warm-up has several times a day`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        // motion as most readers have it: with Reduce Motion, app.css's
        // .01ms transition on every element puts off the room the page
        // makes at its foot, and a control pressed there isn't held
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, locale: LOCALE[lang] })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posts = [];
        page.on("pageerror", (e) => { if (engine === "webkit" && /due to access control checks/.test(e.message)) return; errors.push(e.message); }); // a goto in WebKit can cut off a fetch (#1350)
        await page.route("**/*", serve(lang, posts));
        await watchSettingsReads(page);
        await openProvPrefs(page, "codex");
        await settingsDrawn(page); // rows found are the ones that stay (#1659)
        const box = page.locator(".editor .prov-prefs #warmAtSegs"), fields = box.locator("input.at");
        await fields.first().waitFor();
        const times = () => fields.evaluateAll((is) => is.map((i) => i.value));
        const posted = async (n, want) => {
          for (let i = 0; i < 50 && posts.length < n; i++) await page.waitForTimeout(40);
          assert.equal(posts.length, n, "posts");
          assert.deepEqual(posts.at(-1).codexWarmAts, want, "codexWarmAts");
          assert.equal(posts.at(-1).codexWarmAt, want[0] || "", "codexWarmAt, for an older magpie");
          await page.waitForFunction((n) => document.querySelectorAll("#warmAtSegs input.at").length === n, want.length);
        };
        // the one time a payload from before the list has
        assert.deepEqual(await times(), ["09:00"]);
        assert.equal(await box.locator(".warm-rm").count(), 0, "the last time has no ×");
        assert.equal((await page.locator("#warmAtSub").textContent()).trim(), w.one);
        assert.deepEqual(await sideways(page), []);

        await inEditor(page, box.locator(".warm-add"));
        // a reader's click, on what is in sight (Playwright's own would
        // scroll it there first): what was clicked, or what is drawn in its
        // place (after, by default the same), stays where it was on the
        // screen while the row redraws around it
        const press = async (loc, what, after = loc) => {
          const b = await loc.boundingBox(), vh = page.viewportSize().height, was = await after.boundingBox();
          assert(b && b.y >= 0 && b.y + b.height <= vh, `${what} in sight: ${JSON.stringify(b)}`);
          await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
          return async () => {
            await page.waitForTimeout(200);
            const a = await after.boundingBox();
            assert(a && Math.abs(a.y - was.y) <= 1, `${what} moved on the screen: ${was.y} → ${a?.y}`);
          };
        };
        const set = (i, v) => fields.nth(i).evaluate((e, v) => { e.value = v; e.dispatchEvent(new Event("change", { bubbles: true })); }, v);
        // + adds one a window after the last
        let still = await press(box.locator(".warm-add"), "+");
        await posted(1, ["09:00", "14:00"]);
        await still();
        assert.equal(await box.locator(".warm-add").getAttribute("title"), w.add);
        assert.equal((await page.locator("#warmAtSub").textContent()).trim(), w.many);
        // a field changed: the list with it
        await set(1, "15:05");
        await posted(2, ["09:00", "15:05"]);
        await inEditor(page, box.locator(".warm-add"));
        still = await press(box.locator(".warm-add"), "+");
        await posted(3, ["09:00", "15:05", "20:05"]);
        await still();
        assert.deepEqual(await times(), ["09:00", "15:05", "20:05"]);
        assert.deepEqual(await sideways(page), []);
        await inEditor(page, box);
        if (process.env.ARTIFACT_DIR) await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `warm-at-times-${engine}-${lang}-${width}.png`) });
        // × takes one away
        const rm = box.locator(".warm-rm").nth(1);
        assert.equal(await rm.getAttribute("title"), w.rm);
        await inEditor(page, rm);
        still = await press(rm, "×", box.locator(".warm-time").first());
        await posted(4, ["09:00", "20:05"]);
        await still();
        // Off: none
        await inEditor(page, box.locator(".segs .opt", { hasText: w.off }));
        still = await press(box.locator(".segs .opt", { hasText: w.off }), "Off");
        await posted(5, []);
        await still();
        assert.equal(await box.locator(".warm-add").count(), 0);
        assert.equal(await page.locator("select").count(), 0);
        assert.deepEqual(errors, []);
      });
    }
  }
}
