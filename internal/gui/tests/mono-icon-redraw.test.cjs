// Run with Node's test runner and Playwright on the module path; see README.md.
// A mono logo (a CSS mask, e.g. Command Code's) blinked in the routing
// group editor on every redraw while a colour logo (an <img>) beside it
// didn't (#1847). A mask span made afresh keeps nothing of the picture,
// and icon() made a probe Image for every logo, asking for the file again
// on a redraw. Now each mask picture is probed once per page, not once per
// logo, and once loaded a hidden shelf keeps a user of it for as long as
// the page is open, so a logo drawn again paints at once. The rows drawn
// again (the editor's own redraw and drawGroups' replaceKeeping) still
// show the same logo; a logo with no file still shows the plain outline,
// the first time and after a redraw; the shelf takes no room and is not
// read out. Every mask picture on every page goes through maskSpan. In
// English and Chinese, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { id: "cc/deepseek-v4", name: "deepseek-v4", providerName: "Command Code", icon: "commandcode" },
  { id: "ds/deepseek-v4", name: "deepseek-v4", providerName: "DeepSeek", icon: "deepseek-color" },
  { id: "zz/deepseek-v4", name: "deepseek-v4", providerName: "Nowhere", icon: "no-such-logo" },
];
const members = models.map((m) => m.id);
const groups = { models, pools: [], groups: [{ id: "ds", name: "DeepSeek V4", members, fast: [], off: [], routing: "order", ready: true,
  memberInfo: members.map((id) => ({ id, ready: true })) }] };
const words = { en: { edit: "Edit" }, zh: { edit: "编辑" } };

function serve(lang, asked) {
  const state = { agents: [{ id: "codex", name: "Codex", path: "/test/codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (r) => {
    const url = new URL(r.request().url());
    const json = (data) => r.fulfill({ json: data });
    if (url.pathname === "/boot.js") return r.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return r.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) return new Promise(() => {});
      return json({ mine: true, now: new Date().toISOString(), seq: 0, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/groups") return json(groups);
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true, url: "http://127.0.0.1:3999" } });
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.pathname.startsWith("/icons/")) asked.push(url.pathname);
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    let body;
    try { body = await fs.readFile(file); } catch { return r.fulfill({ status: 404, body: "" }); }
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await r.fulfill({ body, contentType, headers: { "Cache-Control": "no-cache" } });
  };
}

