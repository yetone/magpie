// Run with Node's test runner and Playwright on the module path; see README.md.
// mtoolstec on X ran magpie as a LazyCat (懒猫微服) app. Share on local
// network offered the app's domain, which only devices with LazyCat's
// client reach, and the box's container bridge (172.28.3.65), which
// nothing off the box reaches, named other containers'; other devices on
// the home network wanted http://192.168.0.100:3425/v1.
// - On the box's network (the package's network_mode: host) magpie finds
//   the box's LAN address: it comes first, then the domain, named LazyCat
//   client's, in Settings' menu, on the Gateway page and in the sidebar.
// - On a bridge network that address can't be known: the domain alone,
//   named the same, and a line says how the port gets to the box's LAN
//   address (LazyCat's LAN port forwarding app), in Settings and on the
//   Gateway page. No bridge address is offered.
// No backend, the API is faked here.
const assert = require("node:assert/strict");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture } = require("./fixtures/caller-keys.cjs");
const { openPart, reach } = require("./reader.cjs");

const LC = "https://magpie.snyht3.heiyu.space", BOX = "http://192.168.0.100:3999", HERE = "http://127.0.0.1:3999";
const words = {
  en: { client: "LazyCat client", head: "LazyCat", lan: "Local network", here: "This computer",
    sub: "Only devices with the LazyCat client reach this address. For other devices to use the box’s LAN IP on port 3999, forward port 3999 to Magpie in LazyCat’s LAN port forwarding app" },
  zh: { client: "懒猫客户端", head: "懒猫微服", lan: "局域网", here: "本机",
    sub: "只有装了懒猫客户端的设备能访问这个地址。要让其他设备用微服的局域网 IP 加 3999 端口访问，请在懒猫的「局域网端口转发工具」里把 3999 端口转发到 Magpie" },
  "zh-TW": { client: "懶貓用戶端", head: "懶貓微服", lan: "區域網", here: "本機", sub: /區域網 IP 加 3999 連接埠.*「局域網端口轉發工具」/ },
  ja: { client: "LazyCat クライアント", head: "LazyCat", lan: "ローカルネットワーク", here: "このコンピュータ", sub: /LAN IP のポート 3999.*ポート 3999/ },
  de: { client: "LazyCat-Client", head: "LazyCat", lan: "Lokales Netzwerk", here: "Dieser Computer", sub: /LAN-IP der Box auf Port 3999.*Port 3999/ },
};
const same = (got, want) => want instanceof RegExp ? assert.match(got, want) : assert.equal(got, want);

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}: a LazyCat app offers the box's LAN address, or says how to get it`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const w = words[lang];
        const open = async (view, box) => {
          const page = await (await browser.newContext({ viewport: { width, height: 800 }, reducedMotion: "reduce" })).newPage();
          page.setDefaultTimeout(6000);
          page.setDefaultNavigationTimeout(60000);
          const errors = [];
          page.on("pageerror", (e) => errors.push(e.message));
          await page.route("**/*", fixture(lang, "light", [], { lan: true, bootGateway: true, publicURL: LC, lazyCat: LC,
            lazyCatBox: box, lanURLs: box ? [BOX, LC] : [LC] }));
          await page.goto("http://magpie.test/?view=" + view);
          return { page, errors };
        };
        const fits = async (page) => assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, "wider than the window");
        const list = async (page) => {
          await page.locator("#gatewayLan .gw-url").first().waitFor();
          const rows = page.locator("#gatewayLan .gw-url");
          const out = [];
          for (let i = 0; i < await rows.count(); i++)
            out.push([(await rows.nth(i).locator(".gw-url-label").textContent()).trim(), (await rows.nth(i).locator("code").textContent()).trim()]);
          return out;
        };
        const side = (page) => page.locator("#sideGw span").textContent();

        // on the box's network: the box's address first, then the domain
        {
          const { page, errors } = await open("settings", true);
          await page.locator("#sideGw").waitFor({ state: "attached" }); // folded away at 420px, and still drawn
          assert.match(await side(page), /192\.168\.0\.100:3999/);
          await openPart(page, "network");
          await page.locator("#lanList .lan-address-row").waitFor();
          assert.equal(await page.locator("#lanList .lan-container").count(), 0);
          await reach(page, page.locator("#lanList .lan-interface"));
          await page.locator("#lanList .lan-interface").click();
          const items = page.locator(".proto-menu .pm-item");
          await items.first().waitFor();
          assert.deepEqual((await items.locator(".pm-name").allTextContents()).map((x) => x.trim()), [BOX + "/v1", LC + "/v1"]);
          assert.deepEqual((await items.locator(".pm-note").allTextContents()).map((x) => x.trim()), ["", w.client]);
          await fits(page);
          assert.deepEqual(errors, []);
        }
        {
          const { page, errors } = await open("gateway", true);
          assert.deepEqual(await list(page), [[w.here, HERE], [w.lan, BOX], [w.client, LC]]);
          assert.equal(await page.locator("#gatewayLan .gw-url-none").count(), 0);
          await fits(page);
          assert.deepEqual(errors, []);
        }
        // on a bridge network: the domain, and how to reach the box
        {
          const { page, errors } = await open("settings", false);
          await openPart(page, "network");
          await page.locator("#lanList .lan-address-row").waitFor();
          assert.equal((await page.locator("#lanList .lan-address-text").textContent()).trim(), LC + "/v1");
          const note = page.locator("#lanList .lan-container");
          assert.equal((await note.locator(".name").textContent()).trim(), w.head);
          same((await note.locator(".sub").textContent()).trim(), w.sub);
          assert.doesNotMatch(await page.locator("#lanList").textContent(), /172\.28/);
          const border = await note.evaluate((r) => getComputedStyle(r).borderLeftWidth);
          assert(border === "0px" || border === "", "no left border accent: " + border);
          await fits(page);
          assert.deepEqual(errors, []);
        }
        {
          const { page, errors } = await open("gateway", false);
          assert.deepEqual(await list(page), [[w.here, HERE], [w.client, LC]]);
          same((await page.locator("#gatewayLan .gw-url-none").textContent()).trim(), w.sub);
          assert.match(await side(page), /magpie\.snyht3\.heiyu\.space/);
          await fits(page);
          assert.deepEqual(errors, []);
        }
      });
    }
  }
}
