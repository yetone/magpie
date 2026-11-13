// Run with Node's test runner and Playwright on the module path; see README.md.
// hkxhkxhkx on X: with Settings › Privacy › "Open sign-in pages in the
// browser" off (signInLinkOnly), an account's sign-in kept to its link, but
// an MCP server's OAuth sign-in from the Library still opened the default
// browser. Now /api/library/mcp-signin answers linkOnly and opens nothing,
// and the Library says so with the link to copy and an Open in browser of
// its own; a web tab opens no tab of its own either. Every language,
// Chromium and WebKit, 1100 and 420px; no backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const MCP_LINK = "https://dash.cloudflare.com/oauth2/auth?client_id=c1&state=" + "y".repeat(80);

function server(lang, web, st) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data, status) => route.fulfill({ status: status || 200, json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light", signInLinkOnly: true } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/open") {
      st.opens.push(req.postDataJSON().url);
      return route.fulfill({ status: 204, body: "" });
    }
    if (url.pathname === "/api/library") {
      return json({
        dir: "/h/.magpie/library", backups: "/h/.magpie/backups", home: "/h",
        agents: [{ id: "codex", name: "Codex", icon: "", skills: "/h/.codex/skills", mcp: "/h/.codex/mcp.json" }],
        instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [],
        servers: [{ name: "cloudflare-api", transport: "http", url: "https://mcp.cloudflare.com/mcp", agents: ["codex"], signIn: { signedIn: false } }],
      });
    }
    // the start says it was kept to its link; a poll knows nothing of it
    if (url.pathname === "/api/library/mcp-signin") return json({ id: "m1", server: "cloudflare-api", state: "waiting", url: MCP_LINK, linkOnly: true });
    if (url.pathname.startsWith("/api/library/mcp-signin/")) return json({ id: "m1", server: "cloudflare-api", state: "waiting", url: MCP_LINK });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const W = {
  en: { signIn: "Sign in", open: "Open in browser", said: "magpie didn't open the sign-in page, as set in Settings. Copy the link below into the browser you sign in with." },
  zh: { signIn: "登录", open: "在浏览器中打开", said: "magpie 按设置没有打开登录页。把下面的链接复制到你用来登录的浏览器里。" },
  "zh-TW": { signIn: "登入", open: "在瀏覽器中開啟", said: "magpie 依設定沒有開啟登入頁。把下面的連結複製到你用來登入的瀏覽器裡。" },
  ja: { signIn: "サインイン", open: "ブラウザで開く", said: "設定に従い、magpie はサインインページを開いていません。下のリンクをサインインに使うブラウザにコピーしてください。" },
  de: { signIn: "Anmelden", open: "Im Browser öffnen", said: "magpie hat die Anmeldeseite wie in den Einstellungen festgelegt nicht geöffnet. Kopiere den Link unten in den Browser, mit dem du dich anmeldest." },
};

const overflow = (page) => page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(W)) {
    for (const width of [1100, 420]) {
      for (const web of [false, true]) {
        test(`${engine} ${lang} ${width}${web ? " web" : ""}: an MCP sign-in kept to its link opens nothing and shows it`, async (t) => {
          const w = W[lang];
          const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
          t.after(() => browser.close());
          const errors = [];
          const st = { opens: [] };
          const ctx = await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" });
          // window.open is counted, never followed: a test reaches no network
          await ctx.addInitScript(() => {
            try { localStorage.setItem("magpie.libTab", "mcp"); } catch {}
            window.__opened = [];
            window.open = (u) => { window.__opened.push(String(u)); return null; };
          });
          const popups = () => page.evaluate(() => window.__opened);
          const page = await ctx.newPage();
          page.setDefaultTimeout(5000);
          page.setDefaultNavigationTimeout(60000);
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", server(lang, web, st));
          await page.goto("http://magpie.test/");
          await page.locator('button[data-view="library"]').click();
          await page.locator("#view-library .lib-row", { has: page.locator(".name", { hasText: /^cloudflare-api/ }) }).click();
          const lib = page.locator("#modal .lib-signin");
          await Promise.all([page.waitForResponse((r) => r.url().endsWith("/api/library/mcp-signin")), lib.getByRole("button", { name: w.signIn, exact: true }).click()]);
          const note = lib.locator(".note", { hasText: w.said });
          await note.waitFor();
          assert.equal(await note.textContent(), w.said);
          assert.equal(await lib.locator(".lib-signin-err").count(), 0, "not said as an error");
          const code = lib.locator(".lib-signin-link code");
          assert.equal(await code.textContent(), MCP_LINK);
          // a poll later still shows it (the poll says nothing of linkOnly)
          await page.waitForResponse((r) => r.url().endsWith("/api/library/mcp-signin/m1"));
          await note.waitFor();
          assert.deepEqual(st.opens, [], "nothing was opened by the start");
          assert.deepEqual(await popups(), [], "no tab of its own was opened");
          const box = await code.boundingBox(), modal = await page.locator("#modal .lib-editor").boundingBox();
          assert.ok(box.x + box.width <= modal.x + modal.width + 1, "the link stays inside the editor");
          assert.equal(await overflow(page), 0, "nothing past the right edge");
          // the reader's own Open in browser opens it
          const open = lib.locator(".lib-signin-link button", { hasText: w.open });
          if (web) {
            await open.click();
            await page.waitForFunction(() => window.__opened.length > 0);
            assert.deepEqual(await popups(), [MCP_LINK]);
          } else {
            await Promise.all([page.waitForResponse((r) => r.url().endsWith("/api/open")), open.click()]);
            assert.deepEqual(st.opens, [MCP_LINK]);
          }
          await ctx.close();
          assert.deepEqual(errors, []);
        });
      }
    }
  }
}
