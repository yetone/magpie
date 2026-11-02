// The current Claude account can be signed in without its interactive
// first-run setup. Its /usage helper goes through those screens; one it
// can't get past ends the read. A successful refresh HTTP reply still
// carries that account's error: pressing Refresh must say what failed
// and how to recover, on every surface, without changing the account or
// falling back to print mode.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = process.env.MAGPIE_QUOTA_ASSETS || path.resolve(__dirname, "../assets");
const user = "current@example.invalid", other = "saved@example.invalid";
// The error the helper ends a read with when a first-run screen stays
// (claudetui's onboard), the screen's text after it cut short; and the
// one the reporter's gateway returned before it went through them
// (#4817), as an older remote magpie still says it.
const error = "Claude Code: claude-tui: Claude Code's interactive mode stays on its first-run screens, its onboarding not done; run Claude Code once in a terminal and go through them: Security notes:";
const olderError = "Claude Code: claude-tui: Claude Code's interactive mode shows its first-run notes, its onboarding not done: Security notes:";
const advice = "Finish Claude Code's first-run setup, then refresh. Run magpie claude-code to find its executable.";
const provider = { id: "claude", name: "Claude Code", icon: "claude", models: [], agents: [], key: {},
  account: { agent: "claude", agentName: "Claude Code", user, logins: [
    { user, plan: "pro", active: true, on: true }, { user: other, plan: "pro", active: false, on: true },
  ] } };

function serve(lang, state) {
  return async route => {
    const url = new URL(route.request().url()), json = body => route.fulfill({ json: body });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: "light", web: false })};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [provider], presets: [], excluded: [], plugins: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/claude/mode") return json({ tui: true });
    if (url.pathname === "/api/login/usage") return json(Object.fromEntries(state.cards.map(q => [q.user, q])));
    if (url.pathname === "/api/usage/quotas/refresh") {
      assert.equal(route.request().method(), "POST");
      assert.equal(url.searchParams.get("provider"), "claude");
      assert.equal(url.searchParams.get("user"), user);
      state.refreshes++;
      if (state.recovered) state.cards[0] = { provider: "claude", name: "Claude Code", user, readAt: new Date().toISOString(), windows: [{ name: "5 hours", used: 18 }] };
      return json(state.cards);
    }
    if (url.pathname === "/api/usage/quotas") return json(state.cards);
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname.startsWith("/api/")) {
      assert.equal(route.request().method(), "GET", `unexpected mutation: ${url.pathname}`);
      return json({});
    }
    if (url.host !== "magpie.test" || !/^\/[\w./-]*$/.test(url.pathname)) return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: clicking the current Claude account's refresh reports onboarding and recovers`, async t => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await browser.newPage({ viewport: { width: 440, height: 760 }, reducedMotion: "reduce" });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], state = { refreshes: 0, recovered: false, cards: [
        { provider: "claude", name: "Claude Code", user, plan: "pro", windows: [], error },
        { provider: "claude", name: "Claude Code", user: other, windows: [{ name: "7 days", used: 10 }] },
      ] };
      page.on("pageerror", e => errors.push(e.message));
      await page.route("**/*", serve(lang, state));
      await page.goto("http://magpie.test/?view=providers");
      const providerRow = page.locator('.row.provider[data-id="claude"]');
      await providerRow.waitFor({ timeout: 60000 });
      await providerRow.click();
      const row = page.locator("#modal .acc", { hasText: user });
      await row.locator(".aq-none").waitFor();
      const want = await page.evaluate(k => t(k), advice);
      await row.locator(".quota-refresh").click();
      await page.locator('#status[role="alert"]').waitFor();
      assert.equal(await page.locator("#status").textContent(), want);
      assert.equal(state.refreshes, 1, "one press reads only this account");
      assert.equal(await row.locator(".aq-none").textContent(), want);
      assert.equal(await row.locator(".aq").getAttribute("title"), error, "the original detail stays available");
      if (lang !== "en") assert.notEqual(want, advice, "the recovery advice is translated");
      const fits = await row.locator(".aq-none").evaluate(e => {
        const r = document.createRange(); r.selectNodeContents(e);
        return [...r.getClientRects()].every(b => b.left >= 0 && b.right <= innerWidth + 1);
      });
      assert.ok(fits, "the recovery advice fits the narrow account row");
      if (process.env.ARTIFACT_DIR && ["en", "zh"].includes(lang)) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `claude-quota-${engine}-${lang}.png`) });
      }

      // The shared wording also reaches Usage and the tray, without a
      // refresh of their own or any account-setting mutation.
      const siblings = await page.evaluate(q => ({
        usage: quotaWindows(q).textContent,
        panel: panelQuotaCard(q).querySelector(".pq-sub.err").textContent,
      }), state.cards[0]);
      for (const text of Object.values(siblings)) assert.ok(text.includes(want), text);
      assert.equal(await page.evaluate(() => quotaError("network is unreachable")), await page.evaluate(() => t("Allowance unavailable — hover for why")), "unrelated errors keep their classification");
      assert.equal(await page.evaluate(e => quotaError(e), olderError), want, "an older magpie's words are read too");
      // any other reason the helper gave, and a run that said none before
      // magpie stopped waiting, are said on the card, not "unavailable"
      // alone (#4830); the last screen after the reason stays for the
      // tooltip
      for (const [err, why] of [
        ["Claude Code: claude-tui: Claude Code showed no prompt in the 16.9s its caller waits for it: Do you trust this folder? ❯ No, exit",
          "Claude Code showed no prompt in the 16.9s its caller waits for it"],
        ["Claude Code: claude-tui: Claude Code asks whether to trust the folder, which only its user answers\nmagpie: Claude Code's interactive mode didn't run: Claude Code asks whether to trust the folder, which only its user answers",
          "Claude Code asks whether to trust the folder, which only its user answers"],
        ["Claude Code's interactive mode said nothing before magpie stopped waiting for it (context deadline exceeded)",
          "Claude Code's interactive mode said nothing before magpie stopped waiting for it"],
      ]) {
        const said = await page.evaluate(([e, w]) => [quotaError(e), t("Allowance unavailable: {why}", { why: w })], [err, why]);
        assert.equal(said[0], said[1], err);
        if (lang !== "en") assert.ok(!said[0].startsWith("Allowance unavailable"), said[0]);
      }

      await page.waitForFunction(() => !quotaRefreshing.size);
      // Simulate the next read after the user completes setup. Another
      // account's failure in the same HTTP reply is not this click's error.
      state.recovered = true;
      state.cards[1] = { ...state.cards[1], windows: [], error: "another account failed" };
      await page.evaluate(() => status(""));
      await row.locator(".quota-refresh").click();
      await row.locator(".aq-w").waitFor();
      await page.waitForFunction(() => !quotaRefreshing.size);
      assert.equal(state.refreshes, 2);
      assert.equal(await row.locator(".aq-none").count(), 0);
      assert.equal(await page.locator("#status").textContent(), "", "a successful target read doesn't report another account's error");
      assert.deepEqual(errors, []);
    });
  }
}
