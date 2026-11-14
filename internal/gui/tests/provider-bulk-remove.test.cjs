// Run with Node's test runner and Playwright on the module path; see README.md.
// Providers removed several at once (eiyuuzaki2333 on X: many API providers
// added, and removing them one at a time is tedious). Select, by the search
// box, puts a box on every row, on and off; a click on a row picks it
// rather than open its editor, and Shift picks the rows between. Remove
// asks once, in the app's own dialog, naming every provider it removes and
// how many, before it posts providers/delete {ids}; Cancel removes nothing.
// The ones not picked stay, and Escape ends picking. Every language,
// Chromium and WebKit, a narrow and a wide window; no backend, the API is
// faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const prov = (id, name, off) => ({
  id, name, off, icon: "generic", chat: "https://" + id + ".example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "m1", name: "m1", on: true }], agents: [], fallback: [], headers: {}, host: id + ".example.com",
  key: { set: true, masked: "sk-…" + id }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
});

function serve(lang, posts) {
  let list = [prov("a", "Relay A"), prov("b", "Relay B"), prov("c", "Relay C"), prov("d", "Relay D"), prov("e", "Relay E", true)];
  const payload = (extra) => ({ providers: list, presets: [], excluded: [], gateway: { running: true, window: true }, ...extra });
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data, status = 200) => route.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(payload());
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers/delete" || url.pathname === "/api/provider/delete") {
      const body = route.request().postDataJSON();
      posts.push([url.pathname, body]);
      list = list.filter((p) => !(body.ids || [body.id]).includes(p.id));
      return json(payload());
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: { select: "Select", cancel: "Cancel", picked: "3 selected", title: "Remove 2 providers?", done: "2 providers removed" },
  zh: { select: "选择", cancel: "取消", picked: "已选 3 个", title: "移除 2 个供应商？", done: "已移除 2 个供应商" },
  "zh-TW": { select: "選擇", cancel: "取消", picked: "已選 3 個", title: "移除 2 個供應商？", done: "已移除 2 個供應商" },
  ja: { select: "選択", cancel: "キャンセル", picked: "3 件選択中", title: "2 個のプロバイダーを削除しますか？", done: "2 個のプロバイダーを削除しました" },
  de: { select: "Auswählen", cancel: "Abbrechen", picked: "3 ausgewählt", title: "2 Anbieter entfernen?", done: "2 Anbieter entfernt" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(words)) {
    const w = words[lang];
    test(`${engine} ${lang}: providers removed several at once`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      for (const width of [420, 1100]) {
        const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], posts = [];
        page.on("pageerror", (e) => errors.push(e.message));
        page.on("dialog", (d) => { errors.push("native dialog: " + d.message()); d.dismiss(); });
        await page.route("**/*", serve(lang, posts));
        await page.goto("http://magpie.test/?view=providers");
        const row = (name) => page.locator("#view-providers .row.provider", { hasText: name });
        await row("Relay A").waitFor();
        // a switched-off row's name carries its badge; the name itself leads
        const names = async () => (await page.locator("#view-providers .row.provider .name").allTextContents()).map((n) => n.slice(0, 7));

        const sel = page.locator("#pvSelect");
        assert.equal((await sel.textContent()).trim(), w.select);
        await sel.click();
        const bar = page.locator("#pvSel");
        await bar.waitFor();
        assert.equal(await sel.isVisible(), false, "Select gives way to the bar");
        assert.equal(await page.locator("#view-providers .row.pv-pick .pv-pickbox:visible").count(), 4, "a box on every row shown (the turned-off fold is closed)");
        assert.equal(await page.locator("#view-providers .pv-handle, #view-providers .pswitch").count(), 0, "no handle or switch while picking");
        const rm = bar.locator(".pv-remove");
        assert.equal(await rm.isDisabled(), true, "nothing picked, nothing to remove");

        // a row's click picks it, never opens its editor; Shift picks a run
        await row("Relay A").click();
        await row("Relay C").click({ modifiers: ["Shift"] });
        await page.waitForFunction((s) => document.querySelector("#pvSel .note").textContent === s, w.picked);
        assert.equal(await page.locator(".editor").count(), 0, "a click on a row doesn't open its editor");
        await row("Relay B").locator(".pv-pickbox").uncheck();
        assert.deepEqual(await page.locator("#view-providers .row.pv-pick.picked .name").allTextContents(), ["Relay A", "Relay C"]);
        assert.equal(await bar.locator(".pv-pickall").evaluate((x) => x.indeterminate), true);

        // asked once, with the count and every name; Cancel keeps them all
        await rm.click();
        const confirm = page.locator("dialog.action-confirm[open]");
        await confirm.waitFor();
        assert.equal((await confirm.locator("h2").textContent()).trim(), w.title);
        assert.deepEqual(await confirm.locator(".confirm-names li").allTextContents(), ["Relay A", "Relay C"]);
        assert.deepEqual(posts, [], "one click removes nothing");
        await confirm.getByRole("button", { name: w.cancel, exact: true }).click();
        await confirm.waitFor({ state: "detached" });
        assert.deepEqual(posts, [], "Cancel keeps every provider");
        assert.equal(await page.locator("#view-providers .row.pv-pick.picked").count(), 2, "Cancel keeps the picks");

        await rm.click();
        await confirm.locator("button").last().click();
        await bar.waitFor({ state: "hidden" });
        assert.deepEqual(posts, [["/api/providers/delete", { ids: ["a", "c"] }]]);
        await page.waitForFunction((s) => document.querySelector("#status").textContent.includes(s), w.done);
        assert.deepEqual(await names(), ["Relay B", "Relay D", "Relay E"], "the ones not picked stay");
        assert.equal(await page.locator("#view-providers .row.pv-pick").count(), 0, "picking ends");
        assert.equal(await page.locator("#view-providers .pv-handle").count(), 2, "the rows drag again");

        // Escape ends picking, with nothing removed
        await sel.click();
        await bar.waitFor();
        await row("Relay B").click();
        await page.keyboard.press("Escape");
        await bar.waitFor({ state: "hidden" });
        assert.equal(posts.length, 1);
        assert.deepEqual(await names(), ["Relay B", "Relay D", "Relay E"]);

        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), true, "no sideways scroll");
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await sel.click();
          await row("Relay B").click();
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-provider-bulk-remove.png`) });
        }
        assert.deepEqual(errors, []);
        if (lang === "en" && width === 420) {
          const missing = await page.evaluate(() => [
            "Select", "Done", "{n} selected", "Pick several providers to remove together", "Select every provider shown", "Pick the providers to remove",
            "Remove {n} providers?", "Their saved configuration will be removed. Agents using them may switch to another provider or their default model.",
            "{n} providers removed", "{name} wasn't removed: {error}",
          ].flatMap((k) => ["zh", "zh-TW", "ja", "de"].filter((l) => !I18N[l][k]).map((l) => l + ": " + k)));
          assert.deepEqual(missing, [], "every string in every language");
        }
        await page.context().close();
      }
    });
  }
}
