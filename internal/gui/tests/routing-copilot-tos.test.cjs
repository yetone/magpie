// Run with Node's test runner and Playwright on the module path; see README.md.
// GitHub's 403 on a Copilot account's token trade under its Terms of
// Service (yidasanqian on X): the Routing page tells the try as the account
// restricted, not "failed" or signed out, gives GitHub's words as they
// were and magpie's hint apart, in the page's own language, and the
// resting account's row lifts its rest by hand — in every language the
// page speaks.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { reach } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");
const now = new Date();
const day = [now.getFullYear(), now.getMonth() + 1, now.getDate()].map((n) => String(n).padStart(2, "0")).join("-");
const start = new Date(now.getTime() - 60e3).toISOString();
const hint = "GitHub turned this account's Copilot requests away under its Terms of Service (its limits on automated and heavy use), not for its sign-in, so signing in again doesn't help; the account rests, and other accounts and providers are used. If it lasts, look for mail from GitHub about the account, or ask GitHub Support";
const vendor = "copilot: GitHub refused this Copilot account (Resource not accessible by integration. For more on scraping GitHub and how it may affect your rights, please review our Terms of Service (https://docs.github.com/en/site-policy/github-terms/github-terms-of-service))";
const a = { id: "copilot#yida", provider: "copilot", name: "GitHub Copilot", kind: "account", who: "yida", model: "claude-haiku-4.5" };
const b = { id: "copilot#other", provider: "copilot", name: "GitHub Copilot", kind: "account", who: "other", model: "claude-haiku-4.5" };
const rest = { why: "restricted", by: "restricted", status: 502, key: "copilot@yida", until: new Date(now.getTime() + 2 * 3600e3 - 60e3).toISOString() };
const routes = [{
  id: 100, seq: 100, time: start, agent: "claude", model: "copilot/claude-haiku-4.5", provider: "copilot",
  order: [a, b],
  tries: [
    { id: a.id, model: a.model, start, done: true, status: 502, ms: 400, fail: "restricted", rest, error: vendor + " — " + hint },
    { id: b.id, model: b.model, start, done: true, status: 200, ms: 1200 },
  ],
  done: true, status: 200, ms: 2100,
}];

