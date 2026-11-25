// Run with Node's test runner and Playwright on the module path; see README.md.
// @OoopsHh on X: an MCP server's OAuth sign-in from the Library gave no way
// to finish it when the browser can't reach magpie's loopback callback
// (magpie on a server or in Docker, the page open on another machine). Now
// the sign-in box offers a "Callback URL" field: the address the browser
// ended on, pasted, finishes the sign-in. An error is shown and retried;
// the field submits once. Every language, Chromium and WebKit, 1100 and
// 420px; no backend, the API is faked here (@OoopsHh on X).
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const MCP_LINK = "https://dash.cloudflare.com/oauth2/auth?client_id=c1&state=" + "y".repeat(80);
const GOOD = "http://localhost:51088/callback?code=good&state=s1";
const BAD = "http://localhost:51088/callback?code=bad&state=wrong";
const REFUSED = "the pasted address was refused in this test";

function server(lang, web, st) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data, status) => route.fulfill({ status: status || 200, json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/open") { st.opens.push(req.postDataJSON().url); return route.fulfill({ status: 204, body: "" }); }
    if (url.pathname === "/api/library") {
      return json({
        dir: "/h/.magpie/library", backups: "/h/.magpie/backups", home: "/h",
        agents: [{ id: "codex", name: "Codex", icon: "", skills: "/h/.codex/skills", mcp: "/h/.codex/mcp.json" }],
        instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [],
        servers: [{ name: "cloudflare-api", transport: "http", url: "https://mcp.cloudflare.com/mcp", agents: ["codex"], signIn: { signedIn: st.done } }],
      });
    }
    if (url.pathname === "/api/library/mcp-signin") return json({ id: "m1", server: "cloudflare-api", state: "waiting", url: MCP_LINK, pasteCallback: true });
    // the pasted address: a wrong one is refused, the right one finishes it
    if (/\/api\/library\/mcp-signin\/m1\/callback$/.test(url.pathname)) {
      st.calls.push(req.postDataJSON().url);
      if (req.postDataJSON().url !== GOOD) return json({ error: REFUSED }, 400);
      st.done = true;
      return route.fulfill({ status: 204, body: "" });
    }
    if (url.pathname.startsWith("/api/library/mcp-signin/")) return json({ id: "m1", server: "cloudflare-api", state: st.done ? "done" : "waiting", url: MCP_LINK, pasteCallback: true });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const W = {
  en: { signIn: "Sign in", signedIn: "Signed in", label: "Callback URL", finish: "Finish sign-in", note: "If the page the browser ends on won't load (magpie runs on a server or in Docker), copy its whole address and paste it here." },
  zh: { signIn: "登录", signedIn: "已登录", label: "回调 URL", finish: "完成登录", note: "若浏览器最终停在打不开的页面（magpie 运行在服务器或 Docker 中），请复制其完整地址粘贴到这里。" },
  "zh-TW": { signIn: "登入", signedIn: "已登入", label: "回呼 URL", finish: "完成登入", note: "若瀏覽器最終停在打不開的頁面（magpie 執行在伺服器或 Docker 中），請複製其完整位址貼上到這裡。" },
  ja: { signIn: "サインイン", signedIn: "サインイン済み", label: "コールバック URL", finish: "サインインを完了", note: "ブラウザの最後のページが読み込めない場合（magpie がサーバーや Docker で動いているとき）は、そのアドレス全体をコピーしてここに貼り付けてください。" },
  de: { signIn: "Anmelden", signedIn: "Angemeldet", label: "Callback-URL", finish: "Anmeldung abschließen", note: "Lädt die Seite, auf der der Browser landet, nicht (magpie läuft auf einem Server oder in Docker), kopieren Sie ihre vollständige Adresse und fügen Sie sie hier ein." },
};

const overflow = (page) => page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(W)) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}: a pasted callback finishes an MCP sign-in the browser can't reach`, async (t) => {
        const w = W[lang];
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const errors = [];
        const st = { opens: [], calls: [], done: false };
        const ctx = await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" });
        await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "mcp"); } catch {} });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        // cover both web values across the width split, without doubling the grid
        await page.route("**/*", server(lang, width === 420, st));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        await page.locator("#view-library .lib-row", { has: page.locator(".name", { hasText: /^cloudflare-api/ }) }).click();
        const lib = page.locator("#modal .lib-signin");
        await Promise.all([page.waitForResponse((r) => r.url().endsWith("/api/library/mcp-signin")), lib.getByRole("button", { name: w.signIn, exact: true }).click()]);

        // the field and its note appear, the button off until something is typed
        const note = lib.locator(".note", { hasText: w.note });
        await note.waitFor();
        assert.equal(await note.textContent(), w.note);
        const input = lib.getByRole("textbox", { name: w.label });
        const submit = lib.getByRole("button", { name: w.finish, exact: true });
        await input.waitFor();
        assert(await submit.isDisabled(), "the button is off with nothing typed");

        // the field stays inside the editor at a narrow width
        const box = await input.boundingBox(), modal = await page.locator("#modal .lib-editor").boundingBox();
        assert.ok(box.x + box.width <= modal.x + modal.width + 1, "the field stays inside the editor");
        assert.equal(await overflow(page), 0, "nothing past the right edge");

        // a wrong address is refused and shown; the field stays open to try again
        await input.fill(BAD);
        assert(await submit.isEnabled());
        await Promise.all([page.waitForResponse((r) => /\/mcp-signin\/m1\/callback$/.test(r.url())), submit.click()]);
        await lib.locator(".lib-signin-err", { hasText: REFUSED }).waitFor();
        assert.equal(await input.inputValue(), BAD, "the typed address is kept");
        assert(await input.isEnabled());
        assert(await submit.isEnabled());

        // the right address finishes the sign-in; the poll then shows it signed in
        await input.fill(GOOD);
        await Promise.all([page.waitForResponse((r) => /\/mcp-signin\/m1\/callback$/.test(r.url())), submit.click()]);
        await lib.locator(".lib-signed", { hasText: w.signedIn }).waitFor();
        assert.deepEqual(st.calls, [BAD, GOOD], "each address was submitted once");
        assert.equal(await lib.getByRole("textbox", { name: w.label }).count(), 0, "the field is gone once signed in");

        await ctx.close();
        assert.deepEqual(errors, []);
      });
    }
  }
}
