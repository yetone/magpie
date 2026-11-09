// Run with Node's test runner and Playwright on the module path; see README.md.
// EchoChan314 (#3685) had github:mu-zi-lee/magpie-our-free-model installed,
// and the card of Gyarados4157's repository, tagged magpie-plugin and naming
// the same package, read Installed too. plugins/ holds one package of a name,
// so a plugin installed from another source of an installed plugin's package
// takes its place, with its options, Off and sign-ins (plugin.Add).
//
// Install asks first, naming both plugins by their source: a repository's
// card in place of npm's package, npm's card in place of a repository's
// (switched off, which the new one is then installed as), and the field's
// Install given a repository the page has read, or npm:<name>. Cancel
// installs nothing; Replace installs it. A card whose package nothing
// installed has installs at once, and so does the field given the
// repository installed by another spec. Five languages, wide and 440px, and
// nothing scrolls on a click.
//
// A repository reads Installed only when it is the one installed, by
// whichever spec names it (GitHub's shorthand, its page, a git URL over
// https, ssh with a user, scp's colon or a port, ssh.github.com, git://,
// owner/repo, another case and a ref), and the other of its package reads
// Install, which asks, naming the spec installed. English and Chinese.
//
// A search shows npm's package of that name, which neither repository is,
// and leaves out npm's card of a package npm has from a repository on the
// page. Five languages, wide and 440px. The API is faked.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");

const PKG = "magpie-our-free-model";
const repo = (owner, name, pkg, stars) => ({ spec: "github:" + owner + "/" + name, repo: owner + "/" + name, url: "https://github.com/" + owner + "/" + name, owner, description: "Free models for magpie", stars, license: "MIT", pushed: "2026-10-20T08:00:00Z", package: pkg, version: "0.1.0" });
const NAMESAKE = repo("Gyarados4157", PKG, PKG, 3);
const OTHER = repo("keli-wen", "agy-staff", "agy-staff", 723);
const MINE = "github:mu-zi-lee/" + PKG;
// npm's package of the name, which neither repository is
const HIT = { package: PKG, version: "2.0.0", description: "Free models", publisher: "someone", weekly: 40 };

// installed: what /api/plugins says is; repos: GitHub's tagged list; hits:
// npm's answer to a search
function server(lang, setup, asked) {
  const installed = setup.installed.map((e) => ({ providers: [], version: "1.0.0", moved: [], ...e }));
  const state = () => ({ bun: true, bunVersion: "1.4.2", plugins: installed, picker: false, movable: [] });
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true }, plugins: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/gateway/trace") return json({ routes: [] });
    if (url.pathname === "/api/plugins") return json(state());
    if (url.pathname === "/api/plugins/listings") return json({ listings: [] });
    if (url.pathname === "/api/plugins/npm") return json({ npm: {} });
    if (url.pathname === "/api/plugins/github") return json({ repos: setup.repos, topic: "magpie-plugin" });
    if (url.pathname === "/api/plugins/search") return json({ hits: setup.hits || [] });
    if (url.pathname === "/api/plugins/updates") return json({ waiting: [], updated: [] });
    if (url.pathname === "/api/plugins/add") {
      const b = route.request().postDataJSON();
      asked.push(b);
      // as plugin.Add: npm:<name> is npm's, which is kept at @latest, and
      // it goes in place of the entry of the same package, Off and all
      let spec = b.spec.replace(/^npm:\s*/, "");
      const r = setup.repos.find((x) => x.spec === spec || x.url === spec);
      if (!r && spec.lastIndexOf("@") <= 0) spec += "@latest";
      const as = r ? r.package : spec.replace(/(.)@[^@/]*$/, "$1");
      const i = installed.findIndex((e) => (e.package || e.spec.replace(/(.)@[^@/]*$/, "$1")) === as);
      const e = { spec, providers: [], version: "0.1.0", moved: [], ...(r ? { package: as } : {}), ...(i >= 0 && installed[i].off ? { off: true } : {}) };
      if (i >= 0) installed[i] = e;
      else installed.push(e);
      return json(state());
    }
    if (url.pathname.startsWith("/api/")) return json({});
    if (!/^\/[\w./-]*$/.test(url.pathname) || url.host !== "magpie.test") return route.fulfill({ status: 404, body: "" });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

