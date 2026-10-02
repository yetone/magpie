// Run with Node's test runner and Playwright on the module path; see README.md.
// Plugins kept on this computer: the market's field takes a folder, chosen
// with the system's picker rather than typed — and `magpie web`, which has
// no picker, offers no button for it. A folder plugin's page shows the
// README that folder carries, where npm knows nothing of it: no npm link,
// the version facts left out, and the folder's own name for the plugin.
// English and Chinese; the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const FOLDER = "/Users/me/plugins/opencode-mine";
const README = [
  "# opencode-mine",
  "",
  "A plugin kept in a folder, not on npm.",
  "",
  "## Install",
  "",
  "```sh",
  "magpie plugin add " + FOLDER,
  "```",
].join("\n");

function server(lang, asked, opts = {}) {
  const picker = opts.picker !== false;
  const installed = [{ spec: FOLDER, providers: [], version: "" }];
  const market = () => ({ listings: [], state: { bun: true, bunVersion: "1.3.0", plugins: installed, picker } });
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    const body = () => route.request().postDataJSON();
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    // the page asks for the market in parts (#488)
    if (url.pathname === "/api/plugins/market" || url.pathname === "/api/plugins" || url.pathname === "/api/plugins/listings") { const m = market(); return json(url.pathname === "/api/plugins" ? m.state : url.pathname === "/api/plugins/listings" ? { listings: m.listings } : m); }
    if (url.pathname === "/api/plugins/search") return json({ hits: [] });
    if (url.pathname === "/api/plugins/page") {
      asked.push(["page", url.searchParams.get("name")]);
      return json({ readme: README, updated: "2026-09-30T10:00:00Z" });
    }
    if (url.pathname === "/api/plugins/choose") {
      asked.push(["choose", body()]);
      return json({ dir: opts.chosen === undefined ? FOLDER : opts.chosen });
    }
    if (url.pathname === "/api/plugins/add") {
      const b = body();
      asked.push(["add", b]);
      installed.push({ spec: b.spec, providers: [], version: "" });
      return json({});
    }
    if (url.pathname === "/api/open") { asked.push(["open", body()]); return route.fulfill({ status: 204 }); }
    if (url.pathname.startsWith("/api/")) return json({});
    if (!/^\/[\w./-]*$/.test(url.pathname) || url.host !== "magpie.test") { asked.push(["fetched", url.href]); return route.fulfill({ status: 404, body: "" }); }
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const L = {
  en: { tab: "Plugins", pick: "Choose a folder on this computer", install: "Install", folderName: "opencode-mine", readme: "Install" },
  zh: { tab: "插件", pick: "选择本机上的一个文件夹", install: "安装", folderName: "opencode-mine", readme: "Install" },
};

const shot = async (loc, name) => {
  if (!process.env.ARTIFACT_DIR) return;
  await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
  await loc.screenshot({ path: path.join(process.env.ARTIFACT_DIR, name + ".png") });
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a plugin kept on this computer", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) {
      await t.test(lang, async (t2) => {
        const w = L[lang];
        const asked = [];
        const page = await (await browser.newContext({ viewport: { width: 980, height: 820 } })).newPage();
        page.setDefaultTimeout(5000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, asked));

        await page.goto("http://magpie.test/?view=plugins");
        const view = page.locator("#view-plugins");
        await view.waitFor({ state: "visible" });

        // Discover: the folder picker sits beside the field, and fills it
        const manual = view.locator(".pm-manual");
        await manual.waitFor();
        const browse = manual.locator(".pm-browse");
        assert.equal(await browse.count(), 1, "the picker is offered");
        assert.equal(await browse.getAttribute("title"), w.pick);
        const field = manual.locator(".pm-minput");
        assert.equal(await field.inputValue(), "");
        await shot(manual, `plugins-picker-${engine}-${lang}`);
        await browse.click();
        await page.waitForFunction((f) => document.querySelector("#view-plugins .pm-minput").value === f, FOLDER);
        assert.deepEqual(asked.find(([k]) => k === "choose"), ["choose", {}]);
        assert.equal(await page.evaluate(() => document.querySelector("#view-plugins .pm-minput") === document.activeElement), true, "the field takes the focus");
        assert.deepEqual(errors, []);

        // ...and what it filled is what Install sends
        await manual.locator("button", { hasText: new RegExp("^" + w.install + "$") }).click();
        await page.waitForFunction(() => !!document.querySelector("#view-plugins .pm-minput"));
        assert.deepEqual(asked.find(([k]) => k === "add")[1], { spec: FOLDER });

        // Installed: the folder's own name, its page from the folder's README
        await view.locator(".lib-tabs .opt", { hasText: w.tab === "Plugins" ? "Installed" : "已安装" }).click();
        const row = view.locator(".pm-row").first();
        await row.waitFor();
        assert.equal((await row.locator(".name span").first().innerText()).trim(), w.folderName, "the folder names it, not the whole path");
        assert.doesNotMatch(await row.innerText(), /\/Users\/me\/plugins/, "the path isn't the plugin's name");
        await shot(view, `plugins-installed-local-${engine}-${lang}`);
        await row.click();
        const dlg = page.locator("#modal .pm-detail");
        await dlg.locator(".pm-md h4", { hasText: w.readme }).waitFor();
        assert.equal((await dlg.locator(".pm-md h3").innerText()).trim(), w.folderName);
        assert.match(await dlg.locator(".pm-md").innerText(), /A plugin kept in a folder, not on npm\./);
        assert.deepEqual(asked.filter(([k]) => k === "page").pop(), ["page", FOLDER], "the folder is what's asked for");
        assert.equal(await dlg.locator(".pm-links a", { hasText: "npm" }).count(), 0, "a folder has no npm page");
        assert.match(await dlg.locator(".pm-by").innerText(), /\/Users\/me\/plugins\/opencode-mine/);
        await shot(page.locator("#modal .dialog"), `plugins-local-page-${engine}-${lang}`);
        await page.locator("#modal .bar button").last().click().catch(() => {});
        assert.deepEqual(errors, []);
      });
    }

    // `magpie web` has no picker: no button, and the field is typed into
    await t.test("no picker where magpie can't show one", async () => {
      const asked = [];
      const page = await (await browser.newContext({ viewport: { width: 980, height: 820 } })).newPage();
      page.setDefaultTimeout(5000);
      await page.route("**/*", server("en", asked, { picker: false }));
      await page.goto("http://magpie.test/?view=plugins");
      const manual = page.locator("#view-plugins .pm-manual");
      await manual.waitFor();
      assert.equal(await manual.locator(".pm-browse").count(), 0, "no picker to offer");
      assert.equal(await manual.locator(".pm-minput").count(), 1, "the field is still there to type into");
      assert.equal(await manual.locator("button", { hasText: /^Install$/ }).count(), 1);
      assert.deepEqual(asked.filter(([k]) => k === "choose"), []);
      await shot(manual, `plugins-picker-web-${engine}`);
    });
  });
}
