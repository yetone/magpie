// Run with Node's test runner and Playwright on the module path; see README.md.
// eachg0 on X: the other install searches take a repository or a package
// named outright, and Pi's only searched, so a package found elsewhere
// couldn't be installed from it. A search that names one (pi-rename, its
// GitHub page) now shows it first, to install as named, even when npm's
// search finds nothing; Install goes to the Pi picked, by Pi's own source;
// a Pi that has it shows Installed. The placeholder says it takes a
// package or a git address. In every language, both engines, at a phone's
// width too. No backend: the API is faked here.
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
// a Pi's package as magpie reads it: by its source, with its id (piSourceID)
const pkg = (source, extra) => {
  const git = !source.startsWith("npm:");
  const name = git ? source.split("/").pop() : source.slice(4);
  const id = git ? "git:" + source.replace(/^https:\/\//, "") : source;
  return { source, name, kind: git ? "git" : "npm", id, on: true, installed: true, version: "1.0.0", ...extra };
};
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
  card("pi-subagents", 9050),
];
// what magpie's backend answers for a search that names a package outright
// (PiDirectAt): pi-rename as npmmirror's latest has it, and its GitHub page
const direct = {
  "pi-rename": card("pi-rename", 0, { version: "1.2.0", publisher: "shanelevs", description: "Pi coding agent extension — auto-name a new session after the first turn", repo: "https://github.com/ShaneLevs/pi-rename", kind: "npm", id: "npm:pi-rename" }),
  "https://github.com/ShaneLevs/pi-rename#readme": {
    source: "https://github.com/ShaneLevs/pi-rename", name: "pi-rename", kind: "git", id: "git:github.com/ShaneLevs/pi-rename",
    publisher: "github.com/ShaneLevs/pi-rename", repo: "https://github.com/ShaneLevs/pi-rename", npm: "",
  },
};