// what the page asked the API and hasn't drawn yet: an ask counts until
// its body is read and the task that read it has run, which is where the
// page draws its answer
function counting() {
  let n = 0;
  const f = window.fetch;
  const done = () => setTimeout(() => n--, 0);
  window.fetch = async (...a) => {
    if (!String(a[0]).startsWith("/api/")) return f(...a);
    n++;
    let r;
    try { r = await f(...a); } catch (e) { done(); throw e; }
    if (r.status === 204) { done(); return r; }
    for (const k of ["text", "json"]) {
      const read = r[k].bind(r);
      r[k] = () => read().finally(done);
    }
    return r;
  };
  window.asking = () => n;
  try { localStorage.setItem("magpie.pluginTab", "discover"); } catch {}
}

const says = (pkg, old, now) => ({
  en: `${now} and ${old} are both the package ${pkg}, and only one of them can be installed: ${old} is removed, and its options and sign-ins pass to ${now}.`,
  zh: `${now} 和 ${old} 都是 ${pkg} 这个包，两个只能装一个：${old} 会被移除，它的选项和登录都会移交给 ${now}。`,
  "zh-TW": `${now} 和 ${old} 都是 ${pkg} 這個包，兩個只能裝一個：${old} 會被移除，它的選項和登入都會移交給 ${now}。`,
  ja: `${now} と ${old} はどちらも ${pkg} というパッケージで、インストールできるのは一方だけです。${old} は削除され、そのオプションとサインインは ${now} に引き継がれます。`,
  de: `${now} und ${old} sind beide das Paket ${pkg}, und nur eines davon kann installiert sein: ${old} wird entfernt, und ${now} übernimmt dessen Optionen und Anmeldungen.`,
});
const off = (old, now) => ({
  en: ` ${old} is off, so ${now} is installed off too.`,
  zh: `${old} 现在是停用的，所以 ${now} 装上后也是停用的。`,
  "zh-TW": `${old} 現在是停用的，所以 ${now} 裝上後也是停用的。`,
  ja: `${old} はオフなので、${now} もオフのままインストールされます。`,
  de: ` ${old} ist ausgeschaltet, also wird auch ${now} ausgeschaltet installiert.`,
});
const L = {
  en: { section: "Unofficial, on GitHub", onNpm: "On npm", replace: "Replace", cancel: "Cancel", on: "Switch on", title: (old, now) => `Replace ${old} with ${now}?` },
  zh: { section: "非官方插件 · GitHub", onNpm: "npm 上的插件", replace: "替换", cancel: "取消", on: "启用", title: (old, now) => `用 ${now} 替换 ${old}？` },
  "zh-TW": { section: "非官方外掛 · GitHub", onNpm: "npm 上的外掛", replace: "替換", cancel: "取消", on: "啟用", title: (old, now) => `用 ${now} 替換 ${old}？` },
  ja: { section: "非公式 · GitHub", onNpm: "npm 上", replace: "置き換え", cancel: "キャンセル", on: "オンにする", title: (old, now) => `${old} を ${now} に置き換えますか？` },
  de: { section: "Inoffiziell, auf GitHub", onNpm: "Auf npm", replace: "Ersetzen", cancel: "Abbrechen", on: "Einschalten", title: (old, now) => `${old} durch ${now} ersetzen?` },
};

