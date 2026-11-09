// Run with Node's test runner and Playwright on the module path; see README.md.
// A repository tagged magpie-plugin that is gateway hooks alone
// (package.json's magpie.hooks and no entry of its own; MengXiangxi,
// #3821) is in Discover as an installed one's row has it:
// the gateway's chip saying Request hook, never Provider, and picked out
// under Middleware, as it runs in the gateway too. The repositories are
// seven of /api/plugins/github's real answer of 2026-10-27, from a magpie
// built at this change: two providers, three middleware and two of hooks
// alone.
// The API is faked here and no request leaves the page. English, Chinese,
// Traditional Chinese, Japanese and German, 980px and 420px.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const REPOS = [
  { "spec": "magpie-gemini-web", "repo": "BUKOWSKIREAL/magpie-gemini-web", "url": "https://github.com/BUKOWSKIREAL/magpie-gemini-web", "owner": "BUKOWSKIREAL", "ownerAvatar": "https://avatars.githubusercontent.com/u/233685554?v=4", "description": "Gemini Web provider for Magpie with a local backend and experimental Argon support", "stars": 101, "license": "MIT", "pushed": "2026-10-24T05:28:43Z", "package": "magpie-gemini-web", "version": "0.5.6" },
  { "spec": "github:mu-zi-lee/magpie-doubao-translate", "repo": "mu-zi-lee/magpie-doubao-translate", "url": "https://github.com/mu-zi-lee/magpie-doubao-translate", "owner": "mu-zi-lee", "ownerAvatar": "https://avatars.githubusercontent.com/u/75201518?v=4", "description": "Magpie 豆包翻译插件：通过 Cookie 直连豆包 AI、火山及微软翻译，无需 Docker 或额外 API Key。｜Magpie plugin for Doubao web translation: access Doubao AI, Volcengine and Microsoft Translator via Cookie, without Docker or an extra API key.", "stars": 9, "license": "MIT", "pushed": "2026-10-09T19:22:04Z", "package": "magpie-doubao-translate", "version": "0.1.2" },
  { "spec": "github:euanguo/magpie-middleware-image-slim", "repo": "euanguo/magpie-middleware-image-slim", "url": "https://github.com/euanguo/magpie-middleware-image-slim", "owner": "euanguo", "ownerAvatar": "https://avatars.githubusercontent.com/u/82928935?v=4", "description": "magpie gateway middleware: keep the newest images and replace older inline images with a text placeholder — agents resend every screenshot on every turn otherwise", "stars": 2, "license": "MIT", "pushed": "2026-10-10T16:41:27Z", "package": "magpie-middleware-image-slim", "version": "0.2.0", "kind": "middleware" },
  { "spec": "github:ezdemo/magpie-haiku55-thinking", "repo": "ezdemo/magpie-haiku55-thinking", "url": "https://github.com/ezdemo/magpie-haiku55-thinking", "owner": "ezdemo", "ownerAvatar": "https://avatars.githubusercontent.com/u/41895036?v=4", "description": "Magpie Haiku 5.5 thinking middleware with a verified OpenDesign compatibility workaround", "stars": 1, "license": "MIT", "pushed": "2026-10-12T15:07:50Z", "package": "magpie-opendesign-compatible-fix", "version": "0.1.0", "kind": "middleware" },
  { "spec": "github:MengXiangxi/magpie-openviking", "repo": "MengXiangxi/magpie-openviking", "url": "https://github.com/MengXiangxi/magpie-openviking", "owner": "MengXiangxi", "ownerAvatar": "https://avatars.githubusercontent.com/u/10251566?v=4", "description": "OpenViking memory recall and opt-in capture through Magpie async gateway hooks", "stars": 1, "license": "MIT", "pushed": "2026-10-27T02:43:30Z", "package": "magpie-openviking", "version": "0.1.0", "kind": "hooks" },
  { "spec": "github:sdloghub/magpie-session-patcher", "repo": "sdloghub/magpie-session-patcher", "url": "https://github.com/sdloghub/magpie-session-patcher", "owner": "sdloghub", "ownerAvatar": "https://avatars.githubusercontent.com/u/52274876?v=4", "description": "Magpie gateway middleware: opt-in outbound history filtering, optional reasoning cleanup and prompt append", "stars": 0, "pushed": "2026-10-25T07:15:36Z", "package": "magpie-session-patcher", "version": "0.1.1", "kind": "middleware" },
  { "spec": "github:IvanXxxxyyuffff/magpie-system-prompt-file", "repo": "IvanXxxxyyuffff/magpie-system-prompt-file", "url": "https://github.com/IvanXxxxyyuffff/magpie-system-prompt-file", "owner": "IvanXxxxyyuffff", "ownerAvatar": "https://avatars.githubusercontent.com/u/128815537?v=4", "description": "Magpie plugin: a system prompt (persona) loaded from a local .md / .txt file and injected into every agent request. macOS + Windows.", "stars": 0, "license": "MIT", "pushed": "2026-10-26T15:01:39Z", "package": "magpie-system-prompt-file", "version": "0.3.0", "kind": "hooks" },
];
const name = (r) => r.repo.split("/")[1];

