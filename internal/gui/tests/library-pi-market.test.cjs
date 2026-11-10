// Run with Node's test runner and Playwright on the module path; see README.md.
// nineruiing on X: a market of Pi's packages. Under the Pi packages tab,
// Discover lists npm's most downloaded pi-package packages; a package every
// running Pi has shows Installed; Install goes to the one Pi that lacks it,
// or asks which when several do (never a stopped distro); a search npm
// couldn't answer says so over what was found; a package's trash button
// asks first and sends pi remove. In every language, both engines, at a
// phone's width too. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "C:\\Users\\nine";
const lib = () => ({
  dir: `${HOME}\\.magpie\\library`, backups: `${HOME}\\.magpie\\backups`, home: HOME,
  agents: [
    { id: "pi", name: "Pi", icon: "", skills: `${HOME}\\.pi\\agent\\skills` },
    { id: "pi@wsl:Ubuntu", name: "Pi · WSL Ubuntu", icon: "", skills: "\\\\wsl.localhost\\Ubuntu\\home\\nine\\.pi\\agent\\skills", wsl: "Ubuntu" },
  ],
  instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [], servers: [],
});
const pkg = (name, extra) => ({ source: "npm:" + name, name, kind: "npm", on: true, installed: true, version: "1.0.0", ...extra });
// state: which Pi has which package
function piView(has) {
  return {
    pis: [
      { id: "pi", name: "Pi", icon: "", dir: `${HOME}\\.pi\\agent`, extensions: [], packages: has.pi.map((n) => pkg(n)) },
      { id: "pi@wsl:Ubuntu", name: "Pi · WSL Ubuntu", icon: "", wsl: "Ubuntu", dir: "\\\\wsl.localhost\\Ubuntu\\home\\nine\\.pi\\agent", extensions: [], packages: has.ubuntu.map((n) => pkg(n, { installed: false })) },
      { id: "pi@wsl:Arch", name: "Pi · WSL Arch", icon: "", wsl: "Arch", asleep: true, extensions: [], packages: [] },
    ],
  };
}
const card = (name, downloads, extra) => ({
  source: "npm:" + name, name, version: "0.3.1", description: name + " for Pi", publisher: "nine", downloads,
  npm: "https://www.npmjs.com/package/" + name, ...extra,
});
const popular = [
  card("pi-mcp-adapter", 48210, { repo: "https://github.com/nicobailon/pi-mcp-adapter" }),
  card("pi-web-access", 20113),
  card("pi-subagents", 9050),
];

