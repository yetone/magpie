const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const lapse = "Claude Code could not authenticate this account; sign in again in magpie";
const error = "Failed to authenticate: OAuth session expired and could not be refreshed";
const at = new Date().toISOString();
const expired = { id: "claude@expired", provider: "claude", name: "Claude Code", kind: "account", who: "expired@example.com", model: "claude-opus-5-5" };
const healthy = { ...expired, id: "claude", who: "healthy@example.com" };
const routes = [
  { id: 103, seq: 103, time: at, agent: "codex", model: "claude/claude-opus-5-5", provider: "claude",
    order: [expired, healthy], tries: [
      { id: expired.id, model: expired.model, done: true, status: 502, error, fail: "auth", ms: 300 },
      { id: healthy.id, model: healthy.model, done: true, status: 200, ms: 300 },
    ], done: true, status: 200 },
  { id: 102, seq: 102, time: at, agent: "codex", model: "claude/claude-opus-5-5", provider: "claude",
    order: [expired], tries: [{ id: expired.id, model: expired.model, done: true, status: 502, error, fail: "auth", ms: 300 }],
    done: true, status: 502, error },
];
const provider = {
  id: "claude", name: "Claude Code", icon: "claudecode-color", anthropic: "https://api.example.test",
  chat: "", responses: "", catalog: "", models: [{ id: "claude-opus-5-5", on: true }],
  agents: [], fallback: [], headers: {}, keyList: [], proxy: "",
  account: { agent: "claude", agentName: "Claude Code", user: healthy.who, plan: "max", logins: [
    { user: healthy.who, plan: "max", active: true, on: true },
    { user: expired.who, plan: "enterprise", on: true, lapsed: lapse },
  ] },
};
// Claude Code still signed in to the refused account: it can't be removed
const signedInRefused = { ...provider, account: { ...provider.account, user: expired.who, logins: [
  { user: expired.who, plan: "enterprise", active: true, on: true, lapsed: lapse },
  { user: healthy.who, plan: "max", on: true },
] } };
// another subscription's lapse keeps its row as it was
const codex = {
  id: "codex", name: "Codex", icon: "codex-color", responses: "https://api.example.test", chat: "", anthropic: "", catalog: "",
  models: [{ id: "gpt-6-luna", on: true }], agents: [], fallback: [], headers: {}, keyList: [], proxy: "",
  account: { agent: "codex", agentName: "Codex", user: "a@example.com", plan: "plus", logins: [
    { user: "a@example.com", plan: "plus", active: true, on: true },
    { user: "b@example.com", plan: "plus", on: true, lapsed: "refresh token reused" },
  ] },
};

function serve(lang, providers = [provider]) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs={lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [{ id: "codex", name: "Codex", fields: [] }], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers, presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/provider/test") return json({ results: [{ protocol: "anthropic", ok: true, ms: 300, model: healthy.model, account: healthy.who }], provider });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: at, seq: 103, totals: { requests: 2, errors: 1 }, routes });
    }
    if (url.pathname === "/api/gateway/history") return json({ cut: false, days: [], routes: [] });
    if (url.pathname === "/api/login/usage") return json({});
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); }
    catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: Claude auth failures require sign-in instead of a timed retry`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 800 }, reducedMotion: "reduce" })).newPage();
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      page.setDefaultTimeout(5000);
      await page.route("**/*", serve(lang));
      await page.goto("http://magpie.test/?view=routing");
      const steps = page.locator(".rt-steps");
      await steps.getByText(lang === "zh" ? /当前登录不再重试/ : /this login is not retried/).waitFor();
      let story = await steps.textContent();
      assert(story.includes(error), "the original CLI error remains visible");
      assert(story.includes(healthy.who), "the fallback account is named");
      assert(!/backoff|rests|休息/.test(story), "authentication is not a cooldown");
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await steps.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-claude-auth-route.png`) });
      }
      await page.locator(".rt-req").nth(1).click();
      await steps.getByText(lang === "zh" ? /没有其他账号可以回答/ : /No other account could answer/).waitFor();
      assert((await steps.textContent()).includes(error));

      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: "Claude Code" }).first().click();
      const row = page.locator(`.editor .acc[data-account-id="${expired.who}"]`);
      await row.waitFor();
      assert.equal(await row.locator(".dot").isDisabled(), true);
      assert.equal(await row.locator(".dot svg").count(), 0, "an expired account is not shown in use");
      assert.equal(await row.locator(".using").textContent(), lang === "zh" ? "需要重新登录" : "Sign-in required");
      await row.getByRole("button", { name: lang === "zh" ? "重新登录" : "Sign in again", exact: true }).waitFor();
      assert.equal(await row.getByRole("button", { name: lang === "zh" ? "设为首选" : "Make first", exact: true }).count(), 0);
      if (process.env.ARTIFACT_DIR) {
        await page.locator(".editor .accts").screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-claude-auth-accounts.png`) });
      }
      await page.locator(".editor .eps").getByRole("button", { name: lang === "zh" ? "测试" : "Test", exact: true }).click();
      await page.locator(".editor .res").getByText(new RegExp(healthy.who.replaceAll(".", "\\."))).waitFor();
      assert(!(await page.locator(".editor .res").textContent()).includes(expired.who), "test success identifies only the account tested");
      assert.deepEqual(errors, []);
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-claude-auth.png`) });
      }
    });
  }
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: only Claude rows need a new sign-in, and the signed-in one can't be removed`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await (await browser.newContext({ viewport: { width: 1100, height: 800 }, reducedMotion: "reduce" })).newPage();
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      page.setDefaultTimeout(5000);
      await page.route("**/*", serve(lang, [signedInRefused, codex]));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: "Claude Code" }).first().click();
      const own = page.locator(`.editor .acc[data-account-id="${expired.who}"]`);
      await own.waitFor();
      assert.equal(await own.locator(".dot").isDisabled(), true);
      await own.getByRole("button", { name: lang === "zh" ? "重新登录" : "Sign in again", exact: true }).waitFor();
      assert.equal(await own.getByRole("button", { name: lang === "zh" ? "移除" : "Remove", exact: true }).count(), 0, "Claude Code's own account can't be forgotten");

      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: "Codex" }).first().click();
      const other = page.locator('.editor .acc[data-account-id="b@example.com"]');
      await other.waitFor();
      assert.equal(await other.locator(".dot").isDisabled(), false, "a Codex account's dot still turns it off");
      assert.equal(await other.locator(".dot svg").count(), 1, "a Codex account on is shown on");
      assert.equal(await other.getByText(lang === "zh" ? "需要重新登录" : "Sign-in required", { exact: true }).count(), 0);
      assert.deepEqual(errors, []);
    });
  }
}
