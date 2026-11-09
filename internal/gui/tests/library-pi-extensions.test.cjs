// Run with Node's test runner and Playwright on the module path; see README.md.
// leokon3 on Discord: Pi on Windows and in WSL, and no way to see or move
// an extension between them. The Library's Pi packages tab lists each
// Pi's extensions and packages with a switch each; a package that picks
// only some of its parts has its switch off limits (pi config changes it);
// Copy to… offers only the other Pis that are running, and sends the pick;
// a stopped distro says so and lists nothing. In every language, both
// engines, at a phone's width too. No backend: the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const HOME = "C:\\Users\\leo";
const lib = () => ({
  dir: `${HOME}\\.magpie\\library`, backups: `${HOME}\\.magpie\\backups`, home: HOME,
  agents: [
    { id: "pi", name: "Pi", icon: "", skills: `${HOME}\\.pi\\agent\\skills` },
    { id: "pi@wsl:Ubuntu", name: "Pi · WSL Ubuntu", icon: "", skills: "\\\\wsl.localhost\\Ubuntu\\home\\leo\\.pi\\agent\\skills", wsl: "Ubuntu" },
  ],
  instructions: { agents: [], sets: [] }, foundServers: [], projects: [], foundSkills: [], skills: [], servers: [],
});
const piView = (on) => ({
  pis: [
    {
      id: "pi", name: "Pi", icon: "", dir: `${HOME}\\.pi\\agent`,
      extensions: [{ id: "extensions/hello.ts", name: "hello.ts", path: `${HOME}\\.pi\\agent\\extensions\\hello.ts`, on: true }],
      packages: [
        { source: "npm:pi-skills@1.2.0", name: "pi-skills", kind: "npm", pin: "1.2.0", on: true, installed: true, version: "1.2.0" },
        { source: "git:github.com/leo/pi-tools", name: "pi-tools", kind: "git", on: true, filtered: true, installed: false },
      ],
    },
    {
      id: "pi@wsl:Ubuntu", name: "Pi · WSL Ubuntu", icon: "", wsl: "Ubuntu", dir: "\\\\wsl.localhost\\Ubuntu\\home\\leo\\.pi\\agent",
      extensions: [{ id: "extensions/todo", name: "todo", path: "/home/leo/.pi/agent/extensions/todo", on, folder: true }],
      packages: [],
    },
    { id: "pi@wsl:Arch", name: "Pi · WSL Arch", icon: "", wsl: "Arch", asleep: true, extensions: [], packages: [] },
  ],
});

