// Run with Node's test runner and Playwright on the module path; see README.md.
// Share on local network in a container (莫 on Discord: magpie in Docker on
// a NAS showed the container's own 172.17.x address; jiezhengj on Discord:
// other devices used the host's 192.168.31.218:3425 well, and other
// containers on the NAS needed the host's Docker bridge address,
// http://172.17.0.1:3425): the addresses magpie finds for itself there are
// the container's, which neither reach. Served over the network (magpie
// web), the page offers the host it was opened at first, on the gateway's
// port, and after it the host's bridge address, named other containers';
// opened at localhost it offers the bridge address and says the host's
// own is MAGPIE_PUBLIC_URL's. With no default route known it keeps the
// container's addresses, said to be its own. Outside a container nothing
// changes. The OpenAI/Anthropic switch keeps the host and moves nothing.
// The Gateway page's list, the key page's address and the sidebar's line
// say the same, whether Share on local network is on or not (the image's
// MAGPIE_ADDR puts the gateway on the network either way). No backend,
// the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn, openPart } = require("./reader.cjs");
const { fixture } = require("./fixtures/caller-keys.cjs");

const assets = path.resolve(__dirname, "../assets");

function settingsPayload(over) {
  return {
    theme: "light", lang: "en", tray: "panel", quotaLeft: false, currency: "usd",
    dock: false, dockWindow: false, proxy: "", redact: false, redactPersonal: false, redactWords: [],
    codexWarmup: "", claudeWarmup: "", codexWarmAt: "", claudeWarmAt: "", workbuddyCheckin: false, noStats: false,
    trayUsage: "", trayUsageEvery: 3, vision: "", imageGen: "",
    version: "0.1.400", dir: "/config/magpie", gateway: "http://0.0.0.0:3425",
    proxyNow: "none", proxySource: "none", login: false,
    visionModels: [], imageGenModels: [], workbuddyCheckins: [],
    lan: true, lanKey: "sk-magpie-0123456789abcdef", lanURLs: ["http://172.17.0.2:3425"],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
    ...over,
  };
}

