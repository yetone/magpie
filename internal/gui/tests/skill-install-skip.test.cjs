// Run with Node's test runner and Playwright on the module path; see README.md.
// lc on Discord (安装skills的时候，没有跳过已经安装的): installing a set of
// skills from a repository stopped at the first one the library's folder
// already had, and none of the set was installed. Now the set installs, and
// the toast names the ones the library had already and the ones skipped,
// with why, beside how many were installed; the click scrolls nothing; in
// English and Chinese. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "/Users/summit";
const agent = (id, name) => ({ id, name, icon: "", skills: `${HOME}/.${id}/skills` });
const cand = (name) => ({ path: "skills/" + name, name, description: name + " like caveman", have: false });
const probe = { source: "JuliusBrussee/caveman", kind: "github", candidates: [cand("caveman"), cand("caveman-commit"), cand("caveman-review")] };
const lib = () => ({
  dir: `${HOME}/.config/magpie/library`, backups: `${HOME}/.config/magpie/backups`, home: HOME,
  agents: [agent("claude", "Claude Code")],
  instructions: { agents: [] }, servers: [], skills: [], foundServers: [], projects: [], foundSkills: [],
});
const why = `the library's folder already has a caveman-commit that isn't this one (${HOME}/.config/magpie/library/skills/caveman-commit), left as it is: bring it in from the skills found there to use it`;

function server(lang, posts) {
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib() });
    if (url.pathname === "/api/library/skills/probe") return route.fulfill({ json: probe });
    if (url.pathname === "/api/library/skills/install") {
      posts.push(req.postDataJSON().paths);
      return route.fulfill({ json: { ...lib(), result: { changed: ["claude"], problems: [], installed: ["caveman-review"], had: ["caveman"], skipped: [{ agent: "", what: "skill:caveman-commit", error: why }] } } });
    }
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const scrolled = (page) => page.evaluate(() => [window.scrollX, window.scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop || e.scrollLeft).map((e) => `${e.className}:${e.scrollTop},${e.scrollLeft}`)].join(" "));

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": a set of skills with one in the way installs the rest and names it", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    const install = async (lang, width) => {
      const posts = [];
      const ctx = await browser.newContext({ viewport: { width, height: 800 } });
      await ctx.addInitScript(() => { try { localStorage.setItem("magpie.libTab", "skills"); } catch {} });
      const page = await ctx.newPage();
      page.setDefaultTimeout(5000);
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, posts));
      await page.goto("http://magpie.test/");
      await page.locator('button[data-view="library"]').click();
      const src = page.locator('#view-library input[data-lib="source"]');
      await src.fill("JuliusBrussee/caveman");
      await src.press("Enter");
      const go = page.locator("#view-library .lib-probefoot button.primary");
      await go.waitFor();
      const before = await scrolled(page);
      await go.click();
      await page.waitForTimeout(300);
      assert.deepEqual(posts, [["skills/caveman", "skills/caveman-commit", "skills/caveman-review"]]);
      assert.equal(await scrolled(page), before, "the click scrolls nothing");
      const status = page.locator("#status");
      assert.match(await status.getAttribute("class"), /\bwarn\b/);
      return (await status.textContent()).trim();
    };

    await t.test("in English", async () => {
      assert.equal(await install("en", 980), "1 skill installed · already in the library: caveman · caveman-commit skipped: " + why);
    });
    await t.test("in Chinese, narrow", async () => {
      assert.equal(await install("zh", 440), "已安装 1 个技能 · 资源库里已有：caveman · 已跳过 caveman-commit：" + why);
    });

    assert.deepEqual(errors, []);
  });
}