const scrolls = (page) => page.evaluate(() => [window.scrollY, ...[...document.querySelectorAll("*")].filter((e) => e.scrollTop > 0).map((e) => (e.id || e.className) + ":" + e.scrollTop)].join(","));
// every answer drawn, and the GitHub section done arriving: the page
// draws each card again as npm's and git's answers come, so a button
// found before then can be taken out under the press
const settled = (page) => page.waitForFunction(() => window.asking() === 0 && !document.querySelector(".pm-arrive"), null, { polling: "raf" });
// a reader's click where the thing is: Playwright's own scrolls it first
const press = async (page, loc) => {
  await settled(page);
  const b = await loc.boundingBox();
  await page.mouse.click(b.x + b.width / 2, b.y + b.height / 2);
};
// a reader scrolls to it with the wheel: magpie puts back a scroll nobody
// asked for, so Playwright's own wouldn't do (at 440px in ja and de the
// field's Install is below the fold). WebKit's wheel goes on scrolling
// after the event, so it is done when the view holds still for five
// frames: a click's "scrolls nothing" would read the wheel's last pixels
const reach = async (page, loc) => {
  await settled(page);
  const view = await page.locator("#view-plugins").boundingBox();
  await page.mouse.move(view.x + view.width / 2, Math.min(view.y + 200, 800));
  for (let i = 0; i < 60 && !(await loc.evaluate((e) => { const r = e.getBoundingClientRect(), v = e.closest(".view").getBoundingClientRect(); return r.top >= v.top && r.bottom <= v.bottom; })); i++) {
    await page.mouse.wheel(0, 80);
    await page.waitForTimeout(15);
  }
  await page.evaluate(() => new Promise((done) => {
    const v = document.querySelector("#view-plugins");
    let at = v.scrollTop, still = 0;
    const look = () => {
      if (v.scrollTop === at) still++;
      else { at = v.scrollTop; still = 0; }
      if (still >= 5) done();
      else requestAnimationFrame(look);
    };
    requestAnimationFrame(look);
  }));
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Install asks first when it would replace a plugin of the same package", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(L)) {
      const ctx = await browser.newContext({ viewport: { width: 980, height: 860 } });
      await ctx.addInitScript(counting);
      try {
        for (const width of [980, 440]) {
          await t.test(lang + " " + width, async () => {
            const w = L[lang];
            const open = async (setup, asked) => {
              const page = await ctx.newPage();
              await page.setViewportSize({ width, height: 860 });
              page.setDefaultTimeout(5000);
              page.setDefaultNavigationTimeout(60000);
              const errors = [];
              page.on("pageerror", (e) => errors.push(e.message));
              await page.route("**/*", server(lang, setup, asked));
              await page.goto("http://magpie.test/?view=plugins", { timeout: 30000 });
              await page.locator("#view-plugins").waitFor({ state: "visible" });
              return { page, errors };
            };
            // the Install b opens a dialog with: it fits, and names the
            // plugin that goes and the one that comes, two of them
            const ask = async (page, b, old, now) => {
              await reach(page, b);
              const before = await scrolls(page);
              await press(page, b);
              const dlg = page.locator("dialog.action-confirm[open]");
              await dlg.waitFor();
              const box = await dlg.boundingBox();
              assert.ok(box.x >= 0 && box.x + box.width <= width, "the dialog fits: " + JSON.stringify(box));
              assert.notEqual(old, now);
              assert.equal(await dlg.locator("h2").innerText(), w.title(old, now));
              return { dlg, before };
            };
            const ghCard = (page, owner) => page.locator("#view-plugins .pm-sec[data-kind] .pm-card", { has: page.locator(".pm-by", { hasText: owner }) });

            // npm's package installed: the repository's card asks
            {
              const asked = [];
              const { page, errors } = await open({ installed: [{ spec: PKG + "@latest" }], repos: [NAMESAKE, OTHER] }, asked);
              try {
                const card = ghCard(page, "Gyarados4157");
                const old = "npm:" + PKG;
                let { dlg, before } = await ask(page, card.locator(".pm-act.get"), old, NAMESAKE.spec);
                assert.equal(await dlg.locator("p").innerText(), says(PKG, old, NAMESAKE.spec)[lang]);
                await press(page, dlg.locator("button", { hasText: w.cancel }));
                await dlg.waitFor({ state: "detached" });
                assert.equal(await scrolls(page), before, "a click scrolls nothing");
                assert.deepEqual(asked, [], "Cancel installs nothing");
                ({ dlg, before } = await ask(page, card.locator(".pm-act.get"), old, NAMESAKE.spec));
                await press(page, dlg.locator("button.primary", { hasText: w.replace }));
                await card.locator(".pm-act.done").waitFor();
                assert.deepEqual(asked, [{ spec: NAMESAKE.spec }]);
                assert.equal(await scrolls(page), before, "a click scrolls nothing");
                // a package nothing installed has: installed at once
                const other = ghCard(page, "keli-wen");
                await reach(page, other.locator(".pm-act.get"));
                await press(page, other.locator(".pm-act.get"));
                await other.locator(".pm-act.done").waitFor();
                assert.deepEqual(asked, [{ spec: NAMESAKE.spec }, { spec: OTHER.spec }]);
                assert.equal(await page.locator("dialog.action-confirm").count(), 0);
                assert.deepEqual(errors, []);
              } finally { await page.close(); }
            }

            // a repository installed and switched off: npm's card of its
            // package asks, and says the new one is installed off too
            {
              const asked = [];
              const { page, errors } = await open({ installed: [{ spec: MINE, package: PKG, off: true }], repos: [], hits: [HIT] }, asked);
              try {
                await page.locator("#view-plugins .pm-head .pm-find input").fill("free-model");
                const card = page.locator("#view-plugins .pm-sec", { has: page.locator("h3", { hasText: w.onNpm }) }).locator(".pm-card", { hasText: PKG });
                await card.locator(".pm-act.get").waitFor();
                const now = "npm:" + PKG;
                const { dlg, before } = await ask(page, card.locator(".pm-act.get"), MINE, now);
                assert.equal(await dlg.locator("p").innerText(), says(PKG, MINE, now)[lang] + off(MINE, now)[lang]);
                await press(page, dlg.locator("button.primary", { hasText: w.replace }));
                // installed, and off: the card offers to switch it on
                await card.locator(".pm-act", { hasText: w.on }).waitFor();
                assert.deepEqual(asked, [{ spec: PKG }]);
                assert.equal(await scrolls(page), before, "a click scrolls nothing");
                assert.deepEqual(errors, []);
              } finally { await page.close(); }
            }

            // the field's Install: a repository the page has read, and
            // npm:<name>, each in place of the repository installed. That
            // repository by another spec is the same plugin, and installs
            // at once
            {
              const asked = [];
              const OWN = repo("mu-zi-lee", PKG, PKG, 12);
              const { page, errors } = await open({ installed: [{ spec: MINE, package: PKG }], repos: [NAMESAKE, OWN] }, asked);
              try {
                const field = page.locator("#view-plugins .pm-manual");
                const type = async (spec) => {
                  await settled(page);
                  await field.locator("input").fill(spec);
                  return field.locator("button.primary");
                };
                let { dlg, before } = await ask(page, await type(NAMESAKE.spec), MINE, NAMESAKE.spec);
                assert.equal(await dlg.locator("p").innerText(), says(PKG, MINE, NAMESAKE.spec)[lang]);
                await press(page, dlg.locator("button", { hasText: w.cancel }));
                await dlg.waitFor({ state: "detached" });
                assert.equal(await scrolls(page), before, "a click scrolls nothing");
                assert.deepEqual(asked, [], "Cancel installs nothing");
                // the add is asked before the click's tasks end
                await press(page, await type(OWN.url));
                await settled(page);
                assert.deepEqual(asked, [{ spec: OWN.url }], "the same repository installs at once");
                assert.equal(await page.locator("dialog.action-confirm").count(), 0);
                ({ dlg, before } = await ask(page, await type("npm:" + PKG), OWN.url, "npm:" + PKG));
                assert.equal(await dlg.locator("p").innerText(), says(PKG, OWN.url, "npm:" + PKG)[lang]);
                await press(page, dlg.locator("button.primary", { hasText: w.replace }));
                await settled(page);
                assert.deepEqual(asked, [{ spec: OWN.url }, { spec: "npm:" + PKG }]);
                assert.equal(await scrolls(page), before, "a click scrolls nothing");
                assert.deepEqual(errors, []);
              } finally { await page.close(); }
            }
          });
        }
      } finally { await ctx.close(); }
    }
  });
  test(engine + ": a repository reads Installed only when it is the one installed", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    const OWN = repo("mu-zi-lee", PKG, PKG, 12);
    const FORMS = [OWN.spec, OWN.url, `git+https://github.com/${OWN.repo}.git`, `git+ssh://git@github.com/${OWN.repo}.git`, `git+ssh://git@github.com:${OWN.repo}.git`, `git+ssh://git@github.com:22/${OWN.repo}.git`, `git+ssh://git@ssh.github.com:443/${OWN.repo}.git`, `git://github.com/${OWN.repo}.git#main`, OWN.repo, `github:Mu-Zi-Lee/${PKG}#v0.1.0`];
    for (const lang of ["en", "zh"]) {
      const w = L[lang];
      const ctx = await browser.newContext({ viewport: { width: 980, height: 860 } });
      await ctx.addInitScript(counting);
      try {
        for (const spec of FORMS) {
          await t.test(lang + " " + spec, async () => {
            const asked = [];
            const page = await ctx.newPage();
            try {
              page.setDefaultTimeout(5000);
              page.setDefaultNavigationTimeout(60000);
              const errors = [];
              page.on("pageerror", (e) => errors.push(e.message));
              await page.route("**/*", server(lang, { installed: [{ spec, package: PKG }], repos: [OWN, NAMESAKE] }, asked));
              await page.goto("http://magpie.test/?view=plugins", { timeout: 30000 });
              const sec = page.locator("#view-plugins .pm-sec[data-kind]");
              const own = sec.locator(".pm-card", { has: page.locator(".pm-by", { hasText: "mu-zi-lee" }) });
              const theirs = sec.locator(".pm-card", { has: page.locator(".pm-by", { hasText: "Gyarados4157" }) });
              await own.locator(".pm-act.done").waitFor();
              await theirs.locator(".pm-act.get").waitFor();
              await settled(page);
              assert.equal(await sec.locator(".pm-card.have").count(), 1, "one of the two is installed");
              // the other's Install names the one installed, by its spec
              await reach(page, theirs.locator(".pm-act.get"));
              await press(page, theirs.locator(".pm-act.get"));
              const dlg = page.locator("dialog.action-confirm[open]");
              await dlg.waitFor();
              assert.equal(await dlg.locator("h2").innerText(), w.title(spec, NAMESAKE.spec));
              await press(page, dlg.locator("button", { hasText: w.cancel }));
              await dlg.waitFor({ state: "detached" });
              assert.deepEqual(asked, []);
              assert.equal(await sec.locator(".pm-card.have").count(), 1);
              assert.deepEqual(errors, []);
            } finally { await page.close(); }
          });
        }
      } finally { await ctx.close(); }
    }
  });
  test(engine + ": a search shows npm's package of a name a repository's package.json gives", async (t) => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: process.env.PLAYWRIGHT_CHANNEL || "chromium" }));
    t.after(() => browser.close());
    const OWN = repo("mu-zi-lee", PKG, PKG, 12);
    // npm has this one's package from it: its card is npm's
    const NPMS = { ...repo("griffinmartin", "opencode-claude-auth", "opencode-claude-auth", 1301), spec: "opencode-claude-auth" };
    for (const lang of Object.keys(L)) {
      const w = L[lang];
      const ctx = await browser.newContext({ viewport: { width: 980, height: 860 } });
      await ctx.addInitScript(counting);
      try {
        for (const width of [980, 440]) {
          await t.test(lang + " " + width, async () => {
            const page = await ctx.newPage();
            try {
              await page.setViewportSize({ width, height: 860 });
              page.setDefaultTimeout(5000);
              page.setDefaultNavigationTimeout(60000);
              const errors = [];
              page.on("pageerror", (e) => errors.push(e.message));
              await page.route("**/*", server(lang, {
                installed: [{ spec: OWN.spec, package: PKG }], repos: [OWN, NAMESAKE, NPMS],
                hits: [HIT, { package: NPMS.spec, version: "2.2.1", description: "Claude for OpenCode", publisher: "griffinmartin", weekly: 9000 }],
              }, []));
              await page.goto("http://magpie.test/?view=plugins", { timeout: 30000 });
              const view = page.locator("#view-plugins");
              await view.waitFor({ state: "visible" });
              await settled(page);
              await view.locator(".pm-head .pm-find input").fill("free-model");
              const npm = view.locator(".pm-sec", { has: page.locator("h3", { hasText: w.onNpm }) });
              await npm.locator(".pm-card .pm-act.get").waitFor();
              await settled(page);
              assert.deepEqual(await npm.locator(".pm-card .pm-name b").allInnerTexts(), [PKG], "npm's package of the name, and not npm's card of a repository on the page");
              assert.equal(await view.locator(".pm-sec").first().locator(".pm-card.have").count(), 1);
              assert.deepEqual(errors, []);
            } finally { await page.close(); }
          });
        }
      } finally { await ctx.close(); }
    }
  });
}
