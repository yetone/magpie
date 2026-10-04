// Run with Node's test runner and Playwright on the module path; see README.md.
// Remove, in a provider's editor (StringKe on Discord: the dialog stayed
// open, a second Remove said there was no such provider, and the list
// wasn't refreshed). A Remove that fails once the provider is already gone
// closes the editor, drops its row and says what failed; a second one isn't
// needed. A Remove that goes through but leaves an agent it couldn't move on
// a model gone says so, as a warning, rather than failing. One that fails
// with the provider still there keeps the editor open on the error. No
// click moves the page. English and Chinese, Chromium and WebKit; no
// backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const prov = (id, name) => ({
  id, name, icon: "generic", chat: "https://" + id + ".example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "m1", name: "m1", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
});

// mode: "gone" — the delete fails after the provider went; "stuck" — it goes
// through, Hermes left behind; "kept" — it fails, the provider still there
function serve(lang, mode, deletes) {
  let list = [prov("relay", "Relay"), prov("other", "Other")];
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
    if (url.pathname === "/api/provider/delete") {
      const { id } = route.request().postDataJSON();
      deletes.push(id);
      if (mode === "kept") return json({ error: "disk full" }, 400);
      const was = list.length;
      list = list.filter((p) => p.id !== id);
      if (list.length === was) return json({ error: `no provider "${id}"` }, 400);
      if (mode === "gone") return json({ error: "open ~/.hermes/config.yaml: permission denied" }, 400);
      return json(payload({ moved: [{ agent: "Hermes", field: "model", from: "relay/m1", error: "permission denied" }] }));
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: { remove: "Remove", stuck: "Relay removed · Hermes is still on relay/m1, which magpie no longer serves: permission denied" },
  zh: { remove: "移除", stuck: "已移除 Relay · Hermes 仍在用 magpie 已不再提供的 relay/m1：permission denied" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    const w = words[lang];
    test(`${engine} ${lang}: Remove closes on a provider gone`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      const pages = [];
      t.after(async () => {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          for (const [i, p] of pages.entries()) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-provider-remove-${i}.png`) });
        }
        await browser.close();
      });

      const open = async (mode) => {
        const page = await (await browser.newContext({ viewport: { width: 900, height: 760 }, reducedMotion: "reduce" })).newPage();
        pages.push(page);
        page.setDefaultTimeout(5000);
        const errors = [], deletes = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, mode, deletes));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "Relay" }).click();
        await page.locator(".editor").waitFor();
        const del = page.locator(".editor .bar button.danger", { hasText: w.remove });
        await del.scrollIntoViewIfNeeded();
        await page.waitForTimeout(150);
        const top = await del.evaluate((e) => e.getBoundingClientRect().top);
        await del.click();
        assert.deepEqual(deletes, [], "Remove waits for confirmation");
        await page.locator("dialog.action-confirm[open] button").last().click();
        return { page, errors, deletes, top, del };
      };
      const rows = (page) => page.locator(".row.provider").allTextContents();

      await t.test("gone", async () => {
        const { page, errors, deletes } = await open("gone");
        await page.locator(".editor").waitFor({ state: "detached" });
        assert.deepEqual(deletes, ["relay"], "one Remove is enough");
        await page.waitForFunction(() => ![...document.querySelectorAll(".row.provider")].some((r) => r.textContent.includes("Relay")));
        assert((await rows(page)).some((r) => r.includes("Other")), "the rest of the list stays");
        const st = page.locator("#status");
        assert.equal(await st.textContent(), "open ~/.hermes/config.yaml: permission denied");
        assert(/\berr\b/.test(await st.getAttribute("class")));
        assert.deepEqual(errors, []);
      });

      await t.test("stuck", async () => {
        const { page, errors } = await open("stuck");
        await page.locator(".editor").waitFor({ state: "detached" });
        const st = page.locator("#status");
        await page.waitForFunction((s) => document.querySelector("#status").textContent === s, w.stuck);
        assert(/\bwarn\b/.test(await st.getAttribute("class")), "a warning, not a plain ok");
        const border = await st.evaluate((e) => getComputedStyle(e).borderLeftWidth);
        assert(parseFloat(border) <= 1, "no left-border accent");
        assert.deepEqual(errors, []);
      });

      await t.test("kept", async () => {
        const { page, errors, deletes, top, del } = await open("kept");
        await page.locator(".editor .editor-error").waitFor();
        assert.equal(await page.locator(".editor .editor-error").textContent(), "disk full");
        assert.equal(await del.evaluate((e) => e.getBoundingClientRect().top) <= top, true, "the Remove click didn't push the page down");
        assert.deepEqual(deletes, ["relay"]);
        assert((await rows(page)).some((r) => r.includes("Relay")), "still listed");
        assert.deepEqual(errors, []);
      });

      const missing = await pages[0].evaluate(() => ["{agent} is still on {model}, which magpie no longer serves: {error}"].filter((k) => !I18N.zh[k]));
      assert.deepEqual(missing, []);
    });
  }
}
