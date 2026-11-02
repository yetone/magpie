// Run with Node's test runner and Playwright on the module path; see README.md.
// Renaming a profile from its details (#2721, erlinerd): Rename turns the
// name into a field holding it; Enter renames, the chip under its new name
// at once, the details still open on it, what it holds and its In use kept;
// Escape renames nothing; a name already taken is refused, the list put
// back and the reason shown. In the window and the tray panel, in every
// language. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = ["model-a", "model-b"].map((m) => ({ value: m, label: m }));
const agent = { id: "codex", name: "Codex", path: "/test/codex", fields: [{ key: "model", label: "model", value: "model-a", options: models }] };
const profile = (name, inUse) => ({ name, summary: "", inUse, agents: [{ id: "codex", name: "Codex", fields: [{ key: "model", label: "model", value: name + "-model" }] }] });

function server(lang, calls) {
  let profiles = [profile("home"), profile("work", true)];
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const state = () => ({ agents: [agent], profiles, settings: { lang, theme: "light" } });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: state() });
    if (url.pathname.startsWith("/api/profile/")) {
      const action = url.pathname.slice("/api/profile/".length), body = req.postDataJSON();
      calls.push(action + " " + JSON.stringify(body));
      if (action === "rename") {
        if (profiles.some((p) => p.name === body.to)) return route.fulfill({ status: 400, json: { error: `a profile named "${body.to}" already exists` } });
        await new Promise((r) => setTimeout(r, 300)); // magpie reading every agent again
        profiles = profiles.map((p) => (p.name === body.name ? { ...p, name: body.to } : p)).sort((a, b) => (a.name < b.name ? -1 : 1));
      }
      return route.fulfill({ json: state() });
    }
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname === "/api/window/fit") return route.fulfill({ status: 204 });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { rename: "Rename", renamed: "Renamed work to team", inUse: "In use" },
  zh: { rename: "重命名", renamed: "已将 work 重命名为 team", inUse: "使用中" },
  "zh-TW": { rename: "重新命名", renamed: "已將 work 重新命名為 team", inUse: "使用中" },
  ja: { rename: "名前を変更", renamed: "work の名前を team に変更しました", inUse: "使用中" },
  de: { rename: "Umbenennen", renamed: "work in team umbenannt", inUse: "In Verwendung" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a profile renames from its details", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());

    for (const lang of Object.keys(words)) for (const panel of [false, true]) {
      await t.test(`${lang}, ${panel ? "panel" : "window"}`, async () => {
        const w = words[lang], calls = [], errors = [];
        const page = await (await browser.newContext({ viewport: panel ? { width: 360, height: 420 } : { width: 900, height: 560 } })).newPage();
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, calls));
        await page.goto("http://magpie.test/" + (panel ? "?mode=panel" : ""));
        const chip = (name) => page.locator("#profiles .chip", { hasText: new RegExp("^✓?" + name) });
        await chip("work").waitFor({ state: "attached" });
        if (panel) {
          await page.locator("#profBtn").click();
          await page.locator(".profiles.open").waitFor();
        }
        const detail = page.locator(".prof-detail");
        const field = detail.locator(".pd-rename-input");
        const names = () => page.locator("#profiles .chip").evaluateAll((cs) => cs.map((c) => c._profile));

        await chip("work").locator("span").first().click();
        await detail.waitFor();
        assert.equal((await detail.locator(".pd-rename").textContent()).trim(), w.rename);

        // Escape: the name back, nothing sent, the details still open
        await detail.locator(".pd-rename").click();
        await field.waitFor();
        assert.equal(await field.inputValue(), "work", "the field holds the name");
        await field.fill("other");
        await field.press("Escape");
        await detail.locator(".pd-name").waitFor();
        assert.equal(await field.count(), 0);
        assert.equal(await detail.count(), 1, "Escape on the field leaves the details open");
        assert.deepEqual(calls, []);

        // Enter: the chip renamed at once, the details on it, In use kept
        await detail.locator(".pd-rename").click();
        await field.fill("  team ");
        await field.press("Enter");
        await chip("team").waitFor({ state: "attached" });
        assert.deepEqual(await names(), ["home", "team"], "renamed before magpie answered");
        assert.match(await chip("team").getAttribute("class"), /pending/);
        await page.waitForFunction(() => !document.querySelector("#profiles .chip.pending"));
        assert.deepEqual(calls, ['rename {"name":"work","to":"team"}']);
        assert.equal((await page.locator("#status").textContent()).trim(), w.renamed);
        assert.equal(await detail.locator(".pd-name").textContent(), "team", "the details follow the new name");
        assert.equal(await detail.locator(".pd-cur").textContent(), w.inUse);
        assert.match(await chip("team").getAttribute("class"), /\bcur\b/);
        assert.match(await detail.textContent(), /work-model/, "what it holds is kept");

        // a name taken: refused, the list as it was, the reason shown
        calls.length = 0;
        await detail.locator(".pd-rename").click();
        await field.fill("home");
        await field.press("Enter");
        await page.waitForFunction(() => document.querySelector("#status").classList.contains("err"));
        assert.equal((await page.locator("#status").textContent()).trim(), 'a profile named "home" already exists');
        assert.deepEqual(await names(), ["home", "team"]);
        assert.equal(await detail.locator(".pd-name").textContent(), "team");
        assert.deepEqual(calls, ['rename {"name":"team","to":"home"}']);

        // the head fits: the field and its buttons inside the details
        await detail.locator(".pd-rename").click();
        const box = await detail.boundingBox(), f = await field.boundingBox();
        assert(f.x >= box.x && f.x + f.width <= box.x + box.width + 0.5, `field ${JSON.stringify(f)} out of ${JSON.stringify(box)}`);
        assert(f.width >= 60, "the field has room to type in: " + f.width);
        const x = await detail.locator(".pd-close").boundingBox();
        assert(x.x + x.width <= box.x + box.width + 0.5, `× ${JSON.stringify(x)} out of ${JSON.stringify(box)}`);
        await field.press("Escape");
        assert((await detail.locator(".pd-name").boundingBox()).width >= 20, "the name keeps room beside the buttons");
        assert.deepEqual(errors, []);
      });
    }
  });
}