function server(lang, fetched) {
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/plugins") return json({ bun: true, bunVersion: "1.4.2", plugins: [], picker: false, movable: [] });
    if (url.pathname === "/api/plugins/listings") return json({ listings: [] });
    if (url.pathname === "/api/plugins/npm") return json({ npm: {} });
    if (url.pathname === "/api/plugins/github") return json({ repos: REPOS, topic: "magpie-plugin" });
    if (url.pathname === "/api/plugins/search") return json({ hits: [] });
    if (url.pathname === "/api/plugins/updates") return json({ waiting: [], updated: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    if (url.host !== "magpie.test" || !/^\/[\w./-]*$/.test(url.pathname)) { fetched.push(url.href); return route.fulfill({ status: 404, body: "" }); }
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a repository of gateway hooks alone is a request hook", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) for (const width of [980, 420]) {
      await t.test(`${lang} ${width}px`, async () => {
        const ctx = await browser.newContext({ viewport: { width, height: 860 } });
        const page = await ctx.newPage();
        page.setDefaultTimeout(6000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], fetched = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, fetched));
        await page.addInitScript(() => { try { localStorage.setItem("magpie.pluginTab", "discover"); } catch {} });
        try {
          await page.goto("http://magpie.test/?view=plugins");
          const sec = page.locator("#view-plugins");
          const cards = sec.locator(".pm-sec[data-kind] .pm-card");
          await cards.first().waitFor();
          const tr = (s) => page.evaluate((s) => t(s), s);
          const hook = await tr("Request hook");
          if (lang !== "en") assert.notEqual(hook, "Request hook", "Request hook in " + lang);
          const kinds = sec.locator(".pm-bar .pm-kinds .opt");
          assert.deepEqual(await kinds.evaluateAll((bs) => bs.map((b) => [b.firstChild.textContent, b.querySelector("em").textContent])),
            [[await tr("All"), "7"], [await tr("Subscriptions"), "2"], [await tr("Middleware"), "5"]]);

          // picked out under Middleware, with the middleware; each card's
          // one chip the gateway's, which says Request hook for hooks, as
          // an installed one's row does
          for (const [i, of] of [[2, (r) => !!r.kind], [1, (r) => !r.kind]]) {
            await kinds.nth(i).click();
            const rs = REPOS.filter(of);
            assert.deepEqual((await cards.locator(".pm-name b").allInnerTexts()).sort(), rs.map(name).sort());
            for (const r of rs) {
              const chip = cards.filter({ has: page.locator(".pm-name b", { hasText: new RegExp("^" + name(r) + "$") }) }).locator(".pm-chip.kind");
              assert.equal(await chip.count(), 1, r.repo);
              const want = r.kind === "hooks" ? hook : await tr(r.kind === "middleware" ? "Middleware" : "Provider");
              assert.equal((await chip.innerText()).trim(), want, r.repo);
              assert.equal(await chip.evaluate((e) => e.classList.contains("mw")), !!r.kind, r.repo + "'s chip is the gateway's");
            }
          }

          const over = await page.evaluate(() => [document.querySelector("#view-plugins").scrollWidth - document.querySelector("#view-plugins").clientWidth, document.documentElement.scrollWidth - innerWidth]);
          assert.deepEqual(over.map((x) => Math.max(0, x)), [0, 0], "scrolls sideways: " + over);
          if (process.env.ARTIFACT_DIR) await sec.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `plugin-github-hooks-${engine}-${lang}-${width}.png`) });
          assert.deepEqual(errors, []);
        } finally {
          await ctx.close();
        }
      });
    }
  });
}
