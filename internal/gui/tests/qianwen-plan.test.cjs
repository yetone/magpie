// Run with Node's test runner and Playwright on the module path; see README.md.
// A Qwen AI platform Token Plan (qwen-token-plan) had no quota card at all
// (@imthe99galaxy on X): its usage is told only to a sign-in on the
// platform. The card now says so, also when the sign-in ended, in the user's
// language, with the button to the provider's settings; there the user
// signs in through their browser, magpie waits for the approval, and the
// editor then says whose sign-in it is, with Sign out for that provider's
// id. No backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
// qianwen_plan.go's words
const NONE = "this plan's usage is told only to a sign-in on the Qwen AI platform: sign in in Qwen Token Plan's settings";
const ENDED = "signed out of the Qwen AI platform: sign in again in the provider's settings";

const quotas = [
  { provider: "qwen-token-plan", name: "Qwen Token Plan", icon: "qwen-color", plan: "Token Plan", error: NONE, needsAccessKey: true, windows: [] },
  { provider: "qwen-work", name: "Qwen Work", icon: "qwen-color", plan: "Token Plan", error: ENDED, needsAccessKey: true, windows: [] },
];
const qwen = (id, name, signedIn, account) => ({
  id, name, icon: "qwen-color", preset: "qwen-token-plan", chat: "https://token-plan.maas.qianwenaiapi.com/compatible-mode/v1", responses: "", anthropic: "https://token-plan.maas.qianwenaiapi.com/apps/anthropic", catalog: "",
  models: [{ id: "qwen3.6-plus", name: "Qwen3.6 Plus", on: true }], agents: [], fallback: [], headers: {},
  key: { set: true, masked: "sk-sp-…ab12" }, keyList: [], balanceToken: { takes: false, set: false }, proxy: "",
  qianwenPlan: { signedIn, ...(account ? { account } : {}) },
});

function serve(lang, posts, flow) {
  const settings = { theme: "light", lang, tray: "panel", quotaLeft: false, currency: "usd" };
  const list = () => ({ providers: [qwen("qwen-token-plan", "Qwen Token Plan", flow.done, flow.done ? "a@example.com" : ""), qwen("qwen-work", "Qwen Work", false)], presets: [], excluded: [], gateway: { running: true, window: true } });
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (body) => route.fulfill({ json: body });
    // the platform's page, opened in a tab of its own: never the network
    if (url.host !== "magpie.test") {
      posts.push({ method: "TAB", path: url.href });
      return route.fulfill({ contentType: "text/html", body: "" });
    }
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") return json(settings);
    if (url.pathname === "/api/providers") return json(list());
    if (url.pathname === "/api/usage/quotas") return json(quotas);
    if (url.pathname === "/api/usage/quotas/history") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/qianwen/")) {
      const method = route.request().method();
      posts.push({ method, path: url.pathname, body: method === "POST" ? route.request().postDataJSON() : null });
      if (url.pathname.endsWith("/signin") && method === "POST") return json({ url: "https://account.qianwenai.com/sso/OIDCAuth?auth_token=t1" });
      if (url.pathname.endsWith("/signin")) {
        // pending once, then approved
        flow.asked++;
        if (flow.asked > 1) flow.done = true;
        return json({ state: flow.done ? "done" : "pending" });
      }
      if (url.pathname.endsWith("/signout")) return json(list());
      return json({});
    }
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