function server(lang, calls) {
  const has = { pi: ["pi-mcp-adapter", "pi-subagents"], ubuntu: ["pi-mcp-adapter"] };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib() });
    if (url.pathname === "/api/library/pi") return route.fulfill({ json: piView(has) });
    if (url.pathname === "/api/library/market/pi") {
      const q = url.searchParams.get("q") || "";
      calls.push(["market", q]);
      if (!q) return route.fulfill({ json: { items: popular } });
      // npm's search is down: only the popular matches
      return route.fulfill({ json: { items: popular.filter((x) => x.name.includes(q)), error: "npm: timeout", failed: "npm", cause: "timeout" } });
    }
    if (url.pathname === "/api/library/pi/install") {
      const b = req.postDataJSON();
      calls.push(["install", b]);
      has[b.pi === "pi" ? "pi" : "ubuntu"].push(b.source.slice(4));
      return route.fulfill({ json: { ...piView(has), pending: b.pi !== "pi" } });
    }
    if (url.pathname === "/api/library/pi/remove") {
      const b = req.postDataJSON();
      calls.push(["remove", b]);
      const k = b.pi === "pi" ? "pi" : "ubuntu";
      has[k] = has[k].filter((n) => "npm:" + n !== b.source);
      return route.fulfill({ json: piView(has) });
    }
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: {
    tab: "Pi packages", note: "Most downloaded on npm", install: "Install", installed: "Installed", into: "Install into", remove: "Remove",
    pending: "pi-subagents is in Pi · WSL Ubuntu's packages now: Pi installs it when it next starts",
    done: "pi-web-access is installed in Pi: Pi loads it when it next starts, or at /reload",
    partial: "npm's search didn't answer (timeout), so these are only the matches among the most downloaded packages.",
    ask: "Remove pi-subagents from Pi?", removed: "pi-subagents removed from Pi",
  },
  zh: {
    tab: "Pi 包", note: "npm 下载最多", install: "安装", installed: "已安装", into: "安装到", remove: "移除",
    pending: "pi-subagents 已加入 Pi · WSL Ubuntu 的包：Pi 下次启动时安装它",
    done: "pi-web-access 已安装到 Pi：Pi 下次启动或 /reload 时加载",
    partial: "npm 搜索没有响应（timeout），下面只是下载最多的包里匹配的。",
    ask: "从 Pi 移除 pi-subagents？", removed: "已从 Pi 移除 pi-subagents",
  },
  "zh-TW": {
    tab: "Pi 套件", note: "npm 下載最多", install: "安裝", installed: "已安裝", into: "安裝到", remove: "移除",
    pending: "pi-subagents 已加入 Pi · WSL Ubuntu 的套件：Pi 下次啟動時安裝它",
    done: "pi-web-access 已安裝到 Pi：Pi 下次啟動或 /reload 時載入",
    partial: "npm 搜尋沒有回應（timeout），下面只是下載最多的套件裡符合的。",
    ask: "從 Pi 移除 pi-subagents？", removed: "已從 Pi 移除 pi-subagents",
  },
  ja: {
    tab: "Pi パッケージ", note: "npm でダウンロード数上位", install: "インストール", installed: "インストール済み", into: "インストール先", remove: "削除",
    pending: "pi-subagents を Pi · WSL Ubuntu のパッケージに追加しました。Pi が次回起動時にインストールします",
    done: "pi-web-access を Pi にインストールしました。Pi の次回起動時か /reload で読み込まれます",
    partial: "npm の検索が応答しませんでした（timeout）。ダウンロード数上位のパッケージから一致したものだけを表示しています。",
    ask: "Pi から pi-subagents を削除しますか？", removed: "Pi から pi-subagents を削除しました",
  },
  de: {
    tab: "Pi-Pakete", note: "Auf npm am meisten geladen", install: "Installieren", installed: "Installiert", into: "Installieren in", remove: "Entfernen",
    pending: "pi-subagents ist jetzt in den Paketen von Pi · WSL Ubuntu: Pi installiert es beim nächsten Start",
    done: "pi-web-access ist in Pi installiert: Pi lädt es beim nächsten Start oder bei /reload",
    partial: "Die npm-Suche hat nicht geantwortet (timeout), daher sind das nur die Treffer unter den meistgeladenen Paketen.",
    ask: "pi-subagents aus Pi entfernen?", removed: "pi-subagents aus Pi entfernt",
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Pi's market lists npm's packages, installs into a Pi and removes from one", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const errors = [];
    t.after(async () => {
      if (errors.length) console.log(errors);
      await browser.close();
    });
    for (const lang of Object.keys(words)) for (const width of [980, 380]) {
      await t.test(`${lang} at ${width}px`, async () => {
        const w = words[lang], calls = [];
        const ctx = await browser.newContext({ viewport: { width, height: 900 } });
        const page = await ctx.newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, calls));
        await page.goto("http://magpie.test/");
        await page.locator('button[data-view="library"]').click();
        const v = page.locator("#view-library");
        await v.locator(".lib-tabs").getByText(w.tab, { exact: true }).click();
        const mk = v.locator('.mk[data-market="pi"]');
        const cardOf = (name) => mk.locator(`.mk-card[data-id="npm:${name}"]`);
        await cardOf("pi-mcp-adapter").waitFor();
        assert.equal(await mk.locator(".mk-note").textContent(), w.note);
        assert.deepEqual(await mk.locator(".mk-title").allTextContents(), ["pi-mcp-adapter", "pi-web-access", "pi-subagents"]);
        // both running Pis have it: Installed, no button
        const both = cardOf("pi-mcp-adapter");
        assert.equal(await both.locator(".mk-have").textContent(), w.installed);
        assert.equal(await both.locator(".mk-add").count(), 0);
        // only Ubuntu lacks it: Install goes there, no menu
        const sub = cardOf("pi-subagents");
        assert.equal(await sub.locator(".mk-add").textContent(), w.install);
        await sub.locator(".mk-add").click();
        await page.waitForFunction((want) => document.querySelector("#status").textContent === want, w.pending).catch(() => {});
        assert.equal(await page.locator("#status").textContent(), w.pending);
        assert.equal(await page.locator(".proto-menu").count(), 0);
        assert.equal(await cardOf("pi-subagents").locator(".mk-have").textContent(), w.installed);
        const ubuntu = v.locator(".lib-list.lib-pi").nth(1);
        await ubuntu.locator('.lib-pi-row[data-id="npm:pi-subagents"]').waitFor();

        // nobody has it: Install asks which Pi, never the stopped Arch
        await cardOf("pi-web-access").locator(".mk-add").click();
        const menu = page.locator(".proto-menu");
        await menu.locator(".pm-item").first().waitFor();
        assert((await menu.textContent()).includes(w.into), await menu.textContent());
        assert.deepEqual(await menu.locator(".pm-item .pm-name").allTextContents(), ["Pi", "Pi · WSL Ubuntu"]);
        await menu.locator(".pm-item").first().click();
        await page.waitForFunction((want) => document.querySelector("#status").textContent === want, w.done).catch(() => {});
        assert.equal(await page.locator("#status").textContent(), w.done);
        const here = v.locator(".lib-list.lib-pi").nth(0);
        await here.locator('.lib-pi-row[data-id="npm:pi-web-access"]').waitFor();

        // a package's trash button asks first, then pi remove
        await here.locator('.lib-pi-row[data-id="npm:pi-subagents"] .lib-pi-rm').click();
        const ed = page.locator(".lib-rmone");
        await ed.waitFor();
        assert.equal(await ed.locator(".ehead b").textContent(), w.ask);
        await ed.getByRole("button", { name: w.remove, exact: true }).click();
        await page.waitForFunction((want) => document.querySelector("#status").textContent === want, w.removed).catch(() => {});
        assert.equal(await page.locator("#status").textContent(), w.removed);
        await here.locator('.lib-pi-row[data-id="npm:pi-subagents"]').waitFor({ state: "detached" });
        await ed.waitFor({ state: "detached" }); // the dialog closes
        // Pi lacks it again
        assert.equal(await cardOf("pi-subagents").locator(".mk-add").textContent(), w.install);

        // npm's search down: what was found, and why that's all
        await mk.locator('input[data-lib="market-pi"]').fill("web");
        await mk.locator(".mk-partial").waitFor();
        assert.equal(await mk.locator(".mk-partial").textContent(), w.partial);
        assert.deepEqual(await mk.locator(".mk-title").allTextContents(), ["pi-web-access"]);

        assert.deepEqual(calls.filter((c) => c[0] !== "market"), [
          ["install", { pi: "pi@wsl:Ubuntu", source: "npm:pi-subagents" }],
          ["install", { pi: "pi", source: "npm:pi-web-access" }],
          ["remove", { pi: "pi", source: "npm:pi-subagents" }],
        ]);
        assert.deepEqual(calls.filter((c) => c[0] === "market").map((c) => c[1]), ["", "web"]);
        // nothing runs off the side at a phone's width
        const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        assert(over <= 0, `the page scrolls sideways by ${over}px`);
        await ctx.close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
