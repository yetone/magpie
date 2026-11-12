// In a browser tab (magpie web, possibly on another machine than the one
// magpie runs on), Add magpie asks the host to open the app's import link; it
// never navigates the viewer's own browser to it (Jealousy_Du on X).
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const LINK = "cindy://provider/import?v=1&data=fixture";
const cindy = { id: "cindy", name: "Cindy", path: "", icon: "cindy", import: LINK, added: false, fields: [] };

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`web: Add magpie opens the link on the host (${engine})`, async () => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const page = await browser.newPage({ viewport: { width: 960, height: 600 } });
    page.setDefaultTimeout(8000);
    const opened = [];
    const navs = [];
    page.on("framenavigated", (f) => navs.push(f.url()));
    await page.route("http://magpie.test/**", async (route) => {
      const url = new URL(route.request().url());
      const json = (data) => route.fulfill({ json: data });
      if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: 'window.bootPrefs = {lang:"en",theme:"light",web:true,textSize:100};' });
      if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
      if (url.pathname === "/api/state") return json({ agents: [cindy], profiles: [], settings: { lang: "en", theme: "light", textSize: 100 } });
      if (url.pathname === "/api/providers") return json({ providers: [], gateway: { running: true, window: true } });
      if (url.pathname === "/api/plugins") return json({ plugins: [] });
      if (url.pathname === "/api/open") { opened.push(route.request().postDataJSON().url); return route.fulfill({ status: 204 }); }
      if (url.pathname.startsWith("/api/")) return json({});
      const rel = url.pathname === "/" ? "index.html" : url.pathname.slice(1);
      try {
        const body = await fs.readFile(path.join(assets, rel));
        const type = rel.endsWith(".js") ? "text/javascript" : rel.endsWith(".css") ? "text/css" : rel.endsWith(".html") ? "text/html" : undefined;
        return route.fulfill({ body, contentType: type });
      } catch { return route.fulfill({ status: 404, body: "" }); }
    });
    await page.goto("http://magpie.test/");
    await page.waitForLoadState("networkidle");
    const more = page.locator('.agent-more[aria-expanded="false"]');
    if (await more.count()) await more.click();
    const row = page.locator('.row.agent[data-id="cindy"]');
    await row.scrollIntoViewIfNeeded();
    const asked = page.waitForRequest((r) => r.url().includes("/api/open"));
    await row.locator(".ag-start").click();
    await asked;
    await new Promise((r) => setTimeout(r, 300));
    assert.deepEqual(opened, [LINK]);
    assert.ok(navs.every((u) => u.startsWith("http://magpie.test/")), `navigated: ${navs}`);
    await browser.close();
  });
}
