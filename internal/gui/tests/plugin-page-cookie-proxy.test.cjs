// Run with Node's test runner and Playwright on the module path; see README.md.
// Behind a proxy that signs readers in by a SameSite=Lax cookie (Cloudflare
// Access), a plugin page's sandboxed frame is a cross-site origin and sends
// none: its files are redirected to the sign-in page (xuanwo on X, Day card).
// The server writes the page's own files into its HTML (pageinline.go, tested
// in Go); a script the page adds later is fetched by the window, which has
// the cookie (pagebridge.js), and loaded from a data: address.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(process.env.MAGPIE_ASSETS || path.join(__dirname, "../assets"));
const bridge = path.resolve(process.env.MAGPIE_BRIDGE || path.join(__dirname, "../pagebridge.js"));
const tok = "c".repeat(32);
const page = { spec: "/x/cookie", name: "cookie", key: "plugin-cookie", title: "cookie", reads: [], url: `/_magpie/plugin/${tok}/index.html` };

let webkitEngine = false;
async function server(route) {
  const url = new URL(route.request().url());
  const json = (data) => route.fulfill({ json: data });
  if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"en",theme:"light",web:true};` });
  if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
  if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang: "en", theme: "light" } });
  if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
  if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
  if (url.pathname === "/api/plugin-pages") return json([page]);
  if (url.pathname === "/api/plugin-panels") return json([]);
  if (url.pathname.startsWith("/api/")) return json({});
  if (url.pathname.startsWith("/_magpie/plugin/")) {
    const name = url.pathname.slice(`/_magpie/plugin/${tok}/`.length);
    const headers = { "Cache-Control": "no-cache" };
    if (name === "index.html") {
      const body = `<!doctype html><meta charset="utf-8"><script>globalThis.__magpieInlined=1;\n${(await fs.readFile(bridge, "utf8")).replaceAll("</script", "<\\/script")}</script>
<script>window.dyn = new Promise((ok, no) => { const s = document.createElement("script"); s.src = "dyn.js"; s.onload = () => ok(window.DYN); s.onerror = () => no(new Error("dyn.js")); document.head.appendChild(s); });</script><p>page</p>`;
      return route.fulfill({ headers: { ...headers, "Content-Type": "text/html", "Content-Security-Policy": "default-src 'none'; script-src 'unsafe-inline' data:; style-src 'unsafe-inline'" }, body });
    }
    // the proxy: no cookie, no file
    const h = await route.request().allHeaders();
    // Playwright's WebKit shows no cookie on an intercepted request; the
    // window's fetch has a Referer, and the frame (no-referrer) has none
    const signedIn = h.cookie?.includes("CF_Authorization") || (webkitEngine && h.referer);
    if (!signedIn) return route.abort("failed"); // the proxy redirects to its sign-in, which the frame's CORS/CSP turn into an error
    if (name === "dyn.js") return route.fulfill({ headers: { ...headers, "Content-Type": "text/javascript" }, body: "window.DYN = 42;" });
    return route.fulfill({ status: 404, body: "" });
  }
  if (url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
  const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
  const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
  try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a script a page adds loads behind a proxy that wants a cookie`, async (t) => {
    webkitEngine = engine === "webkit";
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    const ctx = await browser.newContext({ viewport: { width: 1100, height: 700 }, reducedMotion: "reduce" });
    await ctx.addCookies([{ name: "CF_Authorization", value: "x", url: "http://magpie.test/", sameSite: "Lax" }]);
    const p = await ctx.newPage();
    p.setDefaultTimeout(8000);
    p.setDefaultNavigationTimeout(60000);
    await p.route("**/*", server);
    await p.goto("http://magpie.test/");
    await p.locator("#nav button[data-plugin-page]").first().waitFor({ state: "attached" });
    await p.evaluate(() => show("plugin-cookie"));
    const frame = () => p.frames().find((x) => x.url().includes(tok));
    for (let i = 0; i < 100 && !frame(); i++) await p.waitForTimeout(50);
    assert.ok(frame(), "the frame opened");
    assert.equal(await frame().evaluate(() => window.dyn), 42);
    assert.equal(await p.locator("#view-plugin-cookie .slot-error").count(), 0);
  });
}
