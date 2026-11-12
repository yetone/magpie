// Run with Node's test runner and Playwright on the module path; see README.md.
// Settings › Gateway address (Magic_zero on Discord: 3425 was taken by another
// program, and a port of one's own is easier to tell apart): any port from
// 1024 to 65535 can be typed and applied. One out of range is refused in the
// page; one another program has is refused with the gateway's reason, in the
// page's language, what was typed kept; a free one moves the gateway and
// says how many agents moved with it, and the row then says the new URL,
// as does the sidebar's gateway line (noahhrreion on Discord).
// With MAGPIE_ADDR set the field is off, shows its host:port and says why;
// the row is titled an address, not a port, since it shows one there
// (Chicring on Discord), and says only the port is typed. The field is an
// input, not a native <select>; applying never scrolls the page; no
// coloured left border. English and Chinese, Chromium and WebKit; no
// backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

function settingsPayload(over) {
  return {
    theme: "light", lang: "en", tray: "panel", quotaLeft: false, currency: "usd",
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.900", dir: "/config/magpie", gateway: "http://127.0.0.1:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [],
    lan: false, lanURLs: [],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    ...over,
  };
}

function server(lang, over, posts) {
  let s = settingsPayload({ lang, ...over });
  return async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const json = (data, status = 200) => route.fulfill({ status, json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:false,sideGateway:{url:"http://127.0.0.1:3425",running:true,boot:true}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: s.fx });
    if (url.pathname === "/api/settings/port") {
      const { port } = JSON.parse(req.postData());
      posts.push(port);
      if (port === 3500) return json({ error: "port 3500 is in use by another program: pick another one" }, 400);
      s = { ...s, port, gateway: "http://127.0.0.1:" + port };
      return json({ settings: s, port: { port, moved: ["Claude Code", "Codex"] } });
    }
    if (url.pathname === "/api/settings") return json(s);
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: {
    name: "Gateway port", addr: "Gateway address", apply: "Apply",
    now: (u) => "Only the port is set here, 1024 to 65535; MAGPIE_ADDR=host:port sets the whole address instead. Agents magpie connected move with it. Now " + u,
    range: "A port is a number from 1024 to 65535",
    taken: "Port 3500 is in use by another program: pick another one",
    moved: "Gateway on port 3591; 2 agents moved with it",
    env: "MAGPIE_ADDR=127.0.0.1:3426 sets the gateway’s whole address, host:port: unset it to set the port here",
  },
  zh: {
    name: "网关端口", addr: "网关地址", apply: "应用",
    now: (u) => "这里只设端口（1024 到 65535）；MAGPIE_ADDR=主机:端口 可指定完整地址。改了之后，已接入 magpie 的 Agent 会一起换过去。当前 " + u,
    range: "端口是 1024 到 65535 之间的数字",
    taken: "端口 3500 已被其他程序占用，请换一个",
    moved: "网关已换到端口 3591，2 个 Agent 已跟着换过去",
    env: "MAGPIE_ADDR=127.0.0.1:3426 已指定网关的完整地址（主机:端口）：去掉这个环境变量后才能在这里设端口",
  },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": the gateway's port is typed in Settings and agents move with it", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const pages = [];
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        for (const [i, p] of pages.entries()) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-gateway-port-${i}.png`) });
      }
      await browser.close();
    });

    const open = async (lang, over) => {
      const page = await (await browser.newContext({ viewport: { width: 900, height: 360 }, reducedMotion: "reduce" })).newPage();
      pages.push(page);
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [], posts = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, over, posts));
      await watchSettingsReads(page);
      await page.goto("http://localhost:3430/?view=settings&tab=network");
      await settingsDrawn(page); // rows found are the ones that stay (#1659)
      await page.locator("#portList .port-row").waitFor();
      return { page, errors, posts };
    };

    for (const lang of ["en", "zh"]) {
      const w = words[lang];
      await t.test(lang, async () => {
        {
          const { page, errors, posts } = await open(lang);
          const row = page.locator("#portList .port-row");
          const field = row.locator("input.gateway-port");
          const sub = row.locator(".sub");
          const apply = row.getByRole("button", { name: w.apply });
          assert.equal((await row.locator(".name").textContent()).trim(), w.name);
          assert.equal(await field.inputValue(), "3425");
          assert.equal((await sub.textContent()).trim(), w.now("http://127.0.0.1:3425"));
          assert.equal(await page.locator("#portList select").count(), 0, "no native select");
          const border = await row.evaluate((r) => getComputedStyle(r).borderLeftWidth);
          assert(border === "0px" || border === "", "no left border accent: " + border);

          // scrolled with the wheel, as a reader would, until the row is in
          // view. Each step is looked at once its scroll has come to rest: on
          // a busy machine wheel events still queued went on past the row,
          // and the field was out of view above when it was typed in
          const scroll = () => page.locator("#view-settings").evaluate((v) => v.scrollTop);
          const rest = async () => {
            for (let last = -1, now = await scroll(); now !== last; last = now, now = await scroll()) await page.waitForTimeout(100);
          };
          const view = await page.locator("#view-settings").boundingBox();
          const inView = async () => {
            for (const b of [await field.boundingBox(), await apply.boundingBox()]) {
              if (!b || b.y < view.y + 8 || b.y + b.height > view.y + view.height - 8) return false;
            }
            return (await scroll()) > 0;
          };
          await page.locator("#view-settings").hover();
          for (let i = 0; i < 60 && !(await inView()); i++) {
            await page.mouse.wheel(0, 40);
            await rest();
          }
          const before = await scroll();
          assert(await inView(), "the settings must be scrolled to the row");

          // out of range: said in the page, nothing sent
          await field.fill("80");
          assert.equal(await scroll(), before, "typing moved the page");
          await apply.click();
          await page.waitForFunction((want) => document.querySelector("#portList .sub")?.textContent.trim() === want, w.range);
          assert(await row.locator(".sub").evaluate((e) => e.classList.contains("err")));
          assert.deepEqual(posts, []);
          assert.equal(await field.inputValue(), "80", "what was typed is kept");
          assert.equal(await scroll(), before, "a port out of range moved the page");

          // taken by another program: the gateway's reason, translated
          await field.fill("3500");
          await apply.click();
          await page.waitForFunction((want) => document.querySelector("#portList .sub")?.textContent.trim() === want, w.taken);
          assert.equal(await field.inputValue(), "3500");
          assert.deepEqual(posts, [3500]);
          assert.equal(await scroll(), before, "a taken port moved the page");

          // a free one, by Enter: moved, and the agents with it
          await field.fill("3591");
          await field.press("Enter");
          await page.waitForFunction((want) => document.querySelector("#status")?.textContent.trim() === want, w.moved);
          await page.waitForFunction((want) => document.querySelector("#portList .sub")?.textContent.trim() === want, w.now("http://127.0.0.1:3591"));
          assert.equal(await field.inputValue(), "3591");
          assert.deepEqual(posts, [3500, 3591]);
          // the sidebar's gateway line says the new address too, not the
          // port before until Providers is opened (noahhrreion on Discord)
          // (the window made wide enough for the sidebar, as the
          // reporter's was)
          await page.setViewportSize({ width: 1200, height: 360 });
          await page.waitForFunction(() => document.body.classList.contains("side-nav") && !document.querySelector("#sideGw").hidden);
          await page.waitForFunction(() => document.querySelector("#sideGw span")?.textContent.includes("127.0.0.1:3591"));
          assert(!(await page.locator("#sideGw").getAttribute("title")).includes(":3425"), "the sidebar's tooltip still says 3425");
          assert.equal(await scroll(), before, "applying moved the page");
          assert.deepEqual(errors, []);
        }
        // MAGPIE_ADDR comes first: the field is off and says why
        {
          const { page, errors, posts } = await open(lang, { addrEnv: "127.0.0.1:3426", gateway: "http://127.0.0.1:3426" });
          const row = page.locator("#portList .port-row");
          assert(await row.locator("input.gateway-port").isDisabled());
          assert.equal(await row.locator("input.gateway-port").inputValue(), "127.0.0.1:3426");
          // the whole host:port shown: an address, not a port
          assert.equal((await row.locator(".name").textContent()).trim(), w.addr);
          assert.equal(await row.locator("input.gateway-port").getAttribute("aria-label"), w.addr);
          assert(await row.getByRole("button", { name: w.apply }).isDisabled());
          assert.equal((await row.locator(".sub").textContent()).trim(), w.env);
          assert.deepEqual(posts, []);
          assert.deepEqual(errors, []);
        }
      });
    }
  });
}
