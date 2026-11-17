// Run with Node's test runner and Playwright on the module path; see README.md.
// An SSH server as a saved proxy (@laiuuui on X: a VPS gives only SSH, an
// IP, port, user and password). Add proxy › SSH takes its host, user and
// password; a refused login says why in the reader's language and keeps
// what was typed; a kept one is tested at once. The private key and the
// trusted host key wait under a closed Advanced.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const FP = "SHA256:Zx9bR4uE0oKp2m3Yd5qVw7nLs8tGh1jC6aF0eI2xQ4k";
function server(lang, posts) {
  let cur = {
    theme: "light", lang, tray: "panel", currency: "usd", textSize: 100,
    proxy: "", proxyNow: "", proxySource: "off", pluginProxy: "",
    trayUsageEvery: 3, version: "test", dir: "/test", lanURLs: [],
    visionModels: [], imageGenModels: [], workbuddyCheckins: [],
    proxies: [{ id: "px2", name: "Home", url: "socks5://192.168.1.2:1080" }],
    fx: { rate: 7.2, at: new Date().toISOString(), stale: false },
  };
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data, status = 200, headers = {}) => route.fulfill({ status, headers, json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = ${JSON.stringify({ lang, theme: "light", web: true })};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" }, fx: cur.fx });
    if (url.pathname === "/api/settings") return json(cur);
    if (url.pathname === "/api/proxies/test") {
      const b = req.postDataJSON();
      posts.push({ test: b.id });
      return json({ ok: true, ms: 42, ip: "203.0.113.5", loc: "JP", at: 1 });
    }
    if (url.pathname === "/api/proxies/save") {
      const b = req.postDataJSON();
      posts.push({ save: b });
      // the server's words for a refused login (netproxy.sshWhy)
      if (b.pass === "wrong") return json({ error: "refused", code: "proxy_why", why: "the SSH server at {addr} turned the login away: check the user and the password or key", addr: "203.0.113.5:22", detail: "ssh: unable to authenticate" }, 400);
      const row = { id: "px3", name: b.name, url: b.url, user: b.user, hasPass: true, hostKey: FP };
      cur = { ...cur, proxies: [...cur.proxies.filter((p) => p.id !== "px3"), row] };
      return json(cur, 200, { "X-Magpie-Proxy": "px3" });
    }
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    return route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const labels = {
  en: { add: "Add proxy", name: "Name", save: "Save", edit: "Edit", host: "SSH server", user: "User", pass: "Password", refused: "turned the login away", ok: "Reachable, 42 ms", adv: "Advanced", key: "Private key", trust: "Trust a new key", trusted: "The key the server shows next is trusted" },
  zh: { add: "添加代理", name: "名称", save: "保存", edit: "编辑", host: "SSH 服务器", user: "用户", pass: "密码", refused: "拒绝登录", ok: "可连接，42 毫秒", adv: "高级", key: "私钥", trust: "信任新密钥", trusted: "将信任服务器下次出示的密钥" },
  "zh-TW": { add: "新增代理", name: "名稱", save: "儲存", edit: "編輯", host: "SSH 伺服器", user: "使用者", pass: "密碼", refused: "拒絕登入", ok: "可連線，42 毫秒", adv: "進階", key: "私鑰", trust: "信任新金鑰", trusted: "將信任伺服器下次出示的金鑰" },
  ja: { add: "プロキシを追加", name: "名前", save: "保存", edit: "編集", host: "SSH サーバー", user: "ユーザー", pass: "パスワード", refused: "ログインを拒否しました", ok: "接続できます、42 ms", adv: "詳細設定", key: "秘密鍵", trust: "新しい鍵を信頼", trusted: "サーバーが次に示す鍵を信頼します" },
  de: { add: "Proxy hinzufügen", name: "Name", save: "Speichern", edit: "Bearbeiten", host: "SSH-Server", user: "Benutzer", pass: "Passwort", refused: "hat die Anmeldung abgelehnt", ok: "Erreichbar, 42 ms", adv: "Erweitert", key: "Privater Schlüssel", trust: "Neuem Schlüssel vertrauen", trusted: "Der Schlüssel, den der Server als Nächstes zeigt, wird vertraut" },
};

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of Object.keys(labels)) {
    const l = labels[lang];
    for (const width of [1100, 420]) {
      test(`${engine} ${lang} ${width}px: an SSH server is added as a saved proxy`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const context = await browser.newContext({ viewport: { width, height: 900 }, reducedMotion: "reduce" });
        const page = await context.newPage();
        page.setDefaultTimeout(4000);
        page.setDefaultNavigationTimeout(60000);
        const posts = [], errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, posts));
        await page.goto("http://magpie.test/?view=settings&tab=network");
        const list = page.locator("#proxiesList");
        await list.getByText("Home", { exact: true }).waitFor();

        await list.getByRole("button", { name: l.add, exact: true }).click();
        await list.getByRole("button", { name: "SSH", exact: true }).click();
        // the key waits under a closed Advanced
        const adv = list.locator("details.proxy-advanced");
        assert.equal(await adv.evaluate((d) => d.open), false);
        assert.equal(await list.getByLabel(l.key, { exact: true }).isVisible(), false);
        await list.getByLabel(l.name, { exact: true }).fill("VPS");
        await list.getByLabel(l.host, { exact: true }).fill("203.0.113.5:22");
        await list.getByLabel(l.user, { exact: true }).fill("root");
        await list.getByLabel(l.pass, { exact: true }).fill("wrong");
        await list.getByRole("button", { name: l.save, exact: true }).click();

        // a refused login says why, translated, and keeps what was typed
        await list.locator(".proxy-form .sub.err").getByText(l.refused, { exact: false }).waitFor();
        const said = await list.locator(".proxy-form .sub.err").textContent();
        assert(said.includes("203.0.113.5:22") && said.includes("ssh: unable to authenticate"), said);
        assert.equal(await list.getByLabel(l.user, { exact: true }).inputValue(), "root");
        assert.equal(await list.getByLabel(l.host, { exact: true }).inputValue(), "203.0.113.5:22");

        await list.getByLabel(l.pass, { exact: true }).fill("s3cret");
        await list.getByRole("button", { name: l.save, exact: true }).click();
        const row = list.locator(".saved-proxy").filter({ hasText: "VPS" });
        await row.getByText("SSH root@203.0.113.5:22", { exact: false }).waitFor();
        // tested at once
        await row.getByText(l.ok, { exact: false }).waitFor();
        const saves = posts.filter((p) => p.save).map((p) => p.save);
        assert.deepEqual([saves.at(-1).url, saves.at(-1).user, saves.at(-1).pass], ["ssh://203.0.113.5:22", "root", "s3cret"]);
        assert.equal(posts.filter((p) => p.test).at(-1).test, "px3");
        assert.equal(await page.evaluate(() => document.body.textContent.includes("s3cret")), false, "the password is never shown");

        // the trusted host key, and trusting a new one
        await row.getByRole("button", { name: l.edit, exact: true }).click();
        await list.locator("details.proxy-advanced summary").click();
        await list.getByText(FP, { exact: false }).waitFor();
        await list.getByRole("button", { name: l.trust, exact: true }).click();
        await list.getByText(l.trusted, { exact: true }).waitFor();
        await list.getByRole("button", { name: l.save, exact: true }).click();
        await list.locator(".proxy-form").waitFor({ state: "detached" });
        const last = posts.filter((p) => p.save).at(-1).save;
        assert.equal(last.trustNewKey, true);
        assert.equal("pass" in last, false, "a blank password keeps the saved one");

        const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        assert(over <= 0, `the page scrolls sideways by ${over}px`);
        assert.deepEqual(errors, []);
      });
    }
  }
}