const words = {
  en: { none: "This plan's usage needs a sign-in to the Qwen AI platform — sign in in the provider's settings", open: "Open settings", signIn: "Sign in to the Qwen AI platform ↗", approve: "Approve the sign-in in your browser…", as: "Signed in as a@example.com", out: "Sign out" },
  zh: { none: "这个套餐的用量需要登录千问 AI 平台——在提供商设置里登录", open: "打开设置", signIn: "登录千问 AI 平台 ↗", approve: "请在浏览器里批准登录…", as: "已登录 a@example.com", out: "退出登录" },
  "zh-TW": { none: "這個方案的用量需要登入千問 AI 平台——在供應商設定裡登入", open: "開啟設定", signIn: "登入千問 AI 平台 ↗", approve: "請在瀏覽器裡核准登入…", as: "已登入 a@example.com", out: "退出登入" },
  ja: { none: "このプランの利用量には Qwen AI プラットフォームへのサインインが必要です — プロバイダの設定でサインインしてください", open: "設定を開く", signIn: "Qwen AI プラットフォームにサインイン ↗", approve: "ブラウザでサインインを承認してください…", as: "a@example.com としてサインインしました", out: "サインアウト" },
  de: { none: "Die Nutzung dieses Plans braucht eine Anmeldung bei der Qwen-AI-Plattform — melde dich in den Einstellungen des Anbieters an", open: "Einstellungen öffnen", signIn: "Bei der Qwen-AI-Plattform anmelden ↗", approve: "Bestätige die Anmeldung im Browser …", as: "Angemeldet als a@example.com", out: "Abmelden" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const [lang, w] of Object.entries(words)) {
    for (const width of [900, 420]) {
      test(`${engine} ${lang} ${width}px: a Qwen AI platform plan's card asks for the sign-in, which the editor makes`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const errors = [];
        const ctx = await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" });
        const page = await ctx.newPage();
        page.setDefaultTimeout(8000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        const posts = [];
        const flow = { asked: 0, done: false };
        await ctx.route("**/*", serve(lang, posts, flow));
        await page.goto("http://magpie.test/?view=usage");

        // the card says why it has no windows, the reason in its tooltip
        const err = page.locator('.subscription-card[data-provider="qwen-token-plan"] .subscription-error');
        await err.waitFor();
        assert.equal(await err.getAttribute("title"), NONE);
        assert.ok((await err.textContent()).includes(w.none), await err.textContent());
        const ended = page.locator('.subscription-card[data-provider="qwen-work"] .subscription-error');
        assert.ok((await ended.textContent()).includes(w.none), await ended.textContent());
        const go = err.locator(".quota-fix");
        assert.equal((await go.textContent()).trim(), w.open);
        const [e, b] = [await err.boundingBox(), await go.boundingBox()];
        assert.ok(e.x + e.width <= width && b.x + b.width <= width, "the error and its button fit the page");

        // to the provider's own settings, where the sign-in starts
        await go.click();
        await page.waitForURL(/view=providers/);
        assert.equal(new URL(page.url()).searchParams.get("edit"), "qwen-token-plan");
        const box = page.locator(".editor .stepplan");
        const signIn = box.getByRole("button", { name: w.signIn, exact: true });
        await signIn.click();
        // the platform's page opens in the browser, and magpie waits
        await box.getByText(w.approve, { exact: true }).waitFor();
        for (let i = 0; i < 50 && !posts.some((p) => p.method === "TAB"); i++) await page.waitForTimeout(50);
        assert.deepEqual(posts.slice(0, 2).map((p) => p.method + " " + p.path), ["POST /api/qianwen/qwen-token-plan/signin", "TAB https://account.qianwenai.com/sso/OIDCAuth?auth_token=t1"]);
        // approved: whose sign-in it is, and Sign out for this provider
        await box.getByText(w.as, { exact: true }).waitFor({ timeout: 15000 });
        const sb = await box.boundingBox();
        assert.ok(sb.x + sb.width <= width, "the sign-in fits the editor");
        await box.getByRole("button", { name: w.out, exact: true }).click();
        for (let i = 0; i < 50 && !posts.some((p) => p.path.endsWith("/signout")); i++) await page.waitForTimeout(50);
        assert.ok(posts.some((p) => p.path === "/api/qianwen/qwen-token-plan/signout"), JSON.stringify(posts));
        assert.ok(!posts.some((p) => p.path.includes("qwen-work")), "nothing for the other provider");
        assert.deepEqual(errors, []);
      });
    }
  }
}