function server(lang, calls) {
  let on = false;
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: { agents: [], profiles: [], settings: { lang, theme: "light" } } });
    if (url.pathname === "/api/plugins") return route.fulfill({ json: { plugins: [] } });
    if (url.pathname === "/api/library") return route.fulfill({ json: lib() });
    if (url.pathname === "/api/library/pi") {
      if (req.method() === "POST") {
        const b = req.postDataJSON();
        calls.push(["set", b]);
        on = b.on;
      }
      return route.fulfill({ json: piView(on) });
    }
    if (url.pathname === "/api/library/pi/copy") {
      calls.push(["copy", req.postDataJSON()]);
      return route.fulfill({ json: { ...piView(on), install: "~/.pi/agent/extensions/todo" } });
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
  en: { tab: "Pi packages", copy: "Copy to…", stopped: "WSL Arch isn't running", notInstalled: "Not installed", on: "todo is on for Pi · WSL Ubuntu: Pi loads it when it next starts, or at /reload", copied: "todo is copied to Pi. It needs its packages: run npm install in ~/.pi/agent/extensions/todo there" },
  zh: { tab: "Pi 包", copy: "复制到…", stopped: "WSL Arch 未运行", notInstalled: "未安装", on: "todo 已为 Pi · WSL Ubuntu 打开：Pi 下次启动或 /reload 时加载它", copied: "todo 已复制到 Pi。它需要自己的依赖包：请在那里的 ~/.pi/agent/extensions/todo 中运行 npm install" },
  "zh-TW": { tab: "Pi 套件", copy: "複製到…", stopped: "WSL Arch 未執行", notInstalled: "未安裝", on: "todo 已為 Pi · WSL Ubuntu 開啟：Pi 下次啟動或 /reload 時載入它", copied: "todo 已複製到 Pi。它需要自己的相依套件：請在那裡的 ~/.pi/agent/extensions/todo 中執行 npm install" },
  ja: { tab: "Pi パッケージ", copy: "コピー先…", stopped: "WSL Arch は起動していません", notInstalled: "未インストール", on: "Pi · WSL Ubuntu で todo をオンにしました。Pi は次回起動時か /reload で読み込みます", copied: "todo を Pi にコピーしました。依存パッケージが必要です: そちらの ~/.pi/agent/extensions/todo で npm install を実行してください" },
  de: { tab: "Pi-Pakete", copy: "Kopieren nach…", stopped: "WSL Arch läuft nicht", notInstalled: "Nicht installiert", on: "todo ist für Pi · WSL Ubuntu an: Pi lädt es beim nächsten Start oder bei /reload", copied: "todo ist nach Pi kopiert. Es braucht seine Pakete: Führe dort in ~/.pi/agent/extensions/todo npm install aus" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": each Pi's extensions are listed, switched and copied to another Pi", async (t) => {
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
        const lists = v.locator(".lib-list.lib-pi");
        await lists.first().waitFor();
        assert.equal(await lists.count(), 3);
        assert.deepEqual(await v.locator(".lib-pi-head .label").allTextContents(), ["Pi", "Pi · WSL Ubuntu", "Pi · WSL Arch"]);
        const here = lists.nth(0), ubuntu = lists.nth(1), arch = lists.nth(2);
        // a stopped distro is never started to read it
        assert((await arch.textContent()).startsWith(w.stopped), await arch.textContent());
        assert.equal(await arch.locator(".lib-row").count(), 0);
        const row = (list, id) => list.locator(`.lib-pi-row[data-id="${id}"]`);
        assert.equal(await row(here, "extensions/hello.ts").getByRole("switch").getAttribute("aria-checked"), "true");
        assert.deepEqual(await row(here, "npm:pi-skills@1.2.0").locator(".lib-tag").allTextContents(), ["npm", "v1.2.0"]);
        assert.deepEqual(await row(here, "git:github.com/leo/pi-tools").locator(".lib-tag").allTextContents(), ["git", w.notInstalled]);
        // a package picking some of its parts is changed in pi config
        assert.equal(await row(here, "git:github.com/leo/pi-tools").getByRole("switch").isDisabled(), true);
        assert.equal(await row(here, "npm:pi-skills@1.2.0").getByRole("switch").isDisabled(), false);

        const todo = row(ubuntu, "extensions/todo");
        await todo.getByRole("switch").click();
        await page.waitForFunction((want) => document.querySelector("#status").textContent === want, w.on).catch(() => {});
        assert.equal(await page.locator("#status").textContent(), w.on);
        assert.equal(await row(ubuntu, "extensions/todo").getByRole("switch").getAttribute("aria-checked"), "true");

        // Copy to… offers the running Pis but this one: not the stopped Arch
        await row(ubuntu, "extensions/todo").getByRole("button", { name: w.copy, exact: true }).click();
        const items = page.locator(".proto-menu .pm-item .pm-name");
        await items.first().waitFor();
        assert.deepEqual(await items.allTextContents(), ["Pi"]);
        await page.locator(".proto-menu .pm-item").first().click();
        await page.waitForFunction((want) => document.querySelector("#status").textContent === want, w.copied).catch(() => {});
        assert.equal(await page.locator("#status").textContent(), w.copied);
        assert.deepEqual(calls, [
          ["set", { pi: "pi@wsl:Ubuntu", ext: "extensions/todo", on: true }],
          ["copy", { from: "pi@wsl:Ubuntu", to: "pi", ext: "extensions/todo" }],
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
