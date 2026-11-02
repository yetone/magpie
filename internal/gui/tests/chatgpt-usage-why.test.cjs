// Run with Node's test runner and Playwright on the module path; see README.md.
// A ChatGPT account whose usage read failed says why on its row, in every
// language, rather than only "Allowance unavailable" (hmy on Discord: five
// ChatGPT accounts in use, each card saying only that). The errors are the
// words codexUsageWords (codex_usage_words.go) gives the real replies in
// TestCodexUsageSaysWhy; the raw error stays in the row's tooltip. An error
// no one has words for says the tooltip is there. At the window's narrowest
// and in a phone's browser. A ChatGPT API account, whose usage OpenAI's API
// doesn't tell, says where it is told (danzi002 on Discord).
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

// account, error as the gateway sends it, the English key and its values
const cases = [
  ["cf@example.com", "ChatGPT's Cloudflare check turned the usage read away (HTTP 403 Forbidden): chatgpt.com wants a browser check from this network; a proxy or VPN address is the usual cause",
    "ChatGPT's Cloudflare check stopped the read — usually a proxy or VPN address; try another one", {}],
  ["401@example.com", "ChatGPT answered the usage read with HTTP 401 Unauthorized: Could not parse your authentication token. Please try signing in again.",
    "ChatGPT answered HTTP {status} — hover for what it said", { status: "401" }],
  ["403@example.com", "ChatGPT answered the usage read with HTTP 403 Forbidden: deactivated_workspace",
    "ChatGPT answered HTTP {status} — hover for what it said", { status: "403" }],
  ["net@example.com", "couldn't reach chatgpt.com for the usage read: dial tcp: lookup chatgpt.com: no such host",
    "Couldn't reach ChatGPT — check the network or the proxy", {}],
  ["json@example.com", "ChatGPT's usage reply isn't one magpie reads: unexpected end of JSON input",
    "ChatGPT answered in a form magpie doesn't read — hover for what it sent", {}],
  ["asked@example.com", "not read yet: magpie reads allowances only when you ask (Settings → Usage)",
    "Not read yet — magpie reads allowances only when you ask; refresh this card to read it", {}],
  ["other@example.com", "something nobody has words for",
    "Allowance unavailable — hover for why", {}],
];

const codex = () => ({
  id: "codex", name: "Codex", icon: "openai", chat: "", responses: "", anthropic: "", catalog: "", routing: "",
  models: [{ id: "gpt-5.5", name: "GPT-5.5", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: {
    agent: "codex", agentName: "Codex", user: cases[0][0], plan: "pro",
    logins: cases.map(([user], i) => ({ user, plan: "pro", active: i === 0, on: true })),
  },
});

const serve = (lang, web) => async (route) => {
  const url = new URL(route.request().url());
  const json = (data) => route.fulfill({ json: data });
  if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:${JSON.stringify(lang)},theme:"light",web:${web}};` });
  if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
  if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
  if (url.pathname === "/api/providers") return json({ providers: [codex()], presets: [], excluded: [], gateway: { running: true, window: true } });
  if (url.pathname === "/api/groups") return json({ groups: [] });
  if (url.pathname === "/api/plugins") return json({ plugins: [] });
  if (url.pathname === "/api/login/usage") return json(Object.fromEntries(cases.map(([user, error]) => [user, { error }])));
  if (url.pathname.startsWith("/api/")) return json({});
  const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
  const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
  try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
};

for (const [engine, browserType] of [["chromium", chromium], ["webkit", webkit]]) {
  test(engine + ": a ChatGPT account's failed usage read says why on its row", async (t) => {
    const browser = await browserType.launch();
    t.after(() => browser.close());
    for (const [web, width] of [[false, 560], [true, 390]]) for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
      const at = `${lang} ${web ? "web " : ""}${width}`;
      const context = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
      const page = await context.newPage();
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, web));
      await page.goto("http://magpie.test/?view=providers");
      await page.locator(".row.provider", { hasText: "Codex" }).first().click();
      const rows = page.locator(".editor .accts .acc[data-account-id]");
      await rows.nth(cases.length - 1).locator(".aq-none").waitFor();
      const generic = await page.evaluate(() => t("Allowance unavailable — hover for why"));
      for (const [user, error, key, vars] of cases) {
        const row = page.locator(`.editor .accts .acc[data-account-id="${user}"]`);
        const want = await page.evaluate(([k, v]) => t(k, v), [key, vars]);
        const said = await row.locator(".aq-none").textContent();
        assert.ok(said.endsWith(want), `${at}: ${user} says ${JSON.stringify(said)}, want ${JSON.stringify(want)}`);
        if (key !== "Allowance unavailable — hover for why") assert.ok(!said.includes(generic), `${at}: ${user} is not the generic line`);
        if (lang !== "en") assert.ok(!said.includes(key.replace("{status}", vars.status || "")), `${at}: ${lang} has its own words for ${user}`);
        if (vars.status) assert.ok(said.includes(vars.status), `${at}: ${user} names the status`);
        assert.equal(await row.locator(".aq").getAttribute("title"), error, `${at}: ${user}'s tooltip is the error itself`);
        // the words fit the row: nothing runs past its right edge
        const over = await row.evaluate((r) => { const b = r.getBoundingClientRect(), q = r.querySelector(".aq").getBoundingClientRect(); return Math.round(q.right - b.right); });
        assert.ok(over <= 0, `${at}: ${user}'s line runs ${over}px past its row`);
      }
      // a ChatGPT API account (siwcNoUsage): OpenAI's API tells no usage,
      // and the line says where it is told (danzi002 on Discord)
      const siwc = "a ChatGPT API account's usage isn't told through OpenAI's API, only at https://chatgpt.com/settings/usage; the same account added as ChatGPT shows it here";
      const siwcKey = "OpenAI's API doesn't tell a ChatGPT API account's usage — see chatgpt.com/settings/usage, or add the account as ChatGPT to see it here";
      const [siwcSaid, siwcWant] = await page.evaluate(([e, k]) => [quotaError(e), t(k)], [siwc, siwcKey]);
      assert.equal(siwcSaid, siwcWant, `${at}: a ChatGPT API account says where its usage is`);
      assert.ok(siwcSaid.includes("chatgpt.com/settings/usage"), at);
      if (lang !== "en") assert.notEqual(siwcSaid, siwcKey, `${at}: ${lang} has its own words`);
      assert.deepEqual(errors, [], at);
      await context.close();
    }
  });
}