function server(lang, web, over) {
  const s = settingsPayload({ lang, ...over });
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:${web}};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: s.fx });
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
    container: "In a container",
    opened: "Other devices use the host’s address this page was opened at, and other containers on this host the host’s Docker bridge address, both on the gateway’s port; MAGPIE_PUBLIC_URL sets another",
    bridge: "Other containers on this host use the host’s Docker bridge address, on the gateway’s port; other devices use the host’s own, which MAGPIE_PUBLIC_URL puts here",
    own: "The container’s own addresses, which other devices can’t reach: set MAGPIE_PUBLIC_URL to the host’s",
    others: "Other containers on this host", lan: "Local network", here: "This computer",
  },
  zh: {
    container: "在容器中运行",
    opened: "其他设备用打开这个页面所用的宿主机地址，本机上的其他容器用宿主机的 Docker bridge 地址，端口都是网关的端口；MAGPIE_PUBLIC_URL 可另设",
    bridge: "本机上的其他容器用宿主机的 Docker bridge 地址，端口是网关的端口；其他设备用宿主机自己的地址，用 MAGPIE_PUBLIC_URL 把它设在这里",
    own: "容器自身的地址，其他设备无法访问：请将 MAGPIE_PUBLIC_URL 设为宿主机地址",
    others: "同一主机上的其他容器", lan: "局域网", here: "本机",
  },
  "zh-TW": { others: "同一主機上的其他容器", lan: "區域網", here: "本機" },
  ja: { others: "同じホストのほかのコンテナ", lan: "ローカルネットワーク", here: "このコンピュータ" },
  de: { others: "Andere Container auf diesem Host", lan: "Lokales Netzwerk", here: "Dieser Computer" },
};
const BRIDGE = "http://172.17.0.1:3425";

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(engine + ": in a container, the share address is the host's, not the container's", async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    const pages = [];
    t.after(async () => {
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        for (const [i, p] of pages.entries()) await p.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `${engine}-lan-docker-${i}.png`) });
      }
      await browser.close();
    });

    const open = async (lang, at, web, over) => {
      const page = await (await browser.newContext({ viewport: { width: 900, height: 360 }, reducedMotion: "reduce" })).newPage();
      pages.push(page);
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", server(lang, web, over));
      await watchSettingsReads(page);
      await page.goto(at + "/?view=settings&tab=network");
      await settingsDrawn(page); // rows found are the ones that stay (#1659)
      await page.locator("#lanList .lan-address-row").waitFor();
      return { page, errors };
    };
    const address = (page) => page.locator("#lanList .lan-address-text, #lanList .lan-interface .lan-url").textContent();
    const note = (page) => page.locator("#lanList .lan-container");

    for (const lang of ["en", "zh"]) {
      const w = words[lang];
      await t.test(lang, async () => {
        // magpie web, opened at the NAS's address: the gateway there
        {
          const { page, errors } = await open(lang, "http://192.168.1.20:3430", true, { lanContainer: true, lanContainerHost: BRIDGE });
          assert.equal(await address(page), "http://192.168.1.20:3425/v1");
          assert.equal((await note(page).locator(".name").textContent()).trim(), w.container);
          assert.equal((await note(page).locator(".sub").textContent()).trim(), w.opened);
          const scroll = () => page.locator("#view-settings").evaluate((v) => v.scrollTop);
          const anthropic = page.locator("#lanList .lan-address-controls .segs .opt").nth(1);
          // scrolled with the wheel, as a reader would (a script's scroll
          // is put back), until the switch is well inside the view
          await page.locator("#view-settings").hover();
          for (let i = 0; i < 40; i++) {
            const box = await anthropic.boundingBox();
            if (box && box.y > 80 && box.y + box.height < 340 && (await scroll()) > 0) break;
            await page.mouse.wheel(0, 120);
            await page.waitForTimeout(30);
          }
          const before = await scroll();
          assert(before > 0, "the settings must be scrolled to the switch");
          await anthropic.click();
          assert.equal(await address(page), "http://192.168.1.20:3425");
          assert.equal(await scroll(), before, "the switch moved the page");
          const border = await note(page).evaluate((r) => getComputedStyle(r).borderLeftWidth);
          assert(border === "0px" || border === "", "no left border accent: " + border);
          // the host's bridge address is offered after it, named other
          // containers' (after the switch, so the menu's own scroll is not
          // the switch's); the container's own is not offered
          await page.locator("#lanList .lan-interface").click();
          const items = page.locator(".proto-menu .pm-item");
          assert.deepEqual((await items.locator(".pm-name").allTextContents()).map((x) => x.trim()),
            ["http://192.168.1.20:3425", BRIDGE]);
          assert.deepEqual((await items.locator(".pm-note").allTextContents()).map((x) => x.trim()), ["", w.others]);
          assert.deepEqual(errors, []);
        }
        // opened at localhost (a tunnel, or on the NAS itself): the host's
        // bridge address, for other containers, and the host's own is
        // MAGPIE_PUBLIC_URL's
        {
          const { page, errors } = await open(lang, "http://localhost:3430", true, { lanContainer: true, lanContainerHost: BRIDGE });
          assert.equal(await address(page), BRIDGE + "/v1");
          assert.equal((await note(page).locator(".sub").textContent()).trim(), w.bridge);
          assert.deepEqual(errors, []);
        }
        // no default route known: the container's own, said to be its own
        {
          const { page, errors } = await open(lang, "http://localhost:3430", true, { lanContainer: true });
          assert.equal(await address(page), "http://172.17.0.2:3425/v1");
          assert.equal((await note(page).locator(".sub").textContent()).trim(), w.own);
          assert.deepEqual(errors, []);
        }
        // not in a container (or MAGPIE_PUBLIC_URL set): as it was
        {
          const { page, errors } = await open(lang, "http://192.168.1.20:3430", true, { lanURLs: ["http://192.168.1.5:3425"] });
          assert.equal(await address(page), "http://192.168.1.5:3425/v1");
          assert.equal(await note(page).count(), 0);
          assert.deepEqual(errors, []);
        }
      });
    }
  });
}