function serve(lang, lifted) {
  const state = { agents: [{ id: "claude", name: "Claude Code", path: "/test/settings.json", fields: [] }], profiles: [], settings: { lang, theme: "light" } };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/gateway/trace") {
      if (url.searchParams.get("wait")) await new Promise((r) => setTimeout(r, 20e3));
      return json({ mine: true, now: now.toISOString(), seq: 1, totals: { requests: 0, rerouted: 0, errors: 0 }, routes: [] });
    }
    if (url.pathname === "/api/gateway/history") {
      const d = url.searchParams.get("day");
      return json({ cut: false, days: [{ day, requests: routes.length }], routes: d ? routes : [] });
    }
    if (url.pathname === "/api/gateway/unrest") {
      lifted.push(JSON.parse(route.request().postData() || "{}"));
      return json({ lifted: true });
    }
    if (url.pathname === "/api/groups") return json({ groups: [] });
    if (url.pathname === "/api/providers") return json({ providers: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const want = {
  en: { label: "account restricted by the vendor", why: "only the vendor lifting the restriction brings it back. Appeal with the vendor", hint, lift: "The restriction is lifted — try it again" },
  zh: { label: "账号被服务商限制", why: "只有服务商解除限制才能让它恢复。请向服务商申诉", hint: "GitHub 依据其服务条款（对自动化和大量使用的限制）拒绝了这个账号的 Copilot 请求，并不是登录失效，重新登录也没有用；这个账号会暂停使用，改用其他账号或供应商。如果一直这样，请查看 GitHub 发来的关于该账号的邮件，或联系 GitHub Support", lift: "限制已解除，重新使用" },
  "zh-TW": { label: "帳號被服務商限制", why: "只有服務商解除限制才能讓它恢復。請向服務商申訴", hint: "GitHub 依據其服務條款（對自動化和大量使用的限制）拒絕了這個帳號的 Copilot 請求，並不是登入失效，重新登入也沒有用；這個帳號會暫停使用，改用其他帳號或供應商。如果一直這樣，請查看 GitHub 寄來的關於該帳號的郵件，或聯絡 GitHub Support", lift: "限制已解除，重新使用" },
  ja: { label: "ベンダーがアカウントを制限", why: "制限を解除できるのはベンダーだけなので", hint: "GitHub が利用規約（自動化された大量の利用への制限）に基づいて、このアカウントの Copilot リクエストを拒否しました。サインインの問題ではないので、サインインし直しても解決しません。このアカウントは休止し、他のアカウントやプロバイダが使われます。続く場合は、このアカウントについて GitHub からのメールを確認するか、GitHub Support に問い合わせてください", lift: "制限が解除された — もう一度試す" },
  de: { label: "Konto vom Anbieter gesperrt", why: "denn nur die Aufhebung der Sperre durch den Anbieter bringt es zurück. Legen Sie beim Anbieter Einspruch ein", hint: "GitHub hat die Copilot-Anfragen dieses Kontos nach seinen Nutzungsbedingungen (seinen Grenzen für automatisierte und intensive Nutzung) abgewiesen, nicht wegen der Anmeldung, daher hilft erneutes Anmelden nicht; das Konto pausiert, und andere Konten und Anbieter werden genutzt. Hält es an, suchen Sie nach einer E-Mail von GitHub zu dem Konto oder wenden Sie sich an den GitHub Support", lift: "Die Sperre ist aufgehoben – erneut versuchen" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(want)) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}px: a Copilot account GitHub turned away under its terms is told as restricted, with the way back`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const context = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], lifted = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, lifted));
        t.after(async () => {
          if (process.env.ARTIFACT_DIR) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-${lang}-${width}-copilot-tos.png`), fullPage: true });
          }
          await browser.close();
        });
        await page.goto("http://magpie.test/?view=routing");
        await page.locator(".rt-days .rt-day").nth(1).click();
        await page.locator(".rt-req").first().waitFor();
        await page.locator(".rt-req").first().click();
        await page.locator(".rt-steps li").first().waitFor();
        await page.waitForTimeout(300);
        const w = want[lang];
        const steps = await page.locator(".rt-steps li").evaluateAll((ls) => ls.map((l) => [l.className, l.textContent]));
        // the try: the label, and why it rests as long as it does
        const told = steps.map(([, s]) => s).filter((s) => s.includes(w.label));
        assert.equal(told.length, 1, JSON.stringify(steps));
        assert(told[0].includes(w.why), told[0]);
        // the vendor's words as they were, and magpie's hint on its own line
        assert.equal(steps.filter(([c]) => c === "aside said").length, 1, JSON.stringify(steps));
        assert(steps.find(([c]) => c === "aside said")[1].endsWith(vendor), JSON.stringify(steps));
        assert.deepEqual(steps.filter(([, s]) => s === w.hint).map(([c]) => c), ["aside"]);
        if (lang !== "en") assert(!steps.some(([, s]) => s.includes("signing in again")), JSON.stringify(steps));
        // never told to sign in again
        assert(!steps.some(([, s]) => s.includes("sign in to Copilot again")), JSON.stringify(steps));

        // the account's row: resting, restricted, and a way to lift it
        const row = page.locator(".rt-act").filter({ hasText: w.label }).first();
        await row.waitFor();
        assert((await row.locator(".st.rest").textContent()).includes(w.label));
        const go = row.locator(".fix button", { hasText: w.lift });
        await go.waitFor();
        // nothing of it runs past the page's width
        assert(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1), "the page scrolls sideways");
        // scrolled to where a reader sees it, it is the button under the
        // pointer, not the footer
        // a reader scrolls down to it with the wheel
        await reach(page, go);
        await go.click();
        // the lift once it is posted: a fixed pause read it before the
        // post reached the fake on a busy machine
        for (let i = 0; i < 100 && !lifted.length; i++) await new Promise((r) => setTimeout(r, 50));
        assert.deepEqual(lifted, [{ key: rest.key }]);
        assert.deepEqual(errors, []);
      });
    }
  }
}
