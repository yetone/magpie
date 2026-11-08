// Run with Node's test runner and Playwright on the module path; see README.md.
// A key's row in the provider's editor stays put under the pointer
// (tenacious_eagle_96548 on Discord: the row's controls showed only while
// the pointer was over it, so the row grew a line, the rows below jumped,
// and moving the pointer off to fill in something else took it back).
// Every control of every key's row is shown with the pointer off the list,
// and each row, and each control in it, is where it was with the pointer
// over any row. A balance being typed stays where it is, with its caret and
// what was typed, when the pointer goes to the provider's name. In every
// language, in the app's window and in a browser tab, Chromium and WebKit.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.ASSET_DIR || path.resolve(__dirname, "../assets"));
const relay = () => ({
  id: "relay", name: "Relay", icon: "generic", chat: "https://relay.example.com/v1", responses: "", anthropic: "", catalog: "",
  models: [{ id: "model-a", name: "Model A", on: true }, { id: "model-b", name: "Model B", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-…one" },
  takesOwnBalance: true,
  keyList: [
    { id: "k1", name: "Main", masked: "sk-…one", on: true, balance: { set: 20, at: new Date(Date.now() - 864e5).toISOString(), spent: 7.5, left: 12.5 } },
    { id: "k2", name: "Team", masked: "sk-…two", on: true },
    { id: "k3", name: "Spare", masked: "sk-…three", on: true },
  ],
  balanceToken: { takes: false, set: false }, proxy: "",
});

function serve(lang, web) {
  const providers = { providers: [relay()], presets: [], excluded: [], gateway: { running: true, window: true } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light"${web ? ",web:true" : ""}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json(providers);
    if (url.pathname === "/api/lanes") return json({});
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file).catch(() => ""), contentType });
  };
}

// where each key's row and each of its controls is, inside the list, and
// what is hidden (as good as transparent)
function layout() {
  const list = document.querySelector(".editor .accts.keys");
  const o = list.getBoundingClientRect();
  const at = (e) => { const b = e.getBoundingClientRect(); return `${Math.round(b.left - o.left)},${Math.round(b.top - o.top)},${Math.round(b.width)}x${Math.round(b.height)}`; };
  return [...list.querySelectorAll(".acc[data-account-id]")].map((r) => ({
    id: r.dataset.accountId,
    at: at(r),
    els: [...r.querySelectorAll("button, .plan, .n, input")].filter((e) => e.getBoundingClientRect().width > 0).map((e) => `${String(e.className).split(" ")[0] || e.tagName}@${at(e)}`).join(" | "),
    hidden: [...r.querySelectorAll("button")].filter((e) => e.getBoundingClientRect().width > 0 && +getComputedStyle(e).opacity < 0.5).map((e) => `${String(e.className)}「${e.textContent.trim()}」`),
  }));
}
const settled = (page) => page.waitForFunction(() => document.getAnimations().every((a) => a.playState !== "running"));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    // the app's window (its least is 560px) and a browser tab, which at
    // 760px and under lays a row out as on a phone
    for (const [web, width] of [[false, 560], [false, 1000], [true, 420], [true, 1000]]) {
      const mode = web ? "tab" : "app";
      test(`${engine} ${lang} ${mode} ${width}px: a key's row stays put under the pointer, and a balance being typed with it`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const page = await (await browser.newContext({ viewport: { width, height: 760 } })).newPage();
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${mode}-${width}-key-row-steady.png`) }).catch(() => {});
          }
          await browser.close();
        });
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, web));
        await page.goto("http://magpie.test/?view=providers");
        await page.locator(".row.provider", { hasText: "Relay" }).first().click();
        const row = (id) => page.locator(`.editor .accts.keys .acc[data-account-id="${id}"]`);
        await row("k3").locator(".abal").waitFor({ state: "attached" });
        await row("k3").scrollIntoViewIfNeeded();

        // the pointer off the list: every control is shown
        await page.mouse.move(0, 0);
        await settled(page);
        const off = await page.evaluate(layout);
        for (const r of off) assert.deepEqual(r.hidden, [], `${r.id}: hidden with the pointer off its row`);
        assert.equal(await row("k2").locator(".abal").evaluate((e) => getComputedStyle(e).opacity), "1", "Set balance is hidden off the pointer");

        // over each row in turn: nothing moves, that row's nor another's
        for (const id of ["k1", "k2", "k3"]) {
          await row(id).locator(".dot").hover();
          await settled(page);
          const on = await page.evaluate(layout);
          for (let i = 0; i < off.length; i++) {
            assert.equal(on[i].at, off[i].at, `the pointer over ${id} moved ${off[i].id}'s row`);
            assert.equal(on[i].els, off[i].els, `the pointer over ${id} moved what is in ${off[i].id}'s row`);
          }
        }

        // the report: Set balance, typed, then the pointer goes to the
        // provider's name to fill it in while the caret is still in the
        // balance
        await row("k2").locator(".abal").click();
        const field = row("k2").locator("input.abal-in");
        await field.waitFor();
        await page.keyboard.type("100");
        await settled(page);
        const typed = await field.boundingBox();
        const rows = (await page.evaluate(layout)).map((r) => r.at);
        const name = page.locator(".editor input").first();
        const b = await name.boundingBox();
        await page.mouse.move(b.x + Math.min(20, b.width / 2), b.y + b.height / 2, { steps: 8 });
        await settled(page);
        assert.deepEqual(await field.boundingBox(), typed, "the balance being typed moved as the pointer left its row");
        assert.deepEqual((await page.evaluate(layout)).map((r) => r.at), rows, "the rows moved as the pointer left");
        assert.equal(await field.evaluate((e) => document.activeElement === e), true, "the balance lost its caret");
        assert.equal(await field.inputValue(), "100");
        assert.deepEqual(errors, []);
      });
    }
  }
}
