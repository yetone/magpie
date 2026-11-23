// Run with Node's test runner and Playwright on the module path; see README.md.
// A Mistral Vibe plan (Pro, Student, Team) had no way in but a key copied
// from the console (jtbbtnt54249 on X: "magpie 不支持 Mistral 订阅"). The
// Mistral editor now signs in to Mistral the way Vibe does: Mistral's page
// opens in the browser, magpie waits, and the plan's key goes in the key
// box and is saved; Cancel stops the wait. No backend, the API is faked
// here (mistral_signin_test.go has the console's side).
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const PAGE = "https://console.mistral.ai/codestral/cli/authenticate?process_id=p-1&complete_token=c";
const preset = { id: "mistral", name: "Mistral", icon: "mistral-color", kind: "vendor", catalog: "mistral", chat: "https://api.mistral.ai/v1", website: "https://console.mistral.ai", keysUrl: "https://console.mistral.ai/api-keys", note: "API key or Vibe plan" };
const mistral = {
  id: "mistral", name: "Mistral", icon: "mistral-color", preset: "mistral", chat: "https://api.mistral.ai/v1", responses: "", anthropic: "", catalog: "mistral",
  models: [{ id: "devstral-medium-latest", name: "Devstral Medium", on: true }], agents: [], fallback: [], headers: {},
  key: { set: false, masked: "" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
};

function serve(lang, posts, flow) {
  const settings = { theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd" };
  const list = () => ({ providers: [mistral], presets: [preset], excluded: [], gateway: { running: true, window: true } });
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (body) => route.fulfill({ json: body });
    // Mistral's page, opened in a tab of its own: never the network
    if (url.host !== "magpie.test") {
      posts.push({ method: "TAB", path: url.href });
      return route.fulfill({ contentType: "text/html", body: "" });
    }
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json(list());
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage/quotas/history") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/mistral/") || url.pathname === "/api/provider/save") {
      const method = route.request().method();
      posts.push({ method, path: url.pathname, body: method === "POST" ? route.request().postDataJSON() : null });
      if (url.pathname === "/api/provider/save") return json(list());
      if (url.pathname === "/api/mistral/signin") return json({ id: "f" + ++flow.started, url: PAGE });
      if (url.pathname.endsWith("/cancel")) return route.fulfill({ status: 204, body: "" });
      // pending until the test lets it finish
      if (!flow.finish) return json({ state: "pending" });
      return json({ state: "done", key: "vibe-plan-key-1" });
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: { signIn: "Sign in with Mistral ↗", approve: "Approve the sign-in in your browser…", cancel: "Cancel" },
  zh: { signIn: "用 Mistral 登录 ↗", approve: "请在浏览器里批准登录…", cancel: "取消" },
  "zh-TW": { signIn: "用 Mistral 登入 ↗", approve: "請在瀏覽器裡核准登入…", cancel: "取消" },
  ja: { signIn: "Mistral でサインイン ↗", approve: "ブラウザでサインインを承認してください…", cancel: "キャンセル" },
  de: { signIn: "Mit Mistral anmelden ↗", approve: "Bestätige die Anmeldung im Browser …", cancel: "Abbrechen" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [lang, w] of Object.entries(words)) {
    for (const width of [900, 420]) {
      test(`${engine} ${lang} ${width}px: a Mistral editor signs in for a Vibe plan's key and saves it`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const errors = [];
        const ctx = await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" });
        const page = await ctx.newPage();
        page.setDefaultTimeout(8000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        const posts = [];
        const flow = { started: 0, finish: false };
        await ctx.route("**/*", serve(lang, posts, flow));
        await page.goto("http://magpie.test/?view=providers&edit=mistral");

        const ed = page.locator(".editor");
        const signIn = ed.getByRole("button", { name: w.signIn, exact: true });
        await signIn.waitFor();
        // the key row's own buttons, beside Show and Get a key
        const side = ed.locator(".pair .side").filter({ has: page.locator("button.vibe-in") });
        const sb = await signIn.boundingBox();
        assert.ok(sb.x + sb.width <= width, "the sign-in fits the editor");

        // Cancel stops the wait and asks the server to stop too
        await signIn.click();
        await side.getByRole("button", { name: w.approve, exact: true }).waitFor();
        await side.getByRole("button", { name: w.cancel, exact: true }).click();
        await signIn.waitFor();
        for (let i = 0; i < 50 && !posts.some((p) => p.path.endsWith("/cancel")); i++) await page.waitForTimeout(50);
        assert.ok(posts.some((p) => p.path === "/api/mistral/signin/f1/cancel"), JSON.stringify(posts));

        // Mistral's page opens in the browser, and magpie waits
        await signIn.click();
        await ed.getByRole("button", { name: w.approve, exact: true }).waitFor();
        for (let i = 0; i < 50 && posts.filter((p) => p.method === "TAB").length < 2; i++) await page.waitForTimeout(50);
        assert.deepEqual(posts.filter((p) => p.method === "TAB").map((p) => p.path), [PAGE, PAGE]);
        assert.ok(!posts.some((p) => p.path === "/api/provider/save"), "nothing saved before the sign-in finishes");

        // signed in: the plan's key is saved as the provider's
        flow.finish = true;
        for (let i = 0; i < 200 && !posts.some((p) => p.path === "/api/provider/save"); i++) await page.waitForTimeout(50);
        const saved = posts.find((p) => p.path === "/api/provider/save");
        assert.ok(saved, JSON.stringify(posts));
        assert.equal(saved.body.id, "mistral");
        assert.equal(saved.body.key, "vibe-plan-key-1");
        assert.deepEqual(errors, []);
      });
    }
  }
}