test("every mask picture is drawn through maskSpan", async () => {
  // a page that sets a mask's --i itself makes a logo that blinks again
  const at = [];
  // the plugins' pages too (assets/plugins/<id>/), and maskSpan's own
  // picture, named in full once (pic)
  for (const f of await fs.readdir(assets, { recursive: true })) {
    if (!f.endsWith(".js")) continue;
    const src = await fs.readFile(path.join(assets, f), "utf8");
    src.split("\n").forEach((line, i) => { if (/setProperty\(\s*["']--i["']\s*,\s*([`"']url\(|pic\))/.test(line)) at.push(`${f}:${i + 1}`); });
  }
  assert.equal(at.length, 2, `only maskSpan sets a mask's picture (its span and its shelf's): ${at.join(", ")}`);
  const app = await fs.readFile(path.join(assets, "app.js"), "utf8");
  const start = app.indexOf("function maskSpan("), end = app.indexOf("\n}\n", start);
  for (const a of at) {
    const line = Number(a.split(":")[1]);
    const off = app.split("\n").slice(0, line - 1).join("\n").length;
    assert.ok(a.startsWith("app.js:") && off > start && off < end, `${a} sets a mask's picture outside maskSpan`);
  }
});

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: a mono logo drawn again is probed once and kept on the shelf`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const page = await (await browser.newContext({ viewport: { width: 1000, height: 1000 }, reducedMotion: "reduce" })).newPage();
      t.after(() => browser.close());
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], asked = [];
      page.on("pageerror", (e) => errors.push(e.message));
      // every src given to an <img> not in the page: icon()'s probes
      await page.addInitScript(() => {
        window.probes = {};
        const d = Object.getOwnPropertyDescriptor(HTMLImageElement.prototype, "src");
        Object.defineProperty(HTMLImageElement.prototype, "src", { ...d, set(v) {
          if (!this.isConnected) { const k = String(v).replace(/^.*\bicons\//, ""); window.probes[k] = (window.probes[k] || 0) + 1; }
          return d.set.call(this, v);
        } });
      });
      await page.route("**/*", serve(lang, asked));
      await page.goto("http://magpie.test/?view=groups");
      const card = page.locator(".rt-group", { hasText: "DeepSeek V4" });
      await card.waitFor();
      await card.locator("button", { hasText: words[lang].edit }).click();
      const rows = page.locator(".rt-gedit .fbrow");
      await rows.nth(2).waitFor();
      // Command Code's picture tried and loaded before the rows are drawn again
      await page.waitForFunction(() => window.probes["commandcode.svg"] >= 1);
      await page.waitForTimeout(300);
      await page.waitForFunction(() => document.querySelector(".rt-gedit .fbrow:nth-child(3) .ic")?.classList.contains("generic"));
      const look = () => page.evaluate(() => [...document.querySelectorAll(".rt-gedit .fbrow")].slice(0, 3).map((r) => {
        const ic = r.querySelector(".ic");
        return [ic.className, ic.querySelector(".mask")?.style.getPropertyValue("--i") || ic.querySelector("img")?.getAttribute("src") || ic.querySelector("svg") && "svg"];
      }));
      const before = await look();
      // maskSpan names the picture in full, as a plugin's stylesheet would
      // read a bare name against its own folder
      const ccPic = `url("${new URL("/icons/commandcode.svg", page.url()).href}")`;
      assert.deepEqual(before, [["ic", ccPic], ["ic", "icons/deepseek-color.svg"], ["ic generic", "svg"]]);
      const askedBefore = asked.filter((p) => p === "/icons/commandcode.svg").length;

      // drawn again, both ways: the editor's own redraw (a member switched
      // off and on) and drawGroups' (the page's words said again)
      const first = await rows.first().locator(".ic").elementHandle();
      for (let i = 0; i < 6; i++) {
        await page.evaluate(() => document.querySelectorAll(".rt-gedit .fbrow")[1].querySelector("button.rt-mon").click());
        await page.evaluate((i) => { document.documentElement.lang = i % 2 ? document.documentElement.lang.replace(/-x$/, "") : document.documentElement.lang + "-x"; }, i);
        await page.waitForTimeout(30);
      }
      await page.waitForTimeout(300);
      assert.equal(await first.evaluate((e) => e.isConnected), false, "the rows were drawn afresh");
      assert.deepEqual(await look(), before, "a logo drawn again looks as it did");

      const probes = await page.evaluate(() => window.probes);
      assert.equal(probes["commandcode.svg"], 1, "Command Code's picture is probed once, not once per logo drawn");
      assert.equal(probes["no-such-logo.svg"], 1, "a missing picture is probed once too");
      assert.equal(asked.filter((p) => p === "/icons/commandcode.svg").length, askedBefore, "drawn again, the picture isn't asked for again");
      const st = await page.evaluate(() => {
        const shelf = document.querySelector(".mask-shelf"), r = shelf.getBoundingClientRect(), cs = getComputedStyle(shelf);
        const ms = [...shelf.querySelectorAll(".mask")];
        return { shelved: ms.map((m) => m.style.getPropertyValue("--i")),
          painted: ms.map((m) => { const s = getComputedStyle(m); return s.maskImage || s.webkitMaskImage; }),
          box: [r.width, r.height], visibility: cs.visibility, hidden: shelf.getAttribute("aria-hidden"), parent: shelf.parentElement.tagName,
          scroll: [document.documentElement.scrollWidth <= innerWidth] };
      });
      const cc = st.shelved.indexOf(ccPic);
      assert.ok(cc >= 0, "the shelf keeps Command Code's picture");
      assert.equal(new Set(st.shelved).size, st.shelved.length, "each picture once");
      assert.ok(!st.shelved.some((s) => s.includes("no-such-logo")), "nothing that didn't load");
      assert.match(st.painted[cc], /icons\/commandcode\.svg/, "the shelf's span uses the picture");
      assert.deepEqual(st.box, [0, 0]);
      assert.equal(st.visibility, "hidden");
      assert.equal(st.hidden, "true");
      assert.equal(st.parent, "BODY");
      assert.deepEqual(st.scroll, [true]);
      // the plain outline still stands in for a logo with no file, drawn anew
      assert.equal((await look())[2][0], "ic generic");
      assert.deepEqual(errors, []);
    });
  }
}