function server(lang, calls) {
  const has = { pi: ["npm:pi-mcp-adapter"], ubuntu: [] };
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
      // npmmirror's search finds nothing for these
      return route.fulfill({ json: { items: popular.filter((x) => x.name.includes(q)), ...(direct[q] ? { direct: direct[q] } : {}) } });
    }
    if (url.pathname === "/api/library/pi/install") {
      const b = req.postDataJSON();
      calls.push(["install", b]);
      has[b.pi === "pi" ? "pi" : "ubuntu"].push(b.source);
      return route.fulfill({ json: { ...piView(has), pending: b.pi !== "pi" } });
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
    inUbuntu: "In Pi · WSL Ubuntu", tab: "Pi packages", install: "Install", installed: "Installed", into: "Install into",
    find: "Search Pi packages on npm, or enter a package or git URL…",
    done: "pi-rename is installed in Pi: Pi loads it when it next starts, or at /reload",
    pending: "pi-rename is in Pi · WSL Ubuntu's packages now: Pi installs it when it next starts",
  },
  zh: {
    inUbuntu: "已在 Pi · WSL Ubuntu", tab: "Pi 包", install: "安装", installed: "已安装", into: "安装到",
    find: "在 npm 上搜索 Pi 包，或输入包名、git 地址…",
    done: "pi-rename 已安装到 Pi：Pi 下次启动或 /reload 时加载",
    pending: "pi-rename 已加入 Pi · WSL Ubuntu 的包：Pi 下次启动时安装它",
  },
  "zh-TW": {
    inUbuntu: "已在 Pi · WSL Ubuntu", tab: "Pi 套件", install: "安裝", installed: "已安裝", into: "安裝到",
    find: "在 npm 上搜尋 Pi 套件，或輸入套件名稱、git 網址…",
    done: "pi-rename 已安裝到 Pi：Pi 下次啟動或 /reload 時載入",
    pending: "pi-rename 已加入 Pi · WSL Ubuntu 的套件：Pi 下次啟動時安裝它",
  },
  ja: {
    inUbuntu: "Pi · WSL Ubuntu にあります", tab: "Pi パッケージ", install: "インストール", installed: "インストール済み", into: "インストール先",
    find: "npm で Pi パッケージを検索、またはパッケージ名や git URL を入力…",
    done: "pi-rename を Pi にインストールしました。Pi の次回起動時か /reload で読み込まれます",
    pending: "pi-rename を Pi · WSL Ubuntu のパッケージに追加しました。Pi が次回起動時にインストールします",
  },
  de: {
    inUbuntu: "In Pi · WSL Ubuntu", tab: "Pi-Pakete", install: "Installieren", installed: "Installiert", into: "Installieren in",
    find: "Pi-Pakete auf npm suchen oder Paketnamen bzw. Git-URL eingeben…",
    done: "pi-rename ist in Pi installiert: Pi lädt es beim nächsten Start oder bei /reload",
    pending: "pi-rename ist jetzt in den Paketen von Pi · WSL Ubuntu: Pi installiert es beim nächsten Start",
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": Pi's search installs a package named outright", async (t) => {
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
        await mk.locator('.mk-card[data-id="npm:pi-mcp-adapter"]').waitFor();
        const find = mk.locator('input[data-lib="market-pi"]');
        assert.equal(await find.getAttribute("placeholder"), w.find);

        // pi-rename, typed: npm's search finds nothing, and it's offered first
        await find.fill("pi-rename");
        const named = mk.locator('.mk-card.mk-direct[data-id="npm:pi-rename"]');
        await named.waitFor();
        assert.equal(await mk.locator(".mk-msg").count(), 0, "a package named outright is said to match nothing");
        assert.deepEqual(await mk.locator(".mk-title").allTextContents(), ["pi-rename"]);
        assert.equal(await named.locator(".mk-pub").textContent(), "shanelevs");
        assert.equal(await named.locator(".mk-badge").textContent(), "v1.2.0");
        assert.equal(await named.locator(".mk-add").textContent(), w.install);
        // neither Pi has it: Install asks which, never the stopped Arch
        await named.locator(".mk-add").click();
        const menu = page.locator(".proto-menu");
        await menu.locator(".pm-item").first().waitFor();
        assert((await menu.textContent()).includes(w.into), await menu.textContent());
        assert.deepEqual(await menu.locator(".pm-item .pm-name").allTextContents(), ["Pi", "Pi · WSL Ubuntu"]);
        await menu.locator(".pm-item").first().click();
        await page.waitForFunction((want) => document.querySelector("#status").textContent === want, w.done).catch(() => {});
        assert.equal(await page.locator("#status").textContent(), w.done);
        await v.locator(".lib-list.lib-pi").nth(0).locator('.lib-pi-row[data-id="npm:pi-rename"]').waitFor();
        // Pi has it now: Install goes to Ubuntu alone, no menu
        assert.equal(await named.locator(".mk-add").textContent(), w.install);

        // its GitHub page, pasted: the repository, installed by its address
        await find.fill("https://github.com/ShaneLevs/pi-rename#readme");
        const repo = mk.locator('.mk-card.mk-direct[data-id="https://github.com/ShaneLevs/pi-rename"]');
        await repo.waitFor();
        assert.equal(await repo.locator(".mk-pub").textContent(), "github.com/ShaneLevs/pi-rename");
        assert.equal(await repo.locator(".mk-badge").count(), 0);
        await repo.locator(".mk-add").click();
        await menu.locator(".pm-item").first().waitFor();
        await menu.locator(".pm-item").nth(1).click();
        await page.waitForFunction((want) => document.querySelector("#status").textContent === want, w.pending).catch(() => {});
        assert.equal(await page.locator("#status").textContent(), w.pending);
        await v.locator(".lib-list.lib-pi").nth(1).locator('.lib-pi-row[data-id="https://github.com/ShaneLevs/pi-rename"]').waitFor();
        // Ubuntu has it by its address; Pi, which has it from npm, still lacks it
        assert.match(await repo.getAttribute("class"), /\bhave\b/);
        assert.equal(await repo.locator(".mk-add").textContent(), w.install);
        assert.equal(await repo.locator(".mk-add").getAttribute("title"), w.inUbuntu);

        assert.deepEqual(calls.filter((c) => c[0] !== "market"), [
          ["install", { pi: "pi", source: "npm:pi-rename" }],
          ["install", { pi: "pi@wsl:Ubuntu", source: "https://github.com/ShaneLevs/pi-rename" }],
        ]);
        // nothing runs off the side at a phone's width
        const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        assert(over <= 0, `the page scrolls sideways by ${over}px`);
        await ctx.close();
      });
    }
    assert.deepEqual(errors, []);
  });
}