// The Gateway page, the key page and the sidebar read the gateway's own
// flags, not Settings': in Docker, Share on local network is off and the
// gateway is on the network all the same (jiezhengj: other containers on
// the NAS needed the host's bridge address).
const OWN = "http://172.17.0.2:3999", HOSTBR = "http://172.17.0.1:3999";
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: in a container, Gateway and the sidebar name the host's address first`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const w = words[lang];
      const open = async (at, container, containerHost) => {
        const page = await (await browser.newContext({ viewport: { width: 1100, height: 800 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(6000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", fixture(lang, "light", [], { lan: true, web: true, container, containerHost, lanURLs: [OWN], bootGateway: true }));
        await page.goto(at + "/?view=gateway");
        await page.locator("#gatewayLan .gw-url").first().waitFor();
        await page.waitForFunction(() => document.querySelector("#gatewayKeys .gw-key-page code"));
        return { page, errors };
      };
      const list = async (page) => {
        const rows = page.locator("#gatewayLan .gw-url");
        const out = [];
        for (let i = 0; i < await rows.count(); i++)
          out.push([(await rows.nth(i).locator(".gw-url-label").textContent()).trim(), (await rows.nth(i).locator("code").textContent()).trim()]);
        return out;
      };
      const side = (page) => page.locator("#sideGw span").textContent();
      const keyPage = (page) => page.locator("#gatewayKeys .gw-key-page code").textContent();

      // opened at the NAS's address: the host's first, the host's bridge
      // address after it as other containers'; the sidebar and the key
      // page name the host's
      {
        const { page, errors } = await open("http://192.168.31.218:3430", true, HOSTBR);
        assert.deepEqual((await list(page)).slice(1), [[w.lan, "http://192.168.31.218:3999"], [w.others, HOSTBR]]);
        assert.match(await side(page), /192\.168\.31\.218:3999/);
        assert.equal(await keyPage(page), "http://192.168.31.218:3999/me");
        assert.deepEqual(errors, []);
      }
      // opened at localhost: the bridge address, as other containers', and
      // never the container's own as the local network's
      {
        const { page, errors } = await open("http://localhost:3430", true, HOSTBR);
        assert.deepEqual((await list(page)).slice(1), [[w.others, HOSTBR]]);
        assert.match(await side(page), /172\.17\.0\.1:3999/);
        assert.deepEqual(errors, []);
      }
      // not in a container: as it was
      {
        const { page, errors } = await open("http://192.168.31.218:3430", false);
        assert.deepEqual((await list(page)).slice(1), [[w.lan, OWN]]);
        assert.match(await side(page), /172\.17\.0\.2:3999/);
        assert.equal(await keyPage(page), OWN + "/me");
        assert.deepEqual(errors, []);
      }
    });
  }
}

// jiezhengj on Discord: magpie in Docker on a NAS, MAGPIE_PUBLIC_URL at the
// NAS's own 192.168.31.218. The list on the Gateway page called it "Public
// address" (公网地址), as if anyone online reached it, and Connect's menu
// called the Docker bridge's 172.17.0.1 "Local network" while the list
// called it other containers'. A private address is the local network's,
// the bridge's is other containers', in the list, in Connect's menu and in
// About alike; a public one is the internet's.
const NAS = "http://192.168.31.218:3999";
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}: MAGPIE_PUBLIC_URL at a private address is the local network's, the bridge other containers'`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const w = words[lang];
        const open = async (at, web, over = {}) => {
          const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
          await page.addInitScript(() => { try { localStorage.setItem("magpie.gwConnectFolded", "0"); } catch {} });
          page.setDefaultTimeout(6000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", fixture(lang, "light", [], { lan: true, web, containerHost: HOSTBR, // MAGPIE_PUBLIC_URL says the host's: no container's own addresses
            lanURLs: [NAS], publicURL: NAS, publicKind: "local", ...over }));
          await page.goto(at + "/?view=gateway");
          await page.locator("#gatewayLan .gw-url").first().waitFor();
          await page.locator("#connectAddress").waitFor();
          return { page, errors };
        };
        const list = async (page) => {
          const rows = page.locator("#gatewayLan .gw-url");
          const out = [];
          for (let i = 0; i < await rows.count(); i++)
            out.push([(await rows.nth(i).locator(".gw-url-label").textContent()).trim(), (await rows.nth(i).locator("code").textContent()).trim()]);
          return out;
        };
        const menu = async (page) => {
          await page.locator("#connectAddress").click();
          const items = page.locator(".proto-menu .pm-item");
          await items.first().waitFor();
          const names = (await items.locator(".pm-name").allTextContents()).map((x) => x.trim());
          const notes = (await items.locator(".pm-note").allTextContents()).map((x) => x.trim());
          return names.map((n, i) => [notes[i], n]);
        };
        const fits = async (page) => assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "wider than the window");

        // magpie web, opened at the NAS: the list and Connect's menu name
        // each address the same way
        {
          const { page, errors } = await open("http://192.168.31.218:3430", true);
          assert.deepEqual(await list(page), [[w.lan, NAS], [w.others, HOSTBR]]);
          assert.deepEqual(await menu(page), [[w.here, "http://127.0.0.1:3999/v1"], [w.lan, NAS + "/v1"], [w.others, HOSTBR + "/v1"]]);
          await fits(page);
          assert.deepEqual(errors, []);
        }
        // the app on the NAS: About names it as the Gateway page does
        {
          const { page, errors } = await open("http://magpie.test", false);
          assert.deepEqual(await list(page), [[w.here, "http://127.0.0.1:3999"], [w.lan, NAS], [w.others, HOSTBR]]);
          await page.keyboard.press("Escape");
          await page.locator("#prefs").click();
          await openPart(page, "about");
          await page.locator("#publicURLRow").waitFor();
          assert.equal((await page.locator("#publicURLRow .who .name").textContent()).trim(), w.lan);
          await fits(page);
          assert.deepEqual(errors, []);
        }
        // an internet address is the internet's, in the list and the menu
        {
          const PUB = "http://203.0.113.9:3999";
          const { page, errors } = await open("http://203.0.113.9:3430", true, { containerHost: "",
            lanURLs: [PUB], publicURL: PUB, publicKind: "internet", lanPublic: [PUB] });
          const internet = await page.evaluate(() => t("On the internet"));
          assert.deepEqual(await list(page), [[internet, PUB]]);
          assert.deepEqual(await menu(page), [[w.here, "http://127.0.0.1:3999/v1"], [internet, PUB + "/v1"]]);
          assert.deepEqual(errors, []);
        }
      });
    }
  }
}
